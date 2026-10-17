# check-req-necessity.ps1 — 阶段 0 的机器校验：新增需求文档必须含「必要性」段
#
# 为什么存在（.harness/rules/开发流程规范.md 「零、阶段 0：必要性分析」）：
#   2026-10-17 的待办清单里 9 项有 4 项经实测不需要做。直接开工的代价是「做了一件不该做的事，
#   再花时间回滚」。人写清单时会把假设写成任务，所以要求每个**新需求**先把「为什么要做」
#   写下来——写不出来就是不该做。
#
# 判据（宽，只卡"有没有这一段"，不卡内容质量）：
#   本次**新增**的 docs/prd/REQ-*.md 里，必须有一行标题匹配 `^#{2,6}\s*.*必要性`
#   （`## 必要性`、`## 一、必要性`、`### 必要性分析` 都行）。
#
# 范围：**只查本次新增**（存量的 REQ 不追溯，否则第一次接入就会红一片，规则会被绕过）。
#
# 用法：
#   powershell -NoProfile -File scripts/check-req-necessity.ps1                 # 本地：index + 未跟踪
#   powershell -NoProfile -File scripts/check-req-necessity.ps1 -Base origin/main # CI：<base>...HEAD
#
# 退出码：0 = 通过（含"本次没有新增 REQ"）；1 = 有新增 REQ 缺必要性段；2 = 脚本自身失败

param(
    [string]$Base = ''
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$added = @()
if ($Base -ne '') {
    # CI：改动已提交，只能靠与 base 的差分看到"新增"
    if (-not (git rev-parse --verify --quiet "$Base" )) {
        Write-Host ("错误：-Base 指定的 ref 解析失败：" + $Base) -ForegroundColor Red
        exit 2
    }
    $added = @(& git diff --name-only --diff-filter=A "$Base...HEAD" 2>$null)
} else {
    # 本地：staged 的新增（A）+ 未跟踪（??）。两者都是"本次新写的文件"。
    $added = @(& git diff --name-only --diff-filter=A --cached 2>$null)
    $added += @(& git ls-files --others --exclude-standard 2>$null)
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
    # staged-only 的文件在磁盘上可能不存在（新增后又被删除）；那时从 index 读
    if (Test-Path -LiteralPath $p) {
        $lines = Get-Content -LiteralPath $p
    } else {
        $lines = @(& git show (":" + $p) 2>$null)
    }
    $hit = @($lines | Where-Object { $_ -match '^#{2,6}\s*.*必要性' })
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
