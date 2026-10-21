# 2026-10-21 prd-landed-status-backfill 变更摘要

需求：`docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md`（阶段 1 PRD 的落地状态回填）
分级：L0（纯文档）
ADR：无（F-306/F-307 的 ADR 职责已由 ADR-003 D-400/D-401 覆盖，禁止新建 ADR-013/014）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md` | 在 F-101/F-103/F-201/F-204/F-301~F-304/F-306/F-307 需求行、§四 F-301 取证节与「三处先动清单」、§五 P0 表 #2/#4/#10 与 P1 表 #20 的「方案」单元格内，就近补「✅ 已落地（commit + 关键文件/测试）」注记；C 段表后新增一小段「落地状态汇总（2026-10-21 回填）」映射表。原需求文字、Given-When-Then、取证表格、行号快照一律未动。 |
| `新增：.harness/changes/2026-10-21-prd-landed-status-backfill/summary.md` | 本文件。 |

**没做的（明写，免得下轮重提）**：
- ~~F-305 未动~~ → 二次回填已完成：commit `5b3c245` 落地后补注记（本 summary 与 PRD 均已更新）。
- 未碰 `docs/adr/`、`docs/agent-eval/`、`docs/code-review/`、任何代码文件、`.workbuddy/`。
- 未更新 PRD front matter 的 `last_updated`（任务要求严格保持原格式，仅补状态）。

## 二、怎么验证的

每条「已落地」均经 git 独立实证（`git show <sha> --stat` / `git log -S` / `git log -G` / 当前源码 grep），命令与结果：

| 注记 | 实证命令 | 结果 |
|---|---|---|
| F-101 → `01d76c4` | `git show --stat 01d76c4` | 新增 `internal/auth/lockdown.go`(60行) + `auth.go`(18行) + `lockdown_test.go`(158行) + ADR-012 ✅ |
| F-103 → `962fdc8` | `git log --reverse -S govulncheck -- .github/workflows/`；`git show 962fdc8:.github/workflows/go-binaries.yml` | test job 自初始发布即含 govulncheck 步骤，`on: pull_request/push main`，全仓无 `continue-on-error`（硬门禁）；现随 `9c68947` 合并在 release.yml test job ✅ |
| F-201 → `5ecfe71` | `git log -S accRenderSig -- internal/panel/app.js` | 签名短路引入提交即 `5ecfe71` ✅ |
| F-204 → `acc7c0b` | `git show --stat acc7c0b`；当前 `app.js` grep | `loadErrorHTML`(app.js:243)、用量页 3674、积分构成页 4202、`bindRetry`；frontend_ux_test.go +78 行 ✅ |
| F-301 → `73911e4`+`981b0a1` | `git show --stat 981b0a1` | `internal/session/requestview.go`(167行,新) + `requestview_test.go`(180行,新)；当前 handler.go:1485 `hasImageView(v *session.RequestView)` ✅ |
| F-302 → `73911e4` | `git log -G 'Pool\.Status\(uid\)' -- internal/server/handler.go`；`git show 73911e4 -- handler.go` | diff 实证：`-if a, ok := h.cfg.Pool.Status(uid); ok && a.Realm != ""` → `+realm := h.cfg.Pool.RealmOf(uid)`；当前 handler.go 已无 `Pool.Status`；`RealmOf` 在 pool/state.go:340 ✅（候选 `9489e98`/`d21d5c2` 经 diff 排除，非此项落地提交） |
| F-303 → `73911e4` | `git log -S recentHead -- internal/reqlog/reqlog.go` | 引入提交即 `73911e4`；当前 reqlog.go:146-147 `recent [recentLimit]Event` + `recentHead` ✅ |
| F-304 → `73911e4`+`600fc48` | `git show 73911e4 -- handler.go`；`git show --name-only 600fc48`；`merge-base --is-ancestor 73911e4 600fc48`(exit 0) | **与线索有出入，以实证为准**：`h.mux.HandleFunc("/", h.notFound)` 兜底与 `notFound` 函数是在 `73911e4` 进的 handler.go；`600fc48` 只新增牙齿测试 `notfound_test.go`(105行)。PRD 两行都写了。 |
| F-306/F-307 → `9489e98` | `git show --stat 9489e98`；grep ADR-003 | `breaker_halfopen_test.go`(159)、`admission_test.go`(78)、`globaladmission_test.go`(156) 均在此提交；ADR-003 D-400(half-open)/D-401(准入429) 含 Given-When-Then 与回滚表 ✅ |
| P0 #2 → `acc7c0b` | `git log -S '读取账号池失败' -- app.js`；读 app.js:866-876 | 首屏未拿到数据时表内插持久化错误条 + `bindRetry`；注释自述「首屏失败此前完全静默」 ✅（注：无字面 `booted` 变量，落地形态与原方案措辞略异，已按实证写） |
| P0 #4 → `5ecfe71` | `git log -S VEIL_CLOSERS -- app.js`；当前 app.js:2157-2190 | ✅ |
| P0 #10 → `5ecfe71` | `git log -S skipGuard -- app.js`；当前 app.js:568 `if (!skipGuard) window.scrollTo({top: 0})` | ✅ |
| P1 #20 → `7c1601f` | `git log -S visibilitychange -- app.js`；当前 app.js:2522 | ✅ |
| F-305 → `5b3c245`（二次回填） | `git show --stat 5b3c245`；`git grep` HEAD 核实指标名与配置项 | `internal/metrics/attempts.go`(197新)+`attempts_test.go`+`retry_metrics.go`(37新)+`retry_metrics_test.go`(155新)+prom.go/handler.go/metrics_handler.go/main.go/config.go；三个指标名在 retry_metrics_test.go 暴露断言中，`slow_request_threshold_seconds` 在 cmd/server/config.go:139 ✅ |

**负向探针**：无代码改动，门禁不适用；仅做 `git diff --check`（无空白错误）与逐行复读 diff。
**未跑** `scripts/pre-commit.ps1`（纯文档 L0，且另一 shard 正在跑 go test；文档变更不触及其门禁项）。

## 三、未完成 / 待补（诚实清单）

- [x] **F-305**（重试放大率、慢请求追踪）：✅ 已消解——另一 shard 落地于 commit `5b3c245`（2026-10-21，见 `.harness/changes/2026-10-21-f305-retry-slow-metrics/`），PRD F-305 行与汇总段已补注记。
- [ ] **§四「最该先动的三处」之③**（`handler.go:764` `hasImagePart(body)` 无条件调用）：状态标 **unknown**——本轮未核到对应落地 commit，不等于未做。requires：需 `git log -S hasImagePart -- internal/server/` 逐 diff 核实一次才能消解。
- [ ] P0 表其余条目（#1/#3/#5/#7/#8/#9 等）本次未被 organizer 指派，不核不写。

## 四、怎么回滚

纯文档：`git checkout HEAD -- docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md`；本 changes 目录可随提交整体 revert。

## 五、Learning

根因：organizer 线索与实证有两处出入（F-304 生产代码其实在 `73911e4` 而非 `600fc48`；F-302 的落地提交是 `73911e4` 而非候选 `9489e98`/`d21d5c2`），按「线索若与实证不符以实证为准」逐条 `git show` diff 才发现。转述线索只能当索引，不能当证据——与 L-15 同源。
