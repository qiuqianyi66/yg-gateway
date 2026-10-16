# 变更总结 — T4：R8 协议数据 golden 化（改名即红）

> 承接 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 的 T4。
> 目标：把「上游报文里不得改名/不得改序」的内容从「人记得 + 散落断言」变成**单点**——
> 任何结构变化在同一次运行里报出**精确到行**的 diff。

## 一、改了什么

| 文件 | 内容 |
|---|---|
| `internal/upstream/r8_golden_test.go`（新增） | 三条测试：`TestR8ProtocolGolden`（锁本体，默认只读比对）、`TestR8GoldenHoldsCriticalLiterals`（防 golden 被削弱）、`TestR8GoldenComparatorDetectsEveryByteEdit`（证明比对器不空转） |
| `internal/upstream/testdata/r8_protocol.golden`（新增） | 159 行、10 个 section 的协议数据快照 |

golden 的 10 个 section：`ua` · `paths` · `origin-language` · `headers.chat.cn` · `headers.chat.global` ·
`headers.billing.cn` · `headers.refresh.cn` · `stream` · `conversation-id` · `conversation-id-generators` · `report-body`。

**生产代码零改动。**

## 二、核心设计取舍：钉结构，不钉值

R8 的风险是「改名」；但报文里还有一类**合法变动**——版本号、派生 ID、时间戳、token。

- **钉**：段数、段序、分隔符、大小写、字面量、按 realm 的候选序列与顺序、头族是否发出、真实 id 是否被覆盖。
- **不钉**：上述会变的值，**在建 golden 时**就归一成 `<ver>` / `<32hex>` / `<36hex>` / `<16hex>` / `<uuid>` /
  `msg-<hex>` / `<digits>` / `<token>`。

结果是：升级 `defaultClientVersion`（正常维护）**不会**红；删掉 `CLI/` 段、把平台段品牌换掉、
改名哨兵或任一个 `wb2api-*` 前缀、调换 global 候选顺序、去掉任一会话头**会**红。

**生成过程中踩到的坑（已修，写进代码注释）**：首版漏归一 16-hex，`X-B3-SpanId`（= messageID[:16]，
每次调用都变）进了 golden → **golden 每次都不同**。这说明「归一化清单」本身就是 golden 的失效点，
所以：① 补 `<16hex>` 与 `msg-<hex>`；② 生成后**连续多轮比对**确认确定性（见 §三.2）。

重写方式（唯一）：

```powershell
go test ./internal/upstream/ -run TestR8ProtocolGolden -count=1 -update-r8-golden
```

## 三、怎么验证

### 1. golden 已被生成且内容合理

`-update-r8-golden` 生成 159 行；关键行抽样（`[ua]` 段）：

```
userAgent(cn)            = WorkBuddy/<ver> WorkBuddy/<ver> CLI/<ver>
userAgent(global)        = WorkBuddy/<ver> WorkBuddy AI/<ver> CLI/<ver>
billingUA()              = WorkBuddy/<ver>
billingUA(SaaS)          = (empty)
```

`[conversation-id-generators]` 段（**真跑生成代码，不写字面量**）：

```
DesktopChatWithExpert conversationId           = wb2api-conv-<digits>
SchoolChatTimesEvents traceId                  = wb2api-<uuid>
SchoolChatTimesEvents messageId                = msg-<hex>
RunNightChats 上报 conversationId              = wb2api-night-<digits>-0
```

### 2. 确定性（不许 flaky）

生成后连续 3 轮 `-run TestR8 -count=1` 全绿（`ok ... 0.08s`），无一次抖动。

### 3. 比对器不空转（自证）

`TestR8GoldenComparatorDetectsEveryByteEdit` 对 golden 的**每一个字节**（5968 个位置）做一次篡改，
断言比对器每次都报出差异——**通过**。这条测试的存在理由：没有它，「改 golden 里任一字节 → 测试红」
只是口头承诺，比对器写错也照样是绿的。

另加 `TestR8GoldenHoldsCriticalLiterals`：显式断言 golden 里仍含 11 个关键片段
（`CLI/<ver>`、`WorkBuddy AI/<ver>`、`chatcmpl-wb2api`、`wb2api-conv-<digits>`、`wb2api-<uuid>`、
`wb2api-night-`、`msg-<hex>`、`/v2/chat/completions`、`/v2/billing/meter/`、
`X-Conversation-Request-Id`、`X-Codebuddy-Request`、`X-Auth-Refresh-Source`），防止「golden 被清空/重写坏」后锁形同虚设。

### 4. 真牙齿：7 条 R8 定向变异（在 `%TEMP%` 副本内，原件只读）

先断言 `go env GOMOD` 指向副本（LESSONS L-2），每条变异后还原并**复核 SHA256 一致**：

| # | 变异（改什么行为） | 结果 | golden 报出的定位 |
|---|---|---|---|
| M1 | 三段式 UA 去掉 `CLI/` 段 | **CAUGHT** | 第 8 行，共 8 行不同 |
| M2 | 流式哨兵 `chatcmpl-wb2api` → `chatcmpl-wbapi` | **CAUGHT** | 第 128 行 |
| M3 | global billing 候选顺序反转 | **CAUGHT** | 第 24 行 |
| M4 | CN Origin 基础域 `.cn` → `.com` | **CAUGHT** | 第 33 行，共 5 行不同 |
| M5 | `desktop` conversationId 前缀 `wb2api-conv-` → `wbconv-` | **CAUGHT** | 第 142 行 |
| M6 | `school` 的 `wb2api-<token>` → `wbx-<token>` | **CAUGHT** | 第 146 行，共 2 行不同 |
| M7 | `blackcat` 夜猫子上报 ID `wb2api-night-` → `wbnight-` | **CAUGHT** | 第 152 行 |

**7/7 CAUGHT**，每条都精确到 golden 行号 + golden/当前 两行对照。

### 5. 顺带补掉的第二处同类缺口

M7 那条在补进 golden **之前没有任何断言**——`wb2api-night-<ms>-<i>` 是 inline 生成
（`blackcat.go:62`），改前缀不会让任何测试变红。这与 T3 发现的 M2 是同一类问题
（「生成的 ID 形态没人钉」），本次一并闭掉。

## 四、怎么回滚

`git revert <sha>`：删掉 `r8_golden_test.go` 与 `testdata/r8_protocol.golden`。
回滚后 R8 重新退回「靠人记得 + 散落断言」的状态——**不建议**，除非同时有别的机制接管。

## 五、遗留（已知、未做）

1. **成本**：`[conversation-id-generators]` 段要真跑 `RunNightChats`，而它内含**不可注入的 4s sleep**
   （已知遗留），使 `internal/upstream` 单轮从约 7s 变成约 11s。换来的是一条此前无人断言的 R8 字面量。
2. **覆盖边界**：golden 覆盖的是**可调用面**（函数/方法真实产出），不是源码字面量的静态全扫描。
   仍未被 golden 直接覆盖的 R8 相关字面量：`chatcmpl-wb2api` 之外的帧内其它常量（如白名单字段名，
   已由 `sse_test.go` 的既有断言覆盖）、`headerTransport` 等由 stdlib 决定的头。
3. **`-update-r8-golden` 无二次确认**：这是刻意的（和 Go 官方 golden 惯例一致）——
   但意味着「R8 事故」可以靠重写 golden 掩盖。防线是评审：重写 golden 的提交必须在 body 里
   说明「这是评审通过的报文变更」。
