# 评审记录 — opsagent 可观测闭环（阶段 2）：unknown 全量视图 + 审计轮上面板

- 日期：2026-10-25
- 对象：HEAD `e1f0268`（feat(panel): opsagent 可观测闭环（阶段 2））+ 本目录 summary.md 所列文件
- **复审人：独立 agent 会话（L-16：与实现者无共同记忆）**；只读复审 + 机器验证，
  本次唯一写入的文件是本记录，未改任何其他文件
- 轮次：1 轮

## 结论

**PASS**（0 🔴 / 2 🟡 / 3 🟢 / 3 🔵）

两条 🟡 都是**测试牙齿缺口**，不是实现缺陷——对应实现已逐条读码确认行为正确，
机器验证全绿（含全量 pre-commit 10/10 独立复跑）。不阻塞本提交，建议随下一个
触碰这两处的改动补上。

---

## 一、diff 逐文件核对（清单 1）：有无声称之外的改动

`git show HEAD --stat`：14 文件，+425/−26。与 summary.md「改了什么」逐个对上；
差异只有两处**记账口径**：`opsagent_view_test.go` / `opsagent_wiring_test.go` 不在
「改了什么」清单里，但「怎么验证的」已点名（新增测试 / wiring 三态）——不算未声称改动。

| 文件 | 核对结果 |
|---|---|
| `internal/pool/unknown.go` | 仅 +`AllOpenUnknowns()`（169-186），无顺手改动 |
| `internal/panel/opsagent_view.go` | 新文件，与 summary 描述一致 |
| `internal/panel/opsagent.js` | 新文件 87 行（summary 写「~110 行」，见 🟢-1） |
| `internal/panel/index.html` | 仅 +卡片 8 行 +script 1 行 |
| `internal/panel/panel.go` / `index.go` | 仅 OpsAgent 字段、路由×2、embed+serving |
| `internal/opsagent/opsagent.go` | 仅 +`ConfigSnapshot()`（190-194），只读 getter |
| `cmd/server/main.go` | notifier/agent 构造提前（377-378）+ OpsAgent 字段（404）+ start 调用改签名（490）；`server.Config.Alerting` 仍拿同一 notifier 实例（478），与「二者共用 notifier」声明一致 |
| `cmd/server/opsagent_wiring.go` | startOpsAgent 拆 build/start，与声明一致 |
| `cmd/server/opsagent_wiring_test.go` | 测试随签名重写，断言真实（见 §四） |
| `scripts/check-boundaries.ps1` | panel 行 += `opsagent`,`unknown`，带理由注释，符合「新增方向必须说明理由并更新脚本」 |
| `docs/prd/REQ-opsagent-2026-10-24.md` | 勾销 + 剩余顺序，纯文档 |

**结论：无实现者声称之外的语义变化。** 唯一的"位置移动"是 `newAlertNotifier(cfg)`
提前约 60 行——该函数是纯配置投影（opsagent_wiring.go:23-29），无副作用、不依赖
中间构建物，移动不改行为。

## 二、安全面（清单 2，重点）

### esc() 逐字段核对（opsagent.js）

| 字段 | 位置 | 转义 |
|---|---|---|
| `it.id` | :33 `esc(String(it.id).slice(0,12))` | ✅ |
| `it.question` / `it.requires` / `it.revision` | :34-36 | ✅ 各自独立 esc |
| `s.Rule` / `s.Title` | :43 | ✅ |
| `s.UID` | :44 `esc(String(s.UID).slice(0,8))` | ✅（渲染截 8 位） |
| `rd.At` | :55 经 `fmtTime`（:22 内部已 esc 兜底）再包 esc | ✅（见 🔵-2 双重转义备注） |
| `rd.Rejected` | :48 只取 `.length` 且 esc | ✅（明细内容不渲染，见 🔵-3） |
| 建议数 / unknown 数 | :56 esc 数字 | ✅ |
| fetch 错误消息 | :79 走 `body.textContent` | ✅ 不进 innerHTML |

innerHTML 路径只有两处赋值（:68-71）：disabled 分支是**纯静态串**；主分支的全部
动态片段都在 renderUnknowns/renderRounds 内过 esc。无内联事件（`addEventListener`，
:85-87），无内联脚本。字段名与 Go 侧 JSON 标签核对一致（unknown.Item 小写 tag
`registry.go:47-60`；Round/Suggestion 默认大写导出名——JS 读 `rd.Suggestions`/
`s.Rule` 口径正确，测试 `opsagent_view_test.go:81` 亦证）。

### 其余安全项

- **fetch 仅同源**：相对 URL `/panel/api/opsagent/status`（:63）；CSP `connect-src 'self'`
  （index.go:45）兜底。
- **API 套 withAuth**：`panel.go:174` 路由注册 `p.withAuth(p.opsAgentStatus)`；
  withAuth → authenticateAdmin → `httpauth.VerifyBearer`（panel.go:228-252，R5 口径）。
- **CSP/安全头**：API 响应也走 `setSecurityHeaders`（opsagent_view.go:28），测试
  `TestOpsAgentStatusEnabled` 显式断言 CSP 头存在。
- **UID 是否超 8 位泄漏（R2）**：见 🔵-1——API JSON 里 `rounds[].Suggestions[].UID`
  是全量 uid（wiring :64 `UID: st.UID` → opsagent.go:265），JS 渲染截 8 位；日志侧
  全部走 `logfmt.Label/UID8`（uid8）。判定为与面板既有口径一致，非 R2 违规（R2 管日志）。

## 三、行为面（清单 3）

- **`AllOpenUnknowns` 语义**（pool/unknown.go:169-186）：nil reg 守卫 → `Registry.All()`
  （值拷贝，registry.go:197-207）→ 过滤 `!it.Resolved`。与注释「registry 全量过滤
  resolved，不分账号不分来源」一致。不加 pool 锁是对的：unknownReg 启动期 set 一次，
  Registry 自带锁，与既有 `OpenUnknownCount` 同口径。
- **未启用分支**：`oa == nil` → 200 + `{"enabled":false}`（opsagent_view.go:33-36），
  安全头在分支**之前**写，引导态同样受保护。`omitempty` 使 disabled 响应只有 enabled 键。
- **装配顺序**：main.go:377 `newAlertNotifier` → :378 `buildOpsAgent` → :404 panel
  Config `OpsAgent: opsAg` → :478 `server.Config.Alerting` 同实例 → :483-490 ctx 创建后
  `startOpsAgent(opsAg, ctx)`。notifier→agent→panel→start，与声明一致。
- **enabled=false 零 goroutine**：`buildOpsAgent` 返回 nil（:76-78），`opsagent.New`
  根本不被调用（New 本身只构造、不启动 goroutine，opsagent.go:159-174）；
  `startOpsAgent(nil)` 直接 return（:86-88）。观察面闭包也不创建（在 New 的参数位）。
  enabled 时恰一个 goroutine `go a.Run(ctx)`（:92），app 级 ctx，Run 在 ctx.Done 返回
  （opsagent.go:177-188，R9）。
- **行数声明**：main.go 非空行 999（`grep -cve '^\s*$'` 实测）< 1000 ✓；
  check-depth：`最大前端文件行数 4378 (app.js)`、`>1000 行前端文件数 2`，app.js 不在
  本次 diff（maxFrontendLines 棘轮不动，声明成立）。

## 四、测试诚实度（清单 4）

**真断言的部分**：
- `TestOpsAgentStatusEnabled`：配置快照回传（600s/0.5）、resolved 被过滤（预置一条
  已消解条目 + 断言 unknowns_open 为 0——若过滤缺失该断言会红）、每轮 capacity 建议、
  Rejected 空、CSP 头在 API 也在。**过滤的"排除半边"有牙齿**。
- `TestBuildOpsAgentDisabledNil`：nil pool / disabled / enabled 三态都是行为断言。
- `TestOpsAgentScriptServing`：Content-Type/CSP/非空 embed。
- Registry 层 `TestListOnlyOpen`（registry_test.go:105）已覆盖 List 过滤 + All 全量
  的底层语义。

**断言漏洞（🟡 两条）**：
- 🟡-1 **「open 条目被包含」无任何测试**：全仓 `grep -rn AllOpenUnknowns` 仅 3 处
  （定义 / 唯一调用点 / 测试注释）。测试只预置**已消解**条目——若实现改成
  `return nil`，`TestOpsAgentStatusEnabled` 依然全绿。registry 层语义有测试，但
  pool 层这 8 行装配（nil 守卫 + 过滤循环）的"包含半边"零覆盖。
- 🟡-2 **`maxStatusRounds=10` 截断无测试**（opsagent_view.go:19,44-46）：`>10 轮 →
  取最后 10` 是本端点唯一非透传逻辑，off-by-one（如切错方向丢最新一轮）会静默少显。
  清单点名的「rounds 顺序上限」确实没断言；顺带：History 旧→新与前端 reverse
  （新→旧）的顺序契约也没有测试钉住（2 轮无法区分序）。

- 🟢-3 `TestStartOpsAgentEnabledStartsAndExits` 用 `time.Sleep(20ms)` 无法证明
  goroutine 已退出（无同步点）——沿袭改造前的既有模式，Run 的 ctx 退出路径已读码
  确认，风险低，记录备查。

## 五、机器验证（L-4，输出原样摘录）

```
$ go test ./internal/panel/ -run TestOpsAgent -count=1 -v
--- PASS: TestOpsAgentStatusDisabled (0.00s)
--- PASS: TestOpsAgentStatusEnabled (0.02s)
--- PASS: TestOpsAgentScriptServing (0.00s)
PASS  ok  yg-gateway/internal/panel  0.066s

$ go test ./internal/opsagent/ ./internal/pool/ -count=1
ok  yg-gateway/internal/opsagent  0.087s
ok  yg-gateway/internal/pool      0.391s

$ node --check internal/panel/opsagent.js
NODE_CHECK_OK

$ powershell -NoProfile -File scripts/check-boundaries.ps1
✅ 边界检查通过（依赖方向无误）   EXIT=0

$ powershell -NoProfile -File scripts/check-depth.ps1
  最大前端文件行数  : 4378  (internal\panel\app.js)
  >1000 行前端文件数 : 2
✅ 未回退，且以下指标变好（可 -Update 抬高棘轮）： + minCoverage 73.3% → 74%
EXIT=0

$ powershell -NoProfile -File scripts/pre-commit.ps1        （独立复跑全量总闸）
  [1..10] 全部 OK … 提交前检查全部通过。   PRECOMMIT_EXIT=0

$ go fmt + go vet ./cmd/server ./internal/panel ./internal/pool ./internal/opsagent
VET_FMT_OK（fmt 无输出 = 无漂移）

$ grep -cve '^\s*$' cmd/server/main.go → 999
$ wc -l internal/panel/index.html → 1840（summary 写 1846，见 🟢-1）
```

## 六、红线逐条（清单 6）

| 红线 | 结论 | 证据 |
|---|---|---|
| R8 报文字面量 | **零触碰** | diff 无 internal/upstream 文件；wb2api / chatcmpl / UA 均未出现 |
| R9 goroutine 回收 | **合规** | 唯一新 goroutine `go a.Run(ctx)`，Run 的 select ctx.Done 即返回（opsagent.go:177-188）；disabled 时零 goroutine（§三） |
| R10 错误不静默 | **合规** | 新增静默忽略仅 `_, _ = w.Write(opsAgentJS)` 一处，带理由注释、与 appScript/savingsNoteScript 既有口径相同；agent 登记失败进 `rd.Rejected` 不吞（opsagent.go:252-254）；pre-commit R10 检查通过 |
| R1 凭证不出 auths/ | 零触碰 | diff 无凭证读写 |
| R2 日志 UID 前 8 位 | **合规** | 本 diff 唯一新日志行（opsagent_wiring.go:90）不含 UID；opsSink 走 `logfmt.Label`（uid8）；Round 内 question/title 也都是 uid8（logfmt.Label / costUnknownQuestion） |
| R5 密钥比较 | **合规** | API 走 withAuth→`httpauth.VerifyBearer`，diff 无新增 `==` 比较 |
| app.js 冻结 | **零改动** | 不在 diff；check-depth 4378 未动 |
| esc() 纪律 | **合规** | §二逐字段表 |

## 七、问题清单

| 级别 | 位置 | 问题 | 建议 |
|---|---|---|---|
| 🟡-1 | `opsagent_view_test.go` | AllOpenUnknowns「包含 open 条目」半边零覆盖（实现若退化为 return nil 测试仍绿） | 测试里加一条**未消解**预置条目并断言它出现在 unknowns_open |
| 🟡-2 | `opsagent_view.go:44-46` | maxStatusRounds 截断无测试；History 旧→新 ↔ 前端新→旧的顺序契约未钉住 | 造 12 轮断言返回 10 轮且为最新 10 轮（At 单调） |
| 🟢-1 | summary.md | 行数两处不准：index.html 写「1831→1846」实为 1831→**1840**（wc -l 实测）；opsagent.js 写「~110 行」实为 **87** 行。棘轮口径（非空行）不受影响，check-depth 已实测未回退 | 下次顺手更正 |
| 🟢-2 | `opsagent_wiring_test.go` | enabled 用例靠 sleep 无法证 goroutine 退出（沿袭旧模式） | 可接受；若 Run 将来加完成回调可补同步断言 |
| 🟢-3 | `opsagent.js` | 语法闸只有手工 `node --check`（TestAppJSSyntax 只扫 app.js；acl_notice.js/savings_note.js 同样不在闸内，属既有模式） | 可把独立嵌入 JS 一并纳入语法测试 |
| 🔵-1 | `opsagent_view.go` 响应 | `Suggestions[].UID` 为全量 uid（JS 渲染截 8 位）。R2 管日志，面板对已鉴权管理员本就暴露全量 uid（app.js:710 title、:725-732 data-u），端点走 withAuth——口径一致非违规 | 建议在 opsagent_view.go 注释写明该口径，避免下任复审重查 |
| 🔵-2 | `opsagent.js:55` | `esc(fmtTime(rd.At))` 在 invalid-date 兜底路径会双重转义（fmtTime 内已 esc）——实际不可达（time.Time RFC3339 恒合法），纵深防御无害 | 保持现状即可 |
| 🔵-3 | `opsagent.js:48` | Rejected 只显示条数不显示明细（API 有数据）——审计可见性略打折，非安全问题 | 可后续渲染明细文本（记得过 esc） |

## 八、未覆盖（未覆盖 = 未知，不是没有）

1. **浏览器真实渲染**：esc/innerHTML 核对是读码 + 字段对照，未做浏览器级 E2E
   （与 10-17 复审同一缺口，D-5 重评条件未变）。
2. **summary 覆盖率数字**（panel 95.5% 等）未逐包复核——只通过 check-depth 的
   「最低包覆盖率 74%」与 cmd/server 74.0% 吻合做了旁证。
3. **402 矛盾规则的端到端触发**（真实 hard 冷却账号 → credit-402-contradiction
   建议出现在 rounds）只有单测层面的 observer 桩，生产首现时以面板实际显示为准。
