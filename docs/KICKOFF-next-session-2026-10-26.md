# 新会话开场白（2026-10-26 晚，ROADMAP v3 执行期第 N 天）

> 直接把本文件全文贴给新会话即可。新会话第一件事：读本文件 + `AGENTS.md` +
> `docs/roadmap/ROADMAP-prd-v3-2026-10-25.md`，然后按下面"接下来做什么"开工。

## 本项目是什么

YG Gateway：自托管 OpenAI/Anthropic 兼容反向代理，把腾讯 CodeBuddy 账号池包装成
`/v1` 服务。三护城河 = 实测成本分层（四态）、健康治理（EWMA/熔断/half-open）、
决策溯源（pickReason）。红线与纪律见 `AGENTS.md`（顶层硬约束）。

## 当前提交链（本地 main，未推送）

本会话（2026-10-26）新增 6 个提交，全部过 pre-commit 11/11（`27f75ee` 为本文件初版；
本次复核修订另成 1 个提交——git log 最新一条即最终版）：

| commit | 内容 |
|---|---|
| `f3385f4` | V3-10 agent 流量画像：reqlog.Event 加 `session_fp`（盐哈希）+ `agent_kind`（UA 闭词表）；`/status` 出 `agent_profile` 聚合块 |
| `01f59a5` | .gitignore 补 Windows .exe 构建产物规则 |
| `cbf1399` | V3-13 README 三护城河 + Magpie/CC Switch 章节 |
| `bde24a4` | V3-13 review 修订：按 L-15 删未深读的同类项目名，Magpie 定位纠偏 |
| `7a9ad3c` | routing-p1p2 独立复审 review.md（🟢 通过）+ PLAN-next 进度收口 |
| `27f75ee` | docs: 新会话开工指南 KICKOFF-next-session-2026-10-26.md（初版） |

加上之前已有的：`b1fac0c`(V3-9 harnessrun) → `5166235`(V3-7/V3-8/OpenDesign 吸收) →
`41caf25`(V3-8) → `e704b3e`(PLAN-next 交接) → `4931e4b3`(V3-7 canary)。

**门禁状态**：11/11 全绿，21 包测试全过（2 包无测试文件）。棘轮：config.go 977 / main.go 1000 贴线 /
app.js 4378 冻结 / sse.go 1070（基线 maxFileLines=1089）也在高位 / 上下文预算 15822 压线。

## 接下来做什么（按优先级）

### P0：V3-6 E2E 冒烟测试（M-L，本会话主线任务）

**为什么现在做**：app.js 4378 行冻结锁死全部前端演进；10-17 必要性评审裁定
"无浏览器级 E2E 不拆"——这是解锁 A3/A5 前端演进的前置。

**内容**：
1. 用 Playwright（本机已装 playwright-browser-automation 插件）写冒烟套件：
   - 登录面板（输入 api_key → 进入八视图）
   - 切八个视图每个都能加载、无 JS 报错
   - opsagent 卡片加载（有数据时）
   - 至少 10 条用户可见行为断言
2. 绿了之后抽公共渲染函数瘦身 app.js ≥100 行，棘轮收紧。
3. 参考：仓库内无 Playwright 脚手架（observation，2026-10-26 穷尽复核：全仓 grep 仅命中
   7 份 docs；无 package.json / playwright.config；verify/ 目录不存在且 git 历史从未入库——
   commit message 里的"Playwright 验证"是外部插件手动验证），需从零搭脚手架（参考官方
   `@playwright/test` 起步模板）。注意：ROADMAP v3 第 99/169 行等处"本机 verify/ 有先例"
   已过时，以本实测为准。CI ubuntu 可跑。

**Given-When-Then**：When E2E 套件在本地与 CI 跑，Then 全绿且断言 ≥10 条；
When 瘦身合并，Then maxFrontendLines 下降且 E2E 仍绿。

**前置功课**：
- 先读 `.harness/changes/panel-spec/`、`.harness/changes/panel-key-management/` 了解面板结构；
  unknown 三值化可视化见 `.harness/changes/2026-10-18-b1b-panel-unknown-visual/`
  与 `docs/roadmap/ROADMAP-prd-v3-2026-10-25.md` 的 unknown 章节。
- 读 `internal/panel/frontend_test.go` 看现有 node 断言怎么写的（不替代 E2E，
  只是风格参考）。
- E2E 脚本放哪：建议 `e2e/` 新目录，package.json 最小化（只装 @playwright/test）。

**量级**：M-L。预计 2-4 小时。

### P1：等 V3-3 生产换版后的 48h 数据回填（被动，不要主动做）

V3-3 由用户在 `D:\yg-gateway` 跑：
```powershell
powershell -NoProfile -File scripts/deploy-prod.ps1 -Build -Feature v3-p0
```
之后 48h 要采集并回填校准的三件事：
1. count_tokens 估算精度（±30% 内？）
2. opsagent 建议（capacity/402-contradiction）真机质量
3. 软带命中率 + `bandMult` 的 20k token/请求假设是否要调
4. recent_usage_factor 的 200k scale / 1h 半衰期是否要按实测调整

数据来源：`/status`、reqlog archive、面板用量视图。回填目标：各 REQ 文件。

### P2：顺手小修（下次碰相关文件时做，不开专项）

1. `internal/pool/softband.go:20-21` 注释：`softBandMinMult=1.2` 的注释写
   "floor×0.2" 语义错位，改成"带宽宽度=floor×0.2 → upper=floor×1.2"。
2. `internal/pool/softband.go:68` 预筛 `+0.01` magic number 补一句注释
   （浮点边界余量）。
   （这两条是 routing-p1p2 独立复审 🟡 发现，见
   `.harness/changes/2026-10-25-routing-p1p2/review.md`。）

### P3：等用户那边的（不要代做）

- GitHub 推送（等工单清 unreachable objects；本地 main 领先远端 **34** 个提交，复核于
  `27f75ee`——若本文件之上又新增了提交，开工前先 `git rev-list --count origin/main..HEAD`
  实测，别照抄这个数）
- 管理员重装 Go ≥1.27.3（2026-10-26 复核：`C:\Program Files\Go` 仍是 **1.27.0**，靠
  `GOTOOLCHAIN=auto` 切 mod 缓存里的 1.27.3 在跑，能用但属临时态）；装好后撤
  `YG_GO_EXE` 兜底
- V3-3 生产换版（用户在 D:\yg-gateway 执行）

## 环境须知（新会话必读）

- **Go 环境（2026-10-26 复核）**：系统 Go 仍是 1.27.0，但 `GOTOOLCHAIN=auto` 会自动切到
  mod 缓存里的 go1.27.3 toolchain，`go.exe` 可直接用；`YG_GO_EXE` 是**用户级**环境变量，
  新会话自动继承（脚本兜底仍在），**无需**每次手动设。管理员重装 Go ≥1.27.3 后此兜底可撤。
- PowerShell 语法，不要用 Bash。
- 上下文预算 15822/15822 压线：动 `.harness/rules/*` 或 AGENTS.md 前先等价压缩。
- 子 agent 配额不稳：主会话存活时后台 agent 大概率 `model concurrency limit exceeded`
  ——串行、2 败即弃、转主线自查。
- Mimosa 钩子会误报 SSRF/拦 /tmp 直写——写文件走 Write/Edit 工具，别用 shell 重定向。
- 生成式 PRD/文档写 `docs/`，别动常驻层。

## 纪律提醒（AGENTS.md 摘要）

- 研究明白再动工（参数不拍脑袋）；测试与生产改动同提交（L-11）；提交走 `-F` 文件（L-1）；
  只提交本任务文件（L-21）。
- 否定断言必须穷尽证据源（L-15）：没覆盖 = unknown，不是"没有"。
- 四态纪律：observation/derivation/inference/unknown 分开，推断不能消解推断。
- 门禁：`powershell -NoProfile -File scripts/pre-commit.ps1`（全量）或 `-Fast`（跳 go test）。
- R8：不改 wb2api/chatcmpl/三段 UA 指纹等报文字符串。
- R6：出站请求集中 internal/upstream。
- R10：错误不静默吞（禁 `_ =` 吞业务错误）。

## 本会话已完成（不要重复做）

- ✅ V3-10 agent 流量画像（代码 + 测试 + REQ + summary，commit f3385f4）
- ✅ V3-13 README 三护城河 + Magpie/CC Switch（commit cbf1399 + bde24a4 review 修订）
- ✅ routing-p1p2 独立复审 review.md（commit 7a9ad3c）
- ✅ .gitignore 补 Windows .exe（commit 01f59a5）
- ✅ PLAN-next-2026-10-25.md 进度更新
- ✅ 本文件 KICKOFF-next-session-2026-10-26.md（初版 commit 27f75ee；复核修订随最新提交，
  经子 agent 审查）
