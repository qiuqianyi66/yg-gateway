# 竞品密钥派发实现深挖（阶段 0 研究笔记）

> 目标：为「多 API Key 派发」PRD 提供需求分析输入。只挖**具体实现**，不抄代码。
> 来源：浅克隆三个竞品仓库到 `%TEMP%\yg-compete\`，读源码取证。
> 日期：2026-10-11。ARP 为 GPLv3，只借鉴思路/数据结构设计，不抄代码。

---

## 一、三家竞品实现对比（一张表看懂）

| 维度 | workbuddy-manager（ithtelab） | workbuddy2api-hub（ardeyouxipianyi） | ARP（KaiXuanSama） |
|---|---|---|---|
| 语言 | Python + Next.js | Python | Java (Spring Boot) |
| Key 存储 | **SQLite** `api_keys` 表 | **文件** `settings.json` 的 `api_keys` 数组 | **SQLite** `downstream_api_key` 表 |
| Key 哈希 | **SHA-256 hex**，前缀索引 | **明文**（hmac 常量时间比较） | **明文**（findByApiKey 明文查重） |
| Key 匹配 | `WHERE prefix=?` 缩小范围 → `secrets.compare_digest` | 遍历数组 → `hmac.compare_digest` | 明文查询 |
| 白名单维度 | IP / 模型 / 版本(realm) / 配额(token+credit) / max_ips | 仅模型 + realm | 模型 + 指定账号 + 消费模式 |
| 软删除 | 无（直接删行） | **有**：`deleted_at` + secret 清空，id 保留（usage 按 id 归属） | 无 |
| 单 Key 兼容 | 迁移列 | legacy 字段转 unbound entry | — |

---

## 二、各竞品关键实现细节

### 1. workbuddy-manager（功能最全，B 端生产级）

**表结构**（`server/db.py`，`api_keys` 表）：
```
id / name / key_hash / prefix / enabled / expires_at / max_ips
ip_allowlist(JSON) / models(JSON) / realm / quota / used_tokens
quota_credit / used_credit / created_at / last_used_at
+ api_key_ips 表（key_id, ip, first_seen）记录 key 用过的 IP
```

**哈希与匹配**（`server/keysvc.py`）：
- `_hash(token) = sha256(token).hexdigest()`
- `prefix = token[:12]`，建索引 `idx_keys_prefix`
- 匹配：`SELECT * FROM api_keys WHERE prefix=?` → 候选集 → `secrets.compare_digest(key_hash, digest)` 常量时间

**白名单校验顺序**（`keysvc.validate`，这是关键——顺序即语义）：
```
1. enabled 校验
2. expires_at 过期校验
3. quota（token 额度）已用尽 → 429
4. quota_credit（积分额度）已用尽 → 429
5. ip_allowlist 白名单不匹配 → 400（不用 401，让报文透出提示用户）
6. max_ips 绑定 IP 数超限 → 400
7. realm 版本归属不匹配 → 400
8. models 模型白名单不匹配 → 400
```

**三个值得学的设计**：
- **前缀索引 + 常量时间比较**：Key 多了以后，先按 `prefix` 缩小候选集，再常量时间比哈希。避免 N 把 Key 线性遍历的时序侧信道。
- **白名单坏数据容错**：`ip_allowlist`/`models` 是 JSON 文本列，解析失败按「空列表=不限制」处理（fail-open），宁可放宽也不让一把坏 Key 拖垮整个列表页。因为白名单是**附加收紧项**，不是唯一防线。
- **realm 版本归属**：Key 可限定「只能调国内版/国际版模型」，判定用 `mapped_model`（映射后的实际路由名），与真实流量口径一致。

### 2. workbuddy2api-hub（最轻量，最接近 YG 现状）

**存储**（`wb_settings.py`）：`settings.json` 的 `api_keys` 数组，字段：
```
id / name / key(明文) / realm / models / enabled / created_at / deleted_at
```

**匹配**（`match_api_key`）：
```python
for entry in api_keys(accounts_dir):
    if entry["enabled"] and not entry.get("deleted_at") \
            and hmac.compare_digest(supplied, entry["key"]):
        return entry
```

**两个值得学的设计**：
- **软删除**：删 Key 时 `deleted_at` 打时间戳 + secret 清空 + enabled=False，但 **id 保留**。因为 usage.jsonl 按 key id 归属，删 id 会把历史用量丢进「(未知 key)」。YG 现在用量也是按账号/模型聚合的，如果加 Key 维度，同样要面对这个「删 Key 后历史用量归属」问题。
- **legacy 兼容**：旧版本只有单 `api_key` 字段，读取时自动转成一个 unbound entry，升级零迁移。YG 现在就是单 api_key，多 Key 化时要做同样的兼容。

### 3. ARP（GPLv3，借鉴受限）

**数据模型**（`DownstreamApiKeyRecord`）：
```
id / label / apiKey(明文) / callCount / usedCredits / creditLimit / expiresAt
enabled / consumption / designatedAccountId / supportedModels / createdAt / updatedAt
```

**值得学的两个设计**：
- **supportedModels 三态语义**：`null`=全集（向后兼容）/ `[]`=严格全拒 / `["a","b"]`=白名单。这个三态比「空=不限」更严谨，避免「空数组到底是不限还是全拒」的歧义。
- **designatedAccountId 指定账号绑定**：Key 可绑定到特定账号（选号时优先/独占该账号）。这是「给朋友开 Key，指定他用哪个号」的能力。
- **generateApiKey**：前缀 + 随机字母表，基数 2.4e56，重试 5 次查重。

---

## 三、对 YG Gateway 的启示（决策输入）

### 启示 1：存储选型——建议**文件存储**（对齐 hub，而非 manager/ARP 的 SQLite）

理由：
1. **三家里两家用 SQLite，但 YG 的差异化核心是「单二进制零依赖」**。引入 SQLite 要么 `mattn/go-sqlite3`（cgo，违背 `CGO_ENABLED=0`），要么 `modernc.org/sqlite`（纯 Go，但引入重依赖 + 增大体积）。
2. **多 Key 场景规模小**：YG 是「自用 → 给朋友/小团队用」，几十把 Key 顶天，不是 manager 那种「卖 Key 给一群人」的量级。JSON 文件 + 前缀索引完全够。
3. **对齐现有模式**：state.json / usage.json 已经是文件态，多 Key 也走文件，模式统一、迁移简单。
4. manager 用 SQLite 是因为它还有 request_logs、usage_daily、api_key_ips 等一堆关联表，需要关系查询。YG 的多 Key 需求（Key 列表 + 鉴权匹配 + 每 Key 用量）用文件 + 内存 map 足够。

### 启示 2：Key 哈希——建议 **SHA-256 + 前缀索引 + 常量时间**（对齐 manager 和现有 httpauth）

- YG 现有 `httpauth.VerifyBearer` 已经是 SHA-256 摘要 + `subtle.ConstantTimeCompare`，多 Key 化时**沿用这个原语**，扩展成「先按前缀缩小候选集 → 常量时间比哈希」。
- 不建议像 hub/ARP 那样明文存 Key（虽然实现简单，但 YG 的编码规范 R5 已经定了密钥比较走 httpauth 常量时间，明文存库是倒退）。

### 启示 3：白名单匹配顺序——参考 manager 的顺序，但裁剪

YG 不需要全套（max_ips 是 manager 的 B 端能力，YG 可以不要），建议顺序：
```
enabled → expires_at → models(模型白名单) → realm(版本归属)
```
额度（quota）留作二期（YG 现在没有 token/credit 额度概念，用量统计里能查到但没做成「限流」）。

### 启示 4：软删除——**建议采纳 hub 的软删除**（删 Key 保留 id）

YG 如果给用量加 Key 维度，删 Key 后历史用量要能追溯到「这是已删除的某把 Key」，而不是变成「未知」。软删除（`deleted_at` + secret 清空 + enabled=False）是标准解。

### 启示 5：单 Key 兼容——**必须做**（YG 现在就是单 api_key）

YG 现有 config 里 `api_key` 单值，多 Key 化后要：
- 保留 `api_key` 字段作为「默认 Key」或「legacy 兼容」
- 新增 `api_keys` 数组
- 鉴权时先查数组，查不到再 fallback 到单值（或反之）

---

## 四、阶段 0 结论

- **Key 存储**：文件（JSON 数组），对齐现有 state.json 模式，零依赖 ✅
- **Key 哈希**：SHA-256 + 前缀索引 + 常量时间比较 ✅
- **白名单**：enabled → expires → models → realm（裁剪自 manager）✅
- **软删除**：采纳（删 Key 保留 id，历史用量可追溯）✅
- **单 Key 兼容**：必须（legacy fallback）✅
- **明确不抄**：ARP 是 GPLv3，只借鉴三态 models 语义和 designatedAccountId 思路，不碰代码
