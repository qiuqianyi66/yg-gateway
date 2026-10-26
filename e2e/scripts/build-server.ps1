# V3-6 E2E 冒烟：构建测试实例二进制（输出到 %TEMP%\yg-e2e，不落仓库）。
$ErrorActionPreference = 'Stop'
$root = Join-Path $env:TEMP 'yg-e2e'
New-Item -ItemType Directory -Force -Path $root | Out-Null
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Set-Location -LiteralPath $repo
$exe = Join-Path $root 'yg-gateway-e2e.exe'
go build -trimpath -ldflags="-s -w" -o $exe ./cmd/server
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Write-Output "built: $exe"
