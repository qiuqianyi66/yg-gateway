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
- 生产实例 `D:\yg-gateway` 仍在跑旧 exe（内嵌旧 app.js）：需 `deploy-prod.ps1 -Build` 换版后
  下拉新文案才上线；本变更只改源码 + 提交。
