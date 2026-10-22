# 2026-10-22-split-autotask 变更摘要

需求：`docs/prd/REQ-split-autotask-2026-10-22.md`
分级：L1（纯同包重构）
ADR：不需要（纯文件拆分，无难逆转决策、无真实权衡）

---

## 一、改了什么

| 文件 | 非空行 | 作用 |
|---|---|---|
| `autotask.go`（重写） | 1244 → **492** | 只留：包文档、imports、autoAction 类型 + autoActions 注册表、autoActionFor/autoActionIndex、reportGap、两个 HTTP 入口（accountTaskAuto/accountTaskAutoAll）、taskProgressText/truncateStr、runAutoAll/autoAllClaimOnly |
| `autotask_mp.go`（新增） | 411 | mp 簇：mpTaskCodes/isMPTaskCode、taskByCode 系（含 taskByCodeWaiting/taskByCodeMP）、acceptWithVerifyMP/acceptStatusOr、claimPoll*/mpActionGap/mpChatEventGap、runMPMiniChatTask、runSchoolSeason/runSequentialChat*/runSequentialEventTask/runSequentialAutomation/runSequentialModelChat/runSequentialPlaybook/runMiniExpert |
| `autotask_scripts.go`（新增） | 364 | PC 域剧本：runChat5/runFirstBuddy/runModelChat/runRichMeow/runBuddyApp/runAutomationCreate/runLibraryRead/runBlackCat/runSkillFresh/runExpertLighthouse/runAppearance/runTemplateUse/runPlaybookPrompt/runCreateCanvas/runExpertUse/runExpertTeamUse/runExpertBatch + expertSummonGap |

**没做的（明写，免得下轮重提）**：
- 剧本逻辑/R8 报文字符串（wb2api-*、UA 指纹）一字未改。
- 表驱动注册（autoActions）天然已存在，未改造；剧本名/路由/wire 零变化。
- check-boundaries 白名单不变（同包拆分，无新跨包 import）。
- 未动 panel 其他文件、server/upstream。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| gofmt | `gofmt -l <3 files>` | 无输出（干净） |
| build / vet | `go build/vet ./internal/panel` | 全绿 |
| 面板测试 | `go test ./internal/panel` | 与基线同口径全绿（见提交记录） |
| 全量测试 | `go test ./...` | 20 ok + 3 no test files 基线口径 |
| check-depth | autotask.go 真实变小后 `-Update` 收紧 | 连同 depth-baseline.json 提交 |

**「已检查 / 已验证」必须真跑过**：上述命令实跑，输出见提交记录。
**中途修正 1 处转写错误**：重写时把 `writeJSON(...)` 误写成 `writeErr(...)`（before.Claimed 分支），
`go build` 立即捕获，已按 `git show HEAD:autotask.go` 原文改回。

## 三、未完成 / 待补（诚实清单）

- [x] 浏览器实机跑「一键完成」（2026-10-22 已补，见下方「实机冒烟补充」；链路全通，真实剧本执行分支受"4 账号任务均已领取"环境限制未能现场点亮）

## 实机冒烟补充（2026-10-22，生产 b399125 / exe 54778D6D）

链路：面板「一键完成」按钮 → `POST /panel/api/accounts/{uid}/tasks/auto`（Bearer 管理员鉴权）→ accountTaskAuto → autoActionFor → 账号锁 → taskByCode 前置读取 → 分支返回。

| 用例 | 请求 | 结果 |
|---|---|---|
| 幂等跳过分支 | `create_canvas`（已注册、已领取） | 200 `{"ok":true,"skipped":true,"message":"该任务已领取过奖励"}` |
| 未注册码分支 | `wb_wechat_oa_subscribe_task`（未注册） | 501 `该任务需要客户端内交互（无对应接口）` |

结论：拆分后入口编排 + 注册表 + 账号锁 + 前置读取 + 幂等/501 两分支在生产实机全部按预期返回，服务日志无 panic/错误。**已知局限**：4 个生产账号当日任务均已领取，无「已注册且未完成」任务可现场点亮真实剧本执行（act.run → 上游调用 → 进度回读）分支；该分支证据仍为单测级（coverage_autotask_test.go）。待任一账号出现未完成注册任务时补跑一次即可。

## 四、怎么回滚

纯同包文件拆分：`git checkout HEAD~ -- internal/panel/autotask.go internal/panel/autotask_mp.go internal/panel/autotask_scripts.go` 并删除两个新文件即可。无凭证/数据/wire 改动；`auths/` 不参与回滚。

## 五、Learning

根因：超大文件（1300 行）是「多业务域挤一文件」的累积债，拆分本身零风险但必须以编译+同基线测试证明零行为变化。
提级建议：PRD v2 债务清单「超大文件拆分」落地时，优先确认表驱动注册是否已存在（本例已存在，避免重复造注册表），拆分边界按「查询簇 / 剧本簇 / 编排入口」三段切，而非按函数大小切。
