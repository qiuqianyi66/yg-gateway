# 变更记录 — rhythm 桶名可读化

> 关联：`docs/adr/ADR-024-concurrency-gate-and-rhythm.md`、本需求
> `docs/prd/REQ-2026-11-08-rhythm-readable-keys.md`。

## 改了什么

1. `internal/upstream/concurrency.go`：导出 `MachineIDForUID(uid)`，复用
   `deriveAccountStableID(uid, "machine")`——与 `injectAccountStableHeaders` 同源，
   不是第二套哈希。
2. `internal/server/status.go`：`rhythmSnapshot()` 增加 `rhythmRelabel(raw, Pool.List())`
   一层：已匹配账号的桶名 → `昵称(uid8)`；无昵称 → `uid8`；`global`/未知桶原样保留。
   `shortUID()` 取前 8 位（与日志/stdout 显示口径一致）。

不做的事：不动 `concurrencyWait` 的 key 选择，不动 `RhythmStatus` 分桶逻辑，
不改面板（前端当前不消费 rhythm）。

## 为什么在 server 层做映射

upstream 是出站协议层，知道 machine-id 但**不该知道账号昵称**；昵称在 pool 视图里。
反向从 machine-id 查 uid 不可行（sha256 不可逆），但正向对每个账号重算 machine-id 建
`machine-id → 标签` 表是 O(账号数)=8，可忽略。职责不越界，语义不变。

## 验证（实跑）

- `go build ./...` exit 0。
- `go test ./internal/upstream/ ./internal/server/ -run 'TestMachineIDForUID|TestRhythmRelabel|TestStatusExposesRhythm'`
  三个 PASS、exit 0。
- **牙齿（先红后绿，两处都实跑）**：
  1. `rhythmRelabel` 首行插 `if true { return raw }` → `TestRhythmRelabelReadable`
     FAIL「账号桶未可读化或数值变了」，exit 1；恢复后 PASS。
  2. `MachineIDForUID` 派生盐改成 `machine-temp-teeth` →
     `TestMachineIDForUIDMatchesHeader` FAIL「两处派生漂移」，exit 1；恢复后 PASS。

## 回滚

纯展示层：回退本次 commit 即可，无状态、无配置项、无迁移。

## 生产部署记录（2026-11-08）

- `scripts/deploy-prod.ps1 -Build -Feature rhythm-readable-keys` exit **0**：
  备份 `yg-gateway.exe.pre-rhythm-readable-keys-20261108.bak`（sha256=3BCD209B8CC502F0，与
  上一版一致）→ 换版 → 新 exe sha256=**78B14E8B041C8A76**，PID 21072。
- 冒烟三条全过（`cn:space-bunny ×0.08` 低成本模型）：非流式 finish_reason=stop、
  流式 data 增量帧 + `[DONE]`。
- 生产 `/status` 实测 rhythm 桶名已可读：
  `.(269f9704)` / `🐻(0250aab9)` / `global`，三桶 `in_flight=0 queue_depth=0`（无泄漏）。
  改动前为 `07f7cb98...` / `d27a4369...` 两个裸哈希 + `global`。
