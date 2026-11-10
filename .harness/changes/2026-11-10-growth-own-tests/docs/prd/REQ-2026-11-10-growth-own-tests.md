# REQ — growth 包拥有本包测试（让覆盖率棘轮看见它）

## 必要性

`internal/growth` 自 2026-10-29 拆出（ADR-018）后没有 `*_test.go`，`go test ./... -cover`
对它恒报 `0.0%`；`scripts/check-depth.ps1:210-213` 按设计把 0% 包排除在 `minCoverage` 之外，
于是这 953 行养号逻辑**游离在覆盖率棘轮之外**：哪怕真实覆盖跌到 0，门禁仍全绿。

实测它当前的真实覆盖是 **89.9%**（`go test -coverpkg=yg-gateway/internal/growth ./... -count=1`），
全部来自 upstream 转发存根的间接测试。间接测试能证明"委托后的结果对不对"，但没有本包断言
兜住"错误分支是否仍按既有契约返回"。覆盖率数字的存在本身就是棘轮能咬住的前提。

## 需求

新增 `internal/growth/client_test.go`，用 `Transport` 测试替身直接驱动本包公开方法，
覆盖三条口径：成功解析、传输错误上抛、解信封后的坏 JSON 必须报错。生产代码零改动。

## 验收（GWT）

- Given 本包尚无测试文件 → When 跑 `go test ./internal/growth/ -cover -count=1`
  → Then 输出 `coverage: 9x%`，不再是 `0.0%`。
- Given 传输层返回错误 → When 调 `TravelStatus`/`ClaimReward`/`ClaimTrial`
  → Then 错误原样上抛（`errors.Is` 命中哨兵错误），不被解析层吞成零值。
- Given 领奖响应缺 `credit` 字段 → Then `ClaimGift` 返回错误（unknown ≠ 0）；
  Given `credit` 显式为 0 → Then 返回 `(0, nil)`（可观测的零不是缺失）。
- Given 夜猫子 `need=2` → Then 只等 1 次、时长等于 `nightChatGap`（4s）；
  注入的等待实现收到的时长必须是生产默认值。
- Given `ClaimTrial` 遇到幂等码 14051 → Then `(false, nil)`，不视为失败。
- Given 全量门禁 → Then `scripts/pre-commit.ps1` 退出码 0，`check-depth` 的 `minCoverage`
  不回退（growth 93.4% ≥ 基线 74%）。

## 不做

- 不改任何生产代码（含错误文案、路径常量、等待时长）。
- 不把 upstream 侧的报文金样（`r8_golden_test.go`）搬过来重复断言。
- 不引入新依赖、不新增门禁度量：本包只要能看见覆盖率，棘轮就自动生效。
