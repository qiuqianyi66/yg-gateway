# V3-6 E2E 冒烟：启动测试网关实例（与生产 7863 完全隔离，端口 7864）。
# 配置全走 WB2A_* 环境变量（Load 里 env 覆盖文件），实例状态落在 %TEMP%\yg-e2e。
# 首次运行无 config.json → 自动生成；env 兜底保证 listen/key/auths/state/prompt 全部指向测试侧。
$ErrorActionPreference = 'Stop'
$root = Join-Path $env:TEMP 'yg-e2e'
$exe = Join-Path $root 'yg-gateway-e2e.exe'
if (-not (Test-Path -LiteralPath $exe)) {
  throw "test server exe missing: $exe —— 先跑 scripts/build-server.ps1（或 npm run build:server）"
}
New-Item -ItemType Directory -Force -Path (Join-Path $root 'auths'), (Join-Path $root 'data') | Out-Null
$prompt = Join-Path $root 'prompt.txt'
if (-not (Test-Path -LiteralPath $prompt)) {
  Set-Content -LiteralPath $prompt -Value 'E2E smoke prompt' -Encoding utf8
}
# 上次运行残留的监听进程先清掉（reuseExistingServer=false 场景下由本脚本负责）。
$stale = Get-NetTCPConnection -LocalPort 7864 -State Listen -ErrorAction SilentlyContinue
if ($stale) {
  Stop-Process -Id $stale[0].OwningProcess -Force -ErrorAction SilentlyContinue
  Start-Sleep -Seconds 1
}
Set-Location -LiteralPath $root
$env:WB2A_LISTEN = '127.0.0.1:7864'
$env:WB2A_API_KEY = 'e2e-test-key'
$env:WB2A_AUTH_DIR = (Join-Path $root 'auths')
$env:WB2A_STATE_FILE = (Join-Path $root 'data\state.json')
$env:WB2A_PROMPT_FILE = $prompt
& $exe -config (Join-Path $root 'config.json')
