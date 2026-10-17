# 变更总结 — 未知的 TTL 重开 + 面板能力三值（B1 / B4 / B6 决策）

> 级别 **L2**（行为变更：unknown 生命周期 + 面板筛选语义）。
> 承接 `.harness/changes/2026-10-16-doc-drift-fix/summary.md` §四（B 组待决策清单）。
> 来源：`docs/reference/yg-gateway-deep-read-agent-notes.md` §四 B 组。

## 一、改了什么

### B4：成本未知的 TTL 重开（`internal/unknown` + `internal/pool`）

**问题**：消解是单向闩锁（`unknownSeen` 不删、registry 条目 Resolved 后不回退），而成本观测带
`modelCostTTL=6h`。观测过期后选号侧已按「未知」处理（`credit_floor` 不再信本地实测），
但 `/metrics` 的 `unknowns_open` 仍读 0 —— **指标从「还有多少没摸清」退化成「永远没问题」**。

| 文件 | 改动 |
|---|---|
| `internal/unknown/registry.go` | 新增 `Reopen(id, rev, reason)`：与 `Resolve` 对称的 CAS 契约；`reason` 必填（`ErrReasonRequired`）；未消解幂等；重开时 `Revision++` 且**保留原消解证据**（审计能看到"曾被什么消解、又因什么变回未知"） |
| `internal/pool/entry.go` | `pruneExpiredModelCosts` 返回本次真正删掉的模型名（空 map / 节流时返回 `nil`，零分配） |
| `internal/pool/persist.go` | `pruneAllLocked` 对每个过期组合调 `reopenCostUnknown`；锁序 `p.mu(写) → registry.mu`（registry 零依赖，不会回拿 p.mu） |
| `internal/pool/unknown.go` | 新增 `reopenCostUnknown`：stale 时重读重试上限 2 次，与既有 `resolveCostUnknown` 同风格 |

**触发点刻意在落盘路径而不是选号热路径**：ADR-007 取舍二禁止在 pick 临界区碰别的锁；
落盘点频率低、条目有界、开销纳秒级。

### B1：面板能力字段按三值处理（`internal/panel/app.js`）

**问题**：`mdMatch` 用 `!m.supports_images` 判否，把「字段缺失 = 未知」塌缩成「不支持」——
按视觉筛选时实际支持图片的模型会消失，**且不报错**。`mdRowHtml` 同问题（打印「不支持思考」）。

**改法**：只排除**显式 `false`**（`=== false`）；档位缺失显示「档位未知」。

### B6：技能链校验 —— 决定**不做**（记录理由，避免以后重复讨论）

`.harness/skills/` 11 个技能的「下一步 → `x`」引用目前无机器校验。评估后不机器化：
- 要动 `pre-commit.ps1` 的 8 处编号标签 + 2 份规范文档，成本高于收益（技能变动频率低）；
- 技能名写错会在"下次用这个技能"时立刻暴露，且「下一步」是**指路**而非执行契约。

→ 作为**已接受的边界**记录在此，不新增长期维护面。

## 二、怎么验证

### 1. 新增单元测试 6 个（每个都在注释里写明「牙齿」是哪一行）
`TestReopenNeedsReason`、`TestReopenStaleRevision`、`TestReopenBringsBackOpen`、
`TestReopenOpenItemIdempotent`、`TestExpiredCostObservationReopensUnknown`、
`TestAppJSModelFilterUnknownCapability`

### 2. 反向变异：3/3 全部按预期变红

| 变异 | 结果 | 报错点 |
|---|---|---|
| M1 去掉 `pruneAllLocked` 里的 reopen 调用 | **FAIL ✓** | `unknown_integration_test.go:141`「观测过期后该组合应重新变未知，got 0 条」 |
| M2 `=== false` 改回 `!m.supports_images` | **FAIL ✓** | `frontend_test.go:451` vision 结果少了 `cn:unknown` |
| M3 `Reopen` 不清 `Resolved` | **FAIL ✓** | `registry_test.go:193/199` |

变异在 `%TEMP%` 副本内执行：先断言 `go env GOMOD` 指向副本（LESSONS L-2），
每条「改 → 测 → 还原 → SHA256 复核一致」，副本随后删除。

### 3. 门禁
`scripts/pre-commit.ps1` 全量：gofmt / R10 / build / vet / `go test ./...`（20 包 ok）/ 边界 / 深度棘轮，**全部通过 `exit 0`**。

## 三、怎么回滚

```powershell
git checkout -- internal/unknown/registry.go internal/unknown/registry_test.go `
  internal/pool/entry.go internal/pool/persist.go internal/pool/unknown.go `
  internal/pool/unknown_integration_test.go internal/panel/app.js internal/panel/frontend_test.go
```

纯代码回滚，无状态迁移：`Reopen` 是新增 API（无持久化影响），`unknownSeen` 仍为进程内状态。

## 四、已知边界

1. **面板改动未重新构建生产二进制**：`app.js` 走 `go:embed`（R13），要生效必须在下次部署时
   重新 `go build` 出 exe。本批**没有碰** `D:\yg-gateway`，生产实例仍是旧 embed。
2. `NoteModelCost` 的 `LastSeen` 用 `time.Now()` 而非 `p.now()`——该路径**不可冻结时钟**，
   相关测试按真实时钟推进。属既有可注入性缺口（非本批引入），未改以免波及其它测试。
3. `Reopen` 目前只有「成本 TTL 过期」一个使用点；将来若有别的 TTL 类未知，应复用同一入口，
   不要另造一套"重开"逻辑。
