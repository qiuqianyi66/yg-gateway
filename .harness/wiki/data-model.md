---
last_updated: 2026-10-20
status: active
owner: YG
---

# 数据模型（YG Gateway）

> 本文件写**落盘布局与不变量**，**不复制字段表**。字段唯一真相在各 struct 定义：
> `internal/pool/entry.go`（`stateAccount` / `stateModelCooldown` / `stateModelCost` / `stateFile`）、
> `internal/usage/usage.go`（`file` / `bucket` / `USDPrice`）。
> 理由同 `api-contract.md`：抄一份必然漂移，而字段漂移比路由漂移更隐蔽
> （路由漂移至少 404，字段漂移让人按不存在的字段写代码）。
>
> 配套：`glossary.md`（领域术语）、`api-contract.md`（接口契约语义）。

## 一、四个落点

| 落点 | 默认路径 | 谁写 | 内容 |
|---|---|---|---|
| 配置 | `config.json` | **人** | 装配期 + 热生效配置 |
| 池状态 | `data/state.json` | 程序 | 账号积分 / 冷却 / 熔断 / 降权 / 成本账本 |
| 用量 | `data/usage.json` | 程序 | token 分桶 + per-key 月度累计 |
| 凭证 | `auths/workbuddy-<uid>.json` | 程序（OAuth） | accessToken / refreshToken 明文 |

🔴 **锚点规则（最重要的一条）**：除 `config.json` 外，所有程序写的数据都以
`state_file` 为锚点、取**同目录兄弟文件**：

```
<state_file 所在目录>/
├── state.json              # 池状态（锚点本身）
├── usage.json              # 用量（usagePathFor 派生）
├── model.json              # 上游模型目录缓存
├── output_probes.json      # 模型上限探测
└── request-logs/
    └── requests-*.jsonl    # 请求归档（archiveFileGlob）
```

改 `state_file` 路径 → **这一整套跟着搬家**。Docker volume 只挂这一个目录就够
（出处：`cmd/server/main.go` 的 `usagePathFor` / `stateSibling`）。

## 二、state.json 的不变量

1. **version 门禁**：缺 `version` 字段的老文件按 `0` 读入、正常加载（零迁移成本）；
   `version` **大于**当前值 → **拒绝启动**。理由：旧程序读新文件会按零值丢账号状态，
   静默且不可逆。同族闸门见 ADR-009。
2. **惰性过滤**：过期条目不写也不恢复——`breaker_until` / `degrade_until` /
   `model_cooldowns` / `model_costs`（6h 外）。熔断期重启不失忆，过期条目不复活。
3. **为什么用 `*time.Time`**：Go 的 `omitempty` 对非指针 `time.Time` 的零值**不生效**
   （会序列化成 `0001-01-01T00:00:00Z`）；指针 `nil` 才能真正被省略，与「过期不写」口径一致。
4. **`err_count` → `err_total` 一次性迁移**：旧字段仍可读（加载时映射），**不再回写**。
5. **运行态计数不用 `omitempty`**：`soft_streak` / `session_dead_fails` /
   `consecutive_fails` / `credits_expiring`——零值缺失会让人误以为「没记录」，
   实际是零值被省略。这是**故意**的。

## 三、auths/ 的不变量

- 文件名 `workbuddy-<uid>.json`；UID 白名单 `[A-Za-z0-9_-]` 且 ≤64 字符（**防路径穿越**）。
- 凭证**明文存储** → 落盘 `0600` + Windows ACL 加固（R1）。
- 🔴 **不随二进制回滚**：删除即不可恢复，需重新 OAuth。换版备份时**不要**把 `auths/`
  一起回退。

## 四、config.json 的两类字段

| 类 | 含义 | 例 |
|---|---|---|
| **热生效** | 面板改后下一个请求即用新值，无需重启 | `api_key` / `cooldown` / `pool` / `schedule` / 脱敏开关 |
| **装配期** | 改了必须重启进程 | `listen` / `auth_dir` / `upstream` / `upstash` |

（更完整的口径见 `glossary.md`「热生效」条；`config.example.json` 是字段全集示例。）

## 五、删除与重建语义（**部分 unknown，未实测**）

| 删什么 | 后果 | 四态 |
|---|---|---|
| `auths/` | 全部账号需重新 OAuth | observation（R1 明写） |
| `usage.json` | 用量历史丢失；不影响选号 | inference（未实测，但用量不参与选号决策） |
| `state.json` | **未实测**。推测：账号回到无积分/无冷却初态并从 `auths/` 重建，成本账本 `model_costs` 与 EWMA 丢失 | **unknown**。`requires`：在沙箱实例上删一次再启动，观察选号与面板 |

> 第三行是推测不是观察，所以标 unknown。要消解按 `requires` 做一次实测，
> **不许**把推测写进部署文档或交接（否定性/肯定性断言都要证据，见 `AGENTS.md`）。

## 六、请求归档的留存

`logging.request_archive_enabled` / `request_retention_days` / `request_archive_max_mb` /
`request_client_info` 控制 `request-logs/` 的归档行为。默认值见 `config.example.json`；
**清理策略是否自动执行、按什么周期跑：未核实**，读 `internal/reqlog` 后再下结论。
