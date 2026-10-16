# 变更总结 — T7：unknown 不得冒充 0（写台账/统计的准入）

> 承接 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 的 T7（用户决策 **D3 = 改**）。
> 需求与验收：`docs/prd/REQ-usage-unknown-admission-2026-10-16.md`。
> 本变更**不含 P3**（`anthropic.go` 的 `"stream":"true"` 类型错）与 **P4**（`cmd/login` 4xx 分档），
> 理由写在 PRD §七——它们不是计量口径，混进来会让验收标准变形。

## 一、改了什么

核心问题：**解析失败被迫用零值兜底时，`0` 会被当成观察值写进台账/统计**。
后果不报错：一个付费号可能被判成「免费号」被优先选号；缓存命中率被抬到 100%，
反而压掉了「缓存没生效」的告警。**未知与 0 必须在每一层可区分。**

| # | 文件 | 改动 |
|---|---|---|
| 1 | `internal/server/handler.go` `usageCreditTotal` | 三态化：`ok`（观察）/ `creditUnknown`（**有分母但 credit 不可解析 → 未知**）。旧实现 `c, _ := usage["credit"].(float64)` 的零值兜底被去掉 |
| 2 | `internal/server/chatrun.go` | 非流式：`creditUnknown` 时打 WARN（含脱敏 `logfmt.Label`）且**不调** `NoteModelCost`；miss 改用 `okM` 判定。流式：改用 `CacheTokens` 的四态返回，miss 未知时不写 0 |
| 3 | `internal/server/logging.go` | `CacheTokens()` 增第 3 个返回值 `missKnown`；`chatStats` 增 `cacheMissUnknown`；写 reqlog 时带上该标记 |
| 4 | `internal/reqlog/reqlog.go` | `Event` 增 `CacheMissUnknown bool \`json:"cache_miss_unknown,omitempty"\``（纯增量；观察到的 0 与历史行形态不变） |
| 5 | `internal/server/usage_unknown_test.go`（新） | 3 条测试 22 个子用例 |
| 6 | `internal/server/cache_stats_test.go` | 适配 `CacheTokens` 新签名（3 值 → 4 值） |

**未动 `internal/pool` 的 API**——设计偏差与理由见 PRD §三.0：`float64` 无法携带 provenance，
「未消解」本身就是 pool 侧已有的未知准入（不调 `NoteModelCost` 即不消解）。

## 二、怎么验证

### 1. 单元测试（新增，22 个子用例）

```
--- PASS: TestUsageCreditTotalUnknownIsNotZero       （8 例：字符串/null/缺失 → 未知；真 0 → 观察）
--- PASS: TestCacheTokensMissUnknownIsNotZero         （6 例：显式 miss / 推导 / 未知 / 自相矛盾）
--- PASS: TestReqlogCacheMissUnknownIsAdditive        （2 例：未知打标记且不打数字；观察 0 无标记）
--- PASS: TestStatsReaderCacheTokens                  （既有测试适配新签名后仍绿）
```

### 2. 反向变异（副本内，原件只读；先断言 `go env GOMOD` 指向副本，改前改后 SHA256 校验）

| # | 变异（把实现还原成旧行为） | 结果 | 证据 |
|---|---|---|---|
| M1 | `handler.go`：`if !okC {` → `if false && !okC {`（= 丢弃 ok，零值兜底回归） | **CAUGHT** | `usage_unknown_test.go:87: ok=true unknown=false，want ok=false unknown=true`；两条子用例红 |
| M2 | `logging.go`：`return hit, 0, false, true` → `return hit, 0, true, true`（= miss 未知当 0） | **CAUGHT** | `usage_unknown_test.go:166: ok=true missKnown=true，want ok=true missKnown=false`；两条子用例红 |

两条变异还原后哈希一致，副本已删除，原件工作区未被污染。

### 3. 门禁

`go build ./...` / `go vet ./...` / `gofmt -l` 无输出；`go test ./... -count=1` 全绿；
`check-boundaries.ps1`、`check-depth.ps1`（覆盖率未回退、未 `-Update`）、`pre-commit.ps1` 全量 —— 见提交 body 的实测记录。

## 三、防过度纠正（本变更最容易做错的地方）

**真免费模型（上游明确回 `credit=0`）必须仍按观察记账**。这不是附注，是硬验收
（PRD A2）：如果为了挡未知而把所有 `credit=0` 都判成 unknown，就会丢掉「这个模型免费」这个
**真实且有用**的观测，`credit_floor` 的免费放行逻辑会失去依据。
所以判据只挂在**类型断言的 ok**（能否解析）上，不挂在数值上——`TestUsageCreditTotalUnknownIsNotZero`
的第 4 例（真 0 → 观察）就是这条的钉子。

## 四、怎么回滚

`git revert <sha>`。三处判据改动彼此独立：
- 只回滚 `usageCreditTotal` → 恢复到旧口径（未知冒充 0）；
- `reqlog.CacheMissUnknown` 是纯增量字段，旧消费者不受影响，可单独保留。

## 五、遗留（已知、未做）

1. **P3 / P4 未实施**（PRD §七给了理由）：`anthropic.go` 的 `"stream":"true"` 类型错（走错分支）与
   `cmd/login` 的 4xx 分档（CLI 文案）。
2. **`unknowns_open` 可能长期非零**：若上游普遍不回 `credit`，该指标会持续 >0。按 ADR-007 这是
   **信号**（要么池子在扩，要么 `requires` 设计有问题），不是故障——需要在观测时这样解读。
3. **面板侧未验**：unknown 的展示由并行会话在 `internal/panel/*` 推进，本变更只保证 unknown
   进入既有登记链路；**未在面板上做过端到端观感验证**（避免与并行会话冲突）。
4. **`cacheMissWarn` 的行为变化（刻意）**：miss 未知时该次不再喂入命中率统计，因此这类请求
   既不会抬高命中率、也不会触发低命中率告警——即「因为不知道，所以不下结论」。
