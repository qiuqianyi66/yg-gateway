# REQ：拆分 autotask.go（R-203，P2 可维护性债务）

> **状态：v1（2026-10-22）。纯同包重构，行为零变化。**
> 落地记录见 `.harness/changes/2026-10-22-split-autotask/`。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

PRD v2 §4.4 债务清单 P2 项「超大文件拆分」：`internal/panel/autotask.go` 实测
**1300 行 / 1244 非空行 / 41 个函数**，单文件压在 check-depth FILE_LIMIT=1000 之上。
三类职责挤在同一文件：
- 编排/HTTP（`accountTaskAuto` :620、`runAutoAll` :1119、`accountTaskAutoAll` :1272）；
- mp 双口径查询簇（`taskByCode`/`taskByCodeMP`/`acceptWithVerifyMP`/`runMPMiniChatTask` 等）；
- ~25 个 `runXxx` 养号剧本（chat/Buddy/桌面指纹事件链/专家召唤/主题模板画布）。

**Q2 不做会怎样？** 不报错，但它是 L-11（最大文件 1300 行）与 check-depth 棘轮的
存量超线项：每次改任一剧本都在 1300 行文件里定位，评审 diff 跨业务域难读；FILE_LIMIT
=1000 的棘轮对它形同虚设。

**Q3 现状是不是已经变了？** 表驱动注册**天然已存在**（`autoActions []autoAction`
注册表，剧本名/路由/行为不变），无需新引入——任务①（推荐表驱动）已由现状满足。
本次只做任务②（按业务域拆文件）+ 任务③（autotask.go 非空行降到 1000 以下）。

**Q4 有没有更便宜的等效手段？** 有，且本需求即最便宜路径：同包拆 3 文件，零新 import、
零签名变化、零路由变化，编译+测试同基线即证明行为不变。

**结论：做，L1（纯重构）。** 明确不做：剧本逻辑改写、注册表结构改造、跨包迁移。

---

## 二、实测基线（2026-10-22）

| 事实 | 出处 |
|---|---|
| autotask.go 1300 行 / 41 函数 | `internal/panel/autotask.go` 实测 |
| 表驱动注册已存在（autoActions 切片） | autotask.go:51-178 |
| mp 簇（taskByCode 系 + runMPMiniChatTask + Sequential 链） | autotask.go:222-617 |
| PC 剧本（runChat5…runExpertBatch ~25 个） | autotask.go:743-1108 |
| 编排/HTTP（accountTaskAuto/runAutoAll/accountTaskAutoAll） | autotask.go:620-711,1119-1300 |

---

## 三、范围边界

**做**
- `autotask.go` 只留：包文档、imports、`autoAction` 类型 + `autoActions` 注册表、
  `autoActionFor`/`autoActionIndex`、`reportGap`、两个 HTTP 入口、`taskProgressText`/
  `truncateStr`、`runAutoAll`/`autoAllClaimOnly`。
- `autotask_mp.go`（新）：mp 簇——`mpTaskCodes`/`isMPTaskCode`、`taskByCode`/
  `taskByCodeWaiting`/`taskByCodeMP`/`acceptWithVerifyMP`/`acceptStatusOr`、
  `claimPoll*`/`mpActionGap`/`mpChatEventGap`、`runMPMiniChatTask`、`runSchoolSeason`/
  `runSequentialChat*`/`runSequentialEventTask`/`runSequentialAutomation`/
  `runSequentialModelChat`/`runSequentialPlaybook`/`runMiniExpert`。
- `autotask_scripts.go`（新）：PC 域剧本——`runChat5`/`runFirstBuddy`/`runModelChat`/
  `runRichMeow`/`runBuddyApp`/`runAutomationCreate`/`runLibraryRead`/`runBlackCat`/
  `runSkillFresh`/`runExpertLighthouse`/`runAppearance`/`runTemplateUse`/
  `runPlaybookPrompt`/`runCreateCanvas`/`runExpertUse`/`runExpertTeamUse`/`runExpertBatch`
  + `expertSummonGap`。

**不做（明写）**
- 剧本逻辑/报文内容/R8 字符串（wb2api-* conversationId、三段式 UA）一字不改。
- 导出函数签名、HTTP 路由、JSON wire 形状零变化。
- check-boundaries 白名单不变（同包拆分，无新增跨包 import）。
- 不动 `internal/panel` 其他文件（index.go/panel.go/app.js/index.html/acl_notice.js/
  savings_note.js）、`internal/server/*`、`internal/upstream/*`。

---

## 四、需求项（Given-When-Then 验收）

### F-1 autotask.go 非空行降至 1000 以下

- **Given** 拆分完成
- **When** `gofmt -l` 无输出且 `go build ./internal/panel` 通过
- **Then** autotask.go 非空行 < 1000（实测 492），gofmt 无输出

### F-2 行为零变化

- **Given** 纯同包移动
- **When** `go test ./internal/panel` 全绿
- **Then** 与基线 a0ec952 测试结果一致；无新跨包 import（panel 白名单不变）

### F-3 表驱动注册保持

- **Given** autoActions 注册表
- **When** 拆分后编译通过
- **Then** 注册表条目顺序、TaskCode、run 指向函数完全不变；剧本名/路由/行为不变

---

## 五、验证方式

| 项 | 分级 | 实际验证 |
|---|---|---|
| gofmt / build / vet | L1 | 全绿（见 summary） |
| `go test ./internal/panel` | L1 | 与基线同口径全绿 |
| `go test ./...` | L1 | 20 ok + 3 no test files 基线口径 |
| check-depth | — | autotask.go 真实变小后 `-Update` 收紧，连同 json 提交 |

回滚：纯文件拆分，`git checkout` 即恢复；无凭证/数据/wire 改动。

---

## 六、未完成 / 待补（诚实清单）

- [ ] 浏览器实机跑一遍「一键完成」requires：可交互桌面会话；本轮到编译+单测级证据。
