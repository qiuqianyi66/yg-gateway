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
# 用法：
#   powershell -NoProfile -File scripts/check-depth.ps1          # 检查（CI / 提交前）
#   powershell -NoProfile -File scripts/check-depth.ps1 -Update  # 抬棘轮（仅当指标确实变好）
#   powershell -NoProfile -File scripts/check-depth.ps1 -Detail  # 打印各项明细
#
# 退出码：0 = 未回退；1 = 有指标回退；2 = 脚本自身失败

param(
    [switch]$Update,
    [switch]$Detail
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$baselinePath = Join-Path $repoRoot '.harness\depth-baseline.json'

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

$fileStats = foreach ($f in $prodFiles) {
    [pscustomobject]@{
        Path  = $f.FullName.Replace("$repoRoot\", "")
        Lines = (Get-Content $f.FullName | Measure-Object -Line).Lines
    }
}

$maxFileLines = ($fileStats | Measure-Object -Property Lines -Maximum).Maximum
$oversizeFiles = @($fileStats | Where-Object { $_.Lines -gt $FILE_LIMIT })

# ---------------------------------------------------------------------------
# 度量 3：超长函数
#
# 注：函数体按「本行 `^func ` 到下一个 `^func ` 的前一行」计算，包含其间的注释
# 与空行。这与 Go 语法意义上的函数体长度**不完全等价**（注释多的函数会显得更长），
# 但作为**趋势指标**足够稳定，且实现零依赖。不要把它当精确值引用。
# ---------------------------------------------------------------------------
$longFuncs = New-Object System.Collections.ArrayList

foreach ($f in $prodFiles) {
    $lines = Get-Content $f.FullName
    $starts = @()
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match '^func ') { $starts += $i }
    }
    for ($k = 0; $k -lt $starts.Count; $k++) {
        $s = $starts[$k]
        $e = if ($k + 1 -lt $starts.Count) { $starts[$k + 1] - 1 } else { $lines.Count - 1 }
        $len = $e - $s
        if ($len -gt $FUNC_LIMIT) {
            $sig = ($lines[$s] -replace '\s*\{\s*$', '').Trim()
            if ($sig.Length -gt 80) { $sig = $sig.Substring(0, 80) + '…' }
            [void]$longFuncs.Add([pscustomobject]@{
                Lines = $len
                File  = $f.FullName.Replace("$repoRoot\", "")
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

$coverOut = & $goExe test ./... -cover -count=1 2>&1
if ($LASTEXITCODE -ne 0 -and -not ($coverOut | Select-String 'coverage:')) {
    Write-Host "错误：go test -cover 执行失败：" -ForegroundColor Red
    Write-Host ($coverOut -join "`n")
    exit 2
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
