# 变更总结 — 文档/注释漂移修正（9 处，零行为变更）

> 级别：**L0**（纯文档 + 代码注释，无行为变更）。按 `开发流程规范.md` §一「流程路径分级」，
> L0 走轻量路径：阶段 1-3 合并为本文，强制 Gate 为「红线零违反 + 变更追踪」。
> 来源：`docs/reference/yg-gateway-deep-read-agent-notes.md` §四 A 组（深读时发现的漂移）。

## 一、改了什么（7 个文件，9 处）

| # | 文件 | 改动 | 依据 |
|---|---|---|---|
| 1 | `AGENTS.md` | 「事实语义纪律（**三态**分离）」→「**四态**分离」 | 正文表格本就是四态（observation/derivation/inference/unknown） |
| 2 | `AGENTS.md` | 依赖方向「冻结基线」的手抄代码块 → **指向 `scripts/check-boundaries.ps1` 的 `allow` 表**（唯一真相，18 个源包条目） | 手抄副本已漂移：只有 5 个源包行（缺 `evidence`、`unknown`、`livecfg`、`auth` 等 13 个条目），且 `pool` 行漏 `evidence, unknown` |
| 3 | `internal/pool/entry.go` | `EwmaProvenance` 注释「AGENTS.md「三态语义纪律」」→「四态」 | 同 1 |
| 4 | `internal/pool/entry.go` | `modelCooldowns` 注释「运行态语义（**不持久化**）：重启清零」→「**持久化**（stateAccount.ModelCooldowns）：未过期条目跨重启恢复」 | `persist.go:204-212`（恢复）、`:341-349`（落盘）实测持久化；同文件 `:618` 亦说明"不持久化会导致 healthyForModel 重启失忆"，原注释自相矛盾 |
| 5 | `cmd/server/config.go` | `PreferExpiring` 注释「权重 **×3**」→「权重 **×2**（虚拟实例 slots=2）」 | 实现为 `pool/realm.go:12` 的 `const expiringVirtualSlots = 2`（issue #101 已对齐实现口径，注释未跟） |
| 6 | `internal/panel/autotask.go` | 包头注释「仍未破解：skill_1 / 不做：Expert_lighthouse」→「已补齐（skill_1 `runSkillFresh`、Expert_lighthouse `runExpertLighthouse`）；仍不做 Expert_Philanthropy」 | 同文件 `:122-130` 已实现并注明实测点亮（人杰2 / 两账号） |
| 7 | `cmd/server/main.go` | 注释「请求体已无网关侧上限（max_body_mb 移除）」→「上限见 `server.max_body_bytes`（默认 64MiB，显式 0 才关闭），超限读阶段被 `MaxBytesReader` 截断为 413」 | `main.go:384` 传入 `cfg.MaxBodyBytesEffective`；`handler.go:100-103`、`chatflow.go:74-83` 实现；`config.go:725-727` 默认 64MiB |
| 8 | `.harness/rules/编码规范.md` | ①口径描述「用 `Get-Content \| Measure-Object -Line`」→「**显式过滤空白行**，不用 `Measure-Object -Line`」；②**删除写死的 5 个文件行数**，只保留"两种口径下 >1000 的是同样 5 个文件"这一稳定结论；③复现命令改正 | 实现见 `check-depth.ps1:73-76`。**写死数字就是漂移的根因**：本批执行期间实测 `handler.go` 1387→1389、`autotask.go` 1225→1226（并行会话在改同一工作区），任何写死的行数都会在几分钟内失效 |
| 9 | `.harness/rules/开发流程规范.md` | 「治理/文档 → `verify-harness.cjs`（如已建）」→「`scripts/pre-commit.ps1 -Fast` + CI `gate` job」 | 该 `.cjs` 在仓库内不存在；实际治理校验入口是 pre-commit + CI gate |

**未改（有意）**：`.harness/changes/2026-10-16-dsh-default-input/summary.md` 里那张
"reqlog 21 字段"清单属于**历史留档**（`changes/` 记录的是当时的认识），且 `reqlog.go`
包注释已声明"不写提示词/响应正文/Authorization"，不另加易漂的字段计数。

**本批范围（重要）**：本批只动上表 7 个文件。执行期间工作区里另有 **7 个文件被并行会话改动**
（`.github/workflows/gate.yml`、`.harness/LESSONS.md`、
`.harness/changes/2026-10-16-deploy-prod/summary.md`、
`docs/prd/REQ-deploy-prod-script-2026-10-16.md`、`internal/server/handler.go`、
`scripts/deploy-prod.ps1`、`scripts/deploy-prod.rehearsal.ps1`）——它们**不属于本批**，
提交时按文件挑选请排除；本文的改动清单与回滚命令也只列本批 7 个文件。

## 二、怎么验证的

1. **权威数字来自脚本本身**，不是手算：`powershell -NoProfile -File scripts/check-depth.ps1 -Detail`
   （改动前基线：最大 2058 / >1000 文件 5 / >120 行函数 7 / 最低覆盖率 72.3%，`exit 0`）。
   > 过程记录（诚实）：同一文件我先用两条内联命令手算 `handler.go` 得到 **1382** 与 **1387** 两个值，
   > 无法自洽 → 改用守卫脚本输出取权威值 **1387**。手算结果不可复现者不写入文档。
2. **门禁全量**：`powershell -NoProfile -File scripts/pre-commit.ps1`（gofmt / R10 / build / vet / test / 边界 / 深度棘轮）。
3. **红线自检**：本批只改注释与文档，未触 R1–R15 任一条；未改任何报文、超时、阈值、路径。

## 三、怎么回滚

```powershell
git checkout -- AGENTS.md internal/pool/entry.go cmd/server/config.go `
  internal/panel/autotask.go cmd/server/main.go `
  .harness/rules/编码规范.md .harness/rules/开发流程规范.md
```

改动全部是文本替换，无二进制、无配置、无 state.json 影响，回滚零副作用（无需重启实例）。

## 四、B 组（同批处理，代码侧另立目录）

本目录只含 A 组 + B2/B3 的**文档侧**修正：

| 项 | 处理 | 落点 |
|---|---|---|
| B2 | `ADR-007` 里 `doccheck` 的收益改为**标注未落地**（不建工具，避免新增维护面） | `docs/adr/ADR-007-evidence-and-unknown.md` |
| B3 | `AGENTS.md` 增「实现口径」段：`/v1/models` 的未知以**字段省略**表达，消费方必须按三值处理，**禁止** `!field` 判否 | `AGENTS.md` |
| B1 | 面板能力三值化（代码） | `.harness/changes/2026-10-16-unknown-reopen-and-tristate/` |
| B4 | unknown 的 TTL 重开（代码） | 同上 |
| B5 | 本批已按「指向脚本」处理（回滚第 2 项可恢复手抄副本） | `AGENTS.md` |
| B6 | 决定**不做**机器化（理由见上目录 summary「B6」段） | 同上 |
