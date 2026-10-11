# 收紧 auths/ 凭证文件权限：仅当前用户 + SYSTEM 可访问。
# 背景：Windows 上 Go 的 0o600 不会改写继承 ACL，面板 OAuth 新建的凭证
# 默认对 BUILTIN\Users（本机所有登录用户）可读。凭证是明文 accessToken /
# refreshToken，拿到即可冒用账号。每次加完账号后跑一次本脚本。
#
# 用 icacls 而非 Set-Acl：后者在对象带 SACL 时需要 SeSecurityPrivilege
# （非提升进程没有），icacls 的同语义操作不需要该特权。
$ErrorActionPreference = 'Stop'
$dir = Join-Path $PSScriptRoot 'auths'
if (-not (Test-Path $dir)) { Write-Host "no auths dir: $dir"; exit 0 }

$me = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
$files = @(Get-ChildItem -Path $dir -Filter '*.json' -File)
if ($files.Count -eq 0) { Write-Host 'no credential files found'; exit 0 }

foreach ($f in $files) {
    # /inheritance:r 断开继承并移除继承来的 ACE；/grant:r 替换该主体的现有授权
    & icacls $f.FullName /inheritance:r /grant:r "${me}:(F)" /grant:r "NT AUTHORITY\SYSTEM:(F)" | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Host "FAILED: $($f.Name)"; continue }
    Write-Host "locked: $($f.Name)"
}
Write-Host "$($files.Count) file(s) locked to $me + SYSTEM"
