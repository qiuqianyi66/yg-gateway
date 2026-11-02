# yg-gateway-watch.ps1 — 幂等守护检查：端口 7863 健康就退出；不健康则拉起服务。
# 由计划任务 yg-gateway-watch 每 5 分钟 + 登录时触发，不长期驻留。
$ErrorActionPreference = 'Stop'
$port    = 7863
$exe     = 'D:\yg-gateway\yg-gateway.exe'
$wd      = 'D:\yg-gateway'
$err     = 'D:\yg-gateway\data\stderr.log'
$out     = 'D:\yg-gateway\data\stdout.log'
$pidFile = 'D:\yg-gateway\data\yg-gateway.pid'

# 1) 端口探测（1s 超时；进程在但端口不通同样视为不健康）
$alive = $false
try {
    $c = New-Object System.Net.Sockets.TcpClient
    $iar = $c.BeginConnect('127.0.0.1', $port, $null, $null)
    if ($iar.AsyncWaitHandle.WaitOne(1000)) { $c.EndConnect($iar); $alive = $true }
    $c.Close()
} catch { $alive = $false }

if ($alive) { exit 0 }   # 服务健康，不动

# 2) 端口不通 → 确认 exe 存在后拉起
if (-not (Test-Path -LiteralPath $exe)) { exit 2 }
try {
    $p = Start-Process -FilePath $exe -WorkingDirectory $wd `
        -RedirectStandardError $err -RedirectStandardOutput $out `
        -WindowStyle Hidden -PassThru
} catch {
    # 日志文件可能被残留句柄占用，换应急文件名重试一次
    Start-Sleep -Seconds 2
    $p = Start-Process -FilePath $exe -WorkingDirectory $wd `
        -RedirectStandardError "$err.watch" -RedirectStandardOutput "$out.watch" `
        -WindowStyle Hidden -PassThru
}
$p.Id | Set-Content -LiteralPath $pidFile -Encoding ascii
exit 0
