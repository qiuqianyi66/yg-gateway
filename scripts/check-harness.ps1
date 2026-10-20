# check-harness.ps1 — 治理一致性体检（只读，退出码即结论）
#
# 为什么需要（docs/prd/REQ-harness-engineering-2026-10-19.md §五 F1）：
#   `.harness/` 下有规则 / SKILL.md / REQ / ADR / changes 目录，`AGENTS.md` 里还有一堆
#   指向它们的路径与数字。此前**没有任何脚本能回答「这套治理自身还自不自洽」**——
#   规则↔技能↔文档之间的断链与数字漂移全靠人记，而这类问题当场不报错，几周后才以
#   「AI 又做错了」的形式显形（本仓已发生过 doc-drift、cross-file-reconcile 两次）。
#
#   ⚠ 本文件头部**不写具体数字**（技能几个、REQ 几份、changes 几个）：写死就会漂移，
#   而那正是本脚本要防的那类问题。要数字跑 `-Detail`，或看各检项自己打印的实测值。
#
# 设计取向（三条，别改）：
#   1. **只读**：不写任何文件、不改任何文档。体检不该有副作用。
#   2. **未测 ≠ 通过**：某项跑不了（缺工具 / 文件不存在 / 本轮未实现）一律输出 `未测`，
#      **绝不记 0 问题**。把「没测到」记成「通过」是本仓明写的反模式（四态纪律：
#      未覆盖 = unknown，不是 absence）。未测项不影响退出码，但会在末尾单独列出。
#   3. **按实测证据补检项，不按想象补**（PRD §六 Q4 判据：真问题 ≥1 且误报 ≤1 才补）：
#      [4][6] 由该判据直接授权；[8] 针对 F1 的已知盲区——原版只查路径断链、
#      **不查文档内部编号**，而该类漂移已实证 2 处（LESSONS L-1 写「检查 3/6」、
#      L-3 写「检查 4/7」，实际当时已是 3/10 与 4/10）。
#
# 检项清单：
#   [1] AGENTS.md 里出现的文件路径全部存在（断链即 error）
#   [2] .harness/skills/**/SKILL.md 的 frontmatter 含 name + description，且 name = 目录名
#   [3] 技能族 README 路由表列出的技能与磁盘一致（README 不列 = 视为不存在）
#   [4] docs/adr/ 编号无重号且命名合规；docs/prd/ 文件名符合 REQ-*.md
#   [5] 新增 REQ 含「必要性」段        → 未测（由 pre-commit 检查 8 承担，不重复实现）
#   [6] .harness/changes/*/ 每个目录含 summary.md（_template 豁免）
#   [7] LESSONS 索引里 Seen ≥ 2 的条目必须已升级为规则/机器检查   → F3 的验收项
#   [8] 常驻文档里的「检查 N/M」分母与 pre-commit.ps1 实际项数一致（changes/ 历史记录豁免）
#
# 用法：
#   powershell -NoProfile -File scripts/check-harness.ps1           # 体检
#   powershell -NoProfile -File scripts/check-harness.ps1 -Detail   # 额外打印被检查的路径清单
#
# 退出码：0 = 无 error（未测项不影响）；1 = 有 error；2 = 脚本自身失败

param(
    [switch]$Detail
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# 路径一律分段 Join-Path（LESSONS L-7：反斜杠字面量在 Linux runner 上不是分隔符）。
$harnessDir = Join-Path $repoRoot '.harness'
$skillsRoot = Join-Path $harnessDir 'skills'
$agentsPath = Join-Path $repoRoot 'AGENTS.md'
$lessonsPath = Join-Path $harnessDir 'LESSONS.md'

$errors = New-Object System.Collections.ArrayList
$untested = New-Object System.Collections.ArrayList
$notes = New-Object System.Collections.ArrayList

function Add-Error($msg) { [void]$errors.Add($msg) }
function Add-Untested($name, $why) { [void]$untested.Add(@{ Name = $name; Why = $why }) }

function Write-Check($no, $name, $state, $detail) {
    $tag = switch ($state) {
        'OK' { 'OK   ' }
        'FAIL' { 'FAIL ' }
        default { '未测 ' }
    }
    $color = switch ($state) {
        'OK' { 'Green' }
        'FAIL' { 'Red' }
        default { 'Yellow' }
    }
    $line = ("  [{0}] {1}  {2}  {3}" -f $no, $tag, $name.PadRight(24), $detail)
    Write-Host $line -ForegroundColor $color
}

function Get-RepoRelPath($fullPath) {
    $rel = $fullPath.Substring($repoRoot.Length)
    return (($rel -replace '^[\\/]+', '') -replace '\\', '/')
}

function Read-TextFile($fullPath) {
    # 一律按 UTF-8 读：无 BOM 的 UTF-8 在遗留 ANSI 代码页下会被默认编码误解码 → 中文变乱码。
    return [System.IO.File]::ReadAllText($fullPath, [System.Text.Encoding]::UTF8)
}

Write-Host '=== 治理一致性体检（scripts/check-harness.ps1） ===' -ForegroundColor Cyan

# ---------------------------------------------------------------------------
# 检项 [1]：AGENTS.md 里的路径引用是否断链
#
# 取数方式（两个来源，都再按空白切分，这样「命令行片段里的路径」也能被查到）：
#   (a) 行内代码跨度（反引号包裹）—— AGENTS.md 的路径多数以 `path` 形态出现，信噪比最高；
#   (b) 围栏代码块内的每一行 —— 换版命令等以裸命令形式出现，不在反引号里；
#       实测不放进来的话 `scripts/deploy-prod.ps1` 这类**最关键**的引用反而查不到。
# 已知边界（明说的局限）：散文正文（既不在反引号里也不在围栏里）的裸路径不覆盖，
# 因为「分词的中文散文」误报面大到不可维护。
#
# 剔除规则（每条都有理由，改之前先想清楚会引入什么误报）：
#   - 不含分隔符         → 是包名/命令 flag，不是路径（`pool`、`-Fast`）
#   - 含空格             → 是命令行片段，不是单个路径（`git commit -F <file>`）
#   - 含 < > { } * ?     → 占位符或通配，不可判定（`docs/prd/REQ-*.md`）
#   - 含 =               → 环境赋值 / flag=value，不是路径（`GOPROXY=https://…`）
#   - 绝对盘符 / 绝对 unix / URL / 含 $变量 → 指向仓库外或运行时，不在本仓口径内
#   - **末段无扩展名且不以 / 结尾** → 不是路径（`exe_hash_before/after` 是报告里的字段名，
#     实测过：不排除它会误报成断链）
#
# 关于「被 .gitignore 排除」（如 `auths/` 凭证目录）：它本就不该出现在检出里，**不算断链**，
# 但必须在输出里**可见地**列出跳过项 —— 静默跳过等于放水。判据用 `git check-ignore`，
# 且**保留目录尾斜杠**：`.gitignore` 里写的是 `auths/`，去掉尾斜杠后 check-ignore 不匹配
# （实测 rc=1）→ 会被误判成断链。
# ---------------------------------------------------------------------------
$broken = New-Object System.Collections.ArrayList
$ignoredRefs = New-Object System.Collections.ArrayList
$checkedRefs = New-Object System.Collections.ArrayList

if (-not (Test-Path -LiteralPath $agentsPath)) {
    Add-Untested '[1] AGENTS.md 路径断链' '仓库根找不到 AGENTS.md，无法判定'
} else {
    $agentsText = Read-TextFile $agentsPath

    # 候选段落：反引号跨度 + 围栏内每行
    $candidates = New-Object System.Collections.ArrayList
    foreach ($m in [regex]::Matches($agentsText, '`([^`\r\n]+)`')) { [void]$candidates.Add($m.Groups[1].Value) }
    $inFence = $false
    foreach ($ln in ($agentsText -split "`r?`n")) {
        if ($ln -match '^\s*```') { $inFence = -not $inFence; continue }
        if ($inFence) { [void]$candidates.Add($ln) }
    }
    # 含绝对盘符/绝对 unix/URL 的**整段**直接丢弃，不参与切分：
    # `C:\Program Files\Go\bin\go.exe` 按空白切开会掉出一个 `Files/Go/bin/go.exe`，
    # 它长得像路径、实际是绝对路径被切碎的残片（实测假候选，已排除）。
    $candidates = @($candidates | Where-Object {
            -not ($_ -match '^[A-Za-z]:[\\/]') -and -not ($_ -match '^[/\\]') -and -not ($_ -match '^[a-zA-Z][a-zA-Z0-9+.\-]*://')
        })
    # 每个候选再按空白切分：命令行片段里的路径也要查
    $rawTokens = New-Object System.Collections.ArrayList
    foreach ($c in $candidates) {
        foreach ($t in ($c -split '\s+')) { if ($t -ne '') { [void]$rawTokens.Add($t) } }
    }

    # 顶层目录集合（动态取，不写死）：用于判定「首段是仓库顶层目录」这类无扩展名路径
    # （`./cmd/server`、`internal/httpauth`），同时把 `exe_hash_before/after` 这类
    # 报告字段名挡在外面。
    $topDirs = @{}
    foreach ($d in @(Get-ChildItem -LiteralPath $repoRoot -Directory -ErrorAction SilentlyContinue)) { $topDirs[$d.Name] = $true }

    $trimChars = [char[]]@('，', '。', '、', '；', '：', '）', '）', '」', '】', '"', "'", '(', ')')

    $seenRefs = @{}
    foreach ($raw in $rawTokens) {
        $raw = $raw.Trim().Trim($trimChars)
        if ($raw -notmatch '[\\/]') { continue }
        if ($raw -match '\s') { continue }
        if ($raw -match '[<>{}*?]') { continue }
        if ($raw -match '=') { continue }
        if ($raw -match '^[A-Za-z]:[\\/]') { continue }
        if ($raw -match '^[/\\]') { continue }
        if ($raw -match '^[a-zA-Z][a-zA-Z0-9+.\-]*://') { continue }
        if ($raw -match '\$\{?[A-Za-z_]') { continue }
        $p = (($raw -replace '\\', '/') -replace '^\./', '') -replace '#.*$', ''
        $p = $p.Trim()
        if ($p -eq '') { continue }
        # 认定为「路径」需满足其一：末段有扩展名 / 以 / 结尾（显式目录）/ 首段是仓库顶层目录。
        $lastSeg = $p.TrimEnd('/') -replace '^.*/', ''
        $firstSeg = ($p -replace '^\./', '') -replace '/.*$', ''
        if ($lastSeg -notmatch '\.[A-Za-z0-9]+$' -and -not $p.EndsWith('/') -and -not $topDirs.ContainsKey($firstSeg)) { continue }
        if ($seenRefs.ContainsKey($p)) { continue }
        $seenRefs[$p] = $true
        [void]$checkedRefs.Add($p)
    }

    foreach ($p in ($checkedRefs | Sort-Object)) {
        if (Test-Path -LiteralPath $p) { continue }
        # 被 .gitignore 排除的（如 auths/ 凭证目录）本就不该在检出里出现 → 不算断链，
        # 但要单独列出：跳过必须是**可见**的，否则等于静默放水。
        $prevEap = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $null = & git check-ignore -q -- "$p" 2>$null
        $isIgnored = ($LASTEXITCODE -eq 0)
        $ErrorActionPreference = $prevEap
        if ($isIgnored) { [void]$ignoredRefs.Add($p) } else { [void]$broken.Add($p) }
    }

    if ($broken.Count -gt 0) {
        foreach ($b in $broken) { Write-Host ("       断链: " + $b) -ForegroundColor Red }
        Write-Check 1 'AGENTS.md 路径断链' 'FAIL' ("检查 {0} 个引用，{1} 个断链" -f $checkedRefs.Count, $broken.Count)
        Add-Error ("AGENTS.md 路径断链 {0} 处" -f $broken.Count)
    } else {
        Write-Check 1 'AGENTS.md 路径断链' 'OK' ("检查 {0} 个引用，0 断链（gitignore 跳过 {1}）" -f $checkedRefs.Count, $ignoredRefs.Count)
    }
    if ($Detail) {
        foreach ($p in $checkedRefs) { Write-Host ("         · " + $p) -ForegroundColor DarkGray }
        foreach ($p in $ignoredRefs) { Write-Host ("         · " + $p + "  （被 .gitignore 排除，跳过）") -ForegroundColor DarkGray }
    }
}

# ---------------------------------------------------------------------------
# 检项 [2]：SKILL.md frontmatter
# ---------------------------------------------------------------------------
$skillFiles = @(Get-ChildItem -LiteralPath $skillsRoot -Recurse -Filter 'SKILL.md' -File -ErrorAction SilentlyContinue | Sort-Object FullName)
if ($skillFiles.Count -eq 0) {
    Add-Untested '[2] SKILL.md frontmatter' ('未在 ' + (Get-RepoRelPath $skillsRoot) + ' 下找到任何 SKILL.md')
    Write-Check 2 'SKILL.md frontmatter' '未测' '未找到 SKILL.md'
} else {
    $skillBad = New-Object System.Collections.ArrayList
    foreach ($f in $skillFiles) {
        $rel = Get-RepoRelPath $f.FullName
        $lines = @(Get-Content -LiteralPath $f.FullName -Encoding UTF8)
        $i = 0
        while ($i -lt $lines.Count -and $lines[$i].Trim() -eq '') { $i++ }
        if ($i -ge $lines.Count -or $lines[$i].Trim() -ne '---') {
            [void]$skillBad.Add(("{0}: 缺少 frontmatter（首个非空行必须是 ---）" -f $rel))
            continue
        }
        $end = -1
        for ($k = $i + 1; $k -lt $lines.Count; $k++) {
            if ($lines[$k].Trim() -eq '---') { $end = $k; break }
        }
        if ($end -lt 0) {
            [void]$skillBad.Add(("{0}: frontmatter 未闭合（找不到结束的 ---）" -f $rel))
            continue
        }
        $fm = @{}
        for ($k = $i + 1; $k -lt $end; $k++) {
            if ($lines[$k] -match '^([A-Za-z_][A-Za-z0-9_\-]*)\s*:\s*(.*)$') {
                $fm[$Matches[1].ToLower()] = $Matches[2].Trim()
            }
        }
        if (-not $fm.ContainsKey('name')) { [void]$skillBad.Add(("{0}: frontmatter 缺 name" -f $rel)) }
        if (-not $fm.ContainsKey('description')) { [void]$skillBad.Add(("{0}: frontmatter 缺 description" -f $rel)) }
        if ($fm.ContainsKey('name')) {
            $nameVal = $fm['name'].Trim([char[]]@('"', "'"))
            if ($nameVal -ne $f.Directory.Name) {
                [void]$skillBad.Add(("{0}: name「{1}」≠ 目录名「{2}」" -f $rel, $nameVal, $f.Directory.Name))
            }
        }
    }
    if ($skillBad.Count -gt 0) {
        foreach ($b in $skillBad) { Write-Host ("       " + $b) -ForegroundColor Red }
        Write-Check 2 'SKILL.md frontmatter' 'FAIL' ("{0}/{1} 个不合规" -f $skillBad.Count, $skillFiles.Count)
        Add-Error ("SKILL.md frontmatter 不合规 {0} 处" -f $skillBad.Count)
    } else {
        Write-Check 2 'SKILL.md frontmatter' 'OK' ("{0}/{0} 合规（name/description 齐备且 name=目录名）" -f $skillFiles.Count)
    }
}

# ---------------------------------------------------------------------------
# 检项 [3]：技能族 README 路由表
#
# 判据（PRD §五 F1-3）：README **列出的技能**与磁盘一致；**README 不列 = 视为不存在**。
# 这条的意义是把「技能存在性」从「模型自己检索」换成「静态路由表」（Vercel evals：
# 短静态路由表 pass rate 100% vs 动态技能检索 79%）。
# ---------------------------------------------------------------------------
$routeFiles = @(Get-ChildItem -LiteralPath $skillsRoot -Recurse -File -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -match '^README(\.[A-Za-z0-9]+)?$' } | Sort-Object FullName)

if ($routeFiles.Count -eq 0) {
    # 未测，不是通过：PRD §二 实测「缺 技能族 README 路由表」，本检项目前没有判据来源。
    Add-Untested '[3] 技能族 README 路由表' ('.harness/skills/ 下没有 README（PRD §二 实测缺项）→ 无判据来源')
    Write-Check 3 '技能族 README 路由表' '未测' 'skills 下无 README，无判据来源'
} else {
    $routeBad = New-Object System.Collections.ArrayList
    $diskSet = @{}
    foreach ($f in $skillFiles) {
        $fam = $f.Directory.Parent.Name
        $diskSet[($fam + '/' + $f.Directory.Name)] = $true
    }
    $listedSet = @{}
    foreach ($rf in $routeFiles) {
        $rt = Read-TextFile $rf.FullName
        foreach ($m in [regex]::Matches($rt, '`([^`\r\n]+)`')) {
            $v = $m.Groups[1].Value.Trim()
            if ($v -match '^[A-Za-z0-9_\-]+/[A-Za-z0-9_\-]+$') { $listedSet[$v] = $true }
        }
    }
    foreach ($k in ($listedSet.Keys | Sort-Object)) {
        if (-not $diskSet.ContainsKey($k)) { [void]$routeBad.Add(("路由表列了但磁盘没有: " + $k)) }
    }
    foreach ($k in ($diskSet.Keys | Sort-Object)) {
        if (-not $listedSet.ContainsKey($k)) { [void]$routeBad.Add(("磁盘有但路由表未列（= 视为不存在）: " + $k)) }
    }
    if ($routeBad.Count -gt 0) {
        foreach ($b in $routeBad) { Write-Host ("       " + $b) -ForegroundColor Red }
        Write-Check 3 '技能族 README 路由表' 'FAIL' ("{0} 处不一致（磁盘 {1} 个 / 表 {2} 个）" -f $routeBad.Count, $diskSet.Count, $listedSet.Count)
        Add-Error ("技能路由表与磁盘不一致 {0} 处" -f $routeBad.Count)
    } else {
        Write-Check 3 '技能族 README 路由表' 'OK' ("磁盘 {0} 个技能与路由表一致" -f $diskSet.Count)
    }
}

# ---------------------------------------------------------------------------
# 检项 [4]：ADR 编号唯一 + 命名合规；docs/prd/ 命名合规
#
# 判据：
#   - docs/adr/ADR-NNN-*.md：NNN 不得重复。重号的危害是**引用失效**——正文写「见 ADR-012」
#     而读者找到的是另一个决策；这种错当场不报错。
#   - 命名不符合 ADR-NNN-*.md 形态的报 error（防 ADR-1 / ADRx 这类）。
#   - docs/prd/ 下每个 .md 必须匹配 REQ-*.md。
# - 断号**不判 error**：删除一篇 ADR 是合法操作，会产生断号；只以提示形式报出。
# ---------------------------------------------------------------------------
$docsRoot = Join-Path $repoRoot 'docs'
$adrDir = Join-Path $docsRoot 'adr'
$prdDir = Join-Path $docsRoot 'prd'

if (-not (Test-Path -LiteralPath $adrDir) -or -not (Test-Path -LiteralPath $prdDir)) {
    Add-Untested '[4] ADR 编号 / REQ 命名' 'docs/adr 或 docs/prd 目录不存在，无法判定'
    Write-Check 4 'ADR 编号 / REQ 命名' '未测' 'docs 子目录缺失'
} else {
    $numBad = New-Object System.Collections.ArrayList
    $adrNums = @{}
    $adrFiles = @(Get-ChildItem -LiteralPath $adrDir -Filter 'ADR-*.md' -File -ErrorAction SilentlyContinue | Sort-Object Name)
    foreach ($f in $adrFiles) {
        if ($f.Name -match '^ADR-(\d+)-.*\.md$') {
            $n = [int]$Matches[1]
            if ($adrNums.ContainsKey($n)) {
                [void]$numBad.Add(("ADR 编号重复 {0:D3}：{1} 与 {2}" -f $n, $adrNums[$n], $f.Name))
            } else {
                $adrNums[$n] = $f.Name
            }
        } else {
            [void]$numBad.Add(("ADR 命名不合规（应为 ADR-NNN-*.md）：" + $f.Name))
        }
    }

    $prdBad = New-Object System.Collections.ArrayList
    $prdFiles = @(Get-ChildItem -LiteralPath $prdDir -Filter '*.md' -File -ErrorAction SilentlyContinue | Sort-Object Name)
    foreach ($f in $prdFiles) {
        if ($f.Name -notmatch '^REQ-.+\.md$') { [void]$prdBad.Add($f.Name) }
    }

    $adrMin = 0
    $adrMax = 0
    if ($adrNums.Count -gt 0) {
        $sortedNums = @($adrNums.Keys | Sort-Object)
        $adrMin = [int]$sortedNums[0]
        $adrMax = [int]$sortedNums[$sortedNums.Count - 1]
    }

    if ($numBad.Count -gt 0 -or $prdBad.Count -gt 0) {
        foreach ($b in $numBad) { Write-Host ('       ' + $b) -ForegroundColor Red }
        foreach ($b in $prdBad) { Write-Host ('       docs/prd 命名不合规（应为 REQ-*.md）: ' + $b) -ForegroundColor Red }
        Write-Check 4 'ADR 编号 / REQ 命名' 'FAIL' ("ADR {0} 篇 / REQ {1} 篇，问题 {2} 处" -f $adrFiles.Count, $prdFiles.Count, ($numBad.Count + $prdBad.Count))
        Add-Error ("ADR 编号 / REQ 命名不合规 {0} 处" -f ($numBad.Count + $prdBad.Count))
    } else {
        $gap = 0
        if ($adrNums.Count -gt 0) { $gap = ($adrMax - $adrMin + 1) - $adrNums.Count }
        $extra = ''
        if ($gap -gt 0) { $extra = ('；有 ' + $gap + ' 个断号（删 ADR 会断号，合法，仅提示不判错）') }
        Write-Check 4 'ADR 编号 / REQ 命名' 'OK' ("ADR {0} 篇（{1:D3}-{2:D3}）无重号；REQ {3} 篇命名合规{4}" -f $adrFiles.Count, $adrMin, $adrMax, $prdFiles.Count, $extra)
    }
}

Add-Untested '[5] 新增 REQ 必要性段' '由 pre-commit 检查 8（check-req-necessity.ps1）承担，本脚本不重复'
Write-Check 5 '新增 REQ 必要性段' '未测' '由 pre-commit 检查 8 承担'

# ---------------------------------------------------------------------------
# 检项 [6]：每个 changes/<feat>/ 目录含 summary.md
#
# 为什么补（PRD §六 Q4 判据授权）：changes/ 是「每次改动都有追踪」的落点，
# summary.md 缺失 = 这次改动不可审计，而它不会自己报错。
# `_template` 是模板目录，**豁免**——它不是一次真实变更。
# ---------------------------------------------------------------------------
$changesRoot = Join-Path $harnessDir 'changes'
if (-not (Test-Path -LiteralPath $changesRoot)) {
    Add-Untested '[6] changes/*/summary.md' '.harness/changes 目录不存在，无法判定'
    Write-Check 6 'changes/*/summary.md' '未测' 'changes 目录不存在'
} else {
    $featDirs = @(Get-ChildItem -LiteralPath $changesRoot -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -ne '_template' } | Sort-Object Name)
    $missingSummary = New-Object System.Collections.ArrayList
    foreach ($d in $featDirs) {
        if (-not (Test-Path -LiteralPath (Join-Path $d.FullName 'summary.md'))) { [void]$missingSummary.Add($d.Name) }
    }
    if ($missingSummary.Count -gt 0) {
        foreach ($m in $missingSummary) { Write-Host ('       缺 summary.md: ' + $m) -ForegroundColor Red }
        Write-Check 6 'changes/*/summary.md' 'FAIL' ("{0}/{1} 个 feat 目录缺 summary.md" -f $missingSummary.Count, $featDirs.Count)
        Add-Error ("changes 目录缺 summary.md {0} 处" -f $missingSummary.Count)
    } else {
        Write-Check 6 'changes/*/summary.md' 'OK' ("{0} 个 feat 目录全部含 summary.md" -f $featDirs.Count)
    }
}

# ---------------------------------------------------------------------------
# 检项 [7]：Seen ≥ 2 的经验必须已升级为规则 / 机器检查（F3 的验收项）
#
# 为什么要机器查：Seen 计数制的原话是「同一类坑第 2 次出现就必须升级」，但此前它只是
# 一句话写在 LESSONS.md 头里 —— 是否升级全靠人回头看。本检项把它变成退出码。
# 判据：索引表里每行是 `| 条目 | 一句话 | Seen | 状态 | 正文 |`；Seen ≥ 2 且状态列落在
# 「未升级 / 候选 / 空」→ error。
# ---------------------------------------------------------------------------
if (-not (Test-Path -LiteralPath $lessonsPath)) {
    Add-Untested '[7] Seen≥2 已升级' '.harness/LESSONS.md 不存在，无索引可判'
    Write-Check 7 'Seen≥2 经验已升级' '未测' 'LESSONS 索引不存在'
} else {
    $lessonsLines = @(Get-Content -LiteralPath $lessonsPath -Encoding UTF8)
    $seenRows = New-Object System.Collections.ArrayList
    $unupgraded = New-Object System.Collections.ArrayList
    foreach ($ln in $lessonsLines) {
        if ($ln -notmatch '^\s*\|\s*L-(\d+)\s*\|') { continue }
        $cells = @($ln -split '\|' | ForEach-Object { $_.Trim() })
        # cells[0] 为空（行首 | 前），cells[1]=条目，[2]=一句话，[3]=Seen，[4]=状态
        if ($cells.Count -lt 5) { continue }
        $seenVal = 0
        if (-not [int]::TryParse(($cells[3] -replace '[^0-9]', ''), [ref]$seenVal)) { continue }
        [void]$seenRows.Add($cells[1])
        if ($seenVal -lt 2) { continue }
        $status = $cells[4]
        if ($status -eq '' -or $status -eq '-' -or $status -eq '—' -or $status -match '未升级' -or $status -match '候选') {
            [void]$unupgraded.Add(("{0}（Seen {1}，状态：{2}）" -f $cells[1], $seenVal, $status))
        }
    }
    if ($seenRows.Count -eq 0) {
        Add-Untested '[7] Seen≥2 已升级' 'LESSONS 索引里没解析到条目行（表格格式变了？）→ 不判'
        Write-Check 7 'Seen≥2 经验已升级' '未测' '索引无条目行'
    } elseif ($unupgraded.Count -gt 0) {
        foreach ($u in $unupgraded) { Write-Host ("       " + $u) -ForegroundColor Red }
        Write-Host '       → 按 Seen 计数制：≥2 次必须升级为规则或机器检查（写进 rules/ 或 scripts/）' -ForegroundColor Gray
        Write-Check 7 'Seen≥2 经验已升级' 'FAIL' ("{0} 条未升级" -f $unupgraded.Count)
        Add-Error ("Seen≥2 的经验 {0} 条未升级" -f $unupgraded.Count)
    } else {
        Write-Check 7 'Seen≥2 经验已升级' 'OK' ("索引 {0} 条，Seen≥2 的全部已升级" -f $seenRows.Count)
    }
}

# ---------------------------------------------------------------------------
# 检项 [8]：常驻文档里的「检查 N/M」分母必须与 pre-commit.ps1 实际项数一致
#
# 为什么加这一条（F1 的已知盲区，已实证 2 处）：
#   原版只查 AGENTS.md 的**路径**断链，不查文档**内部编号**。实测漏掉的两处：
#   LESSONS L-1 写「检查 3/6」、L-3 写「检查 4/7」，而当时 pre-commit 已是 10 项。
#   这类漂移的危害是**读者按过期口径行动**——照着 6 项清单做完就以为过了门禁。
#
# 取数方式：
#   - 权威值 = pre-commit.ps1 里 `Write-Section '检查 N/M'` 的分母（去重后必须唯一）；
#   - 受检面 = **常驻文档**（AGENTS.md + .harness/rules + lessons + wiki + skills/README.md）；
#   - **.harness/changes/ 与 scripts/ 豁免**：历史记录与脚本注释里引用「当时的编号」
#     是正确的（本文件头部就写着「L-1 写检查 3/6」这类历史事实）。想把受检面扩到
#     scripts/ 的人，先想清楚这一点，否则一扩就误报一片。
#
# 修法提示（写进 error，别让人猜）：改成当前分母，或去掉分母只写「检查 N」。
# ---------------------------------------------------------------------------
$preCommitPath = Join-Path (Join-Path $repoRoot 'scripts') 'pre-commit.ps1'
if (-not (Test-Path -LiteralPath $preCommitPath)) {
    Add-Untested '[8] 检查编号分母一致性' '找不到 scripts/pre-commit.ps1，无权威值可判'
    Write-Check 8 '检查编号分母一致性' '未测' 'pre-commit.ps1 不存在'
} else {
    $denoms = @{}
    foreach ($m in [regex]::Matches((Read-TextFile $preCommitPath), '检查\s+\d+\s*/\s*(\d+)')) {
        $denoms[$m.Groups[1].Value] = $true
    }
    if ($denoms.Count -eq 0) {
        Add-Untested '[8] 检查编号分母一致性' 'pre-commit.ps1 里解析不到「检查 N/M」形态（Write-Section 换了写法？）'
        Write-Check 8 '检查编号分母一致性' '未测' '无权威值'
    } elseif ($denoms.Count -gt 1) {
        foreach ($k in ($denoms.Keys | Sort-Object)) { Write-Host ('       分母候选: ' + $k) -ForegroundColor Red }
        Write-Check 8 '检查编号分母一致性' 'FAIL' 'pre-commit.ps1 自身出现多个不同分母（先修脚本自己）'
        Add-Error 'pre-commit.ps1 自身的「检查 N/M」分母不一致'
    } else {
        $authority = @($denoms.Keys)[0]
        $scanFiles = New-Object System.Collections.ArrayList
        [void]$scanFiles.Add($agentsPath)
        foreach ($sub in @('rules', 'lessons', 'wiki')) {
            $subDir = Join-Path $harnessDir $sub
            if (Test-Path -LiteralPath $subDir) {
                foreach ($f in @(Get-ChildItem -LiteralPath $subDir -Filter '*.md' -File -ErrorAction SilentlyContinue)) {
                    [void]$scanFiles.Add($f.FullName)
                }
            }
        }
        $routeReadme = Join-Path $skillsRoot 'README.md'
        if (Test-Path -LiteralPath $routeReadme) { [void]$scanFiles.Add($routeReadme) }

        $stale = New-Object System.Collections.ArrayList
        $refCount = 0
        foreach ($fp in $scanFiles) {
            if (-not (Test-Path -LiteralPath $fp)) { continue }
            foreach ($hit in @(Select-String -LiteralPath $fp -Pattern '检查\s+(\d+)\s*/\s*(\d+)' -AllMatches -Encoding UTF8 -ErrorAction SilentlyContinue)) {
                foreach ($mm in $hit.Matches) {
                    $refCount++
                    if ($mm.Groups[2].Value -ne $authority) {
                        [void]$stale.Add(("{0}:{1} 写「检查 {2}/{3}」，当前实际是 {4} 项" -f (Get-RepoRelPath $fp), $hit.LineNumber, $mm.Groups[1].Value, $mm.Groups[2].Value, $authority))
                    }
                }
            }
        }
        if ($stale.Count -gt 0) {
            foreach ($s in $stale) { Write-Host ('       ' + $s) -ForegroundColor Red }
            Write-Host '       → 改成当前分母，或去掉分母只写「检查 N」（不带分母就不会漂）' -ForegroundColor Gray
            Write-Check 8 '检查编号分母一致性' 'FAIL' ("{0}/{1} 处分母过期（当前 {2} 项）" -f $stale.Count, $refCount, $authority)
            Add-Error ("常驻文档「检查 N/M」分母过期 {0} 处" -f $stale.Count)
        } else {
            Write-Check 8 '检查编号分母一致性' 'OK' ("{0} 处引用分母均为 /{1}，与 pre-commit 实际一致" -f $refCount, $authority)
        }
    }
}

# ---------------------------------------------------------------------------
# 结论
# ---------------------------------------------------------------------------
Write-Host ''
if ($errors.Count -eq 0) {
    Write-Host 'ERRORS: []' -ForegroundColor Green
} else {
    Write-Host 'ERRORS:' -ForegroundColor Red
    foreach ($e in $errors) { Write-Host ('  - ' + $e) -ForegroundColor Yellow }
}

if ($untested.Count -gt 0) {
    Write-Host ''
    Write-Host ('未测项：' + $untested.Count + ' 项（未测 ≠ 通过：它们没有产生任何结论）') -ForegroundColor Yellow
    foreach ($u in $untested) { Write-Host ('  ? ' + $u.Name + ' —— ' + $u.Why) -ForegroundColor DarkGray }
}

Write-Host ''
if ($errors.Count -gt 0) { exit 1 }
exit 0
