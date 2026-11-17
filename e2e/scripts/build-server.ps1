# V3-6 E2E 冒烟：构建测试实例二进制（输出到临时目录下的 yg-e2e，不落仓库）。
#
# 跨平台（2026-11-17 修）：CI 的 e2e job 跑在 ubuntu-latest，那里 $env:TEMP 为 null
# （Linux 用 TMPDIR），二进制也没有 .exe 后缀。原实现 Join-Path $env:TEMP 直接报
# "Cannot bind argument to parameter 'Path' because it is null"，e2e 在 CI 里**从未**
# 真正跑起来（每次都是这一步就 exit 1）。改用 [IO.Path]::GetTempPath() + 按 OS 选后缀。
$ErrorActionPreference = 'Stop'
$tmpRoot = if ($env:TEMP) { $env:TEMP } else { [System.IO.Path]::GetTempPath() }
$root = Join-Path $tmpRoot 'yg-e2e'
New-Item -ItemType Directory -Force -Path $root | Out-Null
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Set-Location -LiteralPath $repo
$exeName = if ($env:OS -eq 'Windows_NT') { 'yg-gateway-e2e.exe' } else { 'yg-gateway-e2e' }
$exe = Join-Path $root $exeName
go build -trimpath -ldflags="-s -w" -o $exe ./cmd/server
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Write-Output "built: $exe"
