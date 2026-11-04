# 变更记录 — 出站 RPM 硬闸（rpmTransport，可配置默认关）

日期：2026-11-04
类型：功能开发（REA P0 最高优先级真实缺口，补审重开项）
关联：`docs/prd/REQ-yg-gateway-rpm-hardgate-2026-11-04.md`
基线：`769fec2`；提交：本变更单次提交

## 改了什么

- `internal/upstream/rpm.go`（新）：`rpmLimiter`（60s 滑动窗 + 1s 槽位，
  now/after 可注入）+ `rpmRoundTripper`（按 X-Machine-ID 分桶；无头归 global
  桶兜底；`RpmPerAccount<=0` 直放零回归）+ `Client.rpmWait`（限速器注册表
  双检锁按需创建）。
- `internal/upstream/transport.go`：`newUpstreamHTTP` 方法化，Transport 外层
  统一包 `rpmRoundTripper`（全部出站 client 过同一闸：直连 HTTP/ChatHTTP +
  per-proxy clientForProxy）。
- `internal/upstream/client.go`：`Client` 增 `RpmPerAccount` + `rpmMu/rpmLimiters/
  rpmWindow/rpmSlot`（测试注入用）；`New()` 拆两步（client 就绪后才构造
  HTTP/ChatHTTP，wrapper 需引用 c）；`roundTripCloseIdle` 依赖的
  `CloseIdleConnections` 由 wrapper 透传（不吞能力，清池行为零回归）。
- `cmd/server/config.go`：`upstream.rpm_per_account` 字段 + 注释（0=关默认、
  负值钳 0、与 pool.min_request_interval 选号侧区分）；钳位校验。
- `cmd/server/main.go`：装配 `up.RpmPerAccount = cfg.Upstream.RpmPerAccount`。
- `config.example.json`：`rpm_per_account: 0` 示例。
- 测试 `internal/upstream/rpm_test.go`（新）：窗口满等待最早滑出（可控时钟 +
  可控 after）、ctx 取消返回 `ctx.Err()` 不悬挂、两账号分桶互不占用、无头归
  global、关闭态直放。既有 `transport_failclosed_test.go` / `client_test.go`
  适配方法化与 wrapper（断言改为解包后共享，语义不变）。

## 怎么验证的

- 先红后绿：`TestRpm*` 先编译失败（目标缺失），实现后绿。
- 定向：`go test ./internal/upstream/ -run 'TestRpm|TestProxy'` 绿。
- 五门禁全量（见提交前输出）：test 30 包 / vet / gofmt 零输出 /
  check-boundaries / check-depth 全过。

## 设计要点（为何如此）

- **机制 ≠ 默认值**：P1-1 校准结论（8 天观测支撑 min_request_interval=0 关）
  只回答选号侧默认值；本项实现出站侧**机制**为可配置能力（默认关，
  与校准结论不冲突）。`rpm_per_account` 打开时防「短时间把一个号打热」——
  养号场景最直接的封号路径（REA 节奏面核心结论）。
- **挂 RoundTripper 层而非调用点**：覆盖 chat/billing/models/签到全出站路径，
  不依赖各调用点自觉加闸；开关在 RoundTrip 时判，装配期零值=关闭安全。
- **分桶键 = X-Machine-ID**：账号稳定标识（headers.go 全出站注入），会话/
  多请求天然聚到账号桶；无头请求防御性归 global，不绕过硬闸。
- **不失败不换号**：窗口满阻塞等待（与 magpie rpm.go 同语义）；ctx 取消即
  返回错误，交由既有 429/超时换号路径，不悬挂 goroutine。

## 回滚

- 删除 `rpm.go`，`newUpstreamHTTP` 改回包级函数（或保留方法签名但去掉
  wrapper），config 字段与装配一行删除，测试适配还原。配置默认 0 关，
  未开 `rpm_per_account` 的部署零行为变化。
