# package-release.ps1 — 交付打包（F8）：构建单二进制 → 组装绿色包 → zip。
#
# 用法：
#   powershell -NoProfile -File scripts/package-release.ps1                 # 构建 + zip
#   powershell -NoProfile -File scripts/package-release.ps1 -Version 2.1.0  # 覆盖版本号
#   powershell -NoProfile -File scripts/package-release.ps1 -Installer      # 检测 Inno Setup
#   powershell -NoProfile -File scripts/package-release.ps1 -SkipBuild      # 复用已构建 exe
#
# 产出：dist/yg-gateway-<version>-windows-amd64.zip
#   内含：yg-gateway.exe + config.example.json + README.md + 启动.cmd
#   启动.cmd 首次运行自动把 config.example.json 复制为 config.json 再启动。
#
# 定位纪律（AGENTS.md 第 1 条「单二进制零依赖」）：不打包 Node/Python/Docker，
# 不引入 Electron/WebView 外壳。绿色包 = 解压即用。
param(
    [string]$Version = "",
    [switch]$Installer,
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'

# go 工具链解析（同 deploy-prod 口径：候选逐个实跑 version 验证，不能只 Test-Path）。
function Find-Go {
    $candidates = @(
        (Join-Path $env:ProgramFiles 'Go\bin\go.exe'),
        'C:\Program Files\Go\bin\go.exe'
    )
    $fromPath = Get-Command go -ErrorAction SilentlyContinue
    if ($fromPath) { $candidates += $fromPath.Source }
    foreach ($c in $candidates) {
        if (-not $c -or -not (Test-Path -LiteralPath $c)) { continue }
        try { $v = @(& $c version 2>$null) } catch { continue }
        if ($LASTEXITCODE -eq 0 -and (($v -join ' ') -match '^go version')) { return $c }
    }
    throw 'go 工具链未找到（需 >= 1.27.2）'
}

$root = Split-Path -Parent $PSScriptRoot   # 项目根（scripts/ 的上一级）
$go = Find-Go

# 版本号：默认从 cmd/server/main.go 提取 appVersion（与 /overview 透出版本同源，不手抄）。
if (-not $Version) {
    $main = Join-Path $root 'cmd\server\main.go'
    $m = Get-Content -LiteralPath $main -Raw
    if ($m -match 'const appVersion = "([^"]+)"') {
        $Version = $matches[1]
    } else {
        $Version = '0.0.0'
    }
}

$dist = Join-Path $root 'dist'
$outDir = Join-Path $dist "yg-gateway-$Version-windows-amd64"
$zipPath = "$outDir.zip"

if (Test-Path -LiteralPath $outDir) { Remove-Item -LiteralPath $outDir -Recurse -Force }
New-Item -ItemType Directory -Path $outDir -Force | Out-Null

# 构建（-SkipBuild 跳过：复用上次构建的 exe，须已存在于 outDir）。
$exe = Join-Path $outDir 'yg-gateway.exe'
if (-not $SkipBuild) {
    Write-Host "go build → $exe"
    & $go build -trimpath '-ldflags=-s -w' -o $exe ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'go build 失败' }
} elseif (-not (Test-Path -LiteralPath $exe)) {
    throw "-SkipBuild 但 $exe 不存在"
}

# 组装绿色包内容。
Copy-Item -LiteralPath (Join-Path $root 'config.example.json') -Destination (Join-Path $outDir 'config.example.json')
Copy-Item -LiteralPath (Join-Path $root 'README.md') -Destination (Join-Path $outDir 'README.md')

# 启动.cmd（CRLF，Windows 批处理必须）：首次运行复制 config 模板再启动。
$launcher = "@echo off`r`n" +
    "if not exist config.json copy config.example.json config.json >nul`r`n" +
    "yg-gateway.exe -config config.json`r`n"
[IO.File]::WriteAllText((Join-Path $outDir '启动.cmd'), $launcher, (New-Object Text.UTF8Encoding $false))

# zip 绿色包。
if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
Compress-Archive -Path (Join-Path $outDir '*') -DestinationPath $zipPath

Write-Host "产出绿色包: $zipPath"

# 可选 Installer（P3 后置，本版只做检测 + 提示，不硬失败）。
if ($Installer) {
    $iscc = Get-Command iscc.exe -ErrorAction SilentlyContinue
    if ($iscc) {
        Write-Host '检测到 Inno Setup（iscc.exe），但 .iss 安装脚本模板未内置（P3 后置）；本次仅产出 zip 绿色包。'
    } else {
        Write-Host '未检测到 Inno Setup（iscc.exe）；按约定仅产出 zip 绿色包（退出码 0，非失败）。'
    }
}

exit 0
