# 变更记录 — Playground 模型下拉：未知倍率显式化 + 同名模型区分（L1）

- 日期：2026-10-17
- 分级：**L1**（前端行为变更：仅面板下拉文案，无接口/数据模型/协议变更，不触承重墙）
- 来源：用户报告「模型后面的倍率还没解决」——实测为两件事：①下游/旧缓存页面看不到倍率
  （当前代码已带 `pgModelLabel`，强刷可见）；②**真问题**：同名模型与未知倍率展示不明确。
- 并发边界：工作区另有并行会话的未提交改动
  （`cmd/server/api_keys_config_test.go` / `cmd/server/config.go` /
  `internal/scheduler/scheduler.go` / `internal/scheduler/wakeup_and_window_boundary_test.go`），
  本变更**不碰**这些文件，提交只含本目录与 `internal/panel` 两文件。

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `internal/panel/app.js` | ①`pgModelLabel`：倍率缺失（上游未给出 = 未知，AGENTS.md 四态）由 `—` 改为显式 `倍率未知`；②`pgModelOptions`：同名展示名（实测 `cn:hy4-preview` 与 `cn:hy4-preview-f` 都叫 "Hy4 preview"）在选项文案末尾追加完整 id 区分，独名模型不加后缀；③注释压缩以维持 `app.js` 行数棘轮（4378 不升） |
| `internal/panel/frontend_test.go` | 新增 `TestAppJSPgModelLabel`（node + vm 切片执行）：断言未知倍率标「倍率未知」、同名模型带 id 后缀、独名不加后缀、默认选中首模型 |

## 二、为什么这么改（最小改动原则）

- 用户点名的「倍率没解决」拆成两层：旧页面/旧缓存看不到倍率（非代码问题，强刷即好）；
  真正在代码里且值得修的是「同名两条 Hy4 preview 无法分辨」与「无倍率显示 `—` 有歧义」。
- 未做（用户明确要求别搞复杂）：倍率脏字符串规范化（`x0.71 credits` / `0x` 原样透出），
  模型表格 `rateCell` 的 `—` 口径不动——只改用户截图对应的下拉。

## 三、怎么验证的（直接观测，不是转述）

| 断言 | 证据 |
|---|---|
| 下拉文案带倍率 | 实抓 `http://127.0.0.1:7863/panel/app.js` 含 `pgModelLabel`；实抓 `/panel/api/models` 返回 20 模型且 `credits` 为 `x0.29` 等倍率串 |
| 未知倍率标「倍率未知」 | `go test ./internal/panel/ -run TestAppJSPgModelLabel -count=1 -v` PASS；断言 `labels` 第 3 项 = `Hy4 preview · 倍率未知` |
| 同名带 id 后缀 / 独名不带 | 同一测试断言 `opts` 含 `>Hy4 preview · x0.29 (cn:hy4-preview)</option>` 与 `>Hy4 preview · 倍率未知 (cn:hy4-preview-f)</option>`，且 `GLM-5.3-Flash · x0.06` 无后缀 |
| 全量门禁 | `go test ./... -count=1` 全绿（23 包）；`go vet ./...` 0；`gofmt -l ./cmd ./internal` 无输出；`check-boundaries.ps1` 通过；`check-depth.ps1` **4378 = 基线，未回退**（前端体量棘轮） |

## 四、回滚

`git revert <本提交>`。回滚后下拉恢复为：未知倍率显示 `—`、同名模型不带 id 后缀。

## 五、遗留（显式标注，不假装解决）

- 上游倍率脏字符串（`x0.71 credits` / `0x`）仍原样透出——未做规范化（用户要求最小改动）。
- ~~生产实例仍跑旧 exe~~ **已换版（2026-10-17 16:41）**：`deploy-prod.ps1 -Build -AllowDirty -Feature pg-dropdown-multiplier`，
  备份 `D:\yg-gateway\yg-gateway.exe.pre-pg-dropdown-multiplier-20261017.bak`（旧 sha256=F0422B2EFDD7E898），
  新 exe sha256=E7A676DEC72C1337；健康 4/4、`/v1/models` 20 模型、流式+非流式冒烟全过；
  线上 `/panel/app.js` 已实测含「倍率未知」与同名区分逻辑。
- 部署时工作区含并行会话 4 个脏文件（`cmd/server/config.go`、`cmd/server/api_keys_config_test.go` 等）——
  按用户同意用 `-AllowDirty`，该 exe 无法用提交号追溯；并行会话提交后如需可追溯构建可再换版一次。

## 六、追加（2026-10-17）：`/v1/models` 的 name 拼倍率——让第三方客户端显示

- 触发：用户在 deepseek harness 客户端「重新提取模型」仍看不到倍率。
  实测根因：客户端渲染的是 `/v1/models` 的 **name** 字段，不渲染 `credits` 字段；面板修复
  （app.js）只影响面板自身 UI，与 `/v1/models` 无关。**数据一直在返回里**（`cn:hy4-preview
  → credits=x0.29` 等），是客户端不画这个字段。
- 用户拍板 **方案 A**：接受把倍率拼进 name（代价：所有显示 name 的地方都带倍率；
  面板自身走 `/panel/api/models` 另有倍率列，不受影响）。

| 文件 | 改动 |
|---|---|
| `internal/server/handler.go` | 新增 `nameRateSuffix`：上游 credits 原文（`x0.05 credits`/`x0.29`/`0x`/空）归一为 ` ×N`（去 `credits` 后缀、剥首尾 `x`）；空 = 倍率未知，**不加后缀不编造**（四态纪律）。`applyModelInfoFields` 的 name 由 `mi.Name` 改为 `mi.Name + nameRateSuffix(mi.Credits)`，仅作用于 `/v1/models` |
| `internal/server/handler_test.go` | `TestModelsEndpoint` 新增 name 断言（`GLM-5.2 ×0.05`）；新增 `TestNameRateSuffix` 表测（7 例，含空值不拼后缀） |

- 验证：定向测试 PASS；`go test ./... -count=1` 全绿（23 包）；`go vet` 0；`gofmt -l` 无输出；
  `check-boundaries.ps1` / `check-depth.ps1` 通过（app.js 4378 行基线未动）。
- 换版（2026-10-17 19:20）：`deploy-prod.ps1 -Build -Feature pg-dropdown-multiplier`，
  本次**脏文件=0**（并行会话已提交），exe 可追溯 HEAD=72345ef，sha256=FDF92DE1027C2782；
  备份 `D:\yg-gateway\yg-gateway.exe.pre-pg-dropdown-multiplier-20261017-192048.bak`；
  健康 4/4、冒烟三条全过。线上实测（Python 干净 UTF-8）：`cn:hy4-preview → "Hy4 preview ×0.29"`、
  `cn:space-bunny → "Space-Bunny ×0.08"`；无倍率模型保持裸名（`cn:auto → "Auto"`、
  `cn:hy4-preview-f → "Hy4 preview"`，= 倍率未知，不加后缀不编造）。
- 回滚：`git revert <本提交>`。回滚后 `/v1/models` name 恢复裸名。
