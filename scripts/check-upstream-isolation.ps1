# check-upstream-isolation.ps1 — R6 红线机器检查：出站请求集中在 internal/upstream
#
# 为什么需要：AGENTS.md R6 写了「出站请求集中 internal/upstream，禁其他包直连上游」，
# 但靠 code review 人盯，没有机器检查。一次随手在 panel/scheduler/server 里写个
# http.Get 到 workbuddy.ai，就绕过了 upstream 层的重试/熔断/错误分类。
#
# 检查逻辑（白名单式）：
#   扫描 internal/ 下除 upstream/ 外的所有包，找直接构造出站 HTTP 请求的代码。
#   信号：文件里出现 http.NewRequest / http.Get / http.Post / http.DefaultClient.Do
#   这类出站请求调用。然后查白名单：在白名单里的放行，不在的报违规。
#
#   注意：域名字符串判断（如 auth.go 的 isGlobalDomain）、注释里提到上游域名，
#   都不算违规——只有真的发起出站请求才算。
#
# 白名单（合法例外，每个都有理由）：
#   - internal/panel/login.go：面板内嵌 OAuth 设备授权流程，需要直接调用上游 auth 端点
#     （auth/state、auth/token、login/account）。这些是认证端点不是 chat 流量，
#     upstream.Client 是为 chat 流量设计的，不适合 OAuth 流程。
#
# 用法：
#   pwsh -File scripts/check-upstream-isolation.ps1        # 检查
#
# 退出码：0 = 通过；1 = 存在违规；2 = 脚本自身执行失败

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# 出站请求调用模式（这些是真正发起 HTTP 请求的信号）
$outboundPatterns = @(
    'http\.NewRequest',
    'http\.Get\(',
    'http\.Post\(',
    'http\.DefaultClient',
    '\.Do\(req'
)

# 白名单：允许直连上游的文件（相对仓库根）
$whitelist = @(
    'internal/panel/login.go'
)

# 收集 internal/ 下除 upstream/ 外的所有 .go 文件（排除测试文件）
$goFiles = @()
$internalDir = Join-Path $repoRoot 'internal'
if (-not (Test-Path $internalDir)) {
    Write-Host '错误：internal/ 目录不存在' -ForegroundColor Red
    exit 2
}

Get-ChildItem -Path $internalDir -Recurse -Filter '*.go' | Where-Object {
    # 排除 upstream/ 目录本身
    $_.FullName -notlike '*\upstream\*' -and
    # 排除测试文件
    $_.Name -notlike '*_test.go'
} | ForEach-Object {
    $goFiles += $_.FullName
}

if ($goFiles.Count -eq 0) {
    Write-Host '  [OK]   internal/ 下除 upstream/ 外无生产 .go 文件（无需检查）' -ForegroundColor Green
    exit 0
}

# 检查每个文件是否包含出站请求调用
$violations = @()
foreach ($file in $goFiles) {
    $relPath = $file.Substring($repoRoot.Length + 1) -replace '\\', '/'

    # 白名单放行
    if ($whitelist -contains $relPath) {
        continue
    }

    # 读文件内容
    $content = Get-Content -LiteralPath $file -Raw -ErrorAction SilentlyContinue
    if (-not $content) { continue }

    # 检查是否包含出站请求调用
    foreach ($pattern in $outboundPatterns) {
        if ($content -match $pattern) {
            $violations += [pscustomobject]@{
                File = $relPath
                Pattern = $pattern
            }
            break  # 每个文件只报一次
        }
    }
}

if ($violations.Count -gt 0) {
    Write-Host "❌ R6 违规：出站请求未集中在 internal/upstream（$($violations.Count) 处）：" -ForegroundColor Red
    foreach ($v in ($violations | Sort-Object File -Unique)) {
        Write-Host ("   违规: {0} 包含出站请求调用 {1}" -f $v.File, $v.Pattern) -ForegroundColor Yellow
    }
    Write-Host ""
    Write-Host "所有出站上游请求必须经过 internal/upstream.Client，不要在其他包里直连上游。" -ForegroundColor Gray
    Write-Host "如需新增合法例外（如 OAuth 流程），请更新 scripts/check-upstream-isolation.ps1 的白名单并说明理由。" -ForegroundColor Gray
    exit 1
}

Write-Host "✅ R6 检查通过：出站请求集中在 internal/upstream" -ForegroundColor Green
exit 0
