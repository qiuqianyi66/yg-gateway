# check-depth.ps1 — 工程深度度量与 ratchet 门禁
#
# 为什么需要：本仓功能宽度已足（18 包 / 53k 行），但深度指标在退化——
# 871 行的函数、2185 行的文件、核心层 56.7% 的覆盖率。没有度量，"深度"就是
# 主观词，每次改动都无法回答"是变好了还是变差了"。
#
# 设计取舍：**ratchet（棘轮）而不是绝对阈值**。
#   - 绝对阈值（如"函数必须 ≤120 行"）在存量代码上必然一次性全红，只能选择
#     "要么大重构要么放弃门禁"——现实中会退化成把阈值调到刚好通过。
#   - ratchet 只要求"不许比上次更差"，存量债不阻塞，新增债立即拦截。
#     指标变好时用 -Update 主动抬高棘轮（冻结收益）。
#
# 口径声明（两种「行数」不是一个东西，对外引用必须说清是哪种）：
#   - **文件行数** = **非空行**（`Get-Content | Measure-Object -Line`，不计空行）——
#     即本脚本 maxFileLines / oversizeFiles 用的口径。
#   - **函数行数** = 代码行（排除空行与 `//` 开头的行），见下方「度量 2/2」段。
#   实现上**显式过滤空白行**（`Where-Object { $_.Trim() -ne '' }`），不依赖 `Measure-Object -Line`
#   ——后者对空白行的处理属实现细节，把口径押在它上面会让棘轮在别的 PowerShell 版本/平台上假红假绿。
#   参照物：internal/upstream/client.go 实测 = **2185 非空行 / 2315 实际行**（差 130 空行）。
#   本脚本与 `.harness/rules/编码规范.md` 第九节必须同口径——改任一侧先改这里。
#
# 用法：
#   powershell -NoProfile -File scripts/check-depth.ps1          # 检查（CI / 提交前）
#   powershell -NoProfile -File scripts/check-depth.ps1 -Update  # 抬棘轮（仅当指标确实变好）
#   powershell -NoProfile -File scripts/check-depth.ps1 -Detail  # 打印各项明细
#   powershell -NoProfile -File scripts/check-depth.ps1 -CoverageStdout <file>
#       # 复用一份已有的 `go test ./... -cover` 输出（pre-commit 全量模式用），不再自跑测试
#
# 退出码：0 = 未回退；1 = 有指标回退；2 = 脚本自身失败
#   （「自身失败」含两种情况：①-Update/-CoverageStdout 用法错误；②自跑 `go test -cover` 失败，
#     即测试红 → 覆盖率数字不可信，不再给棘轮结论）

param(
    [switch]$Update,
    [switch]$Detail,
    # 复用的 `go test ./... -cover` 输出文件。`scripts/pre-commit.ps1` 全量模式已经跑过一次
    # 全量测试时传入，避免同一个门禁里把全量测试跑两遍（A/B 实测总差 66.4s，F-902）。
    # 传了就**不再自己起测试**：此时「测试是否绿」由调用方担保（调用方红了根本不会走到这里，
    # 且下方仍会防御性地扫一遍 FAIL 行）。
    # CI 的 gate job 不传本参数 → 行为与改动前一致：自己跑一次。
    [string]$CoverageStdout = ''
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# 路径一律用 Join-Path 分段拼（不写 '.harness\x.json' 反斜杠字面量）：
# Linux/pwsh 上反斜杠不是分隔符，会变成文件名里的字符 → 基线「找不到」→ 脚本走
# 「无基线 → 初始化并 exit 0」分支，**CI 里的棘轮会静默变成永远通过**。
$baselinePath = Join-Path (Join-Path $repoRoot '.harness') 'depth-baseline.json'

# 阈值常量（改这里必须同步改 .harness/rules/编码规范.md）
$FILE_LIMIT = 1000
$FUNC_LIMIT = 120
# 覆盖率容差：同一代码多次运行会有零点几个百分点的抖动（map 遍历顺序等），
# 不设容差会把抖动误报成回退。
$COVERAGE_TOLERANCE = 0.5

# ---------------------------------------------------------------------------
# 度量 1/2：生产代码文件体量
# ---------------------------------------------------------------------------
$prodFiles = Get-ChildItem internal, cmd -Recurse -Filter *.go -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -notlike '*_test.go' }

if (-not $prodFiles) {
    Write-Host "错误：未找到任何生产 .go 文件（工作目录是否正确？）" -ForegroundColor Red
    exit 2
}

# 相对路径：手工剥前缀 + 归一两种分隔符（.NET Framework 无 Path.GetRelativePath，
# 且 Windows 写死的反斜杠在 Linux runner 上匹配不到，会打印绝对路径）。
function Get-RepoRelPath($fullPath) {
    $rel = $fullPath.Substring($repoRoot.Length)
    return ($rel -replace '^[\\/]+', '')
}

$fileStats = foreach ($f in $prodFiles) {
    [pscustomobject]@{
        Path  = Get-RepoRelPath $f.FullName
        # 口径：**非空行**（不含空行）。声明见文件头「口径声明」，别与函数口径混用。
        # 不用 `Measure-Object -Line`：它对空白行的处理是实现的细节（Windows 5.1 实测
        # 不计空行，但把口径押在 cmdlet 行为上，跨平台/跨版本一旦变化就会让棘轮假红或
        # 假绿）。这里显式过滤，口径与实现一一对应。
        Lines = @(Get-Content $f.FullName | Where-Object { $_.Trim() -ne '' }).Count
    }
}

$maxFileLines = ($fileStats | Measure-Object -Property Lines -Maximum).Maximum
$oversizeFiles = @($fileStats | Where-Object { $_.Lines -gt $FILE_LIMIT })

# ---------------------------------------------------------------------------
# 度量 3：超长函数（只统计**代码行**）
#
# 函数体按「本行 `^func ` 到下一个 `^func ` 的前一行」界定，但行数计算**排除空行
# 与纯注释行**（`//` 开头；块注释内的行会被计入，这是接受的近似）。
#
# 为什么排除注释——这是对度量准确性的修正，不是放宽阈值：
#   - 函数体量的意义是「认知负荷 / 能否在脑中保持上下文」；注释**降低**该负荷
#     （它解释意图与约束），而非增加。
#   - 含注释计数会**惩罚写注释**，与本仓编码规范「注释写 why、关键取值附实测
#     依据」直接冲突——同一段逻辑，注释越详尽越早触发门禁，方向是反的。
#   - 实测假阳性案例：本仓 prepareChatRequest 的代码行远少于注释行（注释在讲
#     defer 作用域陷阱、body 上限来源等），含注释计数把它误判为「超长函数」。
#   - 真阳性不受影响：chatCompletions 拆分前是**真**的 800+ 行代码，任何算法都
#     会判它超长。
#
# 边界说明：块注释（/* */）内的行仍被计入；Go 里少见，不为此增加解析复杂度。
# ---------------------------------------------------------------------------
$longFuncs = New-Object System.Collections.ArrayList

# Get-CodeLineCount 统计 [start,end] 区间内的代码行数（排除空行与 `//` 开头的行）。
function Get-CodeLineCount($lines, $start, $end) {
    $n = 0
    for ($i = $start; $i -le $end; $i++) {
        $t = $lines[$i].Trim()
        if ($t -eq '' -or $t.StartsWith('//')) { continue }
        $n++
    }
    return $n
}

foreach ($f in $prodFiles) {
    $lines = Get-Content $f.FullName
    $starts = @()
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match '^func ') { $starts += $i }
    }
    for ($k = 0; $k -lt $starts.Count; $k++) {
        $s = $starts[$k]
        $e = if ($k + 1 -lt $starts.Count) { $starts[$k + 1] - 1 } else { $lines.Count - 1 }
        $len = Get-CodeLineCount $lines $s $e
        if ($len -gt $FUNC_LIMIT) {
            $sig = ($lines[$s] -replace '\s*\{\s*$', '').Trim()
            if ($sig.Length -gt 80) { $sig = $sig.Substring(0, 80) + '…' }
            [void]$longFuncs.Add([pscustomobject]@{
                Lines = $len
                File  = Get-RepoRelPath $f.FullName
                Sig   = $sig
            })
        }
    }
}
$oversizeFuncs = $longFuncs.Count

# ---------------------------------------------------------------------------
# 度量 4：最低包覆盖率
#
# 豁免：覆盖率 0.0% 的包（通常是只有 main() 的 cmd/*，或尚无测试文件的包）。
# 把它们计入会让 minCoverage 恒为 0，门禁失去意义。豁免不等于"这些包不用测"——
# 需要测时应当单独拉测试，而不是靠这里放宽。
# ---------------------------------------------------------------------------
$goExe = 'C:\Program Files\Go\bin\go.exe'
if (-not (Test-Path $goExe)) { $goExe = 'go' }

$env:GOPROXY = 'https://goproxy.cn,direct'
$env:CGO_ENABLED = '0'

$coverOut = @()
$coverageSource = ''
if ($CoverageStdout -ne '') {
    if (-not (Test-Path -LiteralPath $CoverageStdout)) {
        Write-Host ("错误：-CoverageStdout 指定的文件不存在：" + $CoverageStdout) -ForegroundColor Red
        exit 2
    }
    $coverOut = @(Get-Content -LiteralPath $CoverageStdout)
    $coverageSource = "复用 $CoverageStdout（未重复跑测试）"
    # 防御：复用进来的内容若含测试失败行（调用方担保失效 / 传错文件），按失败处理。
    # 这是「覆盖率数据来源错位」唯一可能的残缝（独立复审 🔵-2），成本一行，值得钉。
    $failLines = @($coverOut | Where-Object { $_ -match '^(FAIL|--- FAIL)' })
    if ($failLines.Count -gt 0) {
        Write-Host ("错误：-CoverageStdout 的内容含测试失败行（$($failLines.Count) 行），覆盖率不可信：" + $CoverageStdout) -ForegroundColor Red
        $failLines | Select-Object -First 20 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        exit 2
    }
} else {
    $coverOut = @(& $goExe test ./... -cover -count=1 2>&1)
    if ($LASTEXITCODE -ne 0) {
        # 失败即红（D-1）：棘轮的数字全部来自这份被测代码，测试红时这些数字不可信。
        # 旧实现在「输出里有 coverage: 行」时继续算并给出 PASS——那等于用覆盖率掩盖测试失败。
        Write-Host "错误：go test -cover 失败（测试红 → 棘轮数字不可信）：" -ForegroundColor Red
        $coverOut | Where-Object { $_ -match 'FAIL|panic|--- FAIL' } | Select-Object -First 30 |
            ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        exit 2
    }
    $coverageSource = '本次自跑 go test ./... -cover'
}

$coverages = @()
foreach ($line in $coverOut) {
    if ($line -match 'coverage:\s+([\d.]+)%\s+of statements') {
        $pct = [double]$Matches[1]
        if ($pct -gt 0) { $coverages += $pct }
    }
}

$minCoverage = if ($coverages.Count -gt 0) {
    [Math]::Round(($coverages | Measure-Object -Minimum).Minimum, 1)
} else {
    0.0
}

# ---------------------------------------------------------------------------
# 组装当前指标
# ---------------------------------------------------------------------------
$current = [ordered]@{
    maxFileLines  = [int]$maxFileLines
    oversizeFiles = [int]$oversizeFiles.Count
    oversizeFuncs = [int]$oversizeFuncs
    minCoverage   = [double]$minCoverage
}

Write-Host "=== 工程深度度量 ===" -ForegroundColor Cyan
Write-Host ("  最大文件行数      : {0}  (阈值 {1})" -f $current.maxFileLines, $FILE_LIMIT)
Write-Host ("  >{0} 行文件数     : {1}" -f $FILE_LIMIT, $current.oversizeFiles)
Write-Host ("  >{0} 行函数数     : {1}" -f $FUNC_LIMIT, $current.oversizeFuncs)
Write-Host ("  最低包覆盖率      : {0}%" -f $current.minCoverage)
Write-Host ("  覆盖率来源        : {0}" -f $coverageSource) -ForegroundColor DarkGray

if ($Detail) {
    Write-Host ""
    Write-Host "--- 最大文件 TOP 10 ---" -ForegroundColor Gray
    $fileStats | Sort-Object Lines -Descending | Select-Object -First 10 |
        ForEach-Object { Write-Host ("  {0,6} 行  {1}" -f $_.Lines, $_.Path) -ForegroundColor Gray }
    Write-Host "--- 超长函数 TOP 10 ---" -ForegroundColor Gray
    $longFuncs | Sort-Object Lines -Descending | Select-Object -First 10 |
        ForEach-Object { Write-Host ("  {0,6} 行  {1}" -f $_.Lines, $_.File); Write-Host ("            {0}" -f $_.Sig) -ForegroundColor DarkGray }
}

# ---------------------------------------------------------------------------
# ratchet 对比
# ---------------------------------------------------------------------------
if (-not (Test-Path $baselinePath)) {
    Write-Host ""
    Write-Host "无基线文件 → 以当前值初始化基线" -ForegroundColor Yellow
    $payload = [ordered]@{
        _comment  = '工程深度基线（scripts/check-depth.ps1 维护）。指标只许变好；-Update 抬高棘轮。'
        updatedAt = (Get-Date -Format 'yyyy-MM-dd')
        metrics   = $current
    }
    $json = $payload | ConvertTo-Json -Depth 5
    [System.IO.File]::WriteAllText($baselinePath, $json, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ("已写入 {0}" -f $baselinePath) -ForegroundColor Green
    exit 0
}

$baseline = (Get-Content $baselinePath -Raw | ConvertFrom-Json).metrics

$regressions = @()

# 越大越差的三项
foreach ($key in @('maxFileLines', 'oversizeFiles', 'oversizeFuncs')) {
    $baseVal = [int]$baseline.$key
    $curVal  = [int]$current[$key]
    if ($curVal -gt $baseVal) {
        $regressions += "REGRESSED: {0} {1} → {2}" -f $key, $baseVal, $curVal
    }
}

# 越小越差的一项（带容差）
$baseCov = [double]$baseline.minCoverage
$curCov  = [double]$current.minCoverage
if ($curCov -lt ($baseCov - $COVERAGE_TOLERANCE)) {
    $regressions += "REGRESSED: minCoverage {0}% → {1}%" -f $baseCov, $curCov
}

if ($regressions.Count -gt 0) {
    Write-Host ""
    Write-Host "❌ 深度指标回退（$($regressions.Count) 项）：" -ForegroundColor Red
    foreach ($r in $regressions) { Write-Host ("   " + $r) -ForegroundColor Yellow }
    Write-Host ""
    Write-Host "修复方向：拆分超长函数/文件、补齐核心包测试。" -ForegroundColor Gray
    Write-Host "严禁通过放宽阈值或 -Update 掩盖回退。" -ForegroundColor Gray
    exit 1
}

# 变好 → 提示可抬棘轮（但不自动写，避免静默放水）
$improved = @()
foreach ($key in @('maxFileLines', 'oversizeFiles', 'oversizeFuncs')) {
    if ([int]$current[$key] -lt [int]$baseline.$key) {
        $improved += "{0} {1} → {2}" -f $key, ([int]$baseline.$key), ([int]$current[$key])
    }
}
if ($curCov -gt ($baseCov + $COVERAGE_TOLERANCE)) {
    $improved += "minCoverage {0}% → {1}%" -f $baseCov, $curCov
}

if ($Update) {
    $payload = [ordered]@{
        _comment  = '工程深度基线（scripts/check-depth.ps1 维护）。指标只许变好；-Update 抬高棘轮。'
        updatedAt = (Get-Date -Format 'yyyy-MM-dd')
        metrics   = $current
    }
    [System.IO.File]::WriteAllText($baselinePath, ($payload | ConvertTo-Json -Depth 5), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ""
    Write-Host "✅ 棘轮已抬高" -ForegroundColor Green
    foreach ($i in $improved) { Write-Host ("   + " + $i) -ForegroundColor Green }
    exit 0
}

Write-Host ""
if ($improved.Count -gt 0) {
    Write-Host "✅ 未回退，且以下指标变好（可 -Update 抬高棘轮）：" -ForegroundColor Green
    foreach ($i in $improved) { Write-Host ("   + " + $i) -ForegroundColor Green }
} else {
    Write-Host "✅ 深度指标未回退" -ForegroundColor Green
}
exit 0
