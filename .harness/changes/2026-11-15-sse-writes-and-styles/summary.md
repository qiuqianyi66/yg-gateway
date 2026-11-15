# 变更：SSE 写路径合并（含一处真缺陷）+ 面板样式外置

- 日期：2026-11-15
- 前置：`36bd45e`（main() 483→354）
- 分诊：L2（跨文件结构改动，无对外契约变化）

## 批次一：sse.go 两条重复写路径合并 → 顺带查出未加锁写

### 症状

`StreamHint` 里 `writeSSE`（729–750）与 `writeLine`（753–774）是两份**逐行相同**的
「加锁 → 写 → flush → 更新 lastBeat」序列，唯一差别是写入内容（`"data: "+payload+"\n\n"`
vs 原样 `s`）。二者靠注释互相约束（「与 writeSSE 同锁口径」）——本仓已被证明这种注释约束
会漂移（`guards.go` 那一批同因：pick 与 pickNonCredits 各复制四份护栏，注释写着「改一处
必须同步另一处」，实际早已不同步）。

### 改动

合并成单一 `writeThrough(s)`，`writeSSE` 保留为语义命名薄包装（4 个调用点），`writeLine`
删除，唯一调用点直呼 `writeThrough`。

### 顺带查出的真缺陷（🟡，已修）

`writeLine` 删掉后做「全文件还有没有绕过锁的写」复核，`grep io.WriteString(w` 命中三处：
心跳 goroutine（持锁内联，合法）、`writeThrough` 本体（合法）、以及**第 967 行的
`data: [DONE]` 兜底写**——它直接 `io.WriteString(w, ...)`，**没有加锁**。

为什么是真缺陷：该段在 `readLoop` 之后、心跳的 `defer close(stopKa)` 之前执行，
此时心跳 goroutine **仍在运行**（它按 `keepAlive` 周期写 `: ping\n\n`）。两个 goroutine
并发写同一个 `http.ResponseWriter` 会交错出半帧——SSE 客户端把 `data: [D` + `: ping\n\n`
+ `ONE]\n\n` 读成非法帧。触发条件是「keepAlive 开启 + 恰在写结尾时心跳 tick」，窄但真实。

修法：改为 `writeThrough("data: [DONE]\n\n")`（删除随后多余的 `fl.Flush()`，该函数已含 flush）。

**验证强度说明**：该竞态需要精确时序才复现，本机无 gcc 故 `-race` 不可用（CI 独占），
因此**没有回归测试**；证据是静态的——`grep` 确认全函数只剩「锁内写」与「唯一出口」两类写路径，
且 upstream 包全测试绿。

### 数字

| 指标 | 前 | 后 |
|---|---|---|
| `sse.go` 非空行 | 1070 | 1042 |
| `maxFileLines`（门禁口径） | 1070 | **1041** |
| `StreamHint` 代码行 | 292 | 约 270 |

## 批次二：index.html 内联样式外置（804 行 → 0）

### 症状

`index.html` 1827 非空行里，**804 行是内联 `<style>`**（占 44%），另有 21 行第二段
`<style>` 写在 `<body>` 尾部。页面骨架被当成样式仓库用；而 `maxFrontendLines` 棘轮只许收紧，
于是每加一条样式都在吃页面骨架的额度。

关键前提：项目**已经有**这套机制。`index.go` 的 `panelStyles` 清单注释写明它正是为了
「组件数增长不该受页面骨架行数限制」而建的，`gamify.css` / `provider.css` 两个先例已在跑
（`provider_css_test.go` 甚至记录了「panelStyles 加了但 routes.go 漏注册」的抓虫史）。

### 改动

| 文件 | 内容 |
|---|---|
| `internal/panel/app.css`（新，813 非空行） | 原 head 内的基础样式（设计令牌 / 布局 / 基础组件） |
| `internal/panel/panel.css`（新，27 非空行） | 原 body 尾部的命令面板（Ctrl+K）与 Playground 样式 |
| `index.html` | 删除两个 `<style>` 块，**1827 → 998 非空行** |
| `index.go` | 两条 `//go:embed` + 两个 handler；`panelStyles` 加两项 |
| `routes.go` | 注册 `GET /panel/app.css`、`GET /panel/panel.css` |
| `security_test.go` | 两条新路径纳入「CSP + nosniff」断言列表 |

### 层叠顺序（唯一有真实风险的决策）

原层叠关系：`head 内联样式` → `gamify.css` → `provider.css` → `body 尾部内联样式`。
拆成清单后必须复现同一顺序，否则优先级反转（`panel.css` 的 `.pg-*` / `.cmd-*` 会被
后续样式覆盖）：

```
panelStyles = [app.css, gamify.css, provider.css, panel.css]
```

- `app.css` 首位：设计令牌（`var(--*)`）必须先落地，组件样式才取得到值
- `panel.css` 末位：保住原「最后覆盖」语义

顺序由新测试 `TestPanelStylesExternalized` 钉死（首位/末位/embed 非空/登记齐全），
改清单会红。

### 测试侧判据修正（不是放宽）

4 个既有测试把「样式存在」判据绑死在 `index.html`（`.skel {`、`.req-pager {`、
`#view-keys .rpm-bars i`、`minmax(min(340px, 100%), 1fr)`）。样式外置后它们以「位置变了」
为由误报红。新增 helper `panelStyleSource(t)`，把搜索范围改成 **index.html + panelStyles
清单里的全部 CSS**——判据语义不变（样式必须存在于面板样式全集），范围更正确（样式本就
已分散）。清单取自生产变量 `panelStyles`，不手抄文件名（手抄必漂移）。

## 验证（本会话实跑）

```
go build ./...                         exit 0
go vet ./...                           exit 0
gofmt -l internal cmd                  无输出
go test ./... -count=1                 26 包全绿
check-depth.ps1 -Update                棘轮抬高：maxFileLines 1070→1041、
                                       maxFrontendLines 1827→1640、oversizeFrontendFiles 2→1
```

## 回滚

两批都是纯搬运/合并，各自独立可回滚：

- 批次一：`git checkout <base> -- internal/upstream/sse.go`
- 批次二：`git checkout <base> -- internal/panel/index.html internal/panel/index.go internal/panel/routes.go internal/panel/security_test.go internal/panel/frontend_ux_test.go` 并删除 `app.css` / `panel.css`

## 主动未做

- `StreamHint` 仍是代码行最多的函数（约 270 行）。拆它需要把 7 个跨帧状态
  （toolCallSeen / firstID / repair / synthIdx / maxToolIdx / toolFinishSent / validFrames）
  收进结构体——那是「重排状态所有权」，不是「搬块」，风险与收益需单独立项评估。
- `app.js` 1640 行仍是唯一 >1000 前端文件。
- `[DONE]` 竞态没有回归测试（原因见上，`-race` 本机不可用）。
