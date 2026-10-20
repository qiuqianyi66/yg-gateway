# check-context-budget.ps1 — 常驻上下文预算（token 估算）与棘轮
#
# 为什么需要（docs/prd/REQ-harness-engineering-2026-10-19.md §五 F2）：
#   HarnessTax（UC Berkeley / Arena，2026-09，21 组 model×harness）的实测结论：harness 对
#   **成功率**影响仅 ±2–5%，对**成本**影响可达 5×，而成本的主来源是**首调用上下文**
#   （Claude Code 的首调用上下文是极简 harness 的 10×+）。本仓把 AGENTS.md + rules/*.md +
#   经验索引常驻，实测 22.6k token（PRD §二），此前**没有任何预算线** → 也就没有
#   「压降之后收紧」的棘轮：砍下去的上下文随时会悄悄长回来。
#
# 估算口径（与 tea 的 scripts/audit-context-budget.cjs 同一口径，PRD §二 同源）：
#   token ≈ CJK 字符数 ÷ 1.5 + 其余字符数 ÷ 3.8
#   CJK 判定：U+3000–303F / U+4E00–9FFF / U+FF00–FFEF 三个区间。
#   口径自检（2026-10-19）：本脚本复算 AGENTS.md = 2,607、LESSONS.md 全量 = 7,546，
#   与 PRD §二 表格逐项吻合（总量差 <0.2%，来自字符类边界），说明口径对齐。
#   四态标记：**derivation**（估算值，不是 tokenizer 实测）。用途是做相对比较
#   （今天 vs 上次），**不要**拿它跟真实 tokenizer 的计费数字对账。
#
# 常驻层清单（改这里 = 改预算边界，必须写理由）：
#   - AGENTS.md
#   - .harness/rules/*.md（自动发现：新增规则立刻进预算，不会静默漏掉）
#   - .harness/LESSONS.md —— 常驻的**经验索引**。F3 之后正文在 .harness/lessons/，
#     按需加载，不计入常驻层。
#
# 棘轮纪律（同 depth-baseline.json，那条纪律是被实弹踩出来的）：
#   - 基线**只许下调**；-Update 时若当前值 ≥ 基线 → 拒绝（exit 2），不许「改基线让它绿」。
#   - 基线文件**必须入库**：本地改了不提交，CI 仍按旧值判（旧值更松 = 假绿）。
#
# 用法：
#   powershell -NoProfile -File scripts/check-context-budget.ps1          # 检查（提交前/CI）
#   powershell -NoProfile -File scripts/check-context-budget.ps1 -Update  # 净压降后收紧基线
#
# 退出码：0 = 未超预算；1 = 超预算（回退）；2 = 脚本自身失败（含 -Update 试图上调）

param(
    [switch]$Update
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# 路径一律分段 Join-Path：反斜杠字面量在 Linux runner 上不是分隔符 → 基线「找不到」→
# 走「无基线 → 初始化并 exit 0」分支 → CI 里的棘轮**永久假绿**（LESSONS L-7，Seen 1）。
$harnessDir = Join-Path $repoRoot '.harness'
$rulesDir = Join-Path $harnessDir 'rules'
$baselinePath = Join-Path $harnessDir 'context-baseline.json'
$indexPath = Join-Path $harnessDir 'LESSONS.md'

function Get-RepoRelPath($fullPath) {
    $rel = $fullPath.Substring($repoRoot.Length)
    return (($rel -replace '^[\\/]+', '') -replace '\\', '/')
}

function Get-EstimatedTokens($fullPath) {
    # 一律按 UTF-8 读：无 BOM 的 UTF-8 在遗留 ANSI 代码页下会被默认编码误解码，
    # 中文全变乱码 → 字符统计失真（check-req-necessity.ps1 踩过同一类）。
    $text = [System.IO.File]::ReadAllText($fullPath, [System.Text.Encoding]::UTF8)
    $cjk = [regex]::Matches($text, '[\u3000-\u303F\u4E00-\u9FFF\uFF00-\uFFEF]').Count
    $other = $text.Length - $cjk
    return [int][Math]::Round(($cjk / 1.5) + ($other / 3.8))
}

# ---------------------------------------------------------------------------
# 常驻层清单
# ---------------------------------------------------------------------------
$resident = New-Object System.Collections.ArrayList

$agentsFull = Join-Path $repoRoot 'AGENTS.md'
if (-not (Test-Path -LiteralPath $agentsFull)) {
    Write-Host '错误：仓库根找不到 AGENTS.md（工作目录是否正确？）' -ForegroundColor Red
    exit 2
}
[void]$resident.Add([pscustomobject]@{ Rel = 'AGENTS.md'; Full = $agentsFull })

$ruleFiles = @(Get-ChildItem -LiteralPath $rulesDir -Filter *.md -File -ErrorAction SilentlyContinue | Sort-Object Name)
foreach ($f in $ruleFiles) {
    [void]$resident.Add([pscustomobject]@{ Rel = (Get-RepoRelPath $f.FullName); Full = $f.FullName })
}

if (Test-Path -LiteralPath $indexPath) {
    [void]$resident.Add([pscustomobject]@{ Rel = (Get-RepoRelPath $indexPath); Full = $indexPath })
} else {
    # 索引缺失**不是**「0 token」：它是常驻层的一部分，缺了必须让人看见（未测 ≠ 通过）。
    Write-Host '警告：.harness/LESSONS.md 不存在 → 常驻经验索引未纳入统计（本次总量偏小）' -ForegroundColor Yellow
}

$items = @()
foreach ($r in $resident) {
    $items += [pscustomobject]@{ Rel = $r.Rel; Tokens = (Get-EstimatedTokens $r.Full) }
}
$total = [int]($items | Measure-Object -Property Tokens -Sum).Sum

Write-Host '=== 常驻上下文预算 ===' -ForegroundColor Cyan
Write-Host '  （口径：CJK÷1.5 + 其余÷3.8，derivation；用于相对比较，不是 tokenizer 实测）' -ForegroundColor DarkGray
foreach ($it in ($items | Sort-Object Tokens -Descending)) {
    Write-Host ("  {0,7}  {1}" -f $it.Tokens, $it.Rel)
}

# ---------------------------------------------------------------------------
# 棘轮对比
# ---------------------------------------------------------------------------
if (-not (Test-Path -LiteralPath $baselinePath)) {
    $perFile = [ordered]@{}
    foreach ($it in ($items | Sort-Object Rel)) { $perFile[$it.Rel] = $it.Tokens }
    $payload = [ordered]@{
        _comment     = '常驻上下文预算基线（scripts/check-context-budget.ps1 维护，口径见该脚本头部）。budgetTokens 只许下调，-Update 收紧；perFile 仅用于定位「最该砍的文件」，不作门禁判据。'
        updatedAt    = (Get-Date -Format 'yyyy-MM-dd')
        budgetTokens = $total
        perFile      = $perFile
    }
    [System.IO.File]::WriteAllText($baselinePath, ($payload | ConvertTo-Json -Depth 5), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ''
    Write-Host ("无基线文件 → 以当前实测值初始化：{0} token（已写入 {1}）" -f $total, (Get-RepoRelPath $baselinePath)) -ForegroundColor Yellow
    Write-Host '  该 json 必须入库：本地改了不提交，CI 仍按旧值判。' -ForegroundColor Gray
    exit 0
}

$baselineDoc = (Get-Content -LiteralPath $baselinePath -Raw | ConvertFrom-Json)
$budget = [int]$baselineDoc.budgetTokens
if ($budget -le 0) {
    Write-Host ('错误：基线文件里的 budgetTokens 无效（' + $baselineDoc.budgetTokens + '）——不许按 0 判，那等于没有门禁') -ForegroundColor Red
    exit 2
}

Write-Host ''
Write-Host ("  当前 {0} / 预算 {1}  （差 {2}）" -f $total, $budget, ($total - $budget)) -ForegroundColor Cyan

if ($Update) {
    if ($total -gt $budget) {
        Write-Host ("错误：-Update 只能**下调**基线（当前 {0} > 基线 {1}）——先压降常驻层，不许改基线让它绿" -f $total, $budget) -ForegroundColor Red
        exit 2
    }
    $perFile = [ordered]@{}
    foreach ($it in ($items | Sort-Object Rel)) { $perFile[$it.Rel] = $it.Tokens }
    $comment = if ($baselineDoc._comment) { [string]$baselineDoc._comment } else { '常驻上下文预算基线（scripts/check-context-budget.ps1 维护）。' }
    $payload = [ordered]@{
        _comment     = $comment
        updatedAt    = (Get-Date -Format 'yyyy-MM-dd')
        budgetTokens = $total
        perFile      = $perFile
    }
    [System.IO.File]::WriteAllText($baselinePath, ($payload | ConvertTo-Json -Depth 5), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ("✅ 棘轮已收紧：{0} → {1}（-{2}）" -f $budget, $total, ($budget - $total)) -ForegroundColor Green
    exit 0
}

if ($total -gt $budget) {
    $biggest = $items | Sort-Object Tokens -Descending | Select-Object -First 1
    Write-Host ''
    Write-Host ("❌ 常驻上下文超预算：{0} > {1}（+{2}）" -f $total, $budget, ($total - $budget)) -ForegroundColor Red
    Write-Host ("   最该砍的文件：{0}（{1} token，占 {2}%）" -f $biggest.Rel, $biggest.Tokens, [Math]::Round(100 * $biggest.Tokens / $total, 1)) -ForegroundColor Yellow
    Write-Host '   压降后跑 -Update 收紧基线；严禁直接改 json 上调。' -ForegroundColor Gray
    exit 1
}

if ($total -lt $budget) {
    Write-Host ("✅ 未超预算，且有 {0} token 余量（压降后可 -Update 收紧）" -f ($budget - $total)) -ForegroundColor Green
} else {
    Write-Host '✅ 未超预算（正好压线）' -ForegroundColor Green
}
exit 0
