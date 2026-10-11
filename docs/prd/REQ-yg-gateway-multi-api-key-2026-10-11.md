---
last_updated: 2026-10-11
status: 待确认（已按设计审查修订）
owner: YG
modification_level: L3（架构 / 数据模型 / 鉴权链路承重墙）
complexity: C 耦合（鉴权 + 存储 + 派发，跨模块责任）
research: docs/reference/multi-key-competitive-notes.md
review: 设计审查结论见 §九（已采纳 6 决策方向 + 补齐 5 缺口）
---

# YG Gateway 多 API Key 派发 — 需求分析（PRD 级，v2）

> 十阶段流水线阶段 1 output。基线：2.0.0，单 `api_key` 鉴权。
> 本期（D-102 分期）范围：**配置驱动的多 Key 鉴权**，面板 Key 管理页二期再做。
> 调研来源：workbuddy-manager（SQLite + SHA256 前缀索引）、workbuddy2api-hub（文件 + 明文 hmac）、
> ARP（SQLite + 三态白名单），详见 `docs/reference/multi-key-competitive-notes.md`。
>
> **v2 变更**：已按设计审查补齐 5 个规格缺口（id / realm 执行规则 / 白名单执行位置 /
> 空值语义 / 可验收约束），并把 D-201~D-205 的精确定义写死。源码事实已取证，见 §八。

---

## 一、范围边界

### 做什么

1. **配置层支持多 Key**（F-001）：`config.json` 新增 `api_keys` 数组，与现有单值
   `api_key` 并存；任一匹配即鉴权通过。
2. **多 Key 鉴权原语**（F-002）：扩展 `internal/httpauth`，从单 key 比较升级为
   「SHA256 摘要前缀索引缩小候选桶 → 完整摘要常量时间比较」，保持 R5 红线不破。
3. **单 Key 向后兼容**（F-003）：现有 `api_key` 字段语义不变，`api_keys` 缺失时
   行为与 2.0.0 完全一致，零迁移。
4. **每 Key 约束**（F-004）：每个 Key 带稳定 `id`、`models`（模型白名单三态）与
   `realm`（版本归属），约束在**选号/出站之前**执行。
5. **鉴权结果透出**（F-005）：匹配到的 Key 的 `id`/`name` 进入请求上下文，
   供日志/用量归因；请求上下文**不携带 Key 明文**。

### 不做什么（明确排除）

1. **不做面板 Key 管理页**（D-102 分期）：Key 只在 `config.json` 里写。
2. **不做 Key 额度/限流/过期/软删除**：`quota` / `max_ips` / `expires_at` /
   `deleted_at` 均二期。
3. **不改选号/冷却/熔断/出站管线**：多 Key 只改「谁有资格进来」。
4. **不改上游协议报文**（R8 红线）。
5. **不做用量按 Key 聚合统计**（二期）：但 F-005 必须把 Key 身份链路打通，避免二期返工。

---

## 二、需求（F-编号）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-001 | `config.json` 新增 `api_keys` 数组，与单值 `api_key` 并存 | P0 |
| F-002 | `httpauth` 扩展多 Key 匹配：摘要前缀索引 + 完整摘要常量时间比较 | P0 |
| F-003 | 单 Key 向后兼容：`api_keys` 缺失时行为与 2.0.0 一致 | P0 |
| F-004 | 每 Key 带稳定 `id` + `models` 三态白名单 + `realm` 版本约束，选号前执行 | P0 |
| F-005 | 匹配到的 Key 的 `id`/`name` 进入请求上下文（不含明文），供归因 | P1 |

### 验收标准（Given-When-Then）

- **F-001**：Given 配置同时含 `api_key:"A"` 与 `api_keys:[{id:"k1",key:"B"},{id:"k2",key:"C"}]`
  / When 分别用 A、B、C 请求 / Then 三者均通过；用 D 请求 / Then 401
- **F-002**：Given ≥2 把 Key / When 请求携带其中任意一把 / Then 匹配正确，
  且完整摘要比较走 `subtle.ConstantTimeCompare`；前缀只用于定位候选桶
- **F-003**：Given 配置只有 `api_key:"A"`（无 `api_keys` 键）/ When 请求 / Then
  行为与 2.0.0 逐位一致（含空 key=不鉴权、env 覆盖、缺头/坏方案路径）
- **F-004**：Given Key k1 配 `models:["deepseek-v4-flash"]` + `realm:"cn"`
  / When 用 k1 请求白名单外模型或 `global:` 模型 / Then 400（`model_not_allowed` /
  `realm_mismatch`）；白名单内且 cn 模型 / Then 放行
- **F-005**：Given 用 Key k1 请求 / When 日志与上下文 / Then 含 `key_id="k1"`、`key_name`
  可读，且**不含** Key 明文、不含 Authorization 原文

### Spec 门禁

1. 每条 F 编号映射 ≥1 有牙齿测试（还原 bug 必须 FAIL）。
2. R5 不破：测试断言完整摘要比较走 `subtle.ConstantTimeCompare`，前缀仅索引。
3. R8 不破：出站头/body/协议字段与改动前逐项一致（真实处理链路断言，非单测 httpauth）。
4. 单 Key 回归：2.0.0 现有 `httpauth` 测试全绿不动。
5. 并发热更新：`-race` 下并发请求 + 配置切换，验证不读到半新半旧快照。

---

## 三、数据模型（config.json 新增结构）

```json
{
  "api_key": "sk-单值兼容，空=未用单值",
  "api_keys": [
    {
      "id": "user-001",
      "key": "sk-xxx",
      "name": "小明",
      "models": ["deepseek-v4-flash"],
      "realm": "cn"
    }
  ]
}
```

| 字段 | 类型 | 语义 | 缺省/约束 |
|---|---|---|---|
| `id` | string | **稳定身份标识**（归因/统计用，改名不影响关联） | 必填；非空；不重复；≤64，`[A-Za-z0-9_-]`（R4） |
| `key` | string | 明文 Key（配置源）；运行时转 SHA256 摘要 | 必填；空条目在 normalize 忽略 |
| `name` | string | 展示名（可重复、可改） | 空 |
| `models` | []string | **三态**：缺失=不限 / `[]`=全拒 / 非空=白名单 | 缺失（三态需自定义反序列化，见 §五） |
| `realm` | string | 版本归属 `cn`/`global`/空=不限 | 空；其他值 → 配置校验失败 |

**Key 生成规则**：一期要求用户在 config 里显式填 `id`（保证重启稳定）。`id` 重复 →
normalize 校验失败（fail fast），不静默去重。

---

## 四、配置空值语义（正式契约）

`api_key` 与 `api_keys` 的组合行为表——**区分「旧配置」与「显式新配置」**：

| `api_key` | `api_keys` | 行为 |
|---|---|---|
| 非空 | 缺失 | 旧版单 Key 鉴权（2.0.0 行为） |
| 空 | 缺失 | 旧版空 Key 语义（不鉴权，2.0.0 行为） |
| 非空 | 有 ≥1 有效条目 | 单值 + 多 Key 均放行 |
| 空 | 有 ≥1 有效条目 | 启用多 Key 鉴权 |
| 空/非空 | 显式 `[]` | **配置校验失败**（fail fast，不静默降级为免鉴权） |
| 空/非空 | 字段存在但全无效条目 | **配置校验失败**（fail fast，不意外开放） |

**判定「字段存在」的方式**：`api_keys` 用自定义反序列化保留「键存在」信息，区分
「键缺失」（旧配置）与「键存在但 `null`/`[]`/全空」（显式新配置）。只有「键缺失」
才走旧版兼容路径。

---

## 五、关键技术契约（把审查缺口写死）

### 5.1 运行时索引（D-201 修正：区分「配置明文」与「运行时摘要」）

- **配置文件**：`api_keys[].key` 保存明文（一期用户手填，无法避免），文件按敏感
  凭证处理（`0o600`，与 config.json 同级权限）。
- **运行时索引**：只保存 `sha256(key)` 完整摘要 + 摘要前缀 + `id`/`name`/约束。
  **不额外复制明文**，日志/错误/配置接口响应不出现 Key 明文或 Authorization 原文。
- **摘要定义**：`sha256(key)` 的 32 字节，hex 编码后前 12 个十六进制字符作为**索引前缀**
  （D-202 修正：前缀取**摘要**而非原始 Key，避免不同 Key 格式歧义，也不保留凭证原文前缀）。
- **匹配**（D-203 修正）：`map[prefix][]candidate` 结构，前缀桶可含多个候选（碰撞不覆盖）；
  桶内对完整摘要做 `subtle.ConstantTimeCompare`。前缀仅缩小候选集，**最终校验永远是完整摘要**。

### 5.2 模型白名单执行位置（审查缺口 3）

接收请求 → 提取 Bearer 与请求 `model` → 多 Key 鉴权（识别 Key 身份）→
**realm 与 models 校验**（不符合即拒绝，不进入后续）→ 既有选号/出站管线。

白名单校验**必须在选号和出站之前**，被拒请求零上游消耗。流式与非流式同约束。

**模型名比较规则**：
- 先 `ResolveModel(model)`（`internal/server/resolve_model.go`）得到 `realm` + `bare`。
- 白名单比对用 `bare`（裸名），大小写敏感、不 trim（与现有 `ResolveModel` 一致）。
- 请求缺 `model` / `model` 非字符串 / 空串：若 Key 配了 `models` → 400（与 manager 一致，
  不能因缺 model 跳过白名单）；Key 未配 `models` → 走既有缺省模型逻辑。

### 5.3 realm 约束执行规则（审查缺口 2）

复用现有 `ResolveModel` 判定「目标版本」，规则表：

| `realm` 配置 | 语义 |
|---|---|
| 缺失或空 | 不限制版本 |
| `"cn"` | 只能访问 cn 版本（裸名 / `cn:` 前缀，`ResolveModel` realm=cn） |
| `"global"` | 只能访问 global 版本（`global:` 前缀，realm=global） |
| 其他值 | 配置校验失败 |

错误码 `realm_mismatch`，400，不泄漏 Key 身份。

### 5.4 鉴权结果透出（审查缺口 1：补 id）

请求上下文新增 `key_id` / `key_name`（来自匹配 Key 的 `id`/`name`），**不存 Key 明文**。
日志只记 `key_id`（短、稳定）；`key_name` 仅供展示。二期用量按 `key_id` 聚合时无需改链路。

### 5.5 热更新原子性（审查缺口 5）

- 配置更新时**构建完整新鉴权快照**，校验成功（含空值语义校验）后**一次 `atomic.Store` 原子切换**；
  并发请求要么读到全旧、要么读到全新，**禁止读到半新半旧**。
- 校验失败 → 不落盘、不切换，返回 400 并保留旧快照。

### 5.6 env 覆盖策略（审查缺口 5）

- `WB2A_API_KEY` 只覆盖**单值** `api_key`，**不覆盖** `api_keys` 数组（避免 env 无意抹掉多 Key）。
- 一期**不新增** `WB2A_API_KEYS` env（多 Key 只走 config 文件，控制面单一）。

### 5.7 配置保存保留 api_keys（审查缺口 5 + 源码已证）

面板保存其他配置时，`api_keys` **原样保留**。源码事实：`mergeConfigMaps`（main.go:563）
深合并、`collectConfig`（app.js:1033）白名单字段不含 `api_keys` → 面板不提交它 → 不丢。
**测试需钉住这一点**（回归）。

---

## 六、技术决策（D-编号，最终定义）

| D-编号 | 决策 | 最终定义 |
|---|---|---|
| D-200 | Key 存储位置 | **A**：并入 config.json（`api_keys` 数组），single source of truth |
| D-201 | Key 哈希 | **A 修正**：配置存明文，运行时索引只存 SHA256 摘要 + 元数据 |
| D-202 | 前缀 | **B 修正**：摘要 hex 前 12 字符做索引前缀（非原始 Key 前缀） |
| D-203 | 匹配 | **B 修正**：`map[prefix][]candidate`，碰撞桶多候选，完整摘要常量时间比较 |
| D-204 | 空值语义 | **B 修正**：显式配置但无有效 Key → 启动/保存校验失败（见 §四） |
| D-205 | models 三态 | **A 修正**：自定义反序列化保留「键存在」，三态语义见 §三 |

---

## 七、非功能约束

- **性能**：索引查找 O(1) 平均 + 桶内常量时间比较；热路径零新分配用 `go test -bench`
  + `-benchmem` 验证（不能只靠设计推断）。
- **安全**：运行时无 Key 明文；匹配常量时间（限定在摘要比较原语及其使用方式，不承诺
  整个 HTTP 流程无任何时序差异）；错误响应不泄漏「哪把 Key 存在/失败」细节。
- **兼容**：`api_keys` 键缺失 → 2.0.0 逐位一致；旧 `WB2A_API_KEY` env 语义不变。
- **成本**：零新依赖（stdlib `crypto/sha256` + `crypto/subtle` + `sync`），不引 SQLite。

---

## 八、影响分析与源码取证

- **影响面**：`internal/httpauth`（核心扩展）、`cmd/server/config.go`（字段+normalize+
  自定义反序列化）、`internal/livecfg`（快照加 APIKeys）、`internal/server/handler.go`
  + `internal/panel/panel.go`（withAuth 接线）、`cmd/server/main.go`（saveConfig 热应用）。
- **承重墙**：⚠️ 触及凭证链路（httpauth 单值→多值），需 ADR 记录迁移与兼容策略。
  ❌ 不触及选号/冷却/熔断/出站管线。
- **已取证源码事实**：
  - `httpauth.VerifyBearer(r, key string)` 单值签名（httpauth.go:25），需扩展为多 Key。
  - 两处调用点：`handler.go:197`、`panel.go:203`，均经 `livecfg.APIKey`。
  - `livecfg.Snapshot.APIKey` 单值（livecfg.go:18），需加 `APIKeys`。
  - `saveConfig` 热应用 `live.Store(...)`（main.go:499），需加多 Key 快照。
  - `ResolveModel(model) (realm, bare)` 现成（resolve_model.go:26），realm 约束复用。
  - `mergeConfigMaps` 深合并保留 `api_keys`（main.go:563）；`CFG_MAP` 白名单不含
    `api_keys`（app.js:968），面板不提交 → 不丢。
- **回滚路径**：删掉 `api_keys` 键即回单 Key；二进制备份 + git 回退。

---

## 九、设计审查结论（本轮修订依据）

采纳审查 6 决策方向 + 补齐 5 缺口：

1. **补 `id`**：F-005 需要稳定身份，`name` 可重复可改，不能做归因键。
2. **realm 执行规则**：复用 `ResolveModel` 判目标版本，规则表见 §5.3。
3. **白名单执行位置**：选号/出站之前，零上游消耗，见 §5.2。
4. **空值语义**：六态契约表见 §四，显式空配置 fail fast。
5. **可验收约束**：热更新原子切换 + 零分配 benchmark + 出站逐项比对，见 §五/§七。

---

## 十、测试门禁清单（一期 8 类）

| # | 类别 | 关键场景 |
|---|---|---|
| 1 | 旧版兼容 | 无 `api_keys`、空 `api_key`、env 覆盖不回归 |
| 2 | 多 Key 正确性 | 单/多 Key 混用、重复 Key、重复 id、前缀碰撞、无效配置 |
| 3 | 安全性 | 完整摘要比较、未知 Key 拒绝、日志不泄漏、无效配置不开放 |
| 4 | 白名单 | models 缺失/`[]`/指定、缺 model、异常 model、流式+非流式 |
| 5 | 版本约束 | cn/global/空 realm、无法判目标版本 |
| 6 | 并发热更新 | `-race` 下并发请求+切换，无半新半旧快照 |
| 7 | 出站协议 | 请求头/body/协议字段与改动前逐项一致（真实链路） |
| 8 | 性能 | `go test -bench` + `-benchmem` 测鉴权耗时与每请求分配 |

---

## 十一、下一步

1. 你确认 v2 PRD（或再改）
2. 写 ADR-001（`docs/adr/ADR-001-multi-api-key.md`）：记录 D-200~D-205 + 迁移/兼容/回滚
3. 任务拆分（配置解析 / 鉴权索引 / 上下文透传 / 白名单校验 / 热更新 / 测试，各 ≤4h）
4. 先写关键测试（旧兼容 / 鉴权安全 / 前缀碰撞 / 出站回归）
5. 进编码实现
