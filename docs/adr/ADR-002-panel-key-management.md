# ADR-002：面板 Key 管理页架构决策

> 日期：2026-10-11
> 状态：已交付（卡片+弹窗风格，已合并 main）
> 依赖：ADR-001（多 API Key 鉴权，一期已交付）
> PRD：`docs/prd/REQ-yg-gateway-panel-key-management-2026-10-11.md`

---

## 背景

一期（ADR-001）完成了多 Key 鉴权的全部后端能力：前缀索引 + 常量时间比较、
`normalizeAPIKeys` 校验、`loadConfigMasked` 脱敏回显、`mergeConfigMaps` 深合并
保留 `api_keys`、`SaveConfig` 热应用。多 Key 功能在鉴权层已完整可用，但面板
无管理 UI——用户只能手工编辑 `config.json` + POST 热生效。

二期目标：给多 Key 能力套面板 UI，让用户在浏览器里列表/新增/删除/编辑 Key。

---

## 决策

### D-301：独立视图，不嵌入现有配置页

`view-keys` 作为第 8 个面板视图，与 `view-config` 平级。

**理由**：
1. `view-config` 的 `cfgForm` 是扁平字段映射（`CFG_MAP`），每个表单元素对应
   一个配置路径。`api_keys` 是数组结构，每条含 5 个子字段，嵌套进 `CFG_MAP`
   要改 `collectConfig` 序列化逻辑 + `dig`/`put` 的数组索引处理，复杂度高且
   易破坏现有配置页稳定性。
2. 独立视图可独立 `loadKeys()`/`saveKeys()`，不触碰 `CFG_MAP`/`collectConfig`，
   隔离风险。
3. 导航多一项「API Key」符合用户心智模型——Key 管理是独立功能域。

### D-302：复用 `POST /panel/api/config`，零新增后端接口

前端组装完整 `api_keys` 数组 → `{ "api_keys": [...] }` → POST config。
后端 `mergeConfigMaps` 对数组整体替换 → `normalizeAPIKeys` 校验 → 落盘 → 热应用。

**理由**：
1. 一期 `SaveConfig` 闭包已完整支持 `api_keys` 的校验+落盘+热应用链路。
2. `mergeConfigMaps` 对 `api_keys` 数组走 `cur[k] = v`（整体替换），正好是
   二期想要的语义——编辑后提交完整列表，后端整体替换。
3. 新增接口意味着改 `panel.go` 路由 + `panel` 包的 `SaveConfig` 闭包签名，
   牵连面大。复用零牵连。

### D-303：行内编辑，不做模态弹窗

列表行内「编辑」按钮 → 该行切换为编辑模式（`<tr>` 内嵌表单）。
新增 → 列表顶部展开一行空白表单。

**理由**：
1. `app.js` 是 2950 行单文件（go:embed），加模态弹窗要管理 overlay DOM +
   事件委托 + 焦点陷阱，成本高。
2. YG 的 Key 数量少（自用/小团队，几十把顶天），行内编辑足够。
3. 现有面板风格是表单 + 表格驱动（如账号池、配置页），不是弹窗驱动。

### D-304：删除最后一条 Key 提交 null，不提交空数组

前端检测 `api_keys` 删除后长度 == 0 → 提交 `{ "api_keys": null }`。

**理由**：
1. 一期空值契约（ADR-001 D-200）：显式 `[]` = fail fast（用户明确要求多 Key
   但一条都没配，是配置错误）；`null` = 键缺失 = 旧兼容（退化为单 Key 模式）。
2. 删除最后一条 Key 的用户意图是「不用多 Key 了，回到单 Key」，不是「我要多 Key
   但一条都不配」。提交 null 符合用户意图。
3. 提交 `[]` 会被 `normalizeAPIKeys` 拒绝（400），用户看到错误会困惑。

### D-305：models 三态前端序列化对齐 Go nil 语义

前端 models 编辑框：
- 空输入 → 提交时不带 `models` 字段（JSON.stringify 省略 undefined）→ 后端
  unmarshal 为 nil slice = 缺失 = 不限制。
- 有值 → `value.split(',').map(s=>s.trim()).filter(Boolean)` → 数组。

**理由**：
1. 对齐一期 Go slice nil 区分（ADR-001 §四）：`models` 缺失 = nil = 不限制；
   `[]` = 非nil空 = 全拒；`["a"]` = 白名单。
2. 前端 JSON 里 `undefined` 被 `JSON.stringify` 省略，后端 `json.Unmarshal`
  到 `[]string` 时 nil = 键缺失。语义对齐。

---

## 不变量

1. **密钥不明文回显**：`loadConfigMasked` 已把 `api_keys[].key` 留空。前端列表
   只显示 `key_configured` 状态。编辑时密钥框 password 类型 + placeholder
   「留空 = 不修改」。
2. **保存提交完整数组**：`collectKeyList()` 从 DOM 收集完整 `api_keys`，因为
   `mergeConfigMaps` 对数组整体替换（不是深合并数组元素）。
3. **id 不可编辑**：编辑模式 id readonly。id 是稳定标识 + 用量归因键，改 id
   等于删旧增新，会断归因链路。
4. **保存后必须刷新**：后端 `normalizeAPIKeys` 可能过滤空条目/调整顺序，前端
   必须重新 GET 刷新以保持一致。

---

## 风险与缓解

| 风险 | 缓解 |
|---|---|
| 前端组装 api_keys 时漏了某条 → 误删 | collectKeyList 从 DOM 表格逐行收集，不依赖内存状态；保存前 toast 显示「将提交 N 条 Key」 |
| 删除最后一条误提交 [] → 400 | collectKeyList 检测长度 0 时返回 null 而非 [] |
| models 空输入语义歧义（不限 vs 全拒） | 编辑框 placeholder 明确「留空 = 不限制（缺失）」；全拒需显式输入「（空）」不可达——二期不支持前端构造全拒，全拒只能手编 config.json |

---

## 承重墙声明

本期**不触及鉴权链路承重墙**（httpauth/livecfg/handler 的鉴权逻辑一行不动）。
改动范围：
- `internal/panel/index.html`：+1 导航项 + 1 section
- `internal/panel/app.js`：+1 视图路由 + Key 管理函数（~150 行）
- `cmd/server/main.go`：零改动（loadConfigMasked/mergeConfigMaps 已支持）
- `cmd/server/config.go`：零改动（normalizeAPIKeys 已支持）

零后端改动 = 零回归风险。唯一风险在前端 JS，用 `node --check` 语法校验 + 人工
浏览器验证覆盖。
