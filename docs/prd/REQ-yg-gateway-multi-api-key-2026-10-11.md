---
last_updated: 2026-10-11
status: 待确认
owner: YG
modification_level: L3（架构 / 数据模型 / 鉴权链路承重墙）
complexity: C 耦合（鉴权 + 存储 + 派发，跨模块责任）
research: docs/reference/multi-key-competitive-notes.md
---

# YG Gateway 多 API Key 派发 — 需求分析（PRD 级）

> 十阶段流水线阶段 1 output。基线：2.0.0，单 `api_key` 鉴权。
> 本期（D-102 分期）范围：**配置驱动的多 Key 鉴权**，面板 Key 管理页二期再做。
> 调研来源：workbuddy-manager（SQLite + SHA256 前缀索引）、workbuddy2api-hub（文件 + 明文 hmac）、
> ARP（SQLite + 三态白名单），详见 `docs/reference/multi-key-competitive-notes.md`。

---

## 一、范围边界

### 做什么

1. **配置层支持多 Key**（F-001）：`config.json` 新增 `api_keys` 数组，与现有单值
   `api_key` 并存；任一匹配即鉴权通过。
2. **多 Key 鉴权原语**（F-002）：扩展 `internal/httpauth`，从单 key 比较升级为
   「前缀索引缩小候选集 → 常量时间哈希比较」，保持 R5 红线（常量时间）不破。
3. **单 Key 向后兼容**（F-003）：现有 `api_key` 字段语义不变（空 = 不鉴权），
   多 Key 数组缺失时行为与 2.0.0 完全一致，零迁移。
4. **每 Key 可选约束**（F-004）：每个 Key 可带 `models`（模型白名单）与
   `realm`（版本归属 cn/global），一期只做这两维；enabled/过期/额度留二期。
5. **鉴权结果透出**（F-005）：匹配到的 Key 身份（id/name）进入请求上下文，
   供日志/用量归因（本期只打通链路，用量按 Key 聚合留二期）。

### 不做什么（明确排除）

1. **不做面板 Key 管理页**（D-102 分期）：本期 Key 只在 `config.json` 里写，面板配置页
   仅把 `api_keys` 作为普通 JSON 字段透出/保存，不做增删改查 UI。
2. **不做 Key 额度/限流**：`quota` / `quota_credit` / `max_ips` 是 manager 的 B 端
   能力，YG 一期不做（用量统计已有，额度限制是另一层语义）。
3. **不做软删除**：一期 Key 删除即从配置里删掉（文件态，无历史归属问题）；软删除
   是「用量按 Key 聚合」落地时才需要的配套，二期再上。
4. **不改选号/冷却/熔断/出站管线**：多 Key 只改「谁有资格进来」，进来的请求照旧
   走既有选号调度，选号逻辑零改动。
5. **不改上游协议报文**（R8 红线）：多 Key 是入站鉴权，出站报文形态完全不动。

---

## 二、需求（F-编号）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-001 | `config.json` 新增 `api_keys` 数组，与单值 `api_key` 并存 | P0 |
| F-002 | `httpauth` 扩展多 Key 匹配：前缀索引 + 常量时间比较 | P0 |
| F-003 | 单 Key 向后兼容：`api_keys` 缺失时行为与 2.0.0 一致 | P0 |
| F-004 | 每 Key 可选 `models`（模型白名单）与 `realm`（版本归属） | P1 |
| F-005 | 匹配到的 Key 身份进入请求上下文（日志/用量归因链路） | P1 |

### 验收标准（Given-When-Then）

- **F-001**：Given `config.json` 同时含 `api_key: "A"` 与 `api_keys: [{key:"B"}, {key:"C"}]`
  / When 分别用 A、B、C 请求 / Then 三者均鉴权通过；用 D 请求 / Then 401
- **F-002**：Given 多把 Key / When 请求携带其中任意一把 / Then 匹配正确，
  且比较路径仍走 `subtle.ConstantTimeCompare`（不退化回 `==` 明文比较）
- **F-003**：Given `config.json` 只有 `api_key: "A"`（无 `api_keys` 键）
  / When 请求 / Then 行为与 2.0.0 逐位一致（含空 key = 不鉴权）
- **F-004**：Given Key B 配 `models: ["deepseek-v4-flash"]` / When 用 B 请求
  白名单外模型 / Then 400 模型不在白名单；白名单内模型 / Then 放行
- **F-005**：Given 用 Key B 请求 / When 日志/用量记录 / Then 能标识来源 Key 的 id/name

### Spec 门禁

1. 每条 F 编号映射 ≥1 有牙齿测试（L3 级强制，还原 bug 必须 FAIL）。
2. 鉴权路径改动不破 R5（常量时间）——测试需断言多 Key 匹配走 httpauth 原语。
3. 出站报文形态零改动（R8）——测试需断言出站头/body 与改动前一致。
4. 单 Key 行为回归测试：2.0.0 现有 `httpauth` 测试必须全绿不动。

---

## 三、技术方案（D-编号决策点，待拍板）

| D-编号 | 决策 | 选项 | 建议 | 状态 |
|---|---|---|---|---|
| D-200 | Key 存储位置 | A 并入 config.json（`api_keys` 数组）/ B 独立 keys.json | **A**：与现有单值 `api_key` 同处，配置页透出零成本，single source of truth | 待拍板 |
| D-201 | Key 哈希 | A 存 SHA256 摘要 / B 存明文 | **A**：R5 已定常量时间摘要，存摘要不倒退；前缀索引仍需存明文前缀 | 待拍板 |
| D-202 | Key 前缀长度 | A 8 位 / B 12 位（对齐 manager） | **B**：12 位前缀，与 manager 一致，候选集足够小 | 待拍板 |
| D-203 | 匹配顺序 | A 数组序 / B 前缀索引序 | **B**：先按前缀 hash 到桶，再桶内常量时间比，避免 N 把线性遍历 | 待拍板 |
| D-204 | 空 `api_key` + 非空 `api_keys` 的语义 | A 空 key 仍不鉴权（多 Key 也失效）/ B 空 key 但多 Key 非空时启用多 Key | **B**：有 `api_keys` 就鉴权，单值空只是「未用单值」 | 待拍板 |
| D-205 | `models` 三态语义 | A 缺失=不限/空[]=全拒/非空=白名单（对齐 ARP）/ B 缺失=不限/空[]=不限 | **A**：三态严谨，避免「空数组是不限还是全拒」歧义 | 待拍板 |

---

## 四、非功能约束

- **性能**：鉴权匹配 O(1) 前缀定位 + 桶内常量时间比较，不随 Key 数线性退化；
  请求路径零新分配（复用现有 sha256 摘要）。
- **安全**：Key 摘要不可逆（SHA256），不存明文；匹配走常量时间（R5）；日志/错误
  响应不泄漏「哪把 Key 匹配失败」的细节（避免探测存活 Key）。
- **兼容**：`api_keys` 缺失 → 行为与 2.0.0 逐位一致；`api_key` 仍支持 env 覆盖
  （`WB2A_API_KEY`），新增 `WB2A_API_KEYS` env 覆盖（一期可选）。
- **成本**：零新依赖（stdlib `crypto/sha256` + `crypto/subtle` + `sync` map）。
  不引 SQLite（D-101 已定文件存储，保持单二进制哲学）。

---

## 五、影响分析

- **影响面**：
  - `internal/httpauth/httpauth.go` — 核心扩展（F-002）
  - `cmd/server/config.go` — `APIKeys` 字段 + normalize（F-001）
  - `internal/livecfg/livecfg.go` — 快照加 `APIKeys`（F-001 热生效）
  - `internal/server/handler.go` — `withAuth` 改传多 Key（F-002 接线）
  - `internal/panel/panel.go` — `withAuth` 改传多 Key（F-002 接线）
  - `cmd/server/main.go` — `saveConfig` 热应用多 Key（F-001）
- **承重墙**：⚠️ **触及凭证链路**（httpauth 鉴权口径从单值变多值）。这是 L3 承重墙，
  必须 ADR 记录「单 Key → 多 Key」的迁移与兼容策略。❌ 不触及选号/冷却/熔断/出站管线。
- **契约变化**：
  - 401 语义不变（仍是 invalid_api_key）
  - 新增 400 语义（F-004 模型白名单拒绝）——`model_not_allowed`
  - `config.json` 新增 `api_keys` 键，旧配置无此键零影响
- **回滚路径**：二进制备份 + git 回退；`api_keys` 键删除即回到单 Key 行为（无迁移包袱）

---

## 六、数据模型（config.json 新增结构）

```json
{
  "api_key": "sk-单值兼容，空=未用单值",
  "api_keys": [
    {
      "key": "sk-xxx",
      "name": "小明",
      "models": ["deepseek-v4-flash"],
      "realm": "cn"
    }
  ]
}
```

| 字段 | 类型 | 语义 | 缺省 |
|---|---|---|---|
| `key` | string | 明文 Key（内存转摘要） | 必填，空条目忽略 |
| `name` | string | 展示名/归属标识 | 空 |
| `models` | []string | 模型白名单三态：缺失=不限 / [] =全拒 / 非空=白名单 | 缺失 |
| `realm` | string | 版本归属 cn/global，空=不限制 | 空 |

---

## 七、决策点（D-编号）待拍板汇总

本文已含 D-200~D-205 六个技术决策（见「三、技术方案」）。加上此前已拍的
D-100/D-101/D-102，共 9 个决策点。D-200~D-205 均附推荐项，等你确认后进 ADR 与编码。

---

## 八、下一步

1. 你确认 D-200~D-205（或授权按建议项全走）
2. 写 ADR（`docs/adr/ADR-001-multi-api-key.md`）：记录「单 Key → 多 Key」迁移决策
3. 任务拆分（≤4h 粒度）
4. 进十阶段流水线编码实现
