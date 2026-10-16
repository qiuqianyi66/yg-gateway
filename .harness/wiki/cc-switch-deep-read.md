---
last_updated: 2026-10-16
status: active
owner: YG
source_repo: https://github.com/farion1231/cc-switch
source_commit: 09bce30171e2bdb8c43bdc0f5f3b4c75124711ea
---

# cc-switch 深读对照笔记（T8）

> 每条结论带 `file:line`。**标记语义**：`observation` = 直接读到的代码事实；`inference` = 从代码推的；
> `unknown` = 源码里看不出来（并写明需要什么才能消解）。
> 行号口径 = `(Get-Content).Count`（含空行），与 `Measure-Object -Line` 会差 ~4%。

## 0. 取材与起点

| 项 | 值 |
|---|---|
| clone | `git clone --depth 1 https://github.com/farion1231/cc-switch $env:TEMP\cc-switch-deepread` |
| SHA | `09bce30171e2bdb8c43bdc0f5f3b4c75124711ea`（读的就是这个 commit，副本仍在 `%TEMP%\cc-switch-deepread`） |
| 组件 | Tauri 桌面应用：Rust 后端 ~29 万行 + TS 前端；SQLite（rusqlite）持久化 |
| 上轮结论 | 只以压缩形式存活于 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md:211-212`（3 条，无 file:line）。本轮把这 3 条**实测定位到证据**，见 §1.4 |

## 1. 持久化：DB 与文件谁是事实源

### 1.1 结论

**DB（`~/.cc-switch/cc-switch.db`）是 provider / settings / 统计的事实源；各客户端配置文件（`~/.claude/settings.json`、`~/.codex/config.toml`…）是派生投影。**
唯一的例外被显式建模：`skills` 的 SSOT 在文件系统，DB 只存元数据。

| # | 结论 | 证据 | 标记 |
|---|---|---|---|
| 1 | DB 单文件固定在 app config 目录；`SCHEMA_VERSION = 20` | `src-tauri/src/database/mod.rs:54`、`:99-101` | observation |
| 2 | 客户端配置是**单向写出的投影**：切换/编辑器改 DB 行 → 再由写引擎写 live 文件 | `services/provider/mod.rs:6099-6103` | observation |
| 3 | 反向导入是**一次性、且刻意保守**：仅当该 app 在 DB 里一行 provider 都没有时才在启动时导入 live；有官方种子行也视为「有」 | `services/provider/live.rs:1154-1168` | observation |
| 4 | provider 可被标为「DB-only」（`meta.live_config_managed = Some(false)`）：此时 live 文件里找不到它不算错误，解析失败也容忍 | `provider.rs:592`、`services/provider/mod.rs:5344-5354` | observation |
| 5 | `skills` 的 SSOT 已迁到文件系统，迁移代码**故意不把**旧 `config.json` 的安装状态写进新表，理由写在注释里：「避免数据库显示已安装但文件缺失」 | `database/migration.rs:195-204` | observation |
| 6 | 更新 provider 时 `is_current` / `in_failover_queue` 从既有行读回、不被内容更新顺手改掉 | `database/dao/providers.rs:275-301` | observation |
| 7 | 改「当前选中」是事务内两条 UPDATE（先全清 0、再置 1），不是写单行 | `database/dao/providers.rs:475-494` | observation |
| 8 | 官方 provider 用固定 id 的种子常量，启动时幂等写入，给用户留「一键切回官方」入口 | `database/dao/providers_seed.rs:1-33` | observation |

### 1.2 迁移怎么保证幂等（5 条机制，按重要性）

| # | 机制 | 证据 | 标记 |
|---|---|---|---|
| 1 | 版本闸门：`PRAGMA user_version` + `while version < SCHEMA_VERSION` 单步 `match`，每步迁完立刻写新版本号 | `database/schema.rs:446-587` | observation |
| 2 | 版本**高于**本程序 → 拒绝启动并提示「请升级应用」，而不是继续跑 | `database/schema.rs:452-458` | observation |
| 3 | 整个迁移包在 `SAVEPOINT schema_migration` 内，任一错误 `ROLLBACK TO` + `RELEASE`（失败不留半迁移状态） | `database/schema.rs:447-448`、`:591-602` | observation |
| 4 | 加列一律走 `add_column_if_missing`：先 `table_exists` → `has_column` → 才 `ALTER TABLE`，返回「是否真的加了」 | `database/schema.rs:3736-3759` | observation |
| 5 | 表/列名拼 SQL 前过 `validate_identifier` 白名单校验 | `database/schema.rs:3678`、`:3691`、`:3742` | observation |
| 6 | 迁移前备份：仅当 `0 < user_version < SCHEMA_VERSION` 时备份库文件，备份失败**只告警继续**（不阻断升级） | `database/mod.rs:128-141` | observation |
| 7 | JSON→SQLite 迁移整体一个事务，全部 `INSERT OR REPLACE`（可重跑） | `database/migration.rs:13-22`、`:84-103` | observation |
| 8 | 迁移有 dry-run：在内存库上跑一遍、显式 drop 事务不提交 | `database/migration.rs:25-42` | observation |
| 9 | `PRAGMA user_version` 随事务一起提交/回滚，所以进程崩溃也不会停在「表改了、版本没升」 | `database/schema.rs:461-589` | inference（未做崩溃注入实测；需 kill -9 在迁移中途验证才能升级为 observation） |

### 1.3 统计侧的不可逆操作

| # | 结论 | 证据 | 标记 |
|---|---|---|---|
| 1 | 明细表按天汇总进 `usage_daily_rollups` 后**删除**明细；保留窗口 30 天，启动时执行 | `database/mod.rs:156`、`database/dao/usage_rollup.rs:62` | observation |
| 2 | 剪枝**前**先尽力回填缺失成本；回填失败只告警、照样剪——注释理由：一行坏定价数据不应永久卡死日志清理 | `database/dao/usage_rollup.rs:79-86` | observation |
| 3 | cutoff 对齐到「`now - retain_days` 的下一个本地零点」，避免某天半汇总半剪枝导致区间查询**静默少计** | `database/dao/usage_rollup.rs:11-35` | observation |
| 4 | 汇总行同时保留 `request_model`（客户端别名→真实模型映射）与 `pricing_model`（写入时的计价基准）两维，历史映射即使明细删了也可审计 | `database/dao/usage_rollup.rs:121-130` | observation |
| 5 | DB 写钩子：任何 insert/update/delete 都触发 webdav/s3 自动同步通知 | `database/mod.rs:84-94` | observation |

### 1.4 上轮 3 条结论的实证核验（把交接文档的一句话升级成证据）

| 上轮结论 | 本轮定位到的证据 | 标记 |
|---|---|---|
| 代理转发内核「预读首包才可切」 | 流式超时分两级：`is_first_chunk` 时用首字节超时（默认 60s），之后用静默期超时（默认 120s）；首包到达才记 debug 并翻转标志 | `proxy/response_processor.rs:746-795`、`database/schema.rs:397-405` | observation |
| 同上（失败前不记成功→可重试） | 存在「流式首包错误在记成功之前可重试」的行为测试；`stream_ended_with_error` 门禁决定是否保留已写出的帧 | `proxy/forwarder.rs:5076`（测试名）、`proxy/providers/streaming.rs:259`、`:749`、`:801` | observation（生产侧判定分支本轮未逐行读，见 §6） |
| 协议转换「星形无 IR」 | 每个 app 一个预设类型 + `apiFormat` 四态（anthropic / openai_chat / openai_responses / gemini_native），转换是配置驱动而非中间表示 | `src/config/claudeProviderPresets.ts:50-59` | observation |
| 配置改写层「token 级往返 + 语义闸门 + 前滚」 | 前滚见 §2；「保留外部字段」由 JSON 补丁的深层 subset 移除实现（只删自己写过的项） | `services/provider/live.rs:39-90` | observation |

## 2. 写盘引擎：本轮最大收获（可复用的工程范式）

一次「切换/保存」被当成**一个操作**提交：`plan`（内存算内容 + 记 pre/planned 的 SHA-256）→ `stage`（写目标旁临时文件、fsync、私有文件 0600）→ 逐个 `commit_staged`（原子替换）→ `commit_target`（改指针/模式状态），全过程有一份 pending 意图日志。

| # | 结论 | 证据 | 标记 |
|---|---|---|---|
| 1 | pending 意图先落盘（含「已开始发布」标记），之后才动第一个文件；崩溃后按 pending 前滚或丢弃 | `mode/operation.rs:1-16`、`:104-140`、`:208-282` | observation |
| 2 | 恢复判定用 hash 三态：文件内容 = pre → 还没发布（丢弃）；= planned → 已发布（补完其余）；**两者都不是 → 被外部改过，以外部为准、不再动它**，但状态照样落定 | `mode/operation.rs:9-14`、`live/engine.rs:105-125` | observation |
| 3 | 原子替换 Windows 走 `ReplaceFileW`，`NotFound`/`ERROR_NOT_SUPPORTED`（WSL UNC）回落 `fs::rename`，最多重试 3 次；失败时**临时文件故意留下**给下次恢复前滚 | `config.rs:592-690` | observation |
| 4 | 每个文件第一次被引擎写入前留一份**字节级**备份（以绝对路径哈希为键，只备一次，附 `.source` 记录原路径）；写入前文件不存在则只记 `.source` | `live/engine.rs:158-184` | observation |
| 5 | 冲突策略是三态枚举 `Refuse`（默认，什么都不写）/ `KeepMine` / `KeepTheirs`，用**三方比较**（打开编辑器时的基线 vs live 现值 vs 待写值）判定 | `services/provider/claude_editor.rs:58-87` | observation |
| 6 | 每个 app 的客户端文件写入都要先拿不可重入的 `AppWriteGuard`（切换/编辑器/模式操作/托盘统一） | `live/engine.rs:186-189` | observation |
| 7 | 编辑保存的顺序是「先存 DB 行 → 再写 live」；live 写失败且**没有 pending**（说明还没开始发布）时回滚刚存的 DB 行，有 pending 则保留行等前滚 | `services/provider/mod.rs:5985-6012`、`:6052-6061` | observation |
| 8 | 切换/进代理前先拿「settled」锁：先补完上次未完成的写、再读模式和指针 | `services/provider/mod.rs:6590-6593` | observation |

## 3. `services/provider/`（重点：`mod.rs` 8312 行）

| # | 结论 | 证据 | 标记 |
|---|---|---|---|
| 1 | 结构：1-152 行声明 + 小函数；**153-5322 是 `mod tests`（约 5170 行，占文件 62%）**；5323-8087 `impl ProviderService`；8088+ 排序结构 | `services/provider/mod.rs:112-153`、`:5323`、`:8088` | observation |
| 2 | 真正业务代码 ≈ 3000 行；`pub fn` 里相当一部分只是 re-export 子模块 | `services/provider/mod.rs:7541-7590` | observation（行数占比为 inference，按行号区间估算） |
| 3 | 切换分三岔：代理模式下**只换代理路由**（契约没变就不读不写客户端文件）、直连模式全量切换、累加式 app 走各自分支 | `services/provider/mod.rs:6595-6622`、`:6629-6701` | observation |
| 4 | 代理模式下**禁止**切到官方 provider，返回本地化错误，理由写死在文案里：「使用代理访问官方 API 可能导致账号被封禁」 | `services/provider/mod.rs:6598-6606` | observation |
| 5 | provider 的「共享配置」抽取按 key 分类：`claude_env_key_is_shared` / `claude_top_key_is_shared` 区分全局字段与该 provider 私有字段，抽出的公共片段存 `settings` 表 | `services/provider/mod.rs:7106-7150`、`database/migration.rs:216-244` | observation |
| 6 | live 写入分发按模式分派（切换式 app 走 `*_direct.rs` 写引擎，Claude Desktop 与累加式 app 在 `live.rs` 里写） | `services/provider/live.rs:1-4` | observation |
| 7 | 「这一行是否真写进了 live」被持久化成元数据 `meta.live_config_managed`，在 `live.rs` 里有 3 处显式置 `Some(true)` | `services/provider/live.rs:1267`、`:1356`、`:1424` | observation |

## 4. `*ProviderPresets.ts`（11 个文件，最大 6557 行）

| # | 结论 | 证据 | 标记 |
|---|---|---|---|
| 1 | 每个 app 一个预设接口：Claude 侧 `settingsConfig`（原样写 `settings.json` 的 env 对象）；Codex 侧拆成 `auth`（写 `~/.codex/auth.json`）+ `config`（一段 TOML 字符串写 `config.toml`） | `src/config/claudeProviderPresets.ts:26-32`、`src/config/codexProviderPresets.ts:18-23` | observation |
| 2 | 预设**没有稳定 id**：claude 文件 130 个条目里 `id`/`nameKey` 只有 8 处，其余靠 `name` 认 | `src/config/claudeProviderPresets.ts`（grep 计数） | observation |
| 3 | 预设承载 OAuth 型分支（`providerType: github_copilot / codex_oauth / xai_oauth` + `requiresOAuth`），不只是 baseURL 模板 | `src/config/claudeProviderPresets.ts:61-67` | observation |
| 4 | 预设里内嵌联盟推广参数（`track_id=…&aff=cc-switch`），说明它是商业/UX 资产 | `src/config/claudeProviderPresets.ts:101-102` | observation |
| 5 | 量级全是数据：claude 2958 / codex 4949 / openclaw 6557 / hermes 3527 行 | 文件行数统计 | observation |

## 5. `usage_stats.rs` + 会话用量

| # | 结论 | 证据 | 标记 |
|---|---|---|---|
| 1 | 统计口径写死在结构体注释里：`real_total = input+output+cache_creation+cache_read`；`cache_hit_rate = cache_read/(fresh_input+cache_creation+cache_read)` | `services/usage_stats.rs:30-63` | observation |
| 2 | 定价来源是**显式两态字符串**：`PRICING_SOURCE_REQUEST="request"` / `PRICING_SOURCE_RESPONSE="response"`，与 `pricing_model` 列一起落库 | `database/dao/proxy.rs:10-11`、`database/dao/usage_rollup.rs:121-130` | observation |
| 3 | 双源去重：客户端会话日志导入的行与代理日志行，在 **±10 分钟**窗口内且四类 token 全等（`unknown` 模型可通配）才判为同一次调用 | `services/usage_stats.rs:395`、`:479-512` | observation |
| 4 | 另有持久去重账本表 `session_usage_dedup`（v17 迁移建），防重复扫描重导入 | `database/schema.rs:1593-1610` | observation |
| 5 | 日志总数缓存 TTL 60s（避免面板轮询打全表 count） | `services/usage_stats.rs:623` | observation |
| 6 | 会话用量从 `~/.claude/projects/**/*.jsonl` **增量**解析：游标是字节偏移 `last_byte_offset`，为 NULL（升级前）时回落旧的行号游标 | `services/session_usage.rs:1-9`、`:601-606`、`:631-648` | observation |
| 7 | 任务清单里的 `services/session_manager/*` **不存在**；实际是 `session_usage*.rs` 一族（8 个文件，最大的 `session_usage_codex.rs` 5922 行） | `Get-ChildItem src-tauri/src/services` | observation |

## 6. 对照本仓（YG Gateway）的启示

> 本仓约束：零新依赖（`go.mod` 直接依赖只有 redis）、单二进制 `go:embed`、自托管 7×24 常驻、
> 养号友好选号（快过期优先 / 成本分层 / 保底）、R8（上游报文不得改名）。
> 注：`internal/pool`、`internal/server` 有并行子代理在改，下面引用它们时只写函数名，不钉行号。

| # | cc-switch 的做法 | 本仓判定 | 理由（一句话） |
|---|---|---|---|
| 1 | 剪枝/删除**前**先尽力回填派生值，失败只告警不阻断（`dao/usage_rollup.rs:79-86`） | **该改** | `model_costs` tier 是 inference（见 AGENTS.md 推断清单），而 YG 的用量一旦落桶/reqlog 归档到期（默认 7 天，`internal/reqlog/reqlog.go:296-297`）就永久失去「拿到 credit 样本后纠成本」的机会——回填必须发生在不可逆动作之前 |
| 2 | 持久化文件的版本闸门 + 迁移前备份 + 「版本过新则拒绝启动」（`database/schema.rs:446-458`、`database/mod.rs:128-141`） | **该改** | YG 的 `usage.json` 有 `Version` 字段（`internal/usage/usage.go:81`，当前 `fileVersion=6`）但 `load()` 只做 unmarshal、**从不校验**；`state.json` 连版本字段都没有（`internal/pool/entry.go` 的 `stateFile`）。旧程序读新文件会静默丢字段，而这是 7×24 常驻 + 滚动替换二进制的部署形态下的常态 |
| 3 | 多文件操作先落「写前意图」，崩溃后前滚（`mode/operation.rs:1-16`） | **该改** | 一次养号/迁移动作会同时动 `auths/workbuddy-*.json` 与 `data/state.json`，而 YG 只有**单文件**原子写（`internal/auth/auth.go:356-368` tmp+rename）。跨文件一致性无日志、无对账（在 `internal/` 下 grep `Reconcile`/`orphan` 只命中 upstream 的 tool-call 清理）→ 需要的是「意图文件 + 启动时前滚」而不是更强的单文件原子性。标记：inference（未验证 `cmd/` 侧是否有补偿路径） |
| 4 | 三方比较 + hash 的冲突策略（`claude_editor.rs:58-87`、`live/engine.rs:105-125`） | **不适用**（保留思想） | YG 进程**从不重写** `config.json`（`cmd/server/config.go:579-607` 用 `O_EXCL` 一次性生成、绝不覆盖），面板改的是内存快照（`internal/livecfg/livecfg.go:1-9`），所以没有「写回时撞上人工编辑」的场景。可迁移的只有「记录自己写过的内容，下次据此判断是否被外部改过」这一条 |
| 5 | Redis 快照与本地文件「比时间戳择新」（`internal/pool/persist.go` 的 `RestoreFromSnapshot` / `saveLocked`） | **keep** | YG 已有等价的择新恢复；cc-switch 用 hash 三态而非时间戳，是因为多机时钟不可信。当前 YG 单机常驻，**先不动**——若将来多节点共享 Redis 快照，再把时间戳换成「最后写入内容哈希」 |
| 6 | 单文件原子写：stage 到目标旁 + fsync + 原子替换，失败留 tmp 给前滚（`config.rs:592-690`） | **keep** | YG 已是 tmp+rename（`internal/auth/auth.go:356-368`、`internal/pool/persist.go` `saveLocked`）。差别只在 fsync 与 Windows `ReplaceFileW` 回落：替换二进制前必须备份是 R15 已覆盖的同类风险，**不引入**新依赖就能保住现状 |
| 7 | 落盘节流：dirty 标记 + 周期 ticker + 显式 Flush（`internal/pool/persist.go` `startFlusher`/`Flush`） | **keep** | YG 的 pool 已是「dirty + 5s ticker + 退出 Flush」，与 cc-switch 的 pending/publish 节奏同族；7×24 常驻下这是必须的（高频写盘会把磁盘打满） |
| 8 | 汇总窗口对齐本地日边界（`dao/usage_rollup.rs:11-35`） | **keep** | YG 的 `Recorder.Rollup` 已按本地日历日折叠小时桶（`internal/usage/usage.go:325-347`），同一思路，无缺口 |
| 9 | 明细 30 天 + 汇总永久，启动时执行清理（`database/mod.rs:153-156`） | **该改**（小） | YG 的清理挂在归档 writer 的写入路径上（`internal/reqlog/reqlog.go:349`、`:503-519`）。对「长期无流量的自托管实例」而言，启动期 + 周期各跑一次更稳；但注意 `internal/usage` 的桶里已含成本，**不要**在没有回填的前提下加激进剪枝（与 #1 绑定） |
| 10 | 130+ 个 provider 预设 + 4 种 apiFormat 的配置驱动转换 | **不适用** | YG 是单一上游（CodeBuddy + 一个备用源），明确不是通用网关；把预设数据塞进 `go:embed` 只会撑大单二进制、并诱导「接所有 provider」的定位漂移 |
| 11 | 从 CLI 会话 JSONL 增量扫描用量 + 双源 ±10 分钟去重（`session_usage.rs:1-9`、`usage_stats.rs:479-512`） | **不适用** | YG 的客户端是 OpenAI 兼容 API 消费者，日志只有一个来源（`internal/reqlog`），没有「代理日志 vs 本地会话日志」的双计数问题 |
| 12 | 引入 SQLite 做持久化 + `user_version` 迁移（`database/mod.rs`） | **不适用** | 零新依赖硬约束 + `CGO_ENABLED=0`：Go 侧要 SQLite 就得 CGO 或自带 driver 依赖，两者都违背单二进制分发。**但迁移纪律（版本闸门/幂等加列/迁移前备份）与实现无关，见 #2** |
| 13 | 「代理模式下禁止切官方 provider」的显式拒绝 + 原因文案（`services/provider/mod.rs:6598-6606`） | **keep**（思想） | 这与 YG「养号优先于压成本」同源：宁可在网关层拒绝一个诱人但会伤号的组合，也不默默转发。落地形态不同（YG 没有官方/第三方分叉），故为思想保留 |
| 14 | 8312 行的 `provider/mod.rs` 里 **62% 是测试代码**，业务逻辑被压在 3000 行内（`mod.rs:153`、`:5323`） | **keep** | 这正是 YG `check-depth.ps1` 的 `maxFileLines` / `oversizeFuncs` 棘轮要防的事：`mod.rs` 靠把测试同文件堆在一处才撑到 8312 行——YG 已用独立 `_test.go` + 行数棘轮避免这个形态，继续收紧即可 |

**top 3 照做**：#1 剪枝前回填 → #2 版本闸门与迁移前备份 → #3 写前意图日志与前滚。

## 7. 未覆盖（本轮**没有**细读的部分，别当成已读过）

| 范围 | 状态 |
|---|---|
| `provider/mod.rs` 的 153-5322 行 | 只确认它是 `mod tests`，未读内容 |
| `provider/` 的 `codex_direct.rs`(1289) / `codex_editor.rs`(606) / `codex_login.rs`(611) / `codex_official_models.rs`(1392) / `codex_client_catalog.rs`(1229) / `claude_direct.rs`(82) / `gemini_*` / `grok_*` / `pi.rs`(784) / `icon.rs`(550) / `usage.rs`(289) / `editor_toml.rs` / `endpoints.rs` | 未读（只从 `mod.rs` 的调用点反推了职责） |
| `provider/mod.rs` 的 `add` / `update`(6107-6334) / `delete`(6392) / `extract_*_common_config`(7245-7541) / `validate_provider_settings`(7652) | 只读了骨架与调用关系，未逐行读 |
| `mode/controller.rs`(8225) / `mode/stack.rs`(1729) / `mode/contract.rs` | 只读了 `operation.rs` 与 `state.rs` 的接口，未读路由契约细则 |
| `database/backup.rs`(3117) 导出/快照协议、`database/tests.rs`(1321) | 未读 |
| `database/dao/` 的 `mcp.rs` / `skills.rs` / `profiles.rs` / `failover.rs` / `settings.rs` / `proxy.rs` 细节 | 只读了 `providers.rs` / `usage_rollup.rs` / `providers_seed.rs` |
| `usage_stats.rs` 的 2968-5960 行（定价候选/前缀匹配/限额度检查）| 未读 |
| `session_usage_codex.rs`(5922) / `_pi` / `_grokbuild` / `_gemini` / `_opencode` / `_mcode` | 只读了 `session_usage.rs`（Claude Code 路径） |
| 各 `*ProviderPresets.ts` | 抽样读结构与计数，未逐条读预设数据 |
| `proxy/` 全目录（转发内核、协议转换） | 只做定点核验（§1.4），未系统读；上轮结论的原始证据不在本会话 |

## 8. 仍为 unknown（需要什么才能消解）

| # | unknown | 消解条件 |
|---|---|---|
| 1 | 迁移在**进程被 kill**（而非 SQL 错误）时的实际持久化结果 | 需要一次崩溃注入：迁移中途 kill -9，再启动看 `user_version` 与表结构是否自洽 |
| 2 | 上轮 3 条结论的**原始**证据（file:line 级别） | 已被交接文档压缩成一句话（`docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md:212`）；原会话记录不在本会话可见范围 |
| 3 | `stream_ended_with_error` 门禁在生产侧的完整判定分支 | 需通读 `proxy/providers/streaming.rs` 240-810 与 `response_processor.rs` 的收敛路径 |
