# 新会话开场白（2026-10-27 起用，ROADMAP v3 执行期）

> 直接把本文件全文贴给新会话即可。新会话第一件事：读本文件 + `AGENTS.md` +
> `docs/roadmap/ROADMAP-prd-v3-2026-10-25.md`，然后按下面"接下来做什么"开工。

## 本项目是什么

YG Gateway：自托管 OpenAI/Anthropic 兼容反向代理，把腾讯 CodeBuddy 账号池包装成
`/v1` 服务。三护城河 = 实测成本分层（四态）、健康治理（EWMA/熔断/half-open）、
决策溯源（pickReason）。红线与纪律见 `AGENTS.md`（顶层硬约束）。

## 当前状态（2026-10-26 晚已推送）

- **HEAD = 远端 main（ahead=0）**；本会话提交链：`4931e4b`(V3-7 canary) → `e704b3e` →
  `41caf25`(V3-8) → `5166235`(V3-7/8/OpenDesign) → `b1fac0c`(V3-9) → `f3385f4`(V3-10) →
  `01f59a5`(.gitignore) → `cbf1399`(V3-13 README) → `bde24a4`(V3-13 review) → `7a9ad3c`
  (routing 复审) → `27f75ee`(KICKOFF 初版) → `5d9e938`(子 agent 复核修订) → `763c7fb`
  (终稿精度) → `8ddbea4`(roadmap 勘误 verify/)。开工前 `git log` 实测，别照抄。
- **生产已换版**：2026-10-26 21:41 `deploy-prod.ps1 -Build -Feature v3-p0` exit 0、冒烟全过
  （/v1/models 21 个、非流式/流式各一发），旧版备份 `.pre-v3-p0-20261026.bak`（R15）。
- **门禁**：pre-commit 11/11 全绿；E2E 套件 4 测试 / 34 断言全绿（隔离实例，见下）。
- 棘轮：config.go 977 / main.go 1000 / app.js **4378** 冻结 / sse.go 1070（基线 maxFileLines=1089）
  / 上下文预算 15822 压线（动常驻层前先等价压缩）。

## 接下来做什么（按优先级）

### P0：V3-6 剩余 —— app.js 瘦身 ≥100 行 + 棘轮收紧（本会话主线）

E2E 冒烟（V3-6 ①）**已完成**：`e2e/` 脚手架（@playwright/test 最小化）+ 4 测试 34 断言
（密钥门 → 登录 → 八视图 → opsagent 卡片），本地 6.3s 全绿；CI 接线 `.github/workflows/e2e.yml`
（先调共享 gate.yml 再跑浏览器，不复制门禁）。顺带修真 bug：`opsagent.js` 请求缺
Authorization 头 → api_key 鉴权下卡片恒 401（已修，需重编译生效，R13）。

**接下来（V3-6 ②③）**：
1. 抽公共渲染函数瘦身 `app.js` ≥100 行（候选区已圈：accRenderSig/accountsSig/unkBadge/
   unkNoteText、逐包明细排序 renderPackages 块等；瘦身前先把 `internal/panel/frontend_test.go`
   里对应断言读了，别动被断言锁定的行为）。
2. 棘轮收紧：`check-depth.ps1 -Update` 后提交 `depth-baseline.json`（maxFrontendLines 必须下降）。
3. 每次改前端后重编译测试二进制再跑 E2E：`cd e2e && npm run build:server && npx playwright test`。

**E2E 套件用法**：测试实例 = `%TEMP%\yg-e2e`（端口 7864、api_key=`e2e-test-key`、零账号池
→ 不触上游；WB2A_* 环境变量全隔离，与生产 7863 无关）。改 `e2e/` 或 `internal/panel/**`
时 CI 的 e2e.yml 会跑。

### P1：48h 数据回填（2026-10-28 21:41 前后做，被动等窗口）

V3-3 已于 10-26 21:41 换版，回填窗口自此时起计。采集并回填：
1. count_tokens 估算精度（±30% 内？）
2. opsagent 建议（capacity/402-contradiction）真机质量
3. 软带命中率 + `bandMult` 20k token/请求假设
4. recent_usage_factor 的 200k scale / 1h 半衰期

数据来源：`/status`、reqlog archive、面板用量视图。回填目标：各 REQ 文件。

### P2：顺手小修（下次碰相关文件时做，不开专项）

1. `internal/pool/softband.go:20-21` 注释："带宽宽度=floor×0.2 → upper=floor×1.2"（现语义错位）。
2. `internal/pool/softband.go:68` 预筛 `+0.01` magic number 补浮点余量注释。
   （来源：routing-p1p2 独立复审 🟡，见 `.harness/changes/2026-10-25-routing-p1p2/review.md`。）

### P3：等用户那边的（不要代做）

- 管理员重装 Go ≥1.27.3（2026-10-26 复核：`C:\Program Files\Go` 仍是 **1.27.0**，靠
  `GOTOOLCHAIN=auto` 切 mod 缓存 1.27.3 在跑，能用但属临时态）；装好后撤 `YG_GO_EXE` 兜底。

## 环境须知（新会话必读）

- **Go**：系统 1.27.0 + `GOTOOLCHAIN=auto` 切 go1.27.3，`go.exe` 直接用；`YG_GO_EXE` 是用户级
  环境变量自动继承，无需手设。
- PowerShell 语法，不要用 Bash；node/npm 可用（npm 10.9.8，node stderr 有 UNDICI-EHPA 警告
  污染，取版本/输出时分离 stderr）。
- 上下文预算 15822/15822 压线：动 `.harness/rules/*` 或 AGENTS.md 前先等价压缩。
- Mimosa 钩子会误报 SSRF/拦 /tmp 直写——写文件走 Write/Edit 工具，别用 shell 重定向。
- 生成式 PRD/文档写 `docs/`，别动常驻层。

## 纪律提醒（AGENTS.md 摘要）

- 研究明白再动工（参数不拍脑袋）；测试与生产改动同提交（L-11）；提交走 `-F` 文件（L-1）；
  只提交本任务文件（L-21）。
- 否定断言必须穷尽证据源（L-15）：没覆盖 = unknown，不是"没有"。
- 四态纪律：observation/derivation/inference/unknown 分开，推断不能消解推断。
- 门禁：`powershell -NoProfile -File scripts/pre-commit.ps1`（全量）或 `-Fast`（跳 go test）。
- R8：不改 wb2api/chatcmpl/三段 UA 指纹等报文字符串；R6：出站请求集中 internal/upstream；
  R10：错误不静默吞。

## 本会话已完成（不要重复做）

- ✅ V3-10 agent 流量画像（f3385f4）、V3-13 README（cbf1399+bde24a4）、routing 复审（7a9ad3c）
- ✅ KICKOFF 初版 + 子 agent 审查 + 终稿（27f75ee/5d9e938/763c7fb）
- ✅ roadmap 三文档 verify/ 勘误（8ddbea4）+ GitHub 推送 36 提交（ahead=0）
- ✅ V3-3 生产换版（deploy-prod exit 0，冒烟全过，备份在 D:\yg-gateway）
- ✅ V3-6 ①：E2E 冒烟套件 + CI 接线 + opsagent 鉴权 bug 修复（e2e/ 与 .github/workflows/e2e.yml）
