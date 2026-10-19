# run-all.ps1 — 全清单变异复跑（A6 收口入口）
#
# 为什么存在：变异清单是逐个 `-MutsFile` 手动跑的，没人记得「上次全量是什么时候、还灵不灵」。
# 这个脚本把 `scripts/mutation/mutations/*.json` 全部跑一遍，输出一张表；
# 任何清单出现「存活 / 无效」即 exit 1（存活是测试缺口，无效是变异脚本自身坏了）。
#
# 用法：
#   powershell -NoProfile -File scripts/mutation/run-all.ps1            # 复用/新建 %TEMP%\mut-all
#   powershell -NoProfile -File scripts/mutation/run-all.ps1 -Fresh     # 删掉副本重新克隆
#   powershell -NoProfile -File scripts/mutation/run-all.ps1 -Only panel-queue   # 只跑某个清单
#
# 包名从清单里第一条的 file 字段推导（internal/panel/x.go → internal/panel，cmd/server/x.go → cmd/server），
# 所以新增清单只需保证 file 字段是仓库相对路径。
param(
    [string]$Clone = (Join-Path $env:TEMP 'mut-all'),
    [string]$Only = '',
    [switch]$Fresh
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)

if (-not [System.IO.Path]::IsPathRooted($Clone)) { $Clone = (Resolve-Path -LiteralPath $Clone).Path }
if ($Fresh -and (Test-Path -LiteralPath $Clone)) { Remove-Item -LiteralPath $Clone -Recurse -Force }
if (-not (Test-Path -LiteralPath $Clone)) {
    Write-Host "克隆副本：$Clone"
    & git clone --quiet $repo $Clone
    if ($LASTEXITCODE -ne 0) { throw "git clone 失败（exit=$LASTEXITCODE）" }
}

$lists = @(Get-ChildItem -LiteralPath (Join-Path $PSScriptRoot 'mutations') -Filter '*.json' | Sort-Object Name)
if ($Only -ne '') { $lists = @($lists | Where-Object { $_.BaseName -like "*$Only*" }) }
if ($lists.Count -eq 0) { throw "没有匹配的清单（-Only $Only）" }

$results = New-Object System.Collections.ArrayList
foreach ($l in $lists) {
    # PS 5.1 的 ConvertFrom-Json 把整个数组当成**一个对象**写进管道（不展开），
    # 直接 @() 包起来会得到 count=1，`$entries[0].file` 还会在数组上取属性（返回多个值）。
    # 用 [array] 强制展开——2026-10-19 实测：写成 @() 时 10 个清单全显示「1 条」。
    [array]$entries = (Get-Content -LiteralPath $l.FullName -Raw | ConvertFrom-Json)
    $file = $entries[0].file
    $pkg = (($file -split '/')[0..1] -join '/')
    Write-Host ''
    Write-Host ("=== {0}（{1} 条，包 {2}）" -f $l.BaseName, $entries.Count, $pkg) -ForegroundColor Cyan

    # 子脚本的 stderr 不能被外层 Stop 当成终止性异常（L-17 同族：先说清在跑什么）
    $prevEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $out = @(& powershell -NoProfile -File (Join-Path $PSScriptRoot 'mutation-check.ps1') `
                -Clone $Clone -MutsFile $l.FullName -Pkg $pkg 2>&1)
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $prevEap }

    $summary = @($out | Select-String -Pattern '咬住 \d+ / 存活' | Select-Object -Last 1)
    $line = if ($summary.Count -gt 0) { $summary[0].Line.Trim() } else { '（无汇总行）' }
    $verdict = if ($code -eq 0) { 'OK' } else { 'CHECK' }
    $color = if ($code -eq 0) { 'Green' } else { 'Red' }
    Write-Host ("  -> {0}  {1}" -f $verdict, $line) -ForegroundColor $color
    [void]$results.Add([pscustomobject]@{ list = $l.BaseName; pkg = $pkg; mutants = $entries.Count; exit = $code; summary = $line })
}

Write-Host ''
Write-Host '==== 全清单汇总'
$results | ForEach-Object { "{0,-18} {1,-16} {2,3} 条  exit={3}  {4}" -f $_.list, $_.pkg, $_.mutants, $_.exit, $_.summary }
$bad = @($results | Where-Object { $_.exit -ne 0 })
$total = ($results | Measure-Object -Property mutants -Sum).Sum
Write-Host ("合计 {0} 条变异 / {1} 个清单；异常清单 {2} 个" -f $total, $results.Count, $bad.Count)
if ($bad.Count -gt 0) {
    Write-Host '✗ 有清单存在存活或无效变异（见上表）' -ForegroundColor Red
    exit 1
}
Write-Host '✓ 全部咬住' -ForegroundColor Green
exit 0
