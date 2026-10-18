# mut-run.ps1 — 在 %TEMP% 副本里做变异核验（PLAN B3）
# 纪律（LESSONS L-2，Seen 2 后的硬规则）：
#   1) 只改副本，改前断言 go env GOMOD 指向副本；
#   2) 每个变异都先用「替换是否真的发生」自检（未生效=无效变异，必须报出来）；
#   3) 每个变异跑完立刻还原，并在整轮结束时核对副本的工作区是否干净。
param(
    [string]$Clone = (Join-Path $env:TEMP 'mut-b3'),
    [string]$Pkg = 'internal/auth',
    [string]$MutsFile = ''
)
$ErrorActionPreference = 'Stop'
$env:GOPROXY = 'https://goproxy.cn,direct'
$env:CGO_ENABLED = '0'
$go = 'C:\Program Files\Go\bin\go.exe'

# 相对路径必须在 Push-Location 之前解析：脚本会切到副本目录，之后相对路径就指向副本了
# （2026-10-17 首次入库后自测踩到：-MutsFile scripts/mutation/... 报「找不到」）。
if (-not [System.IO.Path]::IsPathRooted($Clone)) { $Clone = (Resolve-Path -LiteralPath $Clone).Path }
if (-not [System.IO.Path]::IsPathRooted($MutsFile)) { $MutsFile = (Resolve-Path -LiteralPath $MutsFile).Path }

Push-Location $Clone
try {
    $gomod = (& $go env GOMOD).Trim()
    $want = (Join-Path $Clone 'go.mod')
    if ($gomod -ne $want) { throw "L-2 断言失败：go env GOMOD=$gomod，应为 $want —— 拒绝继续（可能测的不是副本）" }
    Write-Host "L-2 断言通过：GOMOD = $gomod"

    # 基线：未变异的测试必须绿，否则后面「红=咬住」的推理不成立。
    # 注意：`go test` 的**编译错误走 stderr**，而本脚本的 $ErrorActionPreference='Stop' 会把
    # 原生命令的 stderr 当成异常抛出（2026-10-17 实测：一条变异把代码改成编译不过时，
    # 脚本直接中断而不是记成「咬住」）。所以调用期临时降级，异常清理仍在外层。
    $prevEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $baseOut = & $go test "./$Pkg/" -count=1 2>&1
        $baseCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $prevEap }
    if ($baseCode -ne 0) { throw "基线未绿，拒绝继续（先修基线）：$($baseOut | Select-Object -Last 5)" }
    Write-Host "基线绿：$Pkg"

    $muts = Get-Content -LiteralPath $MutsFile -Raw | ConvertFrom-Json
    $results = New-Object System.Collections.ArrayList

    foreach ($m in $muts) {
        $target = Join-Path $Clone $m.file
        $pristine = [System.IO.File]::ReadAllText($target)
        # 锚点必须**唯一**：old 命中多处会同时变异，即使测试红了也无法归因到想测的那一处。
        $hits = ([regex]::Matches($pristine, [regex]::Escape($m.old))).Count
        if ($hits -ne 1) {
            Write-Host ("[{0}] ✗ 锚点不唯一（命中 {1} 次）—— 拒绝执行该变异" -f $m.id, $hits) -ForegroundColor Red
            [void]$results.Add([pscustomobject]@{ id = $m.id; pkg = $Pkg; desc = $m.desc; verdict = '无效变异'; detail = "锚点命中 $hits 次" })
            continue
        }
        $mutated = $pristine.Replace($m.old, $m.new)
        if ($mutated -eq $pristine) {
            Write-Host ("[{0}] ✗ 变异未生效（替换 0 次）—— 该变异无效，必须修脚本" -f $m.id) -ForegroundColor Red
            [void]$results.Add([pscustomobject]@{ id = $m.id; pkg = $Pkg; desc = $m.desc; verdict = '无效变异'; detail = '替换 0 次' })
            continue
        }
        [System.IO.File]::WriteAllText($target, $mutated)
        $prevEap2 = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'   # 编译错误走 stderr，不能被当成异常中断（见基线处注释）
        try {
            $out = & $go test "./$Pkg/" -count=1 2>&1
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $prevEap2
            [System.IO.File]::WriteAllText($target, $pristine)
        }
        # 编译失败 **不等于** 测试覆盖：代码根本跑不起来时「红」不构成任何证据。
        # 2026-10-17 实测：变异去掉 uid 的唯一用法 → declared and not used → build failed，
        # 被本脚本误记成「咬住」。编译失败一律判「无效变异」。
        $buildBroken = [bool]($out | Select-String -Pattern '\[build failed\]|declared and not used|undefined:|cannot use ')
        $verdict = if ($buildBroken) { '无效变异' } elseif ($code -ne 0) { '咬住' } else { '存活' }
        # 抓证据：测试失败（--- FAIL）或编译错误（./x.go:NN: ...）都算「咬住」的依据
        $firstFail = ($out | Select-String -Pattern '^--- FAIL|^FAIL\s|^\./.*\.go:\d+:' | Select-Object -First 1).Line
        $color = if ($verdict -eq '咬住') { 'Green' } else { 'Yellow' }
        Write-Host ("[{0}] {1}  {2}  {3}" -f $m.id, $verdict, $m.desc, $(if ($firstFail) { "| $firstFail" } else { '' })) -ForegroundColor $color
        [void]$results.Add([pscustomobject]@{ id = $m.id; pkg = $Pkg; desc = $m.desc; verdict = $verdict; detail = $firstFail })
    }

    # 收尾：副本必须干净（对应 L-2 规则 2 的「再次 git status 断言」）
    $dirty = @(git status --porcelain | Where-Object { $_ })
    if ($dirty.Count -gt 0) {
        Write-Host '✗ 副本里有残留改动：' -ForegroundColor Red
        $dirty | ForEach-Object { Write-Host "    $_" }
    } else {
        Write-Host '✓ 副本工作区干净（全部变异已还原）' -ForegroundColor Green
    }

    Write-Host ''
    Write-Host '==== 汇总'
    $results | ForEach-Object { "{0,-4} {1,-8} {2}" -f $_.id, $_.verdict, $_.desc }
    $killed = @($results | Where-Object { $_.verdict -eq '咬住' }).Count
    $alive = @($results | Where-Object { $_.verdict -eq '存活' }).Count
    $invalid = @($results | Where-Object { $_.verdict -eq '无效变异' }).Count
    Write-Host ("包 {0}：咬住 {1} / 存活 {2} / 无效 {3}" -f $Pkg, $killed, $alive, $invalid)
    if ($alive -gt 0) { Write-Host '⚠ 存活的变异 = 测试没覆盖到的真实分支（不是失败，是缺口）' -ForegroundColor Yellow }
    # 退出码语义（诊断工具，不在门禁里）：0 = 全部咬住；1 = 有存活缺口或无效变异（需要人看）。
    # 显式 exit，避免把最后一次 go test 的退出码泄漏出去（2026-10-18 实测：全咬住时脚本返回 1）。
    if ($alive -gt 0 -or $invalid -gt 0) { exit 1 }
    exit 0
}
finally {
    Pop-Location
}
