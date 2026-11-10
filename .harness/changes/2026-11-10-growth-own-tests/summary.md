# 2026-11-10-growth-own-tests 变更摘要

需求：`docs/prd/REQ-2026-11-10-growth-own-tests.md`
分级：L0（纯治理 / 只新增测试，生产代码零改动）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/growth/client_test.go` | growth 包首个本包测试：`Transport` 测试替身 + 10 个用例，覆盖成功解析、传输错误上抛、坏 JSON 报错、幂等码、夜猫子等待注入 |
| `新增：.harness/changes/2026-11-10-growth-own-tests/**` | 本次变更的 REQ 与摘要 |

**没做的（明写，免得下轮重提）**：
- 不改生产代码：路径常量、错误文案、`nightChatGap=4s`、`claimCredit` 的 unknown 口径全部原样。
- 不搬 upstream 的 R8 金样（报文字节已在 `internal/upstream/r8_golden_test.go` 锁死，重复断言无收益）。
- 不新增门禁度量：只要该包有测试文件，`check-depth.ps1` 的 `minCoverage` 自动把它纳入。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 本包测试与覆盖 | `go test ./internal/growth/ -cover -count=1` | `ok 0.057s coverage: 93.4%` |
| 静态检查 | `go vet ./internal/growth/` | 退出码 0，无输出 |
| 格式 | `gofmt -l internal\growth` | 无输出 |
| 全量门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 见下（退出码 0，11/11 通过） |
| 覆盖率棘轮 | 同上检查 7 | `minCoverage` 74 未回退（growth 93.4% ≥ 74） |

**负向探针**（在 `%TEMP%\ygg-mut-20261110` 副本里做，已删除；生产树未变异）：
1. 把 `blackcat.go` 的 `if i < need-1` 改成 `if i <= need-1`（末次也多等一次）→
   `TestGrowthRunNightChats` 报红：`等待次数=2 时长=[4s 4s]，want 1 次 4s`。
2. 把 `claimCredit` 的 `if resp.Credit == nil` 改成 `if false`（缺字段不报错）→
   `TestGrowthClaimGiftAndCompensation` 报红（`{}` 走到 `*resp.Credit` 空指针 panic）。
两处均预期报红、实际报红，探针副本已删。
**「已检查 / 已验证」均为真跑记录**，未用声明替代实测。

## 三、未完成 / 待补（诚实清单）

- [ ] upstream 侧 `growth_stub` 的转发断言是否与新本包用例重复，未逐条比对（requires：一次
      `go test -coverpkg` 对照清单，成本约 10 分钟，非阻塞）。
- [ ] `internal/session` 条目上限（2026-10-22 审查 S-3 遗留）、`internal/reqlog` 无 `recover`
      （C-4 遗留）仍未处置；本轮范围不含（requires：用户批准后单独开批次）。

## 四、怎么回滚

纯新增文件：`git rm internal/growth/client_test.go`，或 `git revert <本次提交>`。
不涉及二进制、配置、凭证，回滚零副作用。

## 五、Learning

根因：拆包把生产代码搬走了、测试留在原包用转发存根继续跑——**测试绿了，但覆盖率的归属跟着代码走了**，
而 0% 包被门禁有意豁免，于是谁也没发现这个包从棘轮里消失了。
