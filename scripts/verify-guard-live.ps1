# verify-guard-live.ps1 —— 一条命令验证「估算 vs 上游」的线上状态。
#
# 用法：
#   .\scripts\verify-guard-live.ps1                     # 用最近一次部署时间当 since
#   .\scripts\verify-guard-live.ps1 -Since 2026-11-27T21:34:50
#
# 做什么：
#   1) /status 看护栏块（samples / est_vs_upstream 的 median 与 p90）；
#   2) check-est-drift.py --since <部署时刻> 给出裁决（0 OK / 1 漂移 / 2 样本不足）；
#   3) compare-est-models.py 对照主口径与影子口径，按图片张数给你看谁更准。
#
# 为什么需要：融合口径（max 主/影子）上线后，样本从 0 重新累积；光看 median 不够，
# 要同时看到 p90 和分桶，才能判断「是真的准了」还是「大样本把偏差平均掉了」。

param(
  [string]$Since = "",
  [string]$ApiKey = "sk-local-yg-358531498e824dcb3620c343cd84cc90",
  [string]$BaseUrl = "http://127.0.0.1:7863"
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot

if (-not $Since) {
  # 默认取最近一次 deploy-prod 报告的 stamp（文件名形如 deploy-prod-20261127-213439.json）
  $report = Get-ChildItem (Join-Path "D:\yg-gateway\data\logs" "deploy-prod-*.json") |
    Sort-Object LastWriteTime | Select-Object -Last 1
  if ($report) {
    $m = [regex]::Match($report.BaseName, "deploy-prod-(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})")
    if ($m.Success) {
      $Since = "{0}-{1}-{2}T{3}:{4}:{5}" -f $m.Groups[1], $m.Groups[2], $m.Groups[3], $m.Groups[4], $m.Groups[5], $m.Groups[6]
    }
  }
}

Write-Host "=== 1/3 /status ==="
try {
  $s = Invoke-RestMethod -Uri "$BaseUrl/status" -Headers @{ Authorization = "Bearer $ApiKey" } -TimeoutSec 10
  $g = $s.guardrail_stats
  Write-Host ("samples={0}  warn_hits={1}  warn_tokens={2}  max_tokens={3}" -f $g.samples, $g.warn_hits, $g.warn_tokens, $g.max_tokens)
  if ($g.est_vs_upstream) {
    Write-Host ("est_vs_upstream: n={0} median={1} p90={2} min={3} max={4}" -f `
      $g.est_vs_upstream.n, $g.est_vs_upstream.median, $g.est_vs_upstream.p90, `
      $g.est_vs_upstream.min, $g.est_vs_upstream.max)
  } else {
    Write-Host "est_vs_upstream: 无样本（换版后还没收到带 usage 的请求）"
  }
} catch {
  Write-Warning "status 读取失败：$($_.Exception.Message)"
}

Write-Host ""
Write-Host ("=== 2/3 漂移裁决（since={0}）===" -f ($Since ? $Since : "<未指定，用全量>"))
python (Join-Path $repo "scripts\check-est-drift.py") --since $Since
$driftExit = $LASTEXITCODE
Write-Host ("drift exit={0}（0 OK / 1 漂移 / 2 样本不足 / 3 无归档）" -f $driftExit)

Write-Host ""
Write-Host "=== 3/3 主口径 vs 影子口径（按图片张数）==="
python (Join-Path $repo "scripts\compare-est-models.py") --since $Since

exit $driftExit
