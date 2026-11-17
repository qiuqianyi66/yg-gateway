# V3-6 E2E 冒烟：启动测试网关实例（与生产 7863 完全隔离，端口 7864）。
# 配置全走 WB2A_* 环境变量（Load 里 env 覆盖文件），实例状态落在临时目录的 yg-e2e 下。
# 首次运行无 config.json → 自动生成；env 兜底保证 listen/key/auths/state/prompt 全部指向测试侧。
#
# 跨平台（2026-11-17 修三处）：
#   ① $env:TEMP 在 Linux 为 null → 用 [IO.Path]::GetTempPath() 兜底；
#   ② 可执行文件名按 OS 选（Linux 无 .exe）；
#   ③ Get-NetTCPConnection 是 Windows 专属 cmdlet，Linux 上不存在 → 只在 Windows 清理残留。
#   另把 'data\state.json' 改成 Join-Path 嵌套：反斜杠在 Linux 是文件名的一部分，不是分隔符。
$ErrorActionPreference = 'Stop'
$tmpRoot = if ($env:TEMP) { $env:TEMP } else { [System.IO.Path]::GetTempPath() }
$root = Join-Path $tmpRoot 'yg-e2e'
$exeName = if ($env:OS -eq 'Windows_NT') { 'yg-gateway-e2e.exe' } else { 'yg-gateway-e2e' }
$exe = Join-Path $root $exeName
if (-not (Test-Path -LiteralPath $exe)) {
  throw "test server exe missing: $exe —— 先跑 scripts/build-server.ps1（或 npm run build:server）"
}
New-Item -ItemType Directory -Force -Path (Join-Path $root 'auths'), (Join-Path $root 'data') | Out-Null
$prompt = Join-Path $root 'prompt.txt'
if (-not (Test-Path -LiteralPath $prompt)) {
  Set-Content -LiteralPath $prompt -Value 'E2E smoke prompt' -Encoding utf8
}
# 上次运行残留的监听进程先清掉（reuseExistingServer=false 场景下由本脚本负责）。
# Linux runner 是全新的，不会有残留；Get-NetTCPConnection 在那边也不存在。
if ($env:OS -eq 'Windows_NT') {
  $stale = Get-NetTCPConnection -LocalPort 7864 -State Listen -ErrorAction SilentlyContinue
  if ($stale) {
    Stop-Process -Id $stale[0].OwningProcess -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
  }
}
Set-Location -LiteralPath $root
$env:WB2A_LISTEN = '127.0.0.1:7864'
$env:WB2A_API_KEY = 'e2e-test-key'
$env:WB2A_AUTH_DIR = (Join-Path $root 'auths')
$env:WB2A_STATE_FILE = (Join-Path (Join-Path $root 'data') 'state.json')
$env:WB2A_PROMPT_FILE = $prompt
& $exe -config (Join-Path $root 'config.json')
