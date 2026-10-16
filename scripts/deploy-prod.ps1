# deploy-prod.ps1 — 生产换版一条命令：备份 → 等端口释放 → 替换 → 启动 → 健康 → 冒烟 → 失败自动回滚
#
# 为什么需要（LESSONS.md L-9，Seen 1 但代价是「生产中断 1 分钟」）：
# 换版流程写在文档里没用——下次仍会凭记忆做，而凭记忆做的时候漏掉的恰好是
# 「taskkill 返回后监听套接字还没释放」这一步。把流程固化成脚本，漏不掉。
#
# 本脚本落实 L-9 的三条规则：
#   1. 换版前**轮询端口释放**（不是拍固定秒数）；
#   2. 新实例的 **stderr 落到独立文件**，回滚那次写在**另一个**文件（否则回滚覆盖失败现场）；
#   3. 健康轮询超时 → **自动回滚**并重新拉起备份版本，把中断限制在一次尝试之内。
#
# 外加：备份（R15）、冒烟（/v1/models + 流式/非流式各一发）、机器可读的 JSON 报告。
#
# 用法：
#   powershell -NoProfile -File scripts/deploy-prod.ps1 -Feature t8-unknown
#   powershell -NoProfile -File scripts/deploy-prod.ps1 -Build -Feature t8-unknown   # 先 go build 再部署
#   powershell -NoProfile -File scripts/deploy-prod.ps1 -DryRun                      # 只打印计划，不动任何东西
#
# 退出码：
#   0 = 部署成功（含冒烟通过；-SkipSmoke 时不因冒烟失败）
#   1 = 前置检查失败 / 停旧实例失败（生产未被改动或已恢复原状）
#   2 = 新实例健康检查失败 → **已自动回滚**（服务回到旧版）
#   3 = 部署+健康都过，但**冒烟失败**（不回滚：冒烟失败可能是上游账号问题，回滚会来回折腾）
#   4 = 回滚也失败或未预期错误（**生产可能不可用，看 stderr**）
#
# 已知边界（明说的局限，不是放水）：
#   - 不做「端口释放」以外的进程清理：如果还有别的进程占着 7863，脚本会明确报错而不是替你杀；
#   - 冒烟只覆盖一次非流式 + 一次流式，不做全模型矩阵（那是 CI 的事）；
#   - 从 config.json 取 api_key 用**正则**而不是 ConvertFrom-Json：生产 config.json 里存在
#     大小写变体的重复键（如 'Kimi-K2.7-Code' / 'kimi-k2.7-code'），PowerShell 5.1 的
#     ConvertFrom-Json 会直接抛错，取不到任何字段；
#   - 回滚用备份 exe 重新拉起，**不**回滚 config.json（配置不在本脚本职责内）。

param(
    [string]$Exe = '',
    [string]$ProdDir = 'D:\yg-gateway',
    [string]$Feature = '',
    [int]$Port = 7863,
    [int]$HealthTimeoutSec = 45,
    [int]$MinHealthy = 1,
    [int]$PortReleaseTimeoutSec = 30,
    [int]$SmokeTimeoutSec = 60,
    [string]$SmokeModel = '',
    [string]$LogDir = '',
    [string]$ReportPath = '',
    [switch]$Build,
    [switch]$SkipSmoke,
    [switch]$DryRun
)

$ErrorActionPreference = 'Continue'
$repoRoot = Split-Path -Parent $PSScriptRoot

if (-not $LogDir) { $LogDir = $env:TEMP }
if (-not (Test-Path -LiteralPath $LogDir)) { New-Item -ItemType Directory -Force -Path $LogDir | Out-Null }

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$dateStamp = Get-Date -Format 'yyyyMMdd'

$goExe = 'C:\Program Files\Go\bin\go.exe'
if (-not (Test-Path $goExe)) { $goExe = 'go' }

# 报告一律落盘（即使脚本中途挂掉也能看到它走到了哪一步）——rehearsal 与人工排查都读它。
$state = [ordered]@{
    plan                 = 'docs/superpowers/plans/HANDOFF-2026-10-16-ci-closed.md 3-N1'
    timestamp            = (Get-Date -Format 'yyyy-MM-dd HH:mm:ss')
    dry_run              = [bool]$DryRun
    prod_dir             = $ProdDir
    port                 = $Port
    source_exe           = ''
    source_exe_hash      = ''
    exe_hash_before      = ''
    exe_hash_after       = ''
    backup_path          = ''
    backup_hash          = ''
    stage                = 'preflight'
    health_ok            = $false
    health_status        = 0
    healthy              = $null
    total                = $null
    rolled_back          = $false
    rollback_failed      = $false
    skipped_smoke        = [bool]$SkipSmoke
    smoke_ok             = $false
    smoke                = @()
    log_path             = ''
    rollback_log_path    = ''
    elapsed_sec          = 0
    error                = ''
}
$exitCode = 4
$sw = [System.Diagnostics.Stopwatch]::StartNew()

function Write-Step($msg) { Write-Host ('  ' + $msg) }
function Write-Ok($msg) { Write-Host ('  [OK]   ' + $msg) -ForegroundColor Green }
function Write-Bad($msg) { Write-Host ('  [FAIL] ' + $msg) -ForegroundColor Red }
function Write-Warn2($msg) { Write-Host ('  [WARN] ' + $msg) -ForegroundColor Yellow }
function Write-Head($msg) { Write-Host ''; Write-Host ('--- ' + $msg) -ForegroundColor Cyan }

function Sha256($path) {
    if ($path -and (Test-Path -LiteralPath $path)) { return (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash }
    return ''
}

# 端口 → 监听进程 PID（netstat 的 LISTENING 行）
# 用 Where-Object 过滤原始行，不用串联 Select-String：串联时第二段拿到的是 MatchInfo
# 而不是字符串，.Line 的语义取决于实现细节，不值得依赖。
function Get-ListenerPid([int]$p) {
    $lines = @(netstat -ano | Where-Object { $_ -match (':' + $p + '\s') -and $_ -match 'LISTENING' })
    if ($lines.Count -eq 0) { return $null }
    $cols = @($lines[0].Trim() -split '\s+')
    return $cols[$cols.Length - 1]
}

function Wait-PortReleased([int]$p, [int]$timeoutSec) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-ListenerPid $p)) { return $true }
        Start-Sleep -Milliseconds 500
    }
    return (-not (Get-ListenerPid $p))
}

function Stop-Listener([int]$p) {
    $targetPid = Get-ListenerPid $p
    if (-not $targetPid) { return $true }
    taskkill /PID $targetPid /F 2>&1 | Out-Null
    return (Wait-PortReleased $p $PortReleaseTimeoutSec)
}

# 一次 HTTP 请求：**非 2xx 也要拿到 body**（/healthz 在 0 账号时返回 503，
# 只认 200 会把「起来了但没账号」误判成「没起来」，进而触发不该发生的回滚）。
#
# 为什么不用 Invoke-WebRequest：PS 5.1 的 Invoke-WebRequest 在非 2xx 时**已经把响应流读掉**
# 了（把 body 塞进 ErrorDetails），再读 $_.Exception.Response.GetResponseStream() 只能拿到
# 空串 → body 为空 → 「服务活着」被判成「没响应」。代价是排练里所有场景都误触发回滚、
# 报出 14 项假失败。直连 System.Net 就没有这一层：状态码与 body 都是我们自己读的。
function Get-HttpResult([string]$url, [string]$method = 'GET', [string]$body = '', [hashtable]$headers = $null, [int]$timeoutSec = 5) {
    $req = [System.Net.HttpWebRequest]::Create($url)
    $req.Method = $method
    $req.Timeout = $timeoutSec * 1000
    $req.ReadWriteTimeout = $timeoutSec * 1000
    $req.AllowAutoRedirect = $true
    if ($headers) { foreach ($k in $headers.Keys) { $req.Headers[$k] = $headers[$k] } }
    if ($body) {
        $req.ContentType = 'application/json'
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($body)
        $req.ContentLength = $bytes.Length
        $rs = $req.GetRequestStream()
        $rs.Write($bytes, 0, $bytes.Length)
        $rs.Close()
    }
    $resp = $null
    try {
        $resp = $req.GetResponse()
    } catch [System.Net.WebException] {
        $resp = $_.Exception.Response
    } catch {
        return $null
    }
    if ($null -eq $resp) { return $null }
    try {
        $status = [int]$resp.StatusCode
        $sr = New-Object System.IO.StreamReader($resp.GetResponseStream())
        $text = $sr.ReadToEnd()
        $sr.Close()
        return [pscustomobject]@{ Status = $status; Body = $text }
    } catch {
        return $null
    } finally {
        $resp.Close()
    }
}

# 复制目标可能是「刚被 taskkill 的 exe」——Windows 上句柄释放有一小段延迟，
# 一次性 Copy-Item 会偶发 "file is being used by another process"（排练 A1 实测到）。
# 重试是正当修法：不改判据、只等释放，超时仍然失败并报明确原因。
function Copy-WithRetry([string]$src, [string]$dst, [int]$timeoutSec = 10) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    $lastErr = ''
    while ((Get-Date) -lt $deadline) {
        try {
            Copy-Item -LiteralPath $src -Destination $dst -Force -ErrorAction Stop
            return ''
        } catch {
            $lastErr = $_.Exception.Message
            Start-Sleep -Milliseconds 400
        }
    }
    return $lastErr
}

# 换二进制必须走**改名替换**，不能直接覆盖目标名。
#
# 为什么（受控实验实测，$env:TEMP\exp-lock2 形态）：坏 exe 的 cmd 会**长期**持有
# yg-gateway.exe 的句柄（实验里 15s 以上不释放、err 日志 0 字节）。此时
#   (i)  `Copy-Item 好exe -> yg-gateway.exe -Force`        → **失败**（file is being used by another process）
#   (ii) `Copy-Item 好exe -> yg-gateway.exe.staged` + `Move-Item -Force yg-gateway.exe` → **成功**
# 差别在于：覆盖要拿目标文件的写权限，改名只需要 FILE_SHARE_DELETE。而**回滚写不回去**
# 恰恰发生在最需要它的时候（新版本起不来 → 必须恢复旧版本）——那是换版脚本唯一的作用。
function Replace-Binary([string]$src, [string]$dst) {
    $staged = $dst + '.staged'
    $err = Copy-WithRetry $src $staged 20
    if ($err) { return $err }
    try {
        Move-Item -LiteralPath $staged -Destination $dst -Force -ErrorAction Stop
        return ''
    } catch {
        $msg = $_.Exception.Message
        Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
        return $msg
    }
}

function Test-HealthOnce([int]$p) {
    $r = Get-HttpResult ('http://127.0.0.1:' + $p + '/healthz') -timeoutSec 3
    if ($null -eq $r) { return $null }
    $healthy = $null; $total = $null
    if ($r.Body -match '"healthy"\s*:\s*(\d+)') { $healthy = [int]$Matches[1] }
    if ($r.Body -match '"total"\s*:\s*(\d+)') { $total = [int]$Matches[1] }
    $isGateway = ($r.Body -match '"service"\s*:\s*"yg-gateway"')
    $ok = $isGateway
    if ($MinHealthy -gt 0) { $ok = $ok -and ($null -ne $healthy) -and ($healthy -ge $MinHealthy) }
    return [pscustomobject]@{ Ok = $ok; Status = $r.Status; Healthy = $healthy; Total = $total; Body = $r.Body }
}

function Wait-Health([int]$p, [int]$timeoutSec) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    $last = $null
    while ((Get-Date) -lt $deadline) {
        $last = Test-HealthOnce $p
        if ($last -and $last.Ok) { return $last }
        Start-Sleep -Seconds 1
    }
    return $last
}

function Update-PidFile([int]$p) {
    $targetPid = Get-ListenerPid $p
    if (-not $targetPid) { return }
    $pidFile = Join-Path (Join-Path $ProdDir 'data') 'yg-gateway.pid'
    $parent = Split-Path -Parent $pidFile
    if (-not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [System.IO.File]::WriteAllText($pidFile, $targetPid)
}

# 预检：候选 exe 必须是 Windows PE（MZ 头 + PE\0\0 签名），否则**根本不进换版流程**。
#
# 为什么必须有这一步（受控实验的现场证据）：把一个 33 字节的文本文件当 exe 启动时，
# Windows 的兼容层会弹出**模态对话框**「不支持的 16 位应用程序」，而 launcher（cmd）
# 会一直阻塞等用户点「确定」。后果是一串连锁反应：
#   - err 日志 0 字节（错误进了对话框，没进重定向的 stderr）→ 现场无日志可查；
#   - 那个 exe 文件被持续占住 → 直接 Copy-Item 覆盖式回滚失败（见 Replace-Binary 注释）；
#   - launcher 进程永久残留，桌面上还留一个需要人工点的窗口。
# 换版脚本不该让一次「文件搞错了」变成「生产停住 + 桌上一个弹窗」：预检把这类错误
# 挡在**停服之前**——生产连碰都没碰到。
#
# 判据只留 MZ + PE 签名，**不设体积下限**：体积是另一类错误（拿错文件）的证据，而阈值必然
# 武断——排练里那个「合法 PE 但立刻退出」的对照件（where.exe，64KB）就会被误杀，
# 从而把「健康轮询 → 回滚」这条真正要紧的路径变成不可测。体积与哈希照常写进报告与标准输出，
# 由人看。
#
# 边界：预检只能证明「是个合法 PE」。头部完好但内容损坏的 exe 仍会进入流程，
# 由健康轮询兜底（排练 A2 覆盖那条路）。
function Test-PeImage([string]$path) {
    try {
        $fs = [System.IO.File]::Open($path, 'Open', 'Read', 'ReadWrite')
    } catch {
        return ('无法打开：' + $_.Exception.Message)
    }
    try {
        $br = New-Object System.IO.BinaryReader($fs)
        $mz = $br.ReadBytes(2)
        if ($mz.Length -lt 2 -or $mz[0] -ne 0x4D -or $mz[1] -ne 0x5A) { return '缺少 MZ 头，不是 Windows 可执行文件' }
        $fs.Position = 0x3C
        $peOffset = $br.ReadInt32()
        if ($peOffset -le 0 -or $peOffset -gt ($fs.Length - 6)) { return ('PE 头偏移越界（' + $peOffset + '）') }
        $fs.Position = $peOffset
        $sig = $br.ReadBytes(4)
        if ($sig.Length -lt 4 -or $sig[0] -ne 0x50 -or $sig[1] -ne 0x45 -or $sig[2] -ne 0 -or $sig[3] -ne 0) {
            return '缺少 PE 签名，不是有效的 Windows 可执行文件'
        }
        return ''
    } catch {
        return ('读取失败：' + $_.Exception.Message)
    } finally {
        $fs.Close()
    }
}

# 启动新实例：**借 cmd.exe 做重定向**，不用 Start-Process 的 -RedirectStandardError。
#
# 受控实验（scripts/deploy-prod.rehearsal.ps1 场景 D 把它固化成回归断言）：
#   - `Start-Process -RedirectStandardError e.log` 的日志**确实写出**（实验里 15 字节），
#     但长驻的孙子进程**仍持有调用方 stdout 管道的写端** → 用 `& powershell -File deploy-prod.ps1`
#     捕获输出的调用方会一直阻塞到网关退出为止。实验数据：内层脚本 3s 结束，外层阻塞
#     **8865ms**（≈ 孙子进程的 8s 睡眠）；换成 cmd 重定向后外层 **3716ms** 正常返回。
#   - 危害：CI / agent harness / 任何「捕获输出」的调用方都会白等一个常驻服务的寿命。
#   - `cmd /c "<exe>" -config "<cfg>" > out 2> err` 把孙子的句柄直接接到文件上，与调用方
#     stdio 彻底解耦。这也正是 start.bat 的 `start ... >> data\stdout.log` 不落输出的反面
#     ——那个重定向加在 start 自己身上，子进程拿到的是一个新控制台（L-9 的附带发现）。
#
# 注意：这里**不加 -PassThru**。PS 5.1 里请求 -PassThru 会不会把 UseShellExecute 拉成
# false（从而重新继承句柄）没有文档保证，不赌；PID 一律靠轮询端口拿（Update-PidFile）。
#
# 注意 2：整串**必须再包一层引号**。`cmd /c "C:\...\x.exe" -config ...` 里整串以引号开头，
# cmd 的引号规则会剥掉最外一对引号，连带把重定向目标吃掉 → **静默不启动**：进程没起来、
# 日志 0 字节、**没有任何报错**。受控实验（$env:TEMP\exp-launch 形态）：
#   v1 `@('/c', $inner)`        → listeners=0，输出/错误日志均 0 字节（失败且无声）
#   v2 `@('/c', '"' + $inner + '"')` → listeners=1，stderr 1047 字节（正确）
#   v3 写 .cmd 启动器再 /c 它   → listeners=1，stderr 1047 字节（同样正确，但多一个文件）
# 取 v2（一行解决）。rehearsal 的每个场景都会断言「端口有监听」，v1 那种静默失败会被它逮住。
#
# 边界：cmd 的重定向对路径里的 `&`/`^`/`%` 敏感；本项目生产路径不含这些字符。
function Start-Instance([string]$exePath, [string]$configPath, [string]$outLog, [string]$errLog) {
    $inner = '"' + $exePath + '" -config "' + $configPath + '" > "' + $outLog + '" 2> "' + $errLog + '"'
    try {
        $null = Start-Process -FilePath $env:ComSpec -ArgumentList @('/c', ('"' + $inner + '"')) `
            -WorkingDirectory $ProdDir -WindowStyle Hidden
        return [pscustomobject]@{ Ok = $true; Error = '' }
    } catch {
        return [pscustomobject]@{ Ok = $false; Error = $_.Exception.Message }
    }
}

function Save-Report() {
    $state.elapsed_sec = [math]::Round($sw.Elapsed.TotalSeconds, 1)
    if (-not $ReportPath) { $ReportPath = Join-Path $LogDir ('deploy-prod-' + $stamp + '.json') }
    try {
        $json = $state | ConvertTo-Json -Depth 6
        [System.IO.File]::WriteAllText($ReportPath, $json)
    } catch {
        Write-Warn2 ('报告写入失败：' + $_.Exception.Message)
    }
    Write-Step ('报告：' + $ReportPath)
}

# ===========================================================================
Write-Host '=== 生产换版 ===' -ForegroundColor Cyan
Write-Host ('  目标：' + $ProdDir + '  端口：' + $Port + '  备份后缀：' + $(if ($Feature) { $Feature } else { '(自动)' })) -ForegroundColor Gray

try {
    # -----------------------------------------------------------------------
    Write-Head '前置检查'
    # -----------------------------------------------------------------------
    if (-not (Test-Path -LiteralPath $ProdDir)) {
        Write-Bad ('生产目录不存在：' + $ProdDir); $state.error = 'prod dir missing'; $exitCode = 1; return
    }
    $targetExe = Join-Path $ProdDir 'yg-gateway.exe'
    $configPath = Join-Path $ProdDir 'config.json'
    if (-not (Test-Path -LiteralPath $targetExe)) {
        Write-Bad ('生产 exe 不存在：' + $targetExe + '（首次部署请手工放置，本脚本只负责换版）'); $state.error = 'target exe missing'; $exitCode = 1; return
    }
    if (-not (Test-Path -LiteralPath $configPath)) {
        Write-Bad ('生产配置不存在：' + $configPath); $state.error = 'config missing'; $exitCode = 1; return
    }
    Write-Ok ('生产 exe / 配置存在')

    if (-not $Exe) { $Exe = Join-Path $repoRoot 'yg-gateway.exe' }
    if ($Build) {
        Write-Step ('-Build：go build → ' + $Exe)
        $env:GOPROXY = 'https://goproxy.cn,direct'
        $env:CGO_ENABLED = '0'
        Push-Location $repoRoot
        $buildOut = @(& $goExe build -trimpath '-ldflags=-s -w' -o $Exe ./cmd/server 2>&1)
        $buildCode = $LASTEXITCODE
        Pop-Location
        if ($buildCode -ne 0) {
            $buildOut | Select-Object -First 20 | ForEach-Object { Write-Host ('    ' + $_) -ForegroundColor DarkGray }
            Write-Bad 'go build 失败'; $state.error = 'go build failed'; $exitCode = 1; return
        }
        Write-Ok 'go build 完成'
    }
    if (-not (Test-Path -LiteralPath $Exe)) {
        Write-Bad ('新 exe 不存在：' + $Exe + '（先 go build，或加 -Build）'); $state.error = 'source exe missing'; $exitCode = 1; return
    }
    if ((Resolve-Path -LiteralPath $Exe).Path -eq (Resolve-Path -LiteralPath $targetExe).Path) {
        Write-Bad '新 exe 与生产 exe 是同一个文件：先构建到别处再部署（否则「换版」没发生，备份也无意义）'
        $state.error = 'source exe equals target exe'; $exitCode = 1; return
    }
    $state.source_exe = $Exe
    $state.source_exe_hash = Sha256 $Exe
    $state.exe_hash_before = Sha256 $targetExe
    Write-Ok ('新 exe：' + $Exe + '  sha256=' + $state.source_exe_hash.Substring(0, 16))
    Write-Step ('当前生产：sha256=' + $state.exe_hash_before.Substring(0, 16) + '  监听 PID=' + $(if (Get-ListenerPid $Port) { Get-ListenerPid $Port } else { '(无)' }))

    # 候选必须是合法 PE —— 挡在「停服」之前（理由见 Test-PeImage 注释）
    $peErr = Test-PeImage $Exe
    if ($peErr) {
        Write-Bad ('候选 exe 不是有效的 Windows 可执行文件：' + $peErr)
        Write-Step ('  候选：' + $Exe + '（' + (Get-Item -LiteralPath $Exe).Length + ' 字节）')
        Write-Step '  生产**未被触碰**：没停服、没备份、没替换'
        $state.stage = 'rejected-not-pe'
        $state.error = 'candidate is not a PE image: ' + $peErr
        $exitCode = 1
        return
    }
    Write-Ok '候选是合法 PE（MZ + PE 签名）'

    if ($DryRun) {
        Write-Head '-DryRun：只打印计划，不动任何东西'
        Write-Step ('1. 备份 ' + $targetExe + ' → ' + $targetExe + '.pre-<后缀>-' + $dateStamp + '.bak')
        Write-Step ('2. 停监听（端口 ' + $Port + '）并轮询端口释放（最多 ' + $PortReleaseTimeoutSec + 's）')
        Write-Step ('3. 替换为 ' + $Exe)
        Write-Step ('4. 启动并 -RedirectStandardError 到 ' + $LogDir)
        Write-Step ('5. 健康轮询 /healthz（最多 ' + $HealthTimeoutSec + 's，MinHealthy=' + $MinHealthy + '）')
        Write-Step ('6. 冒烟：' + $(if ($SkipSmoke) { '(已 -SkipSmoke)' } else { '/v1/models + 非流式 + 流式' }))
        Write-Step '7. 任一步失败 → 自动回滚到备份'
        $state.stage = 'dry-run'
        $exitCode = 0
        return
    }

    # -----------------------------------------------------------------------
    Write-Head '1/6 备份（R15：替换前必须留可恢复副本）'
    # -----------------------------------------------------------------------
    $feature = $Feature
    if (-not $feature) {
        $feature = 'auto'
        $gitSha = @(& git -C $repoRoot rev-parse --short HEAD 2>$null)
        if ($LASTEXITCODE -eq 0 -and $gitSha.Count -gt 0 -and $gitSha[0]) { $feature = [string]$gitSha[0] }
    }
    $backupPath = Join-Path $ProdDir ('yg-gateway.exe.pre-' + $feature + '-' + $dateStamp + '.bak')
    if (Test-Path -LiteralPath $backupPath) { $backupPath = Join-Path $ProdDir ('yg-gateway.exe.pre-' + $feature + '-' + $stamp + '.bak') }
    Copy-Item -LiteralPath $targetExe -Destination $backupPath -Force
    $state.backup_path = $backupPath
    $state.backup_hash = Sha256 $backupPath
    if ($state.backup_hash -ne $state.exe_hash_before) {
        Write-Bad '备份哈希与替换前不一致，中止（不拿一个可疑的备份去冒险）'
        $state.error = 'backup hash mismatch'; $exitCode = 1; return
    }
    Write-Ok ('备份：' + $backupPath + '  sha256=' + $state.backup_hash.Substring(0, 16))

    # -----------------------------------------------------------------------
    Write-Head '2/6 停旧实例 + 轮询端口释放（L-9 规则 1）'
    # -----------------------------------------------------------------------
    $pidFile = Join-Path (Join-Path $ProdDir 'data') 'yg-gateway.pid'
    if (Test-Path -LiteralPath $pidFile) {
        $oldPid = (Get-Content -LiteralPath $pidFile -Raw).Trim()
        if ($oldPid) { taskkill /PID $oldPid /F 2>&1 | Out-Null; Write-Step ('按 pid 文件停：' + $oldPid) }
        Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
    }
    if (-not (Stop-Listener $Port)) {
        Write-Bad ('端口 ' + $Port + ' 在 ' + $PortReleaseTimeoutSec + 's 内没有释放——**不动**生产 exe，手工查占用进程')
        $state.stage = 'stop-failed'
        $state.error = 'port not released'
        $exitCode = 1
        return
    }
    Write-Ok ('端口 ' + $Port + ' 已释放')

    # -----------------------------------------------------------------------
    Write-Head '3/6 替换二进制'
    # -----------------------------------------------------------------------
    $state.stage = 'replaced'
    $swapErr = Replace-Binary $Exe $targetExe
    if ($swapErr) {
        Write-Bad ('替换失败：' + $swapErr); $state.error = $swapErr; $exitCode = 1; return
    }
    $state.exe_hash_after = Sha256 $targetExe
    Write-Ok ('已替换：sha256=' + $state.exe_hash_after.Substring(0, 16))

    # -----------------------------------------------------------------------
    Write-Head '4/6 启动新实例（stderr 落独立文件）'
    # -----------------------------------------------------------------------
    $state.log_path = Join-Path $LogDir ('yg-gateway-' + $stamp + '.err.log')
    $outLog = Join-Path $LogDir ('yg-gateway-' + $stamp + '.out.log')
    $started = Start-Instance $targetExe $configPath $outLog $state.log_path
    if (-not $started.Ok) { Write-Warn2 ('启动命令本身抛错：' + $started.Error) } else { Write-Step '已下发启动命令' }

    # -----------------------------------------------------------------------
    Write-Head ('5/6 健康轮询 /healthz（最多 ' + $HealthTimeoutSec + 's）')
    # -----------------------------------------------------------------------
    $health = Wait-Health $Port $HealthTimeoutSec
    if ($health) {
        $state.health_status = $health.Status
        $state.healthy = $health.Healthy
        $state.total = $health.Total
    }
    if ($health -and $health.Ok) {
        $state.health_ok = $true
        $state.stage = 'healthy'
        Update-PidFile $Port
        Write-Ok ('健康：HTTP ' + $health.Status + ' healthy=' + $health.Healthy + '/' + $health.Total)
    } else {
        if ($health) { Write-Bad ('健康检查未达标：HTTP ' + $health.Status + ' healthy=' + $health.Healthy + '（要求 >= ' + $MinHealthy + '）') }
        else { Write-Bad '健康检查无任何响应' }

        # -------------------------------------------------------------------
        Write-Head '5b 自动回滚（用独立日志，不覆盖失败现场）'
        # -------------------------------------------------------------------
        $state.stage = 'rolling-back'
        $null = Stop-Listener $Port
        $rbErr = Replace-Binary $backupPath $targetExe
        if ($rbErr) {
            Write-Bad ('回滚替换失败：' + $rbErr + '——生产处于不可用状态')
            $state.stage = 'rollback-failed'
            $state.rollback_failed = $true
            $state.error = $rbErr
            $exitCode = 4
            return
        }
        $state.exe_hash_after = Sha256 $targetExe
        $state.rollback_log_path = Join-Path $LogDir ('yg-gateway-' + $stamp + '.rollback.err.log')
        $rbOutLog = Join-Path $LogDir ('yg-gateway-' + $stamp + '.rollback.out.log')
        $rbStart = Start-Instance $targetExe $configPath $rbOutLog $state.rollback_log_path
        if (-not $rbStart.Ok) { Write-Warn2 ('回滚启动命令抛错：' + $rbStart.Error) }
        $rbHealth = Wait-Health $Port $HealthTimeoutSec
        if ($rbHealth -and $rbHealth.Ok) {
            Update-PidFile $Port
            $state.rolled_back = $true
            $state.stage = 'rolled-back'
            Write-Ok ('已回滚到备份版本并通过健康检查（healthy=' + $rbHealth.Healthy + '/' + $rbHealth.Total + '）')
            Write-Step ('失败现场日志：' + $state.log_path)
            Write-Step ('回滚日志：' + $state.rollback_log_path)
        } else {
            $state.rollback_failed = $true
            $state.stage = 'rollback-failed'
            Write-Bad ('回滚后健康检查仍未通过——**生产可能不可用**')
            Write-Step ('失败现场日志：' + $state.log_path)
            Write-Step ('回滚日志：' + $state.rollback_log_path)
        }
        $exitCode = $(if ($state.rollback_failed) { 4 } else { 2 })
        return
    }

    # -----------------------------------------------------------------------
    Write-Head '6/6 冒烟'
    # -----------------------------------------------------------------------
    if ($SkipSmoke) {
        Write-Warn2 '-SkipSmoke：跳过冒烟（只适用于沙箱排练，生产换版不要用）'
        $state.skipped_smoke = $true
        $state.stage = 'done'
        $exitCode = 0
        return
    }

    # api_key 走正则：生产 config.json 有大小写变体的重复键，ConvertFrom-Json 会抛错
    $cfgText = Get-Content -LiteralPath $configPath -Raw
    $apiKey = ''
    if ($cfgText -match '"api_key"\s*:\s*"([^"]+)"') { $apiKey = $Matches[1] }
    $authHeaders = @{}
    if ($apiKey) { $authHeaders = @{ Authorization = ('Bearer ' + $apiKey) } }
    else { Write-Warn2 'config.json 里没读到 api_key，冒烟将不带鉴权（若被 401 就是它）' }

    $smoke = New-Object System.Collections.ArrayList
    $base = 'http://127.0.0.1:' + $Port

    # 冒烟 1：/v1/models
    $models = Get-HttpResult ($base + '/v1/models') -headers $authHeaders -timeoutSec $SmokeTimeoutSec
    $modelIds = @()
    if ($models -and $models.Status -eq 200) {
        try {
            $parsed = $models.Body | ConvertFrom-Json
            $modelIds = @($parsed.data | ForEach-Object { $_.id })
        } catch { $modelIds = @() }
    }
    $modelsOk = ($models -and $models.Status -eq 200 -and $modelIds.Count -gt 0)
    [void]$smoke.Add([pscustomobject]@{ name = '/v1/models'; ok = $modelsOk; detail = $(if ($modelsOk) { $modelIds.Count.ToString() + ' 个模型' } else { 'HTTP ' + $(if ($models) { $models.Status } else { '无响应' }) }) })
    if ($modelsOk) { Write-Ok ('/v1/models 200，' + $modelIds.Count + ' 个模型') } else { Write-Bad ('/v1/models 失败：HTTP ' + $(if ($models) { $models.Status } else { '无响应' })) }

    $useModel = $SmokeModel
    if (-not $useModel -and $modelIds.Count -gt 0) { $useModel = [string]$modelIds[0] }
    if (-not $useModel) {
        [void]$smoke.Add([pscustomobject]@{ name = 'chat'; ok = $false; detail = '拿不到可用模型，跳过对话冒烟' })
        Write-Bad '拿不到可用模型，跳过非流式/流式冒烟'
    } else {
        $payload = @{ model = $useModel; messages = @(@{ role = 'user'; content = 'ping' }); max_tokens = 16 } | ConvertTo-Json -Depth 5 -Compress
        $payloadStream = @{ model = $useModel; messages = @(@{ role = 'user'; content = 'ping' }); max_tokens = 16; stream = $true } | ConvertTo-Json -Depth 5 -Compress

        # 冒烟 2：非流式
        $nonStream = Get-HttpResult ($base + '/v1/chat/completions') -method 'POST' -body $payload -headers $authHeaders -timeoutSec $SmokeTimeoutSec
        $content = ''
        if ($nonStream -and $nonStream.Body) {
            # 分两步：`[string]($x | ConvertFrom-Json).choices[0]` 会被解析成
            # 先转字符串再取成员，取不到东西又不报错。
            try {
                $parsedNon = $nonStream.Body | ConvertFrom-Json
                $content = [string]$parsedNon.choices[0].message.content
            } catch { $content = '' }
        }
        $nonOk = ($nonStream -and $nonStream.Status -eq 200 -and $content.Trim().Length -gt 0)
        [void]$smoke.Add([pscustomobject]@{ name = '非流式'; ok = $nonOk; detail = $(if ($nonOk) { 'HTTP 200，content ' + $content.Length + ' 字符' } else { 'HTTP ' + $(if ($nonStream) { $nonStream.Status } else { '无响应' }) }) })
        if ($nonOk) { Write-Ok ('非流式冒烟：HTTP 200，content ' + $content.Length + ' 字符（' + $useModel + '）') } else { Write-Bad ('非流式冒烟失败：HTTP ' + $(if ($nonStream) { $nonStream.Status } else { '无响应' })) }

        # 冒烟 3：流式（SSE，必须有 data: 与 [DONE]）
        $stream = Get-HttpResult ($base + '/v1/chat/completions') -method 'POST' -body $payloadStream -headers $authHeaders -timeoutSec $SmokeTimeoutSec
        $body = ''
        if ($stream -and $stream.Body) { $body = $stream.Body }
        $streamOk = ($stream -and $stream.Status -eq 200 -and $body.Contains('data:') -and $body.Contains('[DONE]'))
        [void]$smoke.Add([pscustomobject]@{ name = '流式'; ok = $streamOk; detail = $(if ($streamOk) { 'HTTP 200，含 data: 与 [DONE]' } else { 'HTTP ' + $(if ($stream) { $stream.Status } else { '无响应' }) + '，无 data:/[DONE]' }) })
        if ($streamOk) { Write-Ok '流式冒烟：HTTP 200，含 data: 与 [DONE]' } else { Write-Bad ('流式冒烟失败：HTTP ' + $(if ($stream) { $stream.Status } else { '无响应' })) }
    }

    $state.smoke = @($smoke)
    $state.smoke_ok = (@($smoke | Where-Object { -not $_.ok }).Count -eq 0)
    if ($state.smoke_ok) {
        $state.stage = 'done'
        Write-Ok '冒烟三条全过'
        $exitCode = 0
    } else {
        $state.stage = 'smoke-failed'
        Write-Bad '冒烟未全过：**不回滚**（多为上游账号/额度问题，回滚会来回折腾）。备份仍在：' + $backupPath
        $exitCode = 3
    }
    return
} finally {
    Save-Report
    $sw.Stop()
    if ($exitCode -eq 0) {
        Write-Host ''
        Write-Host ('换版完成，用时 ' + $state.elapsed_sec + 's。') -ForegroundColor Green
    } elseif ($exitCode -eq 2) {
        Write-Host ''
        Write-Host ('换版失败，已自动回滚到旧版，用时 ' + $state.elapsed_sec + 's。') -ForegroundColor Yellow
    }
    exit $exitCode
}
