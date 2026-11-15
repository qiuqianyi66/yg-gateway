# 评审记录：SSE 写路径合并 + 面板样式外置

- 评审对象：本轮两批改动（详见同目录 `summary.md`）
- 评审方式：**自评审**（按 `.harness/skills/main-dev/expert-reviewer` 6 维）
- 证据：本会话实跑输出，非历史记录

> ⚠️ 流程缺口（显式记录）：`开发流程规范` 要求审计级改动有一次**独立复审**。本轮未执行
> （单会话，用户未派子代理）。结论强度 = 「自评审 + 机械门禁全绿」，不等于独立复审通过。

## 1. 正确性

| 检查点 | 结论 | 依据 |
|---|---|---|
| `writeThrough` 与原 `writeSSE`/`writeLine` 语义等价 | 是 | 原逻辑「lock → 写 → flush → unlock → 记时」；新逻辑「lock（defer unlock）→ 写 → flush → 记时」。`defer` 在 return 前执行，unlock 时刻不变；`lastBeat` 更新条件同为 `werr == nil` |
| 心跳 goroutine 是否会自锁 | 否 | 心跳在锁内联写 `: ping`，**不调用** writeThrough；函数注释已写明这条禁令 |
| `[DONE]` 修复后行为 | 等价 + 修复 | 原：直写 + `fl.Flush()`；新：`writeThrough` 内含 flush。写失败语义（返回 err）不变 |
| CSS 拆分是否改变渲染 | 否（有条件） | 内容逐字节搬运（仅新增文件头注释）；层叠顺序由 `app.css`/`gamify.css`/`provider.css`/`panel.css` 的清单顺序复现原序 |
| 层叠顺序是否真的等价 | 是 | 原顺序 = head 内联 → gamify → provider → body 尾内联；清单顺序一一对应 |
| `security_test.go` 新增路径断言 | 通过 | 全绿（26 包） |

## 2. 性能

- `writeThrough` 比两份重复实现在热路径上**少一层闭包间接**（原 `writeSSE` → 内部 lock/unlock
  闭包；现 `writeSSE` → `writeThrough`，lock/unlock 内联为 defer）。未做 bench 对比，
  **不作性能结论**。
- `[DONE]` 走 `writeThrough` 多一次 `lastBeat.Store`（原子写），可忽略。
- CSS 外置后首屏多 2 个 HTTP 请求（`app.css` / `panel.css`）。影响：面板已设
  `Cache-Control`（同 `index.go` 静态资源口径），且这是既有 gamify/provider 的同一形态。
  **未实测首屏耗时变化**，不作声明。

## 3. 安全

| 检查点 | 结论 |
|---|---|
| 新路由是否需要鉴权 | 不需要，与既有 `gamify.css` / `provider.css` 同口径（静态样式、无数据） |
| CSP | `style-src 'self'` 已含，`<link>` 加载无需放宽（与 gamify.css 同因）；`security_test` 已把两条新路径纳入 CSP + nosniff 断言 |
| 是否引入新输入路径 | 否（无 query / body 解析） |
| R1 / R2 / R5 / R6 / R8 | 未触碰 |
| `w.Write` 忽略错误的处理 | 沿用既有口径（静态资源全量写回，写失败=客户端断连），注释随行 |

## 4. 一致性

- 新文件命名沿用 `gamify.css` / `provider.css` 的 `*.css` 平铺惯例 ✓
- handler 命名 `appStyle` / `panelStyle` 与 `gamifyStyle` / `providerStyle` 同族 ✓
- 「忽略写错误」注释逐字沿用既有句式 ✓
- 行数口径：全篇区分「实际行 / 非空行」（LESSONS L-5）✓

## 5. 可维护性

- `index.html` 1827 → **998**（退出 >1000 前端名单）；`maxFrontendLines` 棘轮 1827 → 1640。
- `sse.go` 1070 → 1042；`maxFileLines` 棘轮 1070 → 1041。
- 🟢 保留：`StreamHint` 仍约 270 代码行（跨帧状态需整体收进结构体，单独立项）。
- 🟢 保留：`app.js` 1640 行（唯一 >1000 前端文件）。
- 🟡 已修：`[DONE]` 未加锁写（见 §1）。

## 6. 架构合规

| 检查 | 结果 |
|---|---|
| 依赖方向 | `internal/panel` 内自包含，无跨包新增依赖 ✅ |
| 单二进制零依赖红线 | 仍是 `go:embed` 静态资源，无构建步骤、无外部库 ✅ |
| 事实语义纪律（四态） | 无对外字段变化 ✅ |
| R13（前端改动需重编译） | `go:embed` 编译期嵌入，`go build` 即重编译 ✅ |

## Findings

| 级别 | 条目 | 处置 |
|---|---|---|
| 🔴 | 无 | — |
| 🟡 | `[DONE]` 兜底写未加锁（心跳在途时可能交错半帧） | **已修**（改走 `writeThrough`）；无回归测试，原因见 summary |
| 🟢 | `StreamHint` 仍约 270 代码行 | 记录，单独立项 |
| 🟢 | `app.js` 1640 行 | 记录 |
| 🔵 | `writeThrough` 少一层闭包间接的性能影响 | 未实测，不作结论 |
| ⚠️流程 | 独立复审未执行 | 显式记录，结论强度降级 |
