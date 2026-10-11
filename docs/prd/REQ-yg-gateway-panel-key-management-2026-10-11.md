---
last_updated: 2026-10-11
status: 已完成（卡片+弹窗风格，已合并 main）
owner: YG
modification_level: L1（前端 + go:embed 重编译，零后端新增接口）
complexity: A 耦合（仅面板前端，复用一期后端能力）
research: docs/reference/panel-key-mgmt-ui-competitive-notes.md
depends_on: ADR-001（多 API Key 鉴权，一期已交付）
---

# YG Gateway 面板 Key 管理页 — 需求分析（PRD 级，v1）

> 二期。一期已完成全部后端能力（多 Key 鉴权索引、realm/models 约束、热更新、脱敏回显）。
> 本期范围：**面板前端 Key 管理 UI**（列表 / 新增 / 删除 / 编辑 / 一键生成）。
> 零新增后端接口——复用现有 `GET/POST /panel/api/config`。
>
> 基线：940f141（多 Key 一期已合并 main + D 盘已部署）。

---

## 一、范围边界

### 做什么

1. **Key 列表视图**（F-101）：面板新增「API Key」导航项，展示 `api_keys` 数组每条
   Key 的 id / name / realm / models / 密钥状态（已配置/未配置）。
2. **新增 Key**（F-102）：行内表单，填 id / name / key（可一键生成）/ models / realm，
   保存后立即热生效。
3. **删除 Key**（F-103）：列表行内删除按钮，确认后从数组移除，保存热生效。
4. **编辑 Key**（F-104）：列表行内编辑，可改 name / models / realm / key（留空=不修改）。
5. **一键生成 Key**（F-105）：前端 `crypto.getRandomValues` 生成 `sk-` + 32 字符随机串。

### 不做什么

1. **不做 enabled 开关**：一期 `api_keys` 无 `enabled` 字段；删除 = 从数组移除。
2. **不做软删除**（`deleted_at`）：一期 PRD 标记的二期项，不在本次范围。
3. **不做额度配额**（quota / token / credit limit）：YG 无额度概念。
4. **不做 IP 白名单 / max_ips**：过度 B 端化。
5. **不做账号绑定**（designatedAccountId）：选号绑定是另一条线。
6. **不做后端新增接口**：复用 `GET/POST /panel/api/config`。

---

## 二、用户故事

### US-1：查看 Key 列表
作为网关管理员，打开面板「API Key」页，我能看到所有已配置的多 Key（id / name /
realm / models / 密钥状态），以便快速了解「谁有权限调用我的网关」。

### US-2：新增 Key
作为网关管理员，我能在 Key 页面新增一条 Key（填 id / name / key / models / realm），
点击保存后立即热生效，无需重启进程，以便快速给新用户开权限。

### US-3：删除 Key
作为网关管理员，我能在 Key 列表删除某条 Key，确认后保存即热生效，以便撤销某用户的
访问权限。

### US-4：编辑 Key 约束
作为网GO管理员，我能在 Key 列表编辑某条 Key 的 models 白名单或 realm 约束，
保存后热生效，以便调整某用户的可访问模型范围。

### US-5：一键生成安全 Key
作为网关管理员，新增 Key 时我能点击「生成」按钮自动生成一个高熵随机 Key（`sk-` +
32 字符），避免手填低熵 key 被猜到。

---

## 三、功能规格

### F-101：Key 列表视图

**导航**：在现有导航栏「配置」之前插入「API Key」项，`data-view="keys"`。

**数据源**：`GET /panel/api/config` 返回的 `config.api_keys` 数组（一期 `loadConfigMasked`
已脱敏：每条 `key` 留空 + `key_configured` 布尔状态）。

**列表列**：

| 列 | 来源字段 | 展示 |
|---|---|---|
| ID | `api_keys[i].id` | 明文 |
| 名称 | `api_keys[i].name` | 明文，空显示「—」 |
| 密钥 | `api_keys[i].key_configured` | true=「已配置」绿色标记；false=「未配置」灰色 |
| Realm | `api_keys[i].realm` | `cn` / `global` / 空=「不限」 |
| 模型白名单 | `api_keys[i].models` | 数组用「, 」连接；空数组=「全拒」；缺失=「不限」 |
| 操作 | — | 编辑 / 删除按钮 |

**空状态**：`api_keys` 不存在或为空时，显示提示「未配置多 Key。当前使用单 api_key 鉴权。
点击新增配置第一条多 Key。」+ 新增按钮。

**单 Key 状态提示**：列表上方显示 `api_key_configured` 状态：「管理员密钥（api_key）已配置」
或「未配置」，让用户知道单 Key 仍可用。

### F-102：新增 Key

**入口**：列表上方「新增」按钮，展开行内表单。

**表单字段**：

| 字段 | 类型 | 校验 | 说明 |
|---|---|---|---|
| ID | text | 必填，`[A-Za-z0-9_-]` ≤64，不可与现有 id 重复 | 稳定标识，重启不变 |
| 名称 | text | 可空 | 展示用 |
| 密钥 | password | 必填（新增时） | 可点「生成」自动填入 |
| Realm | select | `cn` / `global` / 空=不限 | 默认空=不限 |
| 模型白名单 | text | 逗号分隔，如 `deepseek-v4-pro, hy4-preview` | 空输入=不限制（缺失）；显式空数组=全拒 |

**生成按钮**：点击后 `crypto.getRandomValues` 生成 `sk-` + 32 字符 base62 随机串，填入密钥框。

**保存**：
1. 前端组装完整 `api_keys` 数组（现有列表 + 新增条目），每条只含 `id`/`name`/`key`/
   `models`/`realm`（不含 `key_configured` 状态字段）。
2. 提交到 `POST /panel/api/config`，body 为 `{ "api_keys": [...] }`。
3. 后端 `mergeConfigMaps` 整体替换 `api_keys` → `normalizeAPIKeys` 校验 → 落盘 →
   热应用。
4. 成功：toast「Key 已新增，热生效」，刷新列表。
5. 失败（校验拒绝）：toast 显示后端错误信息（如「id 重复」「realm 非法」）。

### F-103：删除 Key

**入口**：列表行内「删除」按钮。

**确认**：`confirm('确认删除 Key「{name||id}」？删除后立即生效。')`。

**保存**：前端组装完整 `api_keys` 数组（移除被删条目），提交 `POST /panel/api/config`。

**边界**：删除最后一条 Key 后，`api_keys` 变为空数组 `[]`。后端 `normalizeAPIKeys` 对
显式空数组 fail fast（一期契约：显式 `[]` = fail fast）。**因此前端删除最后一条时，
提交 `api_keys: null`（= 键缺失 = 旧兼容），而不是 `[]`**。

### F-104：编辑 Key

**入口**：列表行内「编辑」按钮，该行切换为编辑模式（行内表单）。

**密钥字段**：显示 `key_configured` 状态；密钥输入框默认空，placeholder「留空 = 不修改」；
填入新值 = 更新密钥。

**保存**：与新增相同，提交完整 `api_keys` 数组（编辑后的版本）。

**取消**：恢复原值，退出编辑模式。

### F-105：一键生成 Key

**实现**：

```javascript
function genApiKey() {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  const arr = new Uint8Array(32);
  crypto.getRandomValues(arr);
  let s = 'sk-';
  for (let i = 0; i < 32; i++) s += chars[arr[i] % chars.length];
  return s;
}
```

**约束**：生成后用户可手动编辑（生成只是填入，不锁定）。

---

## 四、数据流

### 读取流

```
面板进入 view-keys
  → GET /panel/api/config
  → loadConfigMasked 返回 { ..., api_keys: [{ id, name, key:"", key_configured, realm, models }] }
  → 前端渲染列表
```

### 保存流

```
用户编辑/新增/删除
  → 前端 collectKeyList() 组装完整 api_keys 数组
  → POST /panel/api/config  body: { "api_keys": [...] }
  → SaveConfig 闭包：
      1. mergeConfigMaps(cur, incoming)  // api_keys 整体替换
      2. json.Marshal → ParseConfig 校验（normalizeAPIKeys：id 重复/realm/models 校验）
      3. 校验失败 → 400 + 错误信息（不写盘）
      4. 校验通过 → 落盘 config.json → buildAuthSnapshot → livecfg.Store（热生效）
  → 返回 { ok: true, restart_required: [...] }
  → api_keys 不在 restart_required（热字段）
```

### 删除最后一条 Key 的边界

```
前端检测：删除后 api_keys 长度 == 0
  → 提交 { "api_keys": null }（而非 []）
  → mergeConfigMaps：incoming.api_keys = null
  → cur.api_keys 被设为 null（Go unmarshal = 键缺失 = 旧兼容）
  → 热应用：AuthIndex 退化为单 Key 模式
```

---

## 五、前端实现规格

### 5.1 HTML（index.html）

在 `view-config` 之前插入新 section：

```html
<section class="view" id="view-keys" hidden>
  <div class="box">
    <header>
      <h3>API Key 管理</h3>
      <span class="grow"></span>
      <span class="note" id="keyAdminStatus"></span>
      <button class="xs" id="btnKeyReload">刷新</button>
      <button class="primary xs" id="btnKeyAdd">新增 Key</button>
    </header>
    <div class="pad">
      <table class="acc">
        <thead><tr>
          <th>ID</th><th>名称</th><th>密钥</th><th>Realm</th><th>模型白名单</th><th>操作</th>
        </tr></thead>
        <tbody id="keyBody"></tbody>
      </table>
      <div id="keyEmpty" class="hint" hidden>未配置多 Key。当前使用单 api_key 鉴权。</div>
    </div>
  </div>
</section>
```

导航项（在「配置」之前）：

```html
<li><a href="#keys" data-view="keys">
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4">
    <path d="M10.5 1.5l4 4-6.5 6.5-4-4z"/><path d="M8 8L2 14"/></svg>
  API Key</a></li>
```

### 5.2 JavaScript（app.js）

**路由注册**：
- `TITLES.keys = 'API Key 管理'`
- `go()` 里加 `if (v === 'keys') loadKeys();`

**核心函数**：

| 函数 | 职责 |
|---|---|
| `loadKeys()` | GET config → 取 `api_keys` 数组 → 渲染列表 |
| `renderKeyList(keys)` | 生成列表 HTML，每行含编辑/删除按钮 |
| `keyRowHTML(k, editing)` | 单行 HTML（普通/编辑模式） |
| `collectKeyList()` | 从 DOM 收集完整 `api_keys` 数组 |
| `saveKeys(keys)` | POST config `{ api_keys: keys }` → 成功刷新 |
| `genApiKey()` | crypto.getRandomValues 生成 sk- + 32 字符 |
| `deleteKey(id)` | confirm → 移除条目 → saveKeys |
| `editKey(id)` | 行切换为编辑模式 |

**models 三态前端处理**：
- `models` 字段缺失（undefined）→ 显示「不限」→ 编辑时空输入 = 不限制
- `models` = `[]`（显式空数组）→ 显示「全拒」→ 编辑时空输入 = 全拒
- `models` = `["a","b"]` → 显示用逗号连接

**保存时 models 序列化**：
- 编辑框空输入 → 提交时该条不带 `models` 字段（= 缺失 = 不限制）
- 编辑框有值 → `value.split(',').map(s=>s.trim()).filter(Boolean)`

### 5.3 CSS

复用现有 `.acc` 表格样式 + `.fld` 表单样式 + `.xs` 按钮样式。无新增 CSS。

---

## 六、不变量与约束

### INV-1：密钥不明文回显
`loadConfigMasked` 已把 `api_keys[].key` 留空。前端列表只显示 `key_configured` 状态。
新增/编辑时密钥框是 password 类型，值不回显。

### INV-2：保存提交完整数组
前端 `collectKeyList()` 必须提交完整的 `api_keys` 数组（不是增量），因为
`mergeConfigMaps` 对数组是整体替换。

### INV-3：删除最后一条提交 null 而非空数组
显式 `[]` 会触发 `normalizeAPIKeys` 的 fail fast 契约。删除最后一条时提交
`{ "api_keys": null }`，让后端视为「键缺失 = 旧兼容」。

### INV-4：id 不可编辑
编辑模式下 id 字段 readonly。id 是稳定标识，改 id 等于删旧增新。

### INV-5：保存后刷新列表
保存成功后必须重新 `GET /panel/api/config` 刷新列表，确保前端状态与后端一致
（后端 normalize 可能调整顺序/过滤空条目）。

---

## 七、验收标准

| # | 场景 | 预期 |
|---|---|---|
| 1 | 打开 #keys 页 | 显示 Key 列表（或空状态提示） |
| 2 | 新增一条 Key（id/name/key/models/realm） | 保存 200 + 列表刷新出现新条目 + 热生效 |
| 3 | 新增时 id 与现有重复 | toast 显示后端 400 错误「id 重复」 |
| 4 | 新增时 realm 填非法值 | toast 显示后端 400 错误「realm 非法」 |
| 5 | 点击「生成」按钮 | 密钥框填入 `sk-` + 32 字符随机串 |
| 6 | 编辑某 Key 的 models 白名单 | 保存 200 + 热生效 + 列表刷新 |
| 7 | 编辑时密钥留空 | 保存后密钥不变（key_configured 仍为 true） |
| 8 | 编辑时填新密钥 | 保存后密钥更新（key_configured 为 true） |
| 9 | 删除一条 Key | confirm 后保存 200 + 列表移除 + 热生效 |
| 10 | 删除最后一条 Key | 提交 null 而非 [] + 退化为单 Key 模式 |
| 11 | 单 Key 管理员密钥不受影响 | api_key 字段始终保留，不受 api_keys 操作影响 |
| 12 | 未配多 Key 时打开页面 | 显示空状态 + 「当前使用单 api_key」提示 |
| 13 | 保存配置页其他字段 | api_keys 不受影响（CFG_MAP 不含 api_keys + mergeConfigMaps 深合并保留） |

---

## 八、技术决策

### D-301：独立视图 vs 嵌入配置页
**决策**：独立视图 `view-keys`。
**理由**：`api_keys` 是数组结构，与配置页的扁平 CFG_MAP 不兼容；独立视图隔离复杂度。

### D-302：行内编辑 vs 模态弹窗
**决策**：行内编辑。
**理由**：go:embed 单文件前端，模态弹窗 DOM 管理成本高；YG Key 数量少，行内足够。

### D-303：复用 POST /panel/api/config vs 新增专用接口
**决策**：复用现有接口。
**理由**：后端 `mergeConfigMaps` + `normalizeAPIKeys` + `SaveConfig` 已完整支持
`api_keys` 的校验+落盘+热应用，零新增后端逻辑。

### D-304：删除最后一条提交 null vs 空数组
**决策**：提交 null。
**理由**：一期空值契约——显式 `[]` = fail fast，`null` = 键缺失 = 旧兼容。删除最后一条
应退化为单 Key 模式，不是 fail fast。

### D-305：models 三态前端序列化
**决策**：空输入 = 不带 models 字段（缺失=不限）；有值 = 数组。
**理由**：对齐一期 Go slice nil 区分语义（缺失=nil / []=非nil空 / 非空=白名单）。
前端 JSON 里 `undefined` 字段序列化时会被 `JSON.stringify` 省略，后端 unmarshal 为 nil。

---

## 九、测试门禁清单

| # | 类别 | 测试 |
|---|---|---|
| 1 | 渲染 | 空状态（无 api_keys）正确提示 |
| 2 | 渲染 | 有 api_keys 时列表正确展示 |
| 3 | 新增 | 完整字段保存成功 + 热生效 |
| 4 | 新增 | id 重复被拒 |
| 5 | 新增 | realm 非法被拒 |
| 6 | 生成 | genApiKey 产出 sk- + 32 字符 |
| 7 | 编辑 | models 修改保存成功 |
| 8 | 编辑 | 密钥留空 = 不修改 |
| 9 | 删除 | 单条删除成功 |
| 10 | 删除 | 最后一条提交 null + 退化单 Key |
| 11 | 隔离 | 配置页保存不影响 api_keys |
| 12 | 隔离 | Key 页保存不影响其他配置 |

---

## 十、源码事实（取证）

### 一期已就绪的后端能力

| 能力 | 位置 | 状态 |
|---|---|---|
| 多 Key 鉴权索引 | `internal/httpauth/index.go` | ✅ 已交付 |
| 配置解析 + normalize | `cmd/server/config.go` `normalizeAPIKeys` | ✅ 已交付 |
| 热更新 | `cmd/server/main.go` `SaveConfig` → `buildAuthSnapshot` → `livecfg.Store` | ✅ 已交付 |
| 脱敏回显 | `cmd/server/main.go` `loadConfigMasked` | ✅ 已交付 |
| 深合并保留 api_keys | `cmd/server/main.go` `mergeConfigMaps` | ✅ 已交付 |
| 请求日志 key_id 归因 | `internal/reqlog/reqlog.go` `KeyID`/`KeyName` 字段 | ✅ 已交付 |

### 前端现状

| 位置 | 现状 | 二期改动 |
|---|---|---|
| `index.html` 导航 | 7 项（accounts/usage/packages/taskscenter/models/config/logs） | +1 项 keys |
| `app.js` TITLES | 7 个视图 | +1 个 keys |
| `app.js` go() | 7 个视图分支 | +1 个 keys 分支 |
| `app.js` CFG_MAP | 不含 api_keys（一期保护） | 不改 |
| `app.js` collectConfig | 不提交 api_keys | 不改 |
