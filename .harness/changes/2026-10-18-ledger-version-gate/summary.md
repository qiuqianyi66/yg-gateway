# 变更记录 — usage.json 版本闸门（B2 实质 ②，L3）

- 日期：2026-10-18
- 分级：**L3**（数据模型 / 持久化格式的口径变更）→ 先立 ADR
- ADR：`docs/adr/ADR-009-persistence-version-gate.md`
- 计划：`docs/PLAN-open-items-2026-10-17.md` 的 B2 实质（深读「该改」②）

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `internal/usage/usage.go` | `Recorder` 新增 `loadErr`；`New` 记录加载错误；新增 `LoadErr()`；`load()` 加版本闸门（新→错误；旧→备份；缺失/相等→放行） |
| `cmd/server/main.go` | `usage.New` 之后检查 `LoadErr()`，非空即 `log.Fatalf`（拒绝启动） |
| `internal/usage/version_gate_test.go` | 新增五个子用例（见下） |

## 二、为什么（一句话）

旧程序读新 `usage.json` 会**按零值丢字段**并覆盖回盘 —— 在「为了救服务才回滚」的时刻发生，
静默且不可逆。历史实现还会把「文件损坏」也静默成「从零开始」。

## 三、怎么验证的

`go test ./internal/usage/ -run TestLedgerVersionGate -v` → **5/5 PASS**：

| 子用例 | 断言 |
|---|---|
| 版本 999（> 当前 6） | 返回错误，且错误信息点名 999 |
| 版本 1（< 当前） | 可加载，且 `<path>.pre-upgrade-v1.bak` 存在、内容是原文件 |
| 版本 == 当前 | 可加载，且**不**产生备份 |
| 无 version 字段（老文件） | 可加载（零迁移成本） |
| 文件损坏 | 返回错误（不再静默从零） |

另：`go build ./...` 通过（`cmd/server` 的 `LoadErr` 接入无编译问题）。

**牙齿**：删掉 `if f.Version > fileVersion { return ... }` → 第一条子用例红；
删掉备份分支 → 第二条红（这两条是「还原 bug 就 FAIL」的直接对应）。

## 四、边界与风险（明说）

1. **回滚会启动失败**：新版写过 usage.json 后回滚 → 旧版拒绝启动 → 部署脚本健康检查失败 →
   自动回滚 → `exit 4`（生产可能不可用，需人工）。这是刻意的取舍（见 ADR §三），
   错误信息里给了操作路径。
2. **`state.json` 未覆盖**：它没有版本字段，且 `pool.load()` 对 unmarshal 失败静默 return。
   同族问题，但改成可失败会牵动 `pool.New()` 签名，单独立项。
3. 生产上的 `D:\yg-gateway\data\usage.json` 当前应是 v6（与程序一致）→ 升级后行为不变；
   首次启动会正常加载、不产生备份。

## 五、回滚

`git revert <本提交>`。回滚后重新回到「静默从零开始 / 静默丢字段」的旧行为。