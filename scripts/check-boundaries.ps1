# check-boundaries.ps1 — 模块依赖方向守卫
#
# 为什么需要：本仓库当前的依赖方向是**正确的**（server→pool 单向，无环），
# 但没有任何机器校验。一次随手 import 就能引入循环依赖，而这种错误在 Go 里
# 是编译期硬失败，往往等到重构很大一片时才暴露。
#
# 设计取舍：白名单式（只允许列出的方向），而不是黑名单式（只禁几条）。
# 理由：新包被随手引入时，白名单会**默认拒绝**，迫使人显式声明它属于哪一层；
# 黑名单则会默认放行，直到有人记得来更新。
#
# 用法：
#   pwsh -File scripts/check-boundaries.ps1        # 检查
#   pwsh -File scripts/check-boundaries.ps1 -Fix   # 打印当前实际依赖（用于更新白名单）
#
# 退出码：0 = 通过；1 = 存在违规依赖；2 = 脚本自身执行失败

param(
    [switch]$Fix
)

$ErrorActionPreference = 'Stop'

# 定位仓库根（脚本在 scripts/ 下，向上一级）
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$module = 'yg-gateway'

# ---------------------------------------------------------------------------
# 允许矩阵：key = 源包（internal/ 下的一级目录 或 cmd/server）
#           value = 该包允许 import 的 internal 目标包列表
# 说明："*" 表示允许依赖所有 internal 包（顶层装配层才该有此权限）
# ---------------------------------------------------------------------------
#
# ⚠️ 本表由 `pwsh -File scripts/check-boundaries.ps1 -Fix` 实测生成并手工确认，
#    不是拍脑袋写的。新增方向时必须说明「为什么这一层需要它」。
$allow = @{
    # 顶层装配层：cmd 可以依赖任何 internal
    'cmd'      = @('*')
    # HTTP 适配层：装配并编排，依赖各领域包（实测）
    'server'   = @('alerting','auth','httpauth','livecfg','logfmt','metrics',
                   'pool','prompt','reqlog','session','upstream','usage')
    # 领域层：账号池依赖凭证、日志脱敏，以及 domain 层（四态标注 / 未知跟踪）
    'pool'     = @('auth','evidence','logfmt','unknown')
    # 出站适配层：依赖凭证、日志脱敏、会话键解析（实测）
    'upstream' = @('auth','logfmt','session')
    # 会话键派生：依赖 redisstore（粘性绑定持久化）（实测）
    'session'  = @('redisstore')
    # 面板：只读运维视图，可依赖领域包但不可反向被依赖（实测）
    'panel'    = @('auth','httpauth','livecfg','logfmt','pool','reqlog',
                   'scheduler','session','upstream','usage')
    # 调度：依赖凭证、日志、池、上游（实测）
    'scheduler'= @('auth','logfmt','pool','upstream')
    # 热配置：依赖 httpauth（密钥约束校验）（实测）
    'livecfg'  = @('httpauth')
    # 凭证层：依赖日志脱敏（实测）
    'auth'     = @('logfmt')
    # 纯叶子：无 internal 依赖
    'usage'      = @()
    'reqlog'     = @()
    'metrics'    = @()
    'logfmt'     = @()
    # 告警外发：webhook body 需按 R2 口径截断完整 UID → 依赖 logfmt（纯脱敏 helper 叶子包）。
    # 2026-10-22 体检 C-2 引入，-Fix 实测生成；无反向依赖风险。
    'alerting'   = @('logfmt')
    'httpauth'   = @()
    'prompt'     = @()
    'redisstore' = @()
    # domain 层（事实四态标记 / unknown 消解）：零 internal 依赖，可被任何层引用
    'evidence'   = @()
    'unknown'    = @()
}

# 取每个包的 import 列表。
#
# 模板走 range 而不是 join：Go 模板的 join 需要字符串字面量参数
# （{{join .Imports ","}}），而 PowerShell 5.1 把参数传给原生命令时会吃掉
# 引号，导致 go 报 "unexpected ',' in operand"。range 版本不需要字面量
# 参数，可安全穿过 PowerShell 的引号处理。
#
# 模板落在 .git/ 下：不入库、不污染工作区；缺失时现场生成。
$deps = @{}
# 分段 Join-Path（不写 '.git\golist.tmpl'）：Linux 上反斜杠不是分隔符，会盖出一个
# 名叫 `.git\golist.tmpl` 的普通文件，污染工作区且 CI 里表现为莫名 untracked。
$tmplFile = Join-Path (Join-Path $repoRoot '.git') 'golist.tmpl'
if (-not (Test-Path $tmplFile)) {
    New-Item -ItemType Directory -Force -Path (Split-Path $tmplFile) | Out-Null
    [System.IO.File]::WriteAllText($tmplFile, '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' + "`n")
}
$tmpl = (Get-Content $tmplFile -Raw).Trim()

$list = & go list -f $tmpl ./... 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Host "go list 执行失败：" -ForegroundColor Red
    Write-Host ($list -join "`n")
    exit 2
}

foreach ($line in $list) {
    if (-not $line -or -not $line.Trim()) { continue }
    $fields = $line.Trim() -split '\s+' | Where-Object { $_ }
    if ($fields.Count -lt 1) { continue }
    $deps[$fields[0]] = @($fields | Select-Object -Skip 1)
}

# 归一：yg-gateway/internal/pool -> pool ；yg-gateway/cmd/server -> cmd/server
function Get-Layer($importPath) {
    if ($importPath -eq $module) { return '.' }
    if ($importPath -like "$module/internal/*") {
        $rest = $importPath.Substring("$module/internal/".Length)
        return ($rest -split '/')[0]
    }
    if ($importPath -like "$module/cmd/*") {
        return 'cmd'
    }
    if ($importPath -like "$module/*") {
        return ($importPath.Substring($module.Length + 1) -split '/')[0]
    }
    return $null # 外部包
}

if ($Fix) {
    Write-Host "=== 当前实际 internal 依赖（用于更新白名单）===" -ForegroundColor Cyan
    foreach ($pkg in ($deps.Keys | Sort-Object)) {
        $src = Get-Layer $pkg
        if (-not $src) { continue }
        $targets = @()
        foreach ($imp in $deps[$pkg]) {
            $t = Get-Layer $imp
            if ($t) { $targets += $t }
        }
        $targets = $targets | Sort-Object -Unique
        if ($targets.Count -gt 0) {
            Write-Host ("{0,-12} -> {1}" -f $src, ($targets -join ', '))
        }
    }
    exit 0
}

# 检查
$violations = @()
foreach ($pkg in ($deps.Keys | Sort-Object)) {
    $src = Get-Layer $pkg
    if (-not $src) { continue }

    # 白名单未收录的层：默认拒绝（新包必须显式声明归属）
    if (-not $allow.ContainsKey($src)) {
        $violations += [pscustomobject]@{
            From = $src
            To   = '(层未收录于白名单，请显式声明)'
            Pkg  = $pkg
        }
        continue
    }

    $allowed = $allow[$src]
    foreach ($imp in $deps[$pkg]) {
        $t = Get-Layer $imp
        if (-not $t) { continue }          # 外部包，不管
        if ($t -eq '.') { continue }       # 主模块根，不管
        if ($allowed -contains '*') { continue }
        if ($allowed -notcontains $t) {
            $violations += [pscustomobject]@{
                From = $src
                To   = $t
                Pkg  = $pkg
            }
        }
    }
}

if ($violations.Count -gt 0) {
    Write-Host "❌ 依赖方向违规（$($violations.Count) 处）：" -ForegroundColor Red
    foreach ($v in ($violations | Sort-Object From, To -Unique)) {
        Write-Host ("   违规: {0} -> {1}   (包: {2})" -f $v.From, $v.To, $v.Pkg) -ForegroundColor Yellow
    }
    Write-Host ""
    Write-Host "依赖必须向内流动：domain/contract <- provider <- application <- adapter" -ForegroundColor Gray
    Write-Host "如需新增合法方向，请更新 scripts/check-boundaries.ps1 的白名单并说明理由。" -ForegroundColor Gray
    exit 1
}

Write-Host "✅ 边界检查通过（依赖方向无误）" -ForegroundColor Green
exit 0
