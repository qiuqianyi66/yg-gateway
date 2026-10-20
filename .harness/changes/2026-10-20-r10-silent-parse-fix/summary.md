# r10-silent-parse-fix 变更记录

> 需求来源：`docs/code-review/2026-10-20-four-dimensions.md` §一 [P3] R10 个别静默解析（真问题）。
> 本次只修 `blackcat.go` 的 `ClaimGift`/`ClaimCompensation` 两处——它们**污染的是会被写进日志的事实**，
> 风险高于同为 P3 的 `global_models.go`（后者只影响列表元数据）。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | r10-silent-parse-fix（领取类响应积分的四态语义纠偏） |
| 类型 | fix + test |
| 涉及范围 | `internal/upstream/blackcat.go`、`internal/upstream/coverage_sequences_test.go` |
| 契约变化 | **有**（语义收窄，见下） |

## 二、问题

原实现：

```go
var resp struct{ Credit int64 `json:"credit"` }
_ = json.Unmarshal(data, &resp)
return resp.Credit, nil
```

`_` 吞掉解析错误后，无论「解析失败」「字段缺失」还是「真的领到 0」，都返回 `(0, nil)`。
调用方 `scheduler/streak.go:44-49` 只要 `err == nil` 就打日志 `🎊 新手礼包 +%dc`——
于是日志会写下**凭空捏造的事实**（"观测到领了 0 信用"），而真实状态是 unknown。

这正命中本项目 core discipline：「缺失 ≠ 否定」「Missing coverage is unknown, not absence」，
以及 AGENTS.md 的警告——*把 unknown 当成观察到的事实，比功能缺失更危险，因为它不报错*。

## 三、方案

新增 `claimCredit(data []byte, op string) (int64, error)`，两种函数共用：

| 上游响应 | 返回 | 语义 |
|---|---|---|
| `{"credit":500}` | `(500, nil)` | observation：观测到 +500 |
| `{"credit":0}` | `(0, nil)` | observation：确实观测到 0 |
| `{"unrelated":1}`（缺字段） | `(0, err)` | **unknown**：缺字段 = 未知，不是 0 |
| `data` 非对象/非法 JSON | `(0, err)` | **unknown**：解析失败，不是 0 |

`Credit` 用 `*int64` 正是为了区分「字段缺失」与「字段存在且为 0」——这是四态语义的落地点。
错误信息沿用本文件惯例带截断原始报文（`%.120s`，对齐 `RunNightChats`），便于排障。

**行为权衡**：调用方对业务错误本就是 `err == nil` 才记录（"无则业务错误静默跳过"），
所以 unknown 现在**不再产生那条虚假的 +0c 日志**，与既有口径一致，无副作用。

## 四、测试与原测试的刻意改写

`coverage_sequences_test.go` 原有一条断言**钉死了旧行为**：

```go
// data 非对象：credit=0 且无错误（兜底语义）
if got, err := c2.ClaimGift(a); err != nil || got != 0 { ... }
```

这条断言**钉住的就是本次要修的 bug**（旧注释甚至明写了「兜底语义」）。已改为 `want 0/err`，并补两个用例：

- 缺 `credit` 字段 → 必须是 `0/err`（unknown ≠ 0）
- `credit` 显式为 0 → 必须是 `0/nil`（防止把 fix 做过头，把真实观测误判成 unknown）

**牙齿验证**：把实现退回旧的 `_ = json.Unmarshal` 写法 → 3 条断言精确 FAIL
（非对象 gift / 非对象 compensation / 缺字段）；还原 → PASS。

## 五、自检清单

- [x] `go build ./...` + `go vet` 通过
- [x] `go test ./internal/upstream ./internal/scheduler` 全绿（含调用方 scheduler 15.8s 用例）
- [x] gofmt 干净
- [x] 牙齿验证：退回旧实现 → 精确 FAIL；还原 → PASS
- [x] 同为 P3 的 `global_models.go:645-656` 本次**未改**（仅影响列表元数据，另案）
