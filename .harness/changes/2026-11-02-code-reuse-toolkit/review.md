# 评审记录 — 2026-11-02-code-reuse-toolkit

> 按 `.harness/rules/开发流程规范.md` §四：L2 及以上必须有一次独立复审，且复审者只读；
> 多轮评审**按轮次分段**，不用新一轮结论覆盖上一轮。

---

## 轮次 1：自评审（作者，2026-11-02）

范围：`internal/pkg/{timex,jsonsafe}`、10 个包的迁移点、`check-boundaries.ps1` 白名单、
`AGENTS.md` 数字、`.harness/prompts/`、以及两处为解棘轮回退的机械拆分。

自评结论：门禁 11/11 通过。自评**不作为通过凭据**（L-16），故发起独立复审。

---

## 轮次 2：独立复审（子代理，只读，2026-11-02）

复审结论原文：**整树驳回；L2 本体有条件通过**。计数 🔴1 / 🟡4 / 🟢4 / 🔵3。

复审做了三件我自评审没做的事，值得记下来：①在 `%TEMP%` 副本里对 `timex`/`jsonsafe` 做了
**3 个变异**并全部变红（证明测试有牙齿）；②用 `git diff -U0 | grep 'wb2api|chatcmpl-wb2api|WorkBuddy/'`
证明 R8 零命中；③扫整棵树（而不只是我的改动）找红线，于是抓到了不属于本任务的凭证泄露。

### 逐条处置（12 条，全部有落点）

| 编号 | 问题 | 处置 | 落点 |
|---|---|---|---|
| 🔴-1 | `transport.go:111/128/136` 打印含 `user:pass@` 的完整 proxyURL（R1/R2） | **已修** | 新增 `redactProxyURL` + 三处改用它 + `TestRedactProxyURL` |
| 🟡-1 | `!isKnownModel(...)` 把「目录为空=unknown」当「模型不存在」，冷启动时把真实模型直接 400 | **已修** | 新增 `modelCatalogVerdict` 三值 + 改调用点 + `TestModelCatalogVerdictTriState` |
| 🟡-2 | 代理配错时 fail-open 直连（该 feature 的唯一目的就是换出口 IP） | **未修（升级为待决策）** | 改 fail-closed 会改可用性语义，属 ban-risk feature 的取舍，需 owner 拍板；建议至少让 `/status` 显示「代理未生效」 |
| 🟡-3 | `gofmt -l` 输出 `cmd\server\config.go`，且 summary 声称"无输出"不可复现 | **驳回（观测过期）** | 复审在我 `gofmt -w` 之前读的树。现树 `gofmt -l ./cmd ./internal` 无输出、`pre-commit` 检查 1 通过（45 个目标） |
| 🟡-4 | 工作区混装两个 feature，无法独立 revert / `deploy-prod -Build` 会拒绝 | **部分采纳** | 已在 summary 增「工作区边界」段，明确本记录只覆盖 code-reuse + 两处机械拆分；不代为拆分他人未提交改动（L-21） |
| 🟢-1 | `rotateBackoff` 外层重复 `d<=0` 守卫（把刚消掉的复制写回来） | **已修** | 删守卫，直接 `timex.SleepCtx(ctx, backoffAfter(i))` |
| 🟢-2 | `chatClientFor` 重复 `chatHTTP()`，后者退化为测试专用 | **未修** | 属 ban-risk feature 代码，不在本任务范围 |
| 🟢-3 | `archiveDay` 用 `len(timex.DateLayout)` 当文件名宽度（布局非定宽时会静默切错） | **已修** | 改为 `const archiveDateLen = 10` + 注释说明是文件名契约 |
| 🟢-4 | 每次 11102 复制两次模型快照 | **未修** | 属 ban-risk feature；量级小（模型目录） |
| 🔵-1 | `sessionID==""` 时每请求生成新 ID（与"像机器人"的目标相反） | **未修（建议）** | 属 ban-risk feature；建议 `sync.Once` 惰性固化 |
| 🔵-2 | 局部变量 `auth` 遮蔽 `internal/auth` 包名 | **已修** | 改名 `socksAuth` |
| 🔵-3 | 测试文件仍硬编码日期布局字面量（与"只留一处"口径不一致） | **采纳为口径声明** | PRD 补一句「测试故意保留字面量，独立于实现」 |

### 残余风险（明说，不掩盖）

- 🟡-1 的**调用分支**仍无端到端测试：三值契约有测试（有牙齿），但
  「目录为空 + 11102 → 仍换号」这条路径没有用例覆盖（构造 `chatRun` + `Handler` 的成本高）。
  下一轮补一条表驱动用例即可闭合。
- 🟡-2 / 🟢-2 / 🟢-4 / 🔵-1 四条都落在 ban-risk feature 的代码里，本记录只登记不代改。

### 复审自身的一处误判（保留，作为"复审也要验"的样本）

复审报的 🟡-3 是基于过期快照的结论。我按 L-22 复核后驳回：`gofmt -l` 现在无输出，
`pre-commit.ps1` 检查 1（45 个目标）也通过。**复审结论同样是二手证据，必须回到当前树复核。**
