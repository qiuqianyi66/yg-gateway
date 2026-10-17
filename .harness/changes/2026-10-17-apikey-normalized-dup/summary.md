# 变更记录 — 顶层 api_key 与多 Key 的重复检测同口径（B3-c，L2）

- 日期：2026-10-17
- 分级：**L2**（配置校验行为改变：一类配置从静默放行变 fail fast）
- 需求：`docs/prd/REQ-apikey-normalized-dup-2026-10-17.md`
- 来源：独立复审（2026-10-17）自选的 3 个「更难变异」之一，当时**存活**，登记为 B3-c

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `cmd/server/config.go` | 一行比较：`key == c.APIKey` → `key == strings.TrimSpace(c.APIKey)`，并写清「为何只改比较、不改运行时语义」 |
| `cmd/server/api_keys_config_test.go` | 新增 `TestAPIKeysPaddedSingleKeyDuplicateRejected`（两个方向：顶层带空格 / 多 Key 带空格），入口特意用 `ParseConfig` |

## 二、为什么必须用 ParseConfig 而不是 Load（复审给的坑）

`Load()` 会 normalize **两遍**（`config.go` 里 `Load` 与 `ParseConfigInto` 各一次）。第二遍时
`k.Key` 已被首遍 trim，于是「比较未归一化」这个缺口被**自己掩盖**掉。
独立复审实测：同一个变异在 `Load` 入口下「咬住」、在 `ParseConfig`（单次 normalize，面板保存路径）下才暴露。
新测试因此显式走 `ParseConfig`，并在注释里写清原因——否则下一个人会「顺手」改回 `loadWith` 而悄悄失去覆盖。

## 三、怎么验证的

1. 修复后：`go test ./cmd/server/ -run 'TestAPIKeysPaddedSingleKeyDuplicateRejected|TestAPIKeysDuplicateKeyFailsFast'`
   → 两个都 PASS；`go test ./cmd/server/` 整包绿。
2. **牙齿（在副本里对未修复的代码跑新测试）**：`%TEMP%\mut-b3` 的 HEAD 早于本次修复，把新测试拷进去跑：

```
--- FAIL: TestAPIKeysPaddedSingleKeyDuplicateRejected
    api_keys_config_test.go:231: 顶层 api_key 与多 Key 归一化后同值时必须 fail fast，got nil；
    body={"api_key":" sk-dup ", "api_keys":[{"id":"k1","key":"sk-dup","models":["m1"]}]}
```
   即：**未修复时新测试红、修复后绿**，且验证在副本里做（L-2 硬规则），副本已还原干净。
3. `gofmt -l` 无输出。

## 四、边界（别读成「已完全防住」）

- 这只堵住「归一化后同值」这一种误配。`api_key` 本身仍不 trim（运行时有空格就按空格认），
  那是**既有语义**，本次不动（改它会改变鉴权行为，需要单独的决策）。
- 与 B3-b（`session` 闲置号优先无测试）无关，那条仍开。

## 五、回滚

`git revert <本提交>`。回滚后带空格的同值配置重新静默放行（新测试会红，这是预期）。