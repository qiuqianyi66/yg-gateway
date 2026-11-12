# 配置变更生效分类（热改 / 重算在途 / 重启）

> 学习清单 #2（DSH `src/policy.ts` 的 `policyVersion` 只哈希 rules/mode/sources、
> `revalidate` 按强度重判）落到本仓的对应物：**判断一个配置字段改完后，
> 在途的旧决策算不算数**。本仓的实现是「保存即热应用 + 显式返回重启清单」，
> 缺的只是把这三档写成一份可核对的表。

## 一、三档定义

| 档 | 含义 | 面板表现 |
|---|---|---|
| A 热生效 | 保存后下一个请求就用新值，不影响任何在途状态 | 「已保存并立即生效」 |
| B 热生效 + 重算在途 | 保存后立即生效，且会**重新计算/清空尚未执行的排程决策** | 同上，效果是「下一次触发时间变了」 |
| C 重启生效 | 字段在装配期被 go 对象捕获（监听地址、HTTP client、目录句柄、handler 内副本），热改不生效 | `restartRequiredFields` 返回字段名，面板提示需重启 |

## 二、A 档：热生效（纯赋值，不动在途状态）

证据 `cmd/server/main.go:795-821`，逐条对应 setter：

| 字段组 | 应用点 | setter 行为 |
|---|---|---|
| `api_key` / `api_keys` | `:795` `live.Store(buildAuthSnapshot(newCfg))` | 重建鉴权索引并原子切换 |
| `features.sanitize_blacklist_fingerprints` | `:796` 原子写 bool | 只影响后续指纹脱敏 |
| `pool.breaker_threshold` / `breaker_cooldown` | `:797` `p.SetBreaker` | `internal/pool/pool.go:205-217`，仅覆盖阈值与时长，**不清已开的熔断** |
| `pool.max_in_flight` / `max_in_flight_global` | `:798-799`、`:804` | `pool.go:447-463`，仅赋值；已在途请求不因降上限被踢 |
| `pool.degrade_threshold` / 降级冷却 | `:800` `p.SetDegrade` | 同上，只换参数 |
| `cooldown.soft_rate` | `:801` | 只影响下一次判定 |
| `pool.cost_explore_interval` | `:802` | 0 = 关停探索窗口 |
| `pool.breaker_probe_gap` | `:803` | half-open 试探间隔 |
| `pool.credit_floor` | `:805` | 积分保底（0 关闭） |
| `pool.idle_weight_*` / `prefer_expiring` | `:806-807` | 选号权重 |
| `pool.ewma.*` | `:808` `p.SetEwma` | `pool.go:228-238`，改 alpha/minSamples，**已累计的样本不重算** |
| `failure_window` | `:809` | 账号级故障窗口长度 |
| `min_request_interval*` | `:810` | 主动 RPM 限流 |
| `pool.pace_mode` | `:811` | pace 选号开关 |

## 三、B 档：热生效 + 重算在途（真正的「作废旧决策」）

本仓没有 superagent 那种 `policyVersion` 哈希使旧授权失效的机制；实际能作废旧决策的只有三处，
且都是**重算排程**而非撤回请求：

| 字段组 | 应用点 | 为什么算 B 档 |
|---|---|---|
| `schedule.*`（六类时点 + 六类开关） | `:813-819` `sch.Reconfigure` | `internal/scheduler/scheduler.go:216-246` 改完 cfg 后 `poke(s.rearmSchedule)`、`poke(s.rearmBalance)`：**下一次触发时间按新时点重算**，旧定时器作废 |
| `balance_refresh_interval` | `:820` `sch.SetBalanceInterval` | `scheduler.go:816-822` 写 `balanceInterval` 后 `poke(s.rearmBalance)`，新间隔即刻生效 |
| `expiring_soon_window` | `:812` `sch.SetExpiringSoonWindow` | `scheduler.go:176-188`：值变了就 `poolRef.ClearExpiringSnapshots()`，**清掉「快过期积分」缓存快照** |
| `schedule.include_disabled_in_tasks` | `:821` | `scheduler.go:210-214`，只影响循环开头的账号过滤 |

判据：**只改 cfg 且 poke rearm / Clear 缓存的 → B 档；只赋值 → A 档。**

## 四、C 档：重启生效

证据 `cmd/server/main.go:998-1036` `restartRequiredFields`，`saveConfig` 在 `:823` 把它回给面板：

- 顶层：`listen`、`auth_dir`、`state_file`
- `upstream.timeout_seconds` / `header_timeout_seconds` / `idle_timeout_seconds`、`upstream.user_agent`
  （`:1010-1014`，user_agent 装配期写进出站 client）
- `upstash`（`:1015-1016`）
- `session_sticky.ttl` / `gc_interval` / `enabled` / `derive`（`:1018-1026`，开关在装配期被 handler 复制）
- `logging.request_archive_*`、`logging.request_retention_days`（`:1027`）
- `server.read_timeout` / `max_body_bytes`（`:1028`）
- `security.trusted_proxy` / `read_only_rate_limit`（`:1031`）
- `alerting.*` 四项（`:1035`）

## 五、边界与未知

- 已确认：A 档 setter 一律只赋值，不清熔断/在途计数/EWMA 样本（`internal/pool/pool.go:205-217,228-238,447-463`）。
  所以「改参数」等于「新判定用新参数、旧判定按旧参数继续」——这是有意的，不是漏。
- 已确认（2026-11-12 补查）：12 个 `pool.Set*` 的生产调用点只有 `cmd/server/main.go` 一处（其余命中全是
  `*_test.go`），不存在「别处调用 setter 顺带清状态」的旁路；先前「未逐个追调用方」的未知已消解。
- 已确认（2026-11-12 修复）：B 档三处 poke 已按字段变化门控——`SetExpiringSoonWindow` 内部自带 changed
  判断；`saveConfig` 先解析旧配置，只有排程 6 组小时窗/6 个启用开关真的变化才调 `Reconfigure`，
  余额间隔真的变化才调 `SetBalanceInterval`（旧配置解析失败时保守按「有变化」处理）。
  守门测试 `TestScheduleReconfigureNeeded`。保存无关字段不再重算排程。
