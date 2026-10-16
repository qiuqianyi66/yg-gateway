# deploy-prod.rehearsal.ps1 — deploy-prod.ps1 的排练（负向 + 正向），**不碰生产**
#
# 为什么需要：换版脚本本身的正确性无法靠读代码确认——它做的恰恰是「让服务短暂不可用」
# 这件事。一旦脚本自己的回滚路径有 bug，坏处比没有脚本更大（会自信地把生产搞停）。
# 本排练在一个 %TEMP% 沙箱里跑完整流程：沙箱有自己的 config.json / auths(空) / data/，
# 端口用 7864，**绝不读写 D:\yg-gateway 的 auths 或 data**（R1：凭证不出 auths/）。
#
# 四个场景（都用同一个 deploy-prod.ps1 实现）：
#   A1 坏 exe（不是合法 PE，33 字节文本）→ **预检在停服前拒绝**：生产零触碰
#      （没备份、没替换、没停服、没下发启动命令 —— 四条硬证据逐项断言）
#   A2 坏 exe（合法 PE 但立刻退出，where.exe）→ 预检放行 → 健康轮询超时 →
#      **必须自动回滚**且服务恢复、回滚日志与失败现场分开
#   B  好 exe（另一个真的能跑的旧构建）→ 替换生效、备份是旧 exe、健康检查通过；
#      同时兼作**防拖死**回归断言（见该段注释：常驻孙子进程不得拖住调用方管道）
#   C  -DryRun 不得改动任何东西
#
# 为什么 A1 与 A2 是两条不同的路：坏 exe 有两种坏法，坏在不同层次。
#   - 「根本不是 PE」在**停服之前**就能判出来（读 MZ/PE 签名），所以 A1 的验收标准是
#     「一步都没走」——这比「走了再回滚」严，也是它该有的语义；
#   - 「是 PE 但跑不起来」只能靠健康轮询判，所以 A2 的验收标准是「自动回滚且服务恢复」。
# 预检的存在还顺带挡掉一个真实危害：把非 PE 当 exe 启动会让 Windows 弹**模态对话框**
# （「不支持的 16 位应用程序」），launcher 卡在等人点确定上——err 日志 0 字节、文件被占住、
# 回滚覆盖式写入失败、桌面留一个要人点的窗口。这是本轮实测到的现场，见 LESSONS.md L-12。
#
# 沙箱一律传 -MinHealthy 0：沙箱没有账号，/healthz 返回 503 且 healthy=0（探针实测），
# 默认 MinHealthy=1 在生产上是对的、在沙箱里永远不成立——不传就会把「起来了但没账号」
# 误判成「没起来」，回滚断言全部失真（本排练第一版就是这么错的）。
#
# 用法：powershell -NoProfile -File scripts/deploy-prod.rehearsal.ps1
# 退出码：0 = 全过；1 = 有断言失败；2 = 排练自身失败（沙箱建不起来等）
#
# 边界（明说的局限，不是放水）：
#   - 冒烟三条（/v1/models + 非流式 + 流式）**不在**本排练覆盖内：沙箱没有账号，
#     打真实上游必然失败。那三条由生产实弹跑一次覆盖（见 change summary）。
#     本排练用 -SkipSmoke 换掉，其余环节（备份/端口轮询/替换/健康/回滚/不拖死）逐项断言。

param(
    [string]$ScriptPath = '',
    [string]$GoodExe = '',
    [string]$OtherExe = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot

if (-not $ScriptPath) { $ScriptPath = Join-Path $PSScriptRoot 'deploy-prod.ps1' }
if (-not $GoodExe) { $GoodExe = 'D:\yg-gateway\yg-gateway.exe' }
if (-not $OtherExe) { $OtherExe = Join-Path $repoRoot 'yg-gateway.exe' }

$sandbox = Join-Path $env:TEMP 'yg-deploy-rehearsal'
# 报告写到沙箱**外面**：沙箱每个场景都会被重建，被清掉的报告等于没有证据（第一版踩过）
$reports = Join-Path $env:TEMP 'yg-deploy-rehearsal-reports'
$port = 7864
if (Test-Path -LiteralPath $reports) { Remove-Item -LiteralPath $reports -Recurse -Force }
New-Item -ItemType Directory -Force -Path $reports | Out-Null

$failures = New-Object System.Collections.ArrayList
function Check($cond, $msg) {
    if ($cond) {
        Write-Host ('  [OK]   ' + $msg) -ForegroundColor Green
    } else {
        Write-Host ('  [FAIL] ' + $msg) -ForegroundColor Red
        [void]$failures.Add($msg)
    }
}
function Section($t) { Write-Host ''; Write-Host ('--- ' + $t) -ForegroundColor Cyan }
function Sha256($p) { if (Test-Path -LiteralPath $p) { (Get-FileHash -LiteralPath $p -Algorithm SHA256).Hash } else { '' } }

function Get-SandboxListenerPid($p) {
    $lines = @(netstat -ano | Where-Object { $_ -match (':' + $p + '\s') -and $_ -match 'LISTENING' })
    if ($lines.Count -eq 0) { return $null }
    $cols = @($lines[0].Trim() -split '\s+')
    return $cols[$cols.Length - 1]
}

function Stop-Sandbox() {
    $pidOnPort = Get-SandboxListenerPid $port
    if ($pidOnPort) { taskkill /PID $pidOnPort /F 2>&1 | Out-Null }
    # cmd.exe 是网关的父进程，会一直等到网关退出；万一网关已被杀它还赖着，就一起收掉
    $leftover = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
            $_.Name -eq 'cmd.exe' -and $_.CommandLine -and $_.CommandLine -like ('*' + $sandbox + '*') })
    foreach ($m in $leftover) { taskkill /PID $m.ProcessId /F 2>&1 | Out-Null }
    Start-Sleep -Milliseconds 700
}

# 沙箱目录的清理必须**容忍锁**：失败时最多杀一次本沙箱的残留进程再重试，仍失败就明确报错，
# 而不是让 Remove-Item 的异常把整场排练带走（第一版就是这样：A1 的残留日志被锁 →
# A2 的建沙箱抛异常 → 后面所有场景一个都没跑）。
function Clear-SandboxDir() {
    if (-not (Test-Path -LiteralPath $sandbox)) { return $true }
    for ($attempt = 1; $attempt -le 2; $attempt++) {
        try {
            Remove-Item -LiteralPath $sandbox -Recurse -Force -ErrorAction Stop
            return $true
        } catch {
            if ($attempt -eq 1) { Stop-Sandbox }
        }
    }
    return (-not (Test-Path -LiteralPath $sandbox))
}

# 起沙箱服务：**与生产同形态**（借 cmd 做重定向）。若这里图省事用
# Start-Process -RedirectStandardError，这个常驻孙子进程会拖住调用方（本文件所在 PowerShell）
# 的 stdout 管道——排练自己就会挂死，而那不是生产的错。
function Start-SandboxInstance([string]$tag) {
    $exePath = Join-Path $sandbox 'yg-gateway.exe'
    $outLog = Join-Path $sandbox ($tag + '.out.log')
    $errLog = Join-Path $sandbox ($tag + '.err.log')
    $inner = '"' + $exePath + '" -config "' + (Join-Path $sandbox 'config.json') + '" > "' + $outLog + '" 2> "' + $errLog + '"'
    # 整串再包一层引号：cmd /c 会剥掉「以引号开头的整串」的最外一对引号，剥掉后重定向被吃掉、
    # 进程静默不启动（实测 listeners=0 且日志 0 字节）。见 deploy-prod.ps1 Start-Instance 注释。
    $null = Start-Process -FilePath $env:ComSpec -ArgumentList @('/c', ('"' + $inner + '"')) `
        -WorkingDirectory $sandbox -WindowStyle Hidden
    Start-Sleep -Seconds 2
}

# 用 PowerShell 5.1 起子脚本，捕获退出码与全部输出
function Invoke-Deploy([string[]]$DeployArgs) {
    $call = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $ScriptPath) + $DeployArgs
    $out = & powershell @call 2>&1
    return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = ($out | Out-String) }
}

# 断言失败时把子脚本的完整输出打出来——不然「退出码不符」这句话里没有可诊断的信息，
# 只能重跑一次去猜（本排练第一版就是这么丢掉两条失败证据的）。
function Show-DeployOutput($r, [string]$why) {
    Write-Host ('  [INFO] ' + $why + '，子脚本输出：') -ForegroundColor DarkGray
    foreach ($ln in @(($r.Output -split "`r?`n") | Select-Object -Last 25)) {
        Write-Host ('         | ' + $ln) -ForegroundColor DarkGray
    }
}

function New-Sandbox([string]$exe) {
    if (-not (Clear-SandboxDir)) {
        Write-Host ('  [FAIL] 沙箱目录清不干净（有进程占用）：' + $sandbox) -ForegroundColor Red
        [void]$failures.Add('沙箱目录清不干净')
        return $false
    }
    foreach ($d in @($sandbox, (Join-Path $sandbox 'auths'), (Join-Path $sandbox 'data'))) {
        New-Item -ItemType Directory -Force -Path $d | Out-Null
    }
    $cfg = @'
{
  "listen": "127.0.0.1:7864",
  "api_key": "rehearsal-key",
  "auth_dir": "./auths",
  "state_file": "./data/state.json",
  "logging": { "request_archive_enabled": false },
  "schedule": {
    "checkin_enabled": false, "growth_enabled": false, "travel_enabled": false,
    "activity_enabled": false, "keepalive_enabled": false, "blackcat_enabled": false,
    "balance_refresh_enabled": false
  }
}
'@
    [System.IO.File]::WriteAllText((Join-Path $sandbox 'config.json'), $cfg)
    Copy-Item -LiteralPath $exe -Destination (Join-Path $sandbox 'yg-gateway.exe')
    return $true
}

# --------------------------------------------------------------------------
Section '前置检查'
# --------------------------------------------------------------------------
if (-not (Test-Path -LiteralPath $ScriptPath)) {
    Write-Host ('  [FAIL] 找不到部署脚本：' + $ScriptPath) -ForegroundColor Red
    exit 2
}
Check $true ('部署脚本存在：' + $ScriptPath)
if (-not (Test-Path -LiteralPath $GoodExe)) {
    Write-Host ('  [FAIL] 找不到已知可用的 exe：' + $GoodExe) -ForegroundColor Red
    exit 2
}
$goodHash = Sha256 $GoodExe
Check $true ('已知可用 exe：' + $GoodExe + '  sha256=' + $goodHash.Substring(0, 16))

$otherHash = ''
if (Test-Path -LiteralPath $OtherExe) {
    $otherHash = Sha256 $OtherExe
    Check ($otherHash -ne $goodHash) ('第二个可跑 exe 与第一个不同：' + $otherHash.Substring(0, 16))
} else {
    Write-Host ('  [WARN] 没有第二个可跑 exe（' + $OtherExe + '）：场景 B 只验机制、不验哈希变化') -ForegroundColor Yellow
}

# 沙箱端口必须空闲
if (Get-SandboxListenerPid $port) {
    Write-Host ('  [FAIL] 沙箱端口 ' + $port + ' 已被占用，先清理再跑') -ForegroundColor Red
    exit 2
}
Check $true ('沙箱端口 ' + $port + ' 空闲')

# --------------------------------------------------------------------------
Section '场景 A1：坏 exe（不是合法 PE）→ 预检必须在停服前拒绝，生产零触碰'
# --------------------------------------------------------------------------
try {
    [void](New-Sandbox $GoodExe)
    $exePath = Join-Path $sandbox 'yg-gateway.exe'

    # 先手动把沙箱服务拉起来（模拟「生产上正跑着旧版」）
    Start-SandboxInstance 'pre-a1'
    $pidBefore = Get-SandboxListenerPid $port
    Check ([bool]$pidBefore) '沙箱服务已启动（前置状态：旧版在跑）'

    # 造一个「不是合法 PE」的坏 exe（33 字节文本，比 10MB 的网关小两个数量级）
    $badExe = Join-Path $sandbox 'bad.exe'
    [System.IO.File]::WriteAllText($badExe, 'this is not a portable executable')

    $report = Join-Path $reports 'report-a1.json'
    $r = Invoke-Deploy @('-Exe', $badExe, '-ProdDir', $sandbox, '-Port', "$port",
        '-Feature', 'rehearsal-a1', '-HealthTimeoutSec', '5', '-MinHealthy', '0', '-SkipSmoke',
        '-ReportPath', $report, '-LogDir', $sandbox)

    $exitOk = ($r.ExitCode -eq 1)
    Check $exitOk ('退出码 = 1（预检拒绝，未进入换版流程），实际 ' + $r.ExitCode)
    if (-not $exitOk) { Show-DeployOutput $r 'A1 退出码不符' }

    if (Test-Path -LiteralPath $report) {
        $j = Get-Content -LiteralPath $report -Raw | ConvertFrom-Json
        Check ($j.stage -eq 'rejected-not-pe') ('报告 stage = rejected-not-pe，实际 ' + $j.stage)
        Check ($j.rolled_back -eq $false) '报告 rolled_back = false（根本没换，谈不上回滚）'
        Check ($j.backup_path -eq '') '报告 backup_path 为空'
    } else {
        Check $false '产出了 JSON 报告'
    }

    # 「生产零触碰」的四条硬证据：没备份、没替换、没停服、没下发启动命令
    $baks = @(Get-ChildItem -LiteralPath $sandbox -Filter 'yg-gateway.exe.pre-*.bak' -ErrorAction SilentlyContinue)
    Check ($baks.Count -eq 0) '没有产生备份（= 没进流程）'
    Check ((Sha256 $exePath) -eq $goodHash) '沙箱 exe 未被替换'
    $staged = @(Get-ChildItem -LiteralPath $sandbox -Filter '*.staged' -ErrorAction SilentlyContinue)
    Check ($staged.Count -eq 0) '没有 .staged 残留'
    $newLogs = @(Get-ChildItem -LiteralPath $sandbox -Filter 'yg-gateway-*.err.log' -ErrorAction SilentlyContinue)
    Check ($newLogs.Count -eq 0) '没有产生新实例的日志文件（= 没下发启动命令）'
    Start-Sleep -Milliseconds 500
    $pidAfter = Get-SandboxListenerPid $port
    Check ($pidAfter -eq $pidBefore) ('旧版仍在跑且是同一个进程（PID ' + $pidBefore + ' → ' + $pidAfter + '，= 没停服）')
} finally {
    Stop-Sandbox
}

# --------------------------------------------------------------------------
Section '场景 A2：坏 exe（合法 PE 但立刻退出）→ 健康轮询超时 → 必须自动回滚'
# --------------------------------------------------------------------------
try {
    [void](New-Sandbox $GoodExe)
    $exePath = Join-Path $sandbox 'yg-gateway.exe'

    $p = Start-SandboxInstance 'pre-a2'
    Check ([bool](Get-SandboxListenerPid $port)) '沙箱服务已启动（前置状态：旧版在跑）'

    # where.exe 是合法 PE，但没有 -config 参数、立刻退出 → 永远不监听
    $badExe2 = Join-Path $sandbox 'where.exe'
    Copy-Item 'C:\Windows\System32\where.exe' -Destination $badExe2

    $report = Join-Path $reports 'report-a2.json'
    $r = Invoke-Deploy @('-Exe', $badExe2, '-ProdDir', $sandbox, '-Port', "$port",
        '-Feature', 'rehearsal-a2', '-HealthTimeoutSec', '5', '-MinHealthy', '0', '-SkipSmoke',
        '-ReportPath', $report, '-LogDir', $sandbox)

    $exitOk2 = ($r.ExitCode -eq 2)
    Check $exitOk2 ('退出码 = 2（健康超时后回滚），实际 ' + $r.ExitCode)
    if (-not $exitOk2) { Show-DeployOutput $r 'A2 退出码不符' }
    if (Test-Path -LiteralPath $report) {
        $j = Get-Content -LiteralPath $report -Raw | ConvertFrom-Json
        Check ($j.rolled_back -eq $true) '报告 rolled_back = true'
        Check ($j.stage -eq 'rolled-back') ('报告 stage = rolled-back，实际 ' + $j.stage)
        Check ($j.backup_path -and (Test-Path -LiteralPath $j.backup_path)) '备份文件存在（说明确实替换过才回滚）'
        if ($j.backup_path -and (Test-Path -LiteralPath $j.backup_path)) {
            Check ((Sha256 $j.backup_path) -eq $goodHash) '备份内容 = 替换前的旧 exe（哈希一致）'
        }
        Check ($j.rollback_log_path -and $j.rollback_log_path -ne $j.log_path) '回滚用了独立的日志文件（不覆盖失败现场）'
        Check ((Sha256 $exePath) -eq $goodHash) '沙箱 exe 已恢复成旧版'
    } else {
        Check $false '产出了 JSON 报告'
    }
    Start-Sleep -Seconds 1
    Check ([bool](Get-SandboxListenerPid $port)) '回滚后服务重新在跑'
} finally {
    Stop-Sandbox
}

# --------------------------------------------------------------------------
Section '场景 B：好 exe → 替换生效 + 备份是旧版'
# --------------------------------------------------------------------------
try {
    [void](New-Sandbox $GoodExe)
    $exePath = Join-Path $sandbox 'yg-gateway.exe'

    $p = Start-SandboxInstance 'pre-b'
    Check ([bool](Get-SandboxListenerPid $port)) '沙箱服务已启动（前置状态：旧版在跑）'

    $newExe = if ($otherHash) { $OtherExe } else { $GoodExe }
    $report = Join-Path $reports 'report-b.json'
    # -MinHealthy 0：沙箱 0 账号，/healthz 返回 503 且 healthy=0 —— 这里验的是「进程活着并能出 JSON」
    $bSw = [System.Diagnostics.Stopwatch]::StartNew()
    $r = Invoke-Deploy @('-Exe', $newExe, '-ProdDir', $sandbox, '-Port', "$port",
        '-Feature', 'rehearsal-b', '-HealthTimeoutSec', '15', '-MinHealthy', '0', '-SkipSmoke',
        '-ReportPath', $report, '-LogDir', $sandbox)
    $bSw.Stop()

    # 场景 B 兼作「防拖死」回归断言：部署成功后新实例**继续常驻**，若 Start-Instance 用
    # Start-Process -RedirectStandard*，孙子进程会持有调用方 stdout 管道 → 这个 & 调用
    # 会一直阻塞到网关退出。定时上界就是牙齿。
    Check ($bSw.ElapsedMilliseconds -lt 30000) ('部署调用及时返回，未被常驻子进程拖死（' + $bSw.ElapsedMilliseconds + 'ms < 30000ms）')

    $exitOkB = ($r.ExitCode -eq 0)
    Check $exitOkB ('退出码 = 0（部署成功），实际 ' + $r.ExitCode + '（-SkipSmoke 时不因冒烟失败）')
    if (-not $exitOkB) { Show-DeployOutput $r 'B 退出码不符' }
    if (Test-Path -LiteralPath $report) {
        $j = Get-Content -LiteralPath $report -Raw | ConvertFrom-Json
        Check ($j.health_ok -eq $true) '报告 health_ok = true'
        Check ($j.rolled_back -eq $false) '报告 rolled_back = false'
        Check ($j.skipped_smoke -eq $true) '报告 skipped_smoke = true'
        Check ($j.backup_path -and (Test-Path -LiteralPath $j.backup_path)) '备份文件存在'
        if ($j.backup_path -and (Test-Path -LiteralPath $j.backup_path)) {
            Check ((Sha256 $j.backup_path) -eq $goodHash) '备份 = 替换前的旧 exe（旧版哈希）'
        }
        Check ($j.exe_hash_after -eq (Sha256 $exePath)) '报告里的新哈希 = 磁盘上的新 exe'
    } else {
        Check $false '产出了 JSON 报告'
    }
    Check ((Sha256 $exePath) -eq (Sha256 $newExe)) '沙箱 exe = 传入的新 exe（替换真的发生了）'
    if ($otherHash) { Check ((Sha256 $exePath) -ne $goodHash) '新 exe 与旧 exe 哈希不同（替换可见）' }
    Start-Sleep -Seconds 1
    Check ([bool](Get-SandboxListenerPid $port)) '新版本在跑（端口有监听）'
} finally {
    Stop-Sandbox
}

# --------------------------------------------------------------------------
Section '场景 C：-DryRun 不得改动任何东西'
# --------------------------------------------------------------------------
try {
    [void](New-Sandbox $GoodExe)
    $exePath = Join-Path $sandbox 'yg-gateway.exe'
    $before = Sha256 $exePath
    $r = Invoke-Deploy @('-Exe', $OtherExe, '-ProdDir', $sandbox, '-Port', "$port",
        '-Feature', 'rehearsal-c', '-DryRun', '-LogDir', $sandbox,
        '-ReportPath', (Join-Path $reports 'report-c-dryrun.json'))
    Check ($r.ExitCode -eq 0) ('退出码 = 0，实际 ' + $r.ExitCode)
    Check ((Sha256 $exePath) -eq $before) 'DryRun 后 exe 未被替换'
    $baks = @(Get-ChildItem -LiteralPath $sandbox -Filter '*.bak' -ErrorAction SilentlyContinue)
    Check ($baks.Count -eq 0) 'DryRun 未产生备份'
    Check (-not (Get-SandboxListenerPid $port)) 'DryRun 未启动任何进程'
} finally {
    Stop-Sandbox
    [void](Clear-SandboxDir)
}

# --------------------------------------------------------------------------
Section '结果'
# --------------------------------------------------------------------------
if ($failures.Count -gt 0) {
    Write-Host ('排练未通过（' + $failures.Count + ' 项断言失败）：') -ForegroundColor Red
    foreach ($f in $failures) { Write-Host ('   - ' + $f) -ForegroundColor Yellow }
    exit 1
}
Write-Host '排练全部通过（A1 非 PE 被预检拒绝且生产零触碰、A2 坏 PE 自动回滚、B 替换生效且不拖死调用方、C DryRun 无副作用）。' -ForegroundColor Green
Write-Host ('报告留档：' + $reports) -ForegroundColor Gray
exit 0
