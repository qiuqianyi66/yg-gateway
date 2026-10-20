# pre-commit.ps1 — 提交前检查：把 .harness/rules/ 里的规范变成**退出码**
#
# 为什么需要：规范写在文档里没人看（编码规范.md 第八节那 26 项自检清单，靠人记得住
# 才有效）；只有变成退出码才有约束力。本脚本把下列约束机器化：
#
#   1. 格式零容忍    本次改动涉及的 .go 文件必须 gofmt 干净
#   2. R10 错误处理  diff 里**新增**的静默忽略点必须带解释注释；`_ = <标识符>` 一律拒绝
#                    （判据见 .harness/rules/ERROR-HANDLING.md）
#   3. 门禁四件套    go build / go vet / go test
#   4. 治理门禁      check-boundaries.ps1（依赖方向）+ check-depth.ps1（深度棘轮）
#   5. PS 地形      引号陷阱（L-1）+ 字面名垃圾文件（L-3）
#   6. 阶段 0       新增的 docs/prd/REQ-*.md 必须含「必要性」段（check-req-necessity.ps1，见
#                    .harness/rules/开发流程规范.md「零、阶段 0」）
#   7. 治理自洽     check-harness.ps1（AGENTS.md 路径断链 / SKILL frontmatter / 路由表；
#                    「未测」项不影响退出码，REQ-harness-engineering-2026-10-19 F1）
#   8. 上下文预算   check-context-budget.ps1（常驻层 token 估算棘轮，只许下调；同上 F2）
#
# 为什么新检查一律挂在这里而不是另开脚本入口（2026-10-19 实测）：CI 的 `gate` job
# （.github/workflows/gate.yml）只跑**一条命令**——`pre-commit.ps1 -Fast -AllFiles -Base <ref>`。
# 所以挂进本文件 = 本地与 CI **同时**生效；另开一份门禁实现必然分叉（两份门禁会给出
# 「这条线过了、那条线没过」的假保证）。本文件是全仓唯一的门禁实现。
#
# 用法：
#   powershell -NoProfile -File scripts/pre-commit.ps1           # 全量（提交前必跑）
#   powershell -NoProfile -File scripts/pre-commit.ps1 -Fast     # 跳过 go test ./...
#   powershell -NoProfile -File scripts/pre-commit.ps1 -Install  # 额外安装 .git/hooks/pre-commit
#
# CI 用法（.github/workflows/release.yml 的 gate job；实现见 .github/workflows/gate.yml）：
#   pwsh -NoProfile -File scripts/pre-commit.ps1 -Fast -AllFiles -Base origin/main
#   - -AllFiles：gofmt 查**全仓** ./cmd ./internal（CI 工作区干净，查「改动文件」恒空 → 假绿）
#   - -Base <ref>：R10 与「改动文件」按 `git diff <ref>...HEAD` 判定（CI 里改动已提交，
#     工作区 diff 恒空；不传 base 会把这项变成「检查了 0 个文件」的假绿）
#   - -Fast：go test 由 CI 的 test job 独占，gate job 不重复跑（check-depth 内部仍会跑一次带覆盖率的）
#
# 退出码：0 = 全部通过；1 = 有检查失败；2 = 脚本自身失败
#
# 已知边界（明说的局限，不是放水）：
#   - R10 检查只覆盖**本次改动新增的行**，不翻存量 100+ 处的旧账（那会产生噪音 diff，
#     且失去"改动即论证"的意义）。
#   - "是否有解释注释"是启发式（同行的 `//` 或紧邻两行内含「忽略」/`ignore`）；
#     语义仍需人读。它的价值在于**强迫每条新增忽略都写出理由**，而不是自动判断对错。
#   - 判定以**工作区当前内容**为准（读文件取注释）；若 staged 内容与工作区不一致，
#     仍以工作区为准——避免误报。
#   - --Install 会往 .git/hooks/ 写 hook：此后**每次**提交都会跑本脚本（含 go test，
#     约 40 秒）。多会话并行工作时建议不装，改为手动跑。

param(
    [switch]$Fast,
    [switch]$Install,
    [switch]$AllFiles,
    [string]$Base = ''
)

$ErrorActionPreference = 'Continue'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$goExe = 'C:\Program Files\Go\bin\go.exe'
if (-not (Test-Path $goExe)) { $goExe = 'go' }

# 主机可执行文件探测：本机（Windows PowerShell 5.1）没有独立 pwsh；CI（ubuntu runner）
# 只有 pwsh、没有 `powershell`。子脚本一律用它调起，别写死 `powershell`。
$hostExe = 'powershell'
$pwshCmd = Get-Command pwsh -ErrorAction SilentlyContinue
if ($pwshCmd) { $hostExe = $pwshCmd.Source }

# gofmt 优先取 go 同目录的 gofmt.exe（Windows 官方发行版形态）；拿不到就退 PATH 上的 gofmt
# （Linux runner）。注意 $goExe 退化成 'go' 时 Split-Path 返回空串，Join-Path 会抛绑定错误。
$gofmtExe = 'gofmt'
if (Test-Path $goExe) {
    $candidate = Join-Path (Split-Path $goExe) 'gofmt.exe'
    if (Test-Path $candidate) { $gofmtExe = $candidate }
}

$env:GOPROXY = 'https://goproxy.cn,direct'
$env:CGO_ENABLED = '0'

$failures = New-Object System.Collections.ArrayList

function Add-Failure($msg) { [void]$failures.Add($msg) }
function Write-Section($title) {
    Write-Host ''
    Write-Host ("--- " + $title) -ForegroundColor Cyan
}
function Write-Pass($msg) { Write-Host ("  [OK]   " + $msg) -ForegroundColor Green }
function Write-Bad($msg) { Write-Host ("  [FAIL] " + $msg) -ForegroundColor Red }

# ---------------------------------------------------------------------------
# 本次改动涉及的文件
#   本地：工作区 + 暂存区 + 未跟踪
#   CI（-Base）：与 base 的已提交差分 —— CI 工作区是干净的，工作区 diff 恒为空，
#   若照搬本地口径，下面所有「本次改动」检查都会变成「检查了 0 个文件」的假绿。
# ---------------------------------------------------------------------------
$changed = New-Object System.Collections.ArrayList

if ($Base) {
    $null = & git rev-parse --verify --quiet "$Base^{commit}" 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host "错误：-Base 指定的 ref 解析失败：$Base" -ForegroundColor Red
        exit 2
    }
    foreach ($f in @(& git diff --name-only --diff-filter=ACMR "$Base...HEAD" 2>$null)) {
        if ($f -and -not $changed.Contains($f)) { [void]$changed.Add($f) }
    }
} else {
    foreach ($set in @(
            @(& git diff --name-only --diff-filter=ACMR 2>$null),
            @(& git diff --cached --name-only --diff-filter=ACMR 2>$null),
            @(& git ls-files --others --exclude-standard 2>$null))) {
        foreach ($f in $set) {
            if ($f -and -not $changed.Contains($f)) { [void]$changed.Add($f) }
        }
    }
}

if ($LASTEXITCODE -ne 0 -and $changed.Count -eq 0) {
    # 这条消息曾经只写「不在 git 仓库内？」——但 `-Base` 指向一个**没有共同祖先**的分支
    # （已被 rebase 掉 / 无关分支）时也会走到这里，那个提示会把排查引偏（第 4 轮复审 🔵-1）。
    Write-Host '错误：无法枚举改动文件' -ForegroundColor Red
    Write-Host ("      -Base = '" + $Base + "'") -ForegroundColor Gray
    Write-Host '      可能原因：①不在 git 仓库内；②-Base 指向的分支与当前分支**没有共同祖先**（已被 rebase 掉 / 无关分支）→ git diff 会报 "no merge base"。' -ForegroundColor Gray
    Write-Host '      自查：git diff --name-only "<base>...HEAD" 会失败时，先 git merge-base "<base>" HEAD 确认。' -ForegroundColor Gray
    exit 2
}

Write-Host "=== 提交前检查（$($changed.Count) 个改动文件） ===" -ForegroundColor Cyan
if ($changed.Count -eq 0) {
    Write-Host '  （无改动文件：格式与 R10 检查自动通过，其余门禁照跑）' -ForegroundColor Gray
}

$existing = @($changed | Where-Object { Test-Path -LiteralPath $_ })
$goFiles = @($existing | Where-Object { $_ -like '*.go' })
$prodGoFiles = @($goFiles | Where-Object { $_ -notlike '*_test.go' })

# ---------------------------------------------------------------------------
# 检查 1：gofmt 零漂移（只查本次改动的文件）
# ---------------------------------------------------------------------------
Write-Section '检查 1/10：gofmt'
# 本地只看本次改动的 .go 文件（存量已清零，增量严格拦截）；
# CI 用 -AllFiles 看全仓——CI 工作区干净，「改动文件」恒为空，只看改动等于不检查。
$gofmtTargets = @()
if ($AllFiles) {
    $gofmtTargets = @('./cmd', './internal')
    Write-Host '  范围：全仓 ./cmd ./internal（-AllFiles）' -ForegroundColor Gray
} else {
    $gofmtTargets = @($goFiles)
}
if ($gofmtTargets.Count -eq 0) {
    Write-Pass '无 .go 改动'
} else {
    $unformatted = @(& $gofmtExe -l @gofmtTargets 2>$null)
    if ($unformatted.Count -gt 0) {
        foreach ($f in $unformatted) { Write-Bad ("gofmt 漂移: " + $f) }
        Add-Failure "gofmt 漂移 $($unformatted.Count) 个文件（修法：gofmt -w <文件>）"
    } else {
        Write-Pass "gofmt 干净（$($gofmtTargets.Count) 个目标）"
    }
}

# ---------------------------------------------------------------------------
# 检查 2：R10 静默忽略（只查 diff 新增行，只查生产代码）
#
# 两条规则：
#   (a) `_ = <标识符>`（压制 unused 变量，不是丢弃函数返回值）→ 硬失败。
#       `_ = f()` 是"我不关心返回值"，正当；`_ = someVar` 意味着某个变量算了没用上，
#       掩盖的是"这段逻辑白算了"或"结果本该被使用"（见 ERROR-HANDLING.md 第四节）。
#   (b) 其余新增忽略点（`_ = ...` / `, _ := ...` / `, _ = ...`）→ 必须带解释注释
#       （同行 `//`，或紧邻上两行内含「忽略」/`ignore`）。
# ---------------------------------------------------------------------------
Write-Section '检查 2/10：R10 静默忽略（新增行）'

$ignorePattern = '^\s*_ = |^\s*_ := |,\s*_\s*:?='
$identPattern = '^\s*_ = ([A-Za-z_][A-Za-z0-9_]*)\s*(//.*)?$'

# 收集 diff 的新增行（含未跟踪文件的全部行）
$added = New-Object System.Collections.ArrayList
$diffText = @()
if ($Base) {
    # CI：改动已提交，只有与 base 相比才能看到「本次新增了哪些行」。
    $diffText += @(& git diff -U0 --no-color --no-ext-diff "$Base...HEAD" 2>$null)
} else {
    $diffText += @(& git diff -U0 --no-color --no-ext-diff 2>$null)
    $diffText += @(& git diff --cached -U0 --no-color --no-ext-diff 2>$null)
}

$curFile = $null
$newLine = 0
foreach ($ln in $diffText) {
    if ($ln -like '+++ *') {
        if ($ln -like '+++ b/*') { $curFile = $ln.Substring(6) } else { $curFile = $null }
        continue
    }
    if ($ln -match '^@@ -\d+(,\d+)? \+(\d+)(,\d+)? @@') {
        $newLine = [int]$Matches[2]
        continue
    }
    if ($null -eq $curFile) { continue }
    if ($ln.StartsWith('+')) {
        [void]$added.Add([pscustomobject]@{ File = $curFile; Line = $newLine; Text = $ln.Substring(1) })
        $newLine++
        continue
    }
    if ($ln.StartsWith('-')) { continue }
    $newLine++
}
foreach ($f in @(& git ls-files --others --exclude-standard 2>$null)) {
    if (-not $f -or -not (Test-Path -LiteralPath $f)) { continue }
    $i = 0
    foreach ($ln in @(Get-Content -LiteralPath $f)) {
        $i++
        [void]$added.Add([pscustomobject]@{ File = $f; Line = $i; Text = $ln })
    }
}

$fileCache = @{}
function Get-CachedLines($path) {
    if (-not $fileCache.ContainsKey($path)) {
        if (Test-Path -LiteralPath $path) {
            $fileCache[$path] = @(Get-Content -LiteralPath $path)
        } else {
            $fileCache[$path] = @()
        }
    }
    return $fileCache[$path]
}

$r10bad = New-Object System.Collections.ArrayList
foreach ($a in $added) {
    if ($a.File -notlike '*.go' -or $a.File -like '*_test.go') { continue }
    if ($a.Text -notmatch $ignorePattern) { continue }

    # (a) 压制 unused 变量的形态 → 硬失败
    if ($a.Text -match $identPattern) {
        [void]$r10bad.Add(("{0}:{1}  {2}`n           → 形如「_ = <标识符>」压制 unused 变量：变量算了没用上，「_ =」不是修法。" -f $a.File, $a.Line, $a.Text.Trim()))
        continue
    }

    # 类型断言（`x, _ := v.(T)`、`x, _ = r["k"].(bool)`）按判据④ 自明，不要求注释——
    # 与 .harness/rules/ERROR-HANDLING.md §四「类型断言 / w.Write 自明，不追求全补注释」对齐。
    # 注意：这样排除的只有 `.(T)` 形态；`x, _ = f()`（丢弃多返回值，可能含 error）仍要求注释，
    # 因为本轮审计里真吞错误正是从这种形态里找出来的。
    if ($a.Text -match ',\s*_\s*:?=\s*[^=]*\.\(') { continue }

    # (b) 是否有解释注释（同行，或紧邻上两行含「忽略」/ignore）
    $lines = Get-CachedLines $a.File
    $justified = $false
    if ($a.Text -match '//') { $justified = $true }
    if (-not $justified -and $lines.Count -ge $a.Line) {
        for ($k = $a.Line - 2; $k -le $a.Line - 1; $k++) {
            if ($k -lt 1 -or $k -gt $lines.Count) { continue }
            $prev = $lines[$k - 1]
            if ($prev -match '忽略' -or $prev -match 'ignore' -or $prev -match 'IGNORE') { $justified = $true; break }
        }
    }
    if (-not $justified) {
        [void]$r10bad.Add(("{0}:{1}  {2}`n           → 新增的静默忽略点缺解释注释。补一行「// 忽略：<原因>，不影响 <什么>」" -f $a.File, $a.Line, $a.Text.Trim()))
    }
}

if ($r10bad.Count -gt 0) {
    foreach ($v in $r10bad) { Write-Bad $v }
    Write-Host '  判据见 .harness/rules/ERROR-HANDLING.md' -ForegroundColor Gray
    Add-Failure "R10：新增静默忽略点 $($r10bad.Count) 处未论证"
} else {
    Write-Pass "新增行无未论证的静默忽略（检查了 $($prodGoFiles.Count) 个生产文件）"
}

# ---------------------------------------------------------------------------
# 检查 3：PowerShell 引号陷阱（只查 .ps1 的新增行）
#
# 为什么需要（LESSONS.md L-1，Seen 3 → 按机制落地机器检查）：
# PowerShell 5.1 里**反斜杠不是转义符**——双引号串里出现「反斜杠 + 双引号」会**提前结束**这个串，
# 后面的内容被当成命令或参数解析：轻则参数被切成别的路径（git 报 "/ 不在仓库内"），
# 重则整条命令一个字符都不执行，而执行者以为已经跑过了。
# 本仓已连踩三次：① pre-commit.ps1 自身解析失败 ② 内联命令整条未执行 ③ 提交命令把路径切坏。
# 判据：.ps1 里出现该形态**一律是 bug**（正确写法：行内引用用「」，需要字面双引号用单引号串
# 或 here-string；确实要转义引号则用反引号转义）。
# ---------------------------------------------------------------------------
Write-Section '检查 3/10：PowerShell 引号陷阱（新增行）'
$psTrap = New-Object System.Collections.ArrayList
foreach ($a in $added) {
    if ($a.File -notlike '*.ps1') { continue }
    if ($a.Text -notmatch '\\[\u0022]') { continue }
    [void]$psTrap.Add(("{0}:{1}  {2}" -f $a.File, $a.Line, $a.Text.Trim()))
}
if ($psTrap.Count -gt 0) {
    foreach ($t in $psTrap) { Write-Bad ("PowerShell 引号陷阱: " + $t) }
    Add-Failure "PowerShell 引号陷阱 $($psTrap.Count) 处（PS 5.1 不支持反斜杠转义；行内引用用「」，字面引号用单引号串或 here-string）"
} else {
    Write-Pass '新增 .ps1 行无「反斜杠+双引号」形态'
}

# ---------------------------------------------------------------------------
# 检查 4：仓库根的字面名垃圾文件（LESSONS.md L-3，Seen 2 → 按机制落地为机器检查）
#
# 为什么需要：给原生命令传「带变量的参数」时，PS 5.1 **不展开**变量——不只是单引号串，
# 连**未加引号**的 `-flag=$var` 也不展开（探针实测：`cmd /c echo -coverprofile=$x` 原样输出
# `$x`，而加了双引号才变成 `VAL`）。于是 `go test -coverprofile=$out` 会在**仓库根**
# 写下一个名叫 `$out` 的文件（内容是覆盖率 profile），而且**不报错**：
# 执行者以为产出了 profile、实际产出的是一坨垃圾文件。已踩 2 次
# （`$out`/`$p`/`$tmp`，以及看起来像正常插值的未加引号 `$cov`/`$abs`）。
#
# 判据刻意收窄，避免误报：
#   - 只看 **git 未跟踪** 的文件（`--exclude-standard`，已 `.gitignore` 的不管）；
#   - 只看 **仓库根**，名字里带分隔符的（子目录）一律放过；
#   - 名字以 `$` 开头，或整体形如 `out` / `p` / `tmp` / `cov` / `cover` / `profile`（可带前导点）
#     —— 这类名字在仓库根出现只可能是变量名残留。
# 作用域说明：这条检查天然是「本地」的（CI 工作区干净、无未跟踪文件），CI 里必然通过，
# 这不是放水——它要拦的本来就是本地跑覆盖率时的手误。
# ---------------------------------------------------------------------------
Write-Section '检查 4/10：仓库根的字面名垃圾文件（L-3）'
$literalBad = New-Object System.Collections.ArrayList
foreach ($f in @(& git ls-files --others --exclude-standard 2>$null)) {
    if (-not $f) { continue }
    if ($f -match '[\\/]') { continue }
    if ($f.StartsWith('$') -or $f -match '^\.?(out|p|tmp|cov|cover|profile)$') {
        [void]$literalBad.Add($f)
    }
}
if ($literalBad.Count -gt 0) {
    foreach ($b in $literalBad) { Write-Bad ('仓库根出现字面名垃圾文件: ' + $b) }
    Write-Host '  → 这几乎一定是「带变量的参数没被 PS 展开」：PS 5.1 里单引号串内的变量不展开，未加引号的 -flag=$var 同样不展开。' -ForegroundColor Gray
    Write-Host '  → 正确写法：$out = Join-Path $env:TEMP "cov.out"; go test "-coverprofile=$out"（**双引号**，且路径落到 $env:TEMP）' -ForegroundColor Gray
    Write-Host '  → 若确实是需要的文件名，请把它加到 .gitignore 或改名（仓库根不该有这类名字）。' -ForegroundColor Gray
    Add-Failure "仓库根字面名垃圾文件 $($literalBad.Count) 个（L-3）"
} else {
    Write-Pass '仓库根无字面名垃圾文件'
}

# ---------------------------------------------------------------------------
# 检查 5：构建与静态检查
# ---------------------------------------------------------------------------
Write-Section '检查 5/10：go build + go vet'
$buildOut = @(& $goExe build ./... 2>&1)
if ($LASTEXITCODE -ne 0) {
    $buildOut | Select-Object -First 40 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
    Write-Bad 'go build ./... 失败'
    Add-Failure 'go build ./... 失败'
} else {
    Write-Pass 'go build ./... 通过'
}

$vetOut = @(& $goExe vet ./... 2>&1)
if ($LASTEXITCODE -ne 0) {
    $vetOut | Select-Object -First 40 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
    Write-Bad 'go vet ./... 有输出'
    Add-Failure 'go vet ./... 有输出'
} else {
    Write-Pass 'go vet ./... 无输出'
}

# ---------------------------------------------------------------------------
# 检查 6：测试
# ---------------------------------------------------------------------------
Write-Section '检查 6/10：go test ./...（-cover，输出复用给检查 7）'
# 只跑**一次**全量测试：这份 `-cover` 输出既是「测试是否绿」的判据，也是检查 7 深度棘轮
# 的覆盖率来源（先落 $env:TEMP，再传给 check-depth.ps1）。
# 不加 -cover 会让 check-depth 内部又跑一遍全量 —— 同一个门禁里跑两遍，本机实测第二遍就是
# 63s（A/B 总差 66.4s，见 .harness/changes/2026-10-17-precommit-single-test-and-doc-debt/
# summary.md §2.1）。REQ-precommit-single-test-and-doc-debt-2026-10-17 F-902。
$coverTmpPath = ''   # 临时文件路径：**独立变量**，因为红路径会把 $coverFile 置空（见检查 7 的清理）
$coverFile = ''
if ($Fast) {
    Write-Host '  [SKIP] -Fast 已跳过测试（提交前请至少跑一次全量；此时 check-depth 会自跑一次带覆盖率的）' -ForegroundColor Yellow
} else {
    # 路径落 $env:TEMP：仓库根不该出现 cov/cover/profile 这类字面名垃圾文件（L-3）。
    # 文件名用 GUID 而不是 $PID：PID 会被系统复用，残留文件可能被下一次运行当成自己的。
    $coverTmpPath = Join-Path $env:TEMP ("yg-precommit-cover-{0}.txt" -f [guid]::NewGuid().ToString('N'))
    $coverFile = $coverTmpPath
    $testOut = @(& $goExe test ./... -cover -count=1 2>&1)
    $testOk = ($LASTEXITCODE -eq 0)
    $testOut | Set-Content -LiteralPath $coverFile -Encoding UTF8
    if (-not $testOk) {
        $testOut | Where-Object { $_ -match 'FAIL|panic|--- FAIL' } | Select-Object -First 40 |
            ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad 'go test ./... 有失败'
        Add-Failure 'go test ./... 有失败'
        # 测试红：绝不把这份数字喂给深度棘轮（那等于用覆盖率掩盖测试失败）
        $coverFile = ''
    } else {
        $okCount = @($testOut | Where-Object { $_ -match '^ok\s' }).Count
        Write-Pass "go test ./... 全绿（$okCount 个包 ok；-cover 输出已复用给深度棘轮）"
    }
}

# ---------------------------------------------------------------------------
# 检查 7：治理门禁（依赖方向 + 深度棘轮）
# ---------------------------------------------------------------------------
Write-Section '检查 7/10：边界与深度棘轮'
$bout = @(& $hostExe -NoProfile -File scripts/check-boundaries.ps1 2>&1)
if ($LASTEXITCODE -ne 0) {
    $bout | Select-Object -First 30 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
    Write-Bad 'check-boundaries.ps1 失败（依赖方向违规）'
    Add-Failure 'check-boundaries.ps1 失败'
} else {
    Write-Pass '依赖方向检查通过'
}

$depthArgs = @('-NoProfile', '-File', 'scripts/check-depth.ps1')
if ($coverFile -ne '' -and (Test-Path -LiteralPath $coverFile)) {
    $depthArgs += @('-CoverageStdout', $coverFile)
}
$dout = @(& $hostExe @depthArgs 2>&1)
if ($LASTEXITCODE -ne 0) {
    $dout | Select-Object -Last 20 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
    Write-Bad 'check-depth.ps1 失败（深度指标回退 / 覆盖率不可用）'
    Add-Failure 'check-depth.ps1 失败'
} else {
    Write-Pass '深度棘轮未回退'
}
# 清理临时覆盖率文件：判据用 $coverTmpPath（**不管测试红绿都删**）。
# 早期版本用 `$coverFile -ne ''` 作判据，而红路径会把 $coverFile 置空 → 临时文件泄漏
# （独立复审 🟢-1 实证：%TEMP% 里残留过一份含 4 行 FAIL 的 cover 文件）。
# 不加 -ErrorAction SilentlyContinue：删不掉就是真问题，要看得见（R10）；存在性用 Test-Path 判。
if ($coverTmpPath -ne '' -and (Test-Path -LiteralPath $coverTmpPath)) {
    Remove-Item -LiteralPath $coverTmpPath -Force
}

# ---------------------------------------------------------------------------
# 检查 8/10：新增需求文档必须有「必要性」段（阶段 0）
#
# 由来：2026-10-17 盘点 14 项待办有 6 项经实测不需要做。规则见
# .harness/rules/开发流程规范.md「零、阶段 0：必要性分析」；只对**本次新增**的
# docs/prd/REQ-*.md 生效（存量 11 份不追溯），实现在 scripts/check-req-necessity.ps1。
#
# 退出码三态必须**分开报**（2026-10-17 第 3 轮独立复审 🟢）：脚本自身失败（2）与
# 缺段（1）是两件事，混成一句「缺必要性段」会把排查引到错的地方。
# ---------------------------------------------------------------------------
Write-Section '检查 8/10：新增需求文档的必要性段（阶段 0）'
$reqArgs = @('-NoProfile', '-File', 'scripts/check-req-necessity.ps1')
if ($Base -ne '') { $reqArgs += @('-Base', $Base) }
$rout = @(& $hostExe @reqArgs 2>&1)
switch ($LASTEXITCODE) {
    0 { Write-Pass '需求文档阶段 0 校验通过' }
    1 {
        $rout | Select-Object -Last 12 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad '新增需求文档缺「必要性」段（阶段 0）'
        Add-Failure '阶段 0 必要性段缺失'
    }
    default {
        $rout | Select-Object -Last 12 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad ("check-req-necessity.ps1 自身失败（exit={0}，不是缺段）" -f $LASTEXITCODE)
        Add-Failure '阶段 0 校验脚本自身失败（非缺段）'
    }
}

# ---------------------------------------------------------------------------
# 检查 9/10：治理一致性体检（check-harness.ps1）
#
# 由来（REQ-harness-engineering-2026-10-19 §五 F1）：`.harness/` 下有规则 / 技能 / REQ /
# ADR / changes 目录，`AGENTS.md` 里还有一堆指向它们的路径——此前没有任何脚本能回答
# 「这套治理自身还自不自洽」。断链与数字漂移当场不报错，几周后才以「AI 又做错了」
# 的形式显形（本仓已发生过 doc-drift、cross-file-reconcile 两次）。
# （此处**不写具体数字**——写死就会漂移，那正是这个检查要防的问题。要数字跑 check-harness.ps1。）
#
# 退出码三态分开报（同检查 8）：1 = 有 error（真问题）；2 = 脚本自身失败。
# 另：脚本里**故意未实现**的检项会输出「未测」且**不影响退出码**——未测 ≠ 通过，
# 但不能让「还没做的检查」把门禁堵死（PRD §六 Q4：先跑两周统计真问题再决定是否补齐）。
# ---------------------------------------------------------------------------
Write-Section '检查 9/10：治理一致性体检（8 检项：路径 / 技能 / 编号 / 追踪 / 经验 / 分母）'
$hout = @(& $hostExe -NoProfile -File scripts/check-harness.ps1 2>&1)
switch ($LASTEXITCODE) {
    0 {
        $hout | Select-Object -First 12 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Pass '治理一致性体检无 error'
    }
    1 {
        $hout | Select-Object -Last 20 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad '治理一致性体检有 error'
        Add-Failure '治理一致性体检失败（check-harness.ps1）'
    }
    default {
        $hout | Select-Object -Last 20 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad ("check-harness.ps1 自身失败（exit={0}，不是治理不一致）" -f $LASTEXITCODE)
        Add-Failure '治理一致性体检脚本自身失败'
    }
}

# ---------------------------------------------------------------------------
# 检查 10/10：常驻上下文预算（check-context-budget.ps1）
#
# 由来（同上 PRD §五 F2）：HarnessTax（UC Berkeley/Arena，2026-09）实测——harness 对
# 成功率影响仅 ±2–5%，对成本影响可达 5×，成本主源是**首调用上下文**。本仓常驻层实测
# 22,603 token 却没有预算线 → 砍下去的上下文随时会长回来。
#
# 棘轮纪律：基线**只许下调**（-Update 收紧），基线 json 必须入库（本地改了不提交，
# CI 仍按旧值判 = 假绿）。压降常驻层后跑 -Update，再连同 json 一起提交。
# ---------------------------------------------------------------------------
Write-Section '检查 10/10：常驻上下文预算（棘轮，只许下调）'
$cout = @(& $hostExe -NoProfile -File scripts/check-context-budget.ps1 2>&1)
switch ($LASTEXITCODE) {
    0 {
        $cout | Select-Object -Last 4 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Pass '常驻上下文未超预算'
    }
    1 {
        $cout | Select-Object -Last 12 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad '常驻上下文超预算（回退）'
        Add-Failure '常驻上下文超预算（check-context-budget.ps1）'
    }
    default {
        $cout | Select-Object -Last 12 | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Bad ("check-context-budget.ps1 自身失败（exit={0}，不是超预算）" -f $LASTEXITCODE)
        Add-Failure '上下文预算脚本自身失败'
    }
}

# ---------------------------------------------------------------------------
# 可选：安装 git hook
# ---------------------------------------------------------------------------
if ($Install) {
    $hookPath = Join-Path $repoRoot '.git\hooks\pre-commit'
    if (Test-Path (Join-Path $repoRoot '.git')) {
        $hookBody = "#!/bin/sh`n# 由 scripts/pre-commit.ps1 -Install 生成`nexec powershell -NoProfile -ExecutionPolicy Bypass -File `"`$(git rev-parse --show-toplevel)/scripts/pre-commit.ps1`"`n"
        [System.IO.File]::WriteAllText($hookPath, $hookBody, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host ''
        Write-Host ("  [OK]   已安装 " + $hookPath + "（此后每次提交都会跑本脚本）") -ForegroundColor Green
    }
}

# ---------------------------------------------------------------------------
# 结果
# ---------------------------------------------------------------------------
Write-Host ''
if ($failures.Count -gt 0) {
    Write-Host ("提交前检查未通过（$($failures.Count) 项）：") -ForegroundColor Red
    foreach ($f in $failures) { Write-Host ("   - " + $f) -ForegroundColor Yellow }
    Write-Host ''
    Write-Host '修复后重跑；不要用 --no-verify / 跳过检查来绕过。' -ForegroundColor Gray
    exit 1
}

Write-Host '提交前检查全部通过。' -ForegroundColor Green
exit 0
