# check-req-necessity.ps1 — 阶段 0 的机器校验：新增需求文档必须含「必要性」段
#
# 为什么存在（.harness/rules/开发流程规范.md「零、阶段 0：必要性分析」）：
#   2026-10-17 的待办清单里 14 项有 6 项经实测不需要做。直接开工的代价是「做了一件不该做的事，
#   再花时间回滚」。人写清单时会把假设写成任务，所以要求每个**新需求**先把「为什么要做」
#   写下来——写不出来就是不该做。
#
# 判据（宽，只卡"有没有这一段"，不卡内容质量）：
#   本次**新增**的 docs/prd/REQ-*.md 里，必须有一行标题匹配 `^#{1,6}\s*.*必要性`
#   （`# 必要性`、`## 必要性`、`## 一、必要性` 都行）。代码围栏（```）内的行不计。
#
# 范围：**只查本次新增**（存量的 REQ 不追溯，否则第一次接入就会红一片，规则会被绕过）。
#
# ⚠ 两个必须防住的静默失败（2026-10-17 独立复审 🟡-1/🟡-2，都已在本脚本内处理）：
#   1) **git 调用失败必须报错**，不能把"命令失败"当成"没有新增文件"。否则 `-Base` 写错时
#      会以 exit 1 退出，而 pre-commit 会把它归因成「缺必要性段」——误导诊断。
#   2) **`core.quotePath` 默认 true**：`git diff --name-only` 会把中文文件名输出成
#      `"docs/prd/REQ-\344\270\255...md"`，正则不匹配 → 中文名的 REQ 被**静默跳过**。
#      本机之所以没暴露，是因为用户级 .gitconfig 设了 `false`，而 CI runner 没有那份配置。
#      所以三处 git 调用一律带 `-c core.quotePath=false`。
#
# 用法：
#   powershell -NoProfile -File scripts/check-req-necessity.ps1                    # 本地：index + 未跟踪
#   powershell -NoProfile -File scripts/check-req-necessity.ps1 -Base origin/main  # CI：<base>...HEAD
#
# 退出码：0 = 通过（含"本次没有新增 REQ"）；1 = 有新增 REQ 缺必要性段；2 = 脚本自身失败（git 失败等）

param(
    [string]$Base = ''
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# 统一入口：所有 git 调用都过这里（quotePath 关闭 + 退出码判据 + stdout/stderr 分离）
#
# ⚠ PowerShell 5.1 的两个坑（2026-10-17 第 3 轮独立复审实测，必须一起处理）：
#   1) 脚本级 `$ErrorActionPreference = 'Stop'` 时，**原生程序往 stderr 写一行**就会被包装成
#      **终止性** NativeCommandError → 配合 `2>&1` 会在判 `$LASTEXITCODE` 之前把脚本打死，
#      退出码变成 1、头注释承诺的提示一个字都不打。触发面真实：`no merge base`
#      （-Base 指向已被 rebase 掉/无关分支）、`detected dubious ownership`、`bad object`。
#      修法：**函数内把 EAP 降为 Continue**，并在 finally 里还原。
#   2) `2>&1` 会让**成功**时的 stderr（如 `warning: LF will be replaced by CRLF`）混进
#      "文件列表"。修法：stdout 与 stderr **分开取**（stderr 落临时文件，只在失败时打印）。
function Invoke-Git {
    param([string[]]$GitArgs, [string]$What)
    $prevEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $errFile = [System.IO.Path]::GetTempFileName()
    try {
        $out = @(& git -c core.quotePath=false @GitArgs 2> $errFile)
        $code = $LASTEXITCODE
        $errLines = @()
        if (Test-Path -LiteralPath $errFile) { $errLines = @(Get-Content -LiteralPath $errFile -Encoding UTF8) }
    } finally {
        # 清理放在 finally 里，但**仍在 Continue 语义下**：否则 Remove-Item 一旦抛错
        # （文件被占用等）会变成未捕获的终止性错误 → 脚本 exit 1，又被 pre-commit 读成「缺段」
        # （2026-10-17 第 4 轮复审 🔵-2）。先删再还原 EAP。
        if (Test-Path -LiteralPath $errFile) { Remove-Item -LiteralPath $errFile -Force -ErrorAction SilentlyContinue } # 忽略：临时文件删不掉不影响结论，且下面有唯一权威判据
        $ErrorActionPreference = $prevEap
    }
    if ($code -ne 0) {
        Write-Host ("错误：git {0} 失败（exit={1}）：{2}" -f $What, $code, ($GitArgs -join ' ')) -ForegroundColor Red
        # stderr 的两种形态都要处理（2026-10-17 第 4 轮复审 🟡）：
        #   PS 7：`2>` 落的是**原始** stderr（`fatal: …`）；
        #   PS 5.1：`2>` 落的是**格式化错误记录**（UTF-16），首行是 `git.exe : fatal: …`。
        # 早先的写法直接**丢掉**含 `git.exe :` 的行 —— 在 5.1 上等于把唯一有信息量的那行扔了
        # （诊断全空，而 commit message 还写着"会打印 fatal"）。正确做法是**剥前缀、不丢行**。
        $errLines |
            ForEach-Object {
                if ($_ -match '^\s*git(?:\.exe)?\s*:\s*(.*)$') { $Matches[1].Trim() } else { $_.Trim() }
            } |
            Where-Object { $_ -ne '' -and $_ -notmatch '^(At .*char:|\+ )' } |
            Select-Object -First 10 |
            ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
        Write-Host '  → 这是**脚本自身失败**，不是"没有新增文件"（别把它读成缺必要性段）' -ForegroundColor Gray
        exit 2
    }
    # stdout 字符串化：避免 git 输出被当成对象数组（PS 会把多行拆成多元素，空输出变成 $null）
    return @($out | ForEach-Object { "$_" })
}

$added = @()
if ($Base -ne '') {
    # CI：改动已提交，只能靠与 base 的差分看到"新增"
    $null = Invoke-Git -GitArgs @('rev-parse', '--verify', '--quiet', "$Base^{commit}") -What 'rev-parse'
    $added = @(Invoke-Git -GitArgs @('diff', '--name-only', '--diff-filter=A', "$Base...HEAD") -What 'diff')
} else {
    # 本地：staged 的新增（A）+ 未跟踪（??）。两者都是"本次新写的文件"。
    $added = @(Invoke-Git -GitArgs @('diff', '--name-only', '--diff-filter=A', '--cached') -What 'diff --cached')
    $added += @(Invoke-Git -GitArgs @('ls-files', '--others', '--exclude-standard') -What 'ls-files')
}

$reqs = @($added |
    Where-Object { $_ -and ($_ -match '^docs/prd/REQ-.*\.md$') } |
    ForEach-Object { $_.Trim() } |
    Select-Object -Unique)

if ($reqs.Count -eq 0) {
    Write-Host '  [OK]   本次无新增需求文档（跳过阶段 0 校验）' -ForegroundColor Green
    exit 0
}

$bad = @()
foreach ($p in $reqs) {
    # staged-only 的文件在磁盘上可能不存在（新增后又被删除）；那时从 index 读。
    # 一律按 UTF-8 读：无 BOM 的 UTF-8 在遗留 ANSI 代码页（如 936/1252）下会被 Get-Content
    # 按默认编码误解码 → 中文标题变乱码 → 假红（独立复审 🔵）。
    if (Test-Path -LiteralPath $p) {
        $lines = @(Get-Content -LiteralPath $p -Encoding UTF8)
    } else {
        $lines = @(Invoke-Git -GitArgs @('show', (":" + $p)) -What ('show ' + $p))
    }
    $inFence = $false
    $hit = @()
    foreach ($ln in $lines) {
        # 代码围栏内的行不算"文档里有这一段"（否则贴一段示例就能骗过校验）
        if ($ln -match '^\s*```') { $inFence = -not $inFence; continue }
        if ($inFence) { continue }
        if ($ln -match '^#{1,6}\s*.*必要性') { $hit += $ln }
    }
    if ($hit.Count -eq 0) { $bad += $p }
}

if ($bad.Count -gt 0) {
    foreach ($b in $bad) {
        Write-Host ("  [FAIL] 新增需求文档缺「必要性」段：" + $b) -ForegroundColor Red
    }
    Write-Host '  → 按 .harness/rules/开发流程规范.md「零、阶段 0」补一段：' -ForegroundColor Gray
    Write-Host '     1) 要解决的真实问题（引用观测/日志/症状，不许写"清单上写着"）' -ForegroundColor Gray
    Write-Host '     2) 不做会怎样（后果可见性 × 严重度）  3) 现状是否已变（旧结论还成立吗）' -ForegroundColor Gray
    Write-Host '     4) 有没有更便宜的等效手段；结论落一个：做 / 不做 / 等条件 / 降级做' -ForegroundColor Gray
    exit 1
}

Write-Host ("  [OK]   新增需求文档均含「必要性」段（{0} 个）" -f $reqs.Count) -ForegroundColor Green
exit 0
