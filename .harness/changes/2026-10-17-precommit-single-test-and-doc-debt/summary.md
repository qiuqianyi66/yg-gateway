# 变更记录 — 全量测试只跑一次 + 覆盖率口径实测 + 面板 unknown 观感 + 文档债更正

- 分支/目录：`.harness/changes/2026-10-17-precommit-single-test-and-doc-debt/`
- 需求：`docs/prd/REQ-precommit-single-test-and-doc-debt-2026-10-17.md`
- 日期：2026-10-17
- 分级：**L2**（`check-depth.ps1` 的失败语义属门禁行为变更）
- **并发边界**：A1（CI gate 的 base 口径）由并行会话在 `docs/prd/REQ-ci-gate-base-2026-10-17.md`
  推进，本变更**未碰** `.github/workflows/gate.yml` 与 `.harness/rules/开发流程规范.md`。

## 一、改了什么

| # | 文件 | 改动 | 对应验收 |
|---|---|---|---|
| 1 | `scripts/check-depth.ps1` | 新增 `-CoverageStdout <file>`：给了就复用、不再自跑全量；**自跑时测试失败从「宽容」改为 `exit 2`**；打印「覆盖率来源」 | F-902 |
| 2 | `scripts/pre-commit.ps1` | 检查 6 改跑 `go test ./... -cover`，输出落 `$env:TEMP` 并传给检查 7；测试红时不把该文件喂给棘轮；跑完清理 | F-902 |
| 3 | `internal/panel/app.js` | unknown 徽章/提示行拼接抽成纯函数 `unkBadge(unknowns)` / `unkNoteText(accounts)`（行为零变化），并给 `unkNoteText` 加 `s &&` 守卫 | F-906 |
| 4 | `internal/panel/frontend_test.go` | 新增 `TestAppJSUnknownBadgeAndNote`（真跑 node 调两个函数：空态/计数/question+requires/HTML 转义） | F-906 |
| 5 | `internal/panel/frontend_ux_test.go` | `TestAppJSUnknownsBadge` 的断言锚点跟着换成新函数（该测试是源码文本级断言） | F-906 |
| 6 | `internal/session/session.go` | `deriveKey` 注释补「生产零调用 + 覆盖率口径 + 复现命令」 | F-904 |
| 7 | `.harness/changes/2026-10-16-t5-sandbox/summary.md` | §六.2 追加更正：`dynamicModelsCache` 已不存在（现为 `Handler.modelsCache`） | F-905 |
| 8 | `.harness/changes/2026-10-16-depth-tasks-4-6/summary.md` | §四.1 追加更正（`-count=3` 已不复现）；§四.4 追加更正（「约 89%」改为实测数字） | F-904/F-905 |
| 9 | `HANDOFF.md` | §五 的 7864 实例状态：**没在跑**，那两条配置是历史记录 | F-905 |
| 10 | `README.md` | 「20 个 Go 包测试全绿」→「23 个 Go 包（20 个有测试，3 个 CLI 无）」 | F-905 |

## 二、怎么验证的（全部本机实测，命令可复现）

### 2.1 F-902：门禁耗时 A/B（同一工作区、同一代码，只换脚本）

```
改动前（git checkout -- scripts/ 后跑全量）: exit=0  128.4s
改动后（新脚本跑全量）                      : exit=0   62.0s
→ 省 66.4s（−52%），省掉的就是「同一个门禁里第二遍全量测试」
```

复用分支单独取证：

```
$ go test ./... -cover -count=1            → 63s（这份输出就是改动前要跑的第二遍）
$ check-depth.ps1 -CoverageStdout <file>   → exit=0  1.5s
  最低包覆盖率      : 72.3%
  覆盖率来源        : 复用 …\yg-depth-reuse-probe.txt（未重复跑测试）
```

### 2.2 F-902 的牙齿：测试红必须红（不是"覆盖数字正常就当过"）

临时塞入 `internal/logfmt/zzz_teeth_red_test.go`（`t.Fatal("teeth")`）后跑全量：

```
exit=1
  [FAIL] go test ./... 有失败
  错误：go test -cover 失败（测试红 → 棘轮数字不可信）：
        --- FAIL: TestTeethRed (0.00s)
  [FAIL] check-depth.ps1 失败（深度指标回退 / 覆盖率不可用）
```

关键：**check-depth 这次没有给出任何棘轮结论**（旧实现会按 coverage 行算完并报 PASS）。
探针文件已删除（`Test-Path` 复核为 `False`）。

### 2.3 F-906：三次反向变异全部按预期变红

| 变异 | 结果 |
|---|---|
| M1 去掉 `unkBadge` 里的 `esc()` | **红**（`--- FAIL: TestAppJSUnknownBadgeAndNote`） |
| M2 `if (!unk.length) return '';` → `if (false)` | **红** |
| M3 去掉 `unkNoteText` 的 `s &&` 守卫 | **红**（`Cannot read properties of undefined`） |

还原后复跑 `ok`；`app.js` 与变异前副本 **SHA256 一致**（见 §五 第二个坑）。

### 2.4 F-904：覆盖率口径实测

```
$ go test "-coverprofile=<%TEMP%\sess.out" -count=1 ./internal/session/
  ok  yg-gateway/internal/session  0.223s  coverage: 98.8% of statements
按语句块统计：包 425/430 = 98.8%；剔除测试专用的 deriveKey（20 语句）后 405/410 = 98.8%
→ 「生产可达约 89%」复现不出来，已更正为上述口径
```

生产零调用的证据（穷尽全部 `.go` 引用，见 AGENTS.md L-15 否定性断言纪律）：

```
$ git grep -n 'deriveKey' -- '*.go' | grep -v '_test.go'
internal/session/session.go:323  (注释)
internal/session/session.go:418  return deriveKeyView(v)   ← 另一个函数
internal/session/session.go:439  (注释)
internal/session/session.go:447  func deriveKey(...)       ← 定义
internal/session/session.go:483/485/493  deriveKeyView 相关
internal/session/session.go:599  (注释)
→ deriveKey(map) 本体在非测试代码里**只有定义**，调用点全是 _test.go
```

### 2.5 F-905：文档债四处更正（各带复现命令）

| 更正 | 复现命令 | 实测 |
|---|---|---|
| `dynamicModelsCache` 已不存在 | `git grep -n 'dynamicModelsCache' -- '*.go'` | **零命中**（全仓 .go 穷尽）；现为 `Handler.modelsCache`（`internal/server/handler.go`） |
| `TestModelsEndpoint` 污染不复现 | `go test ./internal/server/ -count=3` | `ok 24.455s`（exit 0） |
| 7864 eval 实例没在跑 | `Get-Process yg-gateway` / `curl 127.0.0.1:7864/healthz` | 只有 7863 的 PID 40912；7864 连接被拒 |
| Go 包数 23 | `go list ./... \| Measure-Object -Line` | 23（20 含 `*_test.go`，3 个 cmd 无） |

### 2.6 五项门禁

```
gofmt -l ./cmd ./internal        → 无输出
go vet ./...                     → 无输出
go test ./...                    → 20 包 ok（另 3 个 cmd 无测试文件）
check-boundaries.ps1             → 通过
check-depth.ps1                  → 棘轮未回退（minCoverage 72.3%，maxFileLines 2058）
pre-commit.ps1（全量总闸）        → exit 0，62s
```

## 三、决策（含未被采纳的方案）

| # | 决策 | 选项 | 选择与理由 |
|---|---|---|---|
| D-1 | check-depth 的失败语义 | ①继续宽容 ②新增复用参数 + 自跑时失败即红 | **②**。复用模式下由调用方担保；自跑（CI gate）时宽容等于用覆盖率掩盖测试失败 |
| D-2 | CI 里 `test` 与 `gate` 的重复全量测试 | ①保留并行重复 ②gate 消费 test 的覆盖率 artifact（需 `needs: test`）③测试从 test job 摘掉、gate 独占 | **①**。②串行化后 wall clock ~2.2min → ~2.8min（只省 runner 分钟数）；③让 PR 的测试信号只剩 gate，且 gate 是 build 前置，失败定位变差。**本地没有并行收益，所以本地去重（F-902，实测省 66.4s/次）**。重开条件：CI 分钟数成为实际约束，或单次流水线 > 20min |
| D-3 | legacy `deriveKey` | ①保留并标注 ②删除 | **①**。它是 `deriveKeyView` 的逐位等价性对照（`coverage_test.go` 1191 行起），删掉失去热路径重构的安全网 |

## 四、回滚

| 范围 | 回滚 |
|---|---|
| 全部 | `git revert <本提交>`（一个提交，纯增量；无数据/协议变更） |
| 只要门禁去重 | `git checkout <本提交>^ -- scripts/pre-commit.ps1 scripts/check-depth.ps1` → 回到「跑两遍全量 + 宽容失败」 |
| 只要面板 | `git checkout <本提交>^ -- internal/panel/app.js internal/panel/frontend_test.go internal/panel/frontend_ux_test.go` |
| 文档 | 各 `.md` 的更正段可直接删（都是追加，未改原文结论） |

> 注意：本变更**未碰生产二进制**，`D:\yg-gateway` 的 7863 实例无需重启；`app.js` 属 `go:embed`，
> 下次构建才会进二进制（面板行为零变化，只是函数位置变了）。

## 五、踩到的两个坑（留证）

1. **`-coverprofile` 的参数拆分（L-3 复活）**：PowerShell 5.1 下 `go test -coverprofile=$tmp ...`
   未加双引号会被拆开，仓库根出现字面名垃圾文件 `$tmp` / `sess`（各 17KB 覆盖率文本）。
   已删除，并且**本轮实测这条已被 `pre-commit.ps1` 检查 4 拦住**（修复前那次全量运行里
   它报的是 gofmt，说明垃圾文件已在我清理后）。正确写法：`go test "-coverprofile=$env:TEMP\x.out"`。
2. **变异还原不能用文本重写**：PowerShell 5.1 的 `Set-Content -Encoding UTF8` **会加 BOM**，
   用它还原 `app.js` 会在文件头塞进 `EF BB BF`。本轮先用 `Copy-Item` 从备份还原，
   再用 SHA256 逐字节确认（`True`）；`app.js` 前 3 字节实测 `39,117,115`（`9us`，无 BOM）。

## 六、遗留（如实记录）

1. **CI 侧重复未消除**（D-2 的取舍，不是漏做）：`test` job 与 `gate` job 各跑一遍全量，
   每次 push 多约 2 runner-min。重开条件已写明。
2. **EWMA 时间敏感 flake** 本轮未复现，保留观察（第 2 次复现即按 LESSONS Seen 制升级）。
3. **`frontend_ux_test.go` 的 `TestAppJSUnknownsBadge` 仍是源码文本级断言**：
   行为级覆盖在 `TestAppJSUnknownBadgeAndNote`，两者互补；文本级那条脆但便宜，未删。
4. **A1 归并行会话**，本变更不含 `gate.yml` 改动；若两边同时合入，`开发流程规范.md` §五
   与 `gate.yml` 需人工确认无冲突（两份改动互不重叠：它改 base 解析，我改 pre-commit/check-depth）。
