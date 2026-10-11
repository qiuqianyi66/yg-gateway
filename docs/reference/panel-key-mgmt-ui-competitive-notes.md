# 竞品面板 Key 管理 UI 调研（二期 PRD 输入）

> 目标：为二期「面板 Key 管理页」PRD 提供 UI/交互设计输入。
> 来源：GitHub 仓库源码取证（前端组件 + 后端 API + README + CSS 类名）。
> 一期竞品深挖见 `multi-key-competitive-notes.md`（存储/哈希/白名单匹配顺序）。
> 日期：2026-10-11。ARP 为 GPLv3，只借鉴 UI 交互思路，不抄代码。

---

## 一、三家竞品 Key 管理 UI 形态总结（源码取证）

### 1. workbuddy-manager（ithtelab）— React/Next.js + shadcn/ui，功能最全

**前端架构**：Next.js App Router，`web/app/(main)/keys/page.tsx`（61KB 单文件页面），用 shadcn/ui 组件库（Table/Dialog/Switch/Select/Badge/Tabs）。

**Key 列表展示**（Table 组件，8 列）：

| 列 | 字段 | 说明 |
|---|---|---|
| 名称 | name | 主标识 |
| 前缀 | prefix | `token[:12]`，公开信息，用于快速认领 |
| 状态 | enabled + 过期/超额 | Badge 组件，区分启用/禁用/过期/超额 |
| 版本 | realm | 紧跟状态列（`cn`/`global`/`''`不限） |
| 有效期 | expires_at | 时间戳格式化 |
| IP/模型 | ip_allowlist + models | 合并列，显示白名单条数 |
| 已用 Token | used_tokens | 数字格式化 |
| 最后使用 | last_used_at | 时间格式化 |
| 操作 | 编辑/删除 | 仅 admin 可见 |

- **分组 Tab**：`Tabs` 组件分「普通密钥」和「红包密钥」两个 tab，数字标在 tab 上（`普通密钥 · 3`）。
- **空状态区分**：取数失败 ≠ 没有密钥。取数失败显示 `LoadError`（重试按钮），没有密钥显示 `EmptyState`（新建按钮）。这是防「误以为密钥被删了而重建重复密钥」。
- **心跳刷新**：60 秒 `useHeartbeat(reload)` 走刷新模式（只换数据，不闪骨架）。

**新增/编辑 Key 的交互**（Dialog 弹窗）：

表单字段（`FormState` 接口）：
- `name`：Input 文本框，必填，不能只有空格
- `expiryMode`：三态 `keep`（编辑时保持不变）/ `days`（从现在起 N 天）/ `never`（永不过期），用 Tabs 切换
- `expiresDays`：Input 数字，配合 `days` 模式
- `maxIps`：Input 数字（IP 绑定数上限，B 端能力）
- `ipAllowlist`：Textarea，换行/逗号分隔 → `toLines()` 解析
- `models`：`ModelWhitelistInput` 组件（自由文本 + 失焦校验，调 `POST /api/keys/check-models` 后端接口查哪些名字匹配不到已知模型，提示挂在输入框下方并 `scrollIntoView`）
- `quota` / `quotaCredit`：Input 数字（token 额度 / 积分额度，0=不限）
- `realm`：Select 下拉（`cn`/`global`/`''`不限），新建时默认跟随当前所在版本
- `upstream_id`：Select 下拉（绑定上游账号池分组，null=默认上游）

- **新建后明文展示**：创建成功后 `setIssued(created.key)` 把明文存到 state，弹窗里展示一次性明文 + 复制按钮 + 导出/导入到 cc-switch/ZCode 客户端。
- **提交锁**：`useRef(submitting)` 同步锁防连点（连点 3 次会建 3 把同名密钥）。
- **编辑 vs 新建**：编辑走 `PATCH /api/keys/{id}`（部分更新，`exclude_unset` 语义），新建走 `POST /api/keys`。

**后端 API**（`server/routers/keys.py`，27KB）：
- `GET /api/keys` — 列表（`list_keys()`）
- `POST /api/keys` — 创建（`KeyIn` 模型，admin 权限）
- `PATCH /api/keys/{id}` — 部分更新（`KeyPatch` 模型，`exclude_unset` 语义）
- `DELETE /api/keys/{id}` — 硬删
- `POST /api/keys/{id}/reset-usage` — 重置用量
- `POST /api/keys/check-models` — 模型白名单失焦校验（只读缓存，不发网络）
- `POST /api/keys/export` — 导出为 cc-switch/ZCode 配置片段（无副作用）
- `POST /api/keys/import-local` — 一键写入本机客户端配置（需开关 + 本机调用）
- `GET /api/keys/import-local/status` — 本机导入可用状态（只读）
- `GET /api/keys/import-local/detect` — 自动检测客户端安装位置

**删除/禁用**：
- **启用/禁用**：列表行内 Switch 组件，`toggle()` 调 `PATCH {enabled: !k.enabled}`，即时保存。
- **删除**：`ConfirmDialog` 确认 → `DELETE /api/keys/{id}`，**硬删**（直接删行，无软删除）。
- **重置用量**：`POST /api/keys/{id}/reset-usage`，单独按钮。

**密钥展示**：
- 列表里只显示 `prefix`（前 12 位），不显示完整明文。
- **明文只在创建那一次返回**（`created.key`），之后面板只存哈希，拿不回明文。
- `CopyButton` 组件复制明文（创建时）。
- 导出/导入：`/api/keys/export`（生成 cc-switch/ZCode 配置片段，无副作用）、`/api/keys/import-local`（一键写进本机客户端配置，需开关 + 本机调用）。

**保存机制**：即时保存（每次 PATCH/DELETE/POST 立即生效，无批量保存按钮）。

**响应式**：shadcn/ui 组件自带响应式，Table 有水平滚动，`flex-wrap` 布局。

---

### 2. workbuddy2api-hub（ardeyouxipianyi）— 单文件 dashboard.html，卡片式布局

**前端架构**：`dashboard.html` 单文件（618KB，内嵌全部 HTML+CSS+JS），Python 后端 `wb_proxy.py` 提供 `/panel/api/*` 接口。**这是最接近 YG Gateway go:embed 单文件模式的竞品。**

**Key 列表展示**（卡片式，非表格）：

从 CSS 类名取证（`dashboard.html` 的 `<style>` 段）：
- `.key-card`：每把 Key 一张卡片，`padding:16px 20px`，hover 有 `border-color` 变化 + `box-shadow` 加深
- `.key-card.disabled`：禁用态 `opacity:.75` + `background:var(--panel2)`（变灰）
- `.key-card-main`：`display:flex; align-items:center; justify-content:space-between; gap:16px; flex-wrap:wrap`（弹性换行）
- `.key-action-btn`：圆角按钮（`border-radius:20px`），`.danger` 变体用于删除（红底）
- `.realm-badge-intl`（国际版蓝色徽章：`color:#0369a1; background:rgba(2,132,199,.12)`）/ `.realm-badge-cn`（国内版红色徽章：`color:#b91c1c; background:rgba(220,38,38,.12)`）：realm 用彩色 badge 区分
- `.realm-switch-group`：胶囊式开关组（`display:inline-flex; border-radius:10px; padding:3px`）
- `.realm-pill`：胶囊按钮（`border-radius:8px`），`.active` 态高亮

列表字段（从一期笔记的 `settings.json` 的 `api_keys` 数组结构 + CSS 推断）：
- `name`：卡片标题
- `key`（掩码）：`maskApiKey` 风格，前 4 + `********` + 后 4
- `realm`：彩色 badge（蓝=国际，红=国内）
- `enabled`：卡片整体变灰 + Switch 开关
- `models`：显示白名单条数或「全部」
- `created_at` / `deleted_at`：时间

**新增/编辑 Key 的交互**：
- `.modal` + `.modal-mask`：弹窗（`display:none` → `.show` 切 `display:flex`），`width:min(620px,100%)`，`max-height:88vh; overflow:auto`
- `.modal-hd`（标题 + 关闭按钮）/ `.modal-bd`（表单体）/ `.modal-ft`（底部按钮组）
- 表单字段（从 `settings.json` 的 `api_keys` 数组结构推断）：`name`（Input）、`key`（创建时自动生成，展示一次）、`realm`（`.realm-switch-group` 胶囊式开关切换）、`models`（多选/手填）

**删除/禁用**：
- **软删除**（一期笔记已确认）：`deleted_at` + secret 清空 + `enabled=False`，id 保留。
- Switch 开关即时切换 enabled。

**密钥展示**：
- 列表里掩码显示（`maskApiKey` 风格）。
- **后端始终返回明文**（分页/详情都给全量），前端只「显示」时脱敏，复制按钮复制明文。← 这点与 manager 的「只存哈希」不同。

**保存机制**：即时保存（`POST /panel/api/config` 保存配置热生效，对齐 YG 现有模式）。

**响应式**：
```css
@media (max-width: 860px){
  header{padding:12px 16px}
  main{padding:16px 12px 60px}
  section{padding:14px 14px;border-radius:12px}
  #cards{grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:8px}
}
```
卡片式布局天然适配移动端（`flex-wrap:wrap`，`grid auto-fit`）。

---

### 3. ARP（KaiXuanSama）— Vue 3 + Naive UI，表格分页式

**前端架构**：Vue 3 `<script setup lang="ts">` + Vite + Naive UI 组件库。`frontend/src/components/DownstreamKeyManagement.vue`（47KB 单文件组件），`createWebHashHistory` 路由，`/downstream-keys` 路由。

**Key 列表展示**（`NDataTable` 表格，13 列）：

| 列 | 字段 | 宽度 | 说明 |
|---|---|---|---|
| 编号 | id | 70 | |
| 标签 | label | 100 | `ellipsis:{tooltip:true}` 省略号+悬停提示 |
| 启用 | enabled | 55 | `NSwitch` 行内开关，乐观更新+失败回滚 |
| API Key | apiKey | 150 | `maskApiKey` 掩码 + `NButton` 复制按钮（Icon） |
| 调用次数 | callCount | 100 | 可排序，点击打开调用日志弹窗 |
| 积分用量 | usedCredits/creditLimit | 120 | 可排序，超限染红，`已用 / 总量` 格式 |
| 消耗方式 | consumption | 110 | `NTag` 标签（指定/量少/量多/临期） |
| 支持模型 | supportedModels | 110 | **三态渲染**：`null`→「全部」灰 / `[]`→「不放行」红 / `[a,b]`→「N 个」+tooltip |
| 指定账号 | designatedAccountId | 130 | 按 consumption 分流显示 |
| 有效期 | expiresAt | 170 | null→「永久」灰 / 时间格式化 |
| 创建时间 | createdAt | 170 | 可排序 |
| 操作 | 编辑/删除 | 140 | `fixed:'right'` 固定列 |

- **分页**：`NPagination` 组件，`pageNum`/`pageSize`/`orderBy`/`asc` 后端分页（`POST /api/downstream-keys/page`），支持排序。
- **调用日志**：点击「调用次数」打开独立弹窗（`NModal`），有自己的分页+排序，4 列（编号/时间/账号/模型），`content` 字段是上游 chunk JSON，`parseUsageInfo` 解析 usage（输入/输出/缓存命中/积分）。

**新增/编辑 Key 的交互**（`NModal` 弹窗）：

表单字段：
- `label`：`NInput` 文本框
- `consumption`：`NSelect` 下拉（指定账号/量少优先/量多优先/临期优先）
- `designatedAccountId`：`NSelect` 下拉（账号列表，`:render-label` 渲染 `昵称（余量/总量）`）
- `creditLimit`：`NInputNumber` 数字（null=不限）
- `expiresEnabled`：`NSwitch` 开关 + `datetime-local` 输入（`toDateTimeLocal`/`fromDateTimeLocal` 转换）
- `enableSupportedModels`：`NSwitch` 开关（false=未配置=回退全集）+ `formSupportedModels`：`NSelect` 多选（`multiple` 模式，选项来自 `useModels` store 的全模型清单，label 格式 `model_id (family)`）
- `saving`：保存中状态，防重复提交

- **新建后明文展示**：`showNewKeyModal` 独立弹窗，`newlyCreatedKey.apiKey` 展示完整明文，`copiedHint` 复制提示。
- **编辑**：`openEditModal(row)` 填充表单（注意 `supportedModels` 三态：null 时 `enableSupportedModels=false`，有值时 `true` + 填入数组）。

**删除/禁用**：
- **启用/禁用**：`NSwitch` 行内开关，`toggleEnabled()` 乐观更新 UI + 后台 PATCH，失败回滚。`togglingEnabledIds` Set 记录飞行中的 id，显示 loading + 禁用。
- **删除**：`showDeleteModal` 确认弹窗 + `deleteTarget` + `deleting` 状态，`DELETE` 硬删。

**密钥展示**：
- `maskApiKey(key)`：`key.substring(0,4) + '********' + key.substring(key.length-4)`，前端脱敏。
- **后端始终返回明文**，前端只显示时脱敏，复制按钮复制明文（`row.apiKey`）。
- 复制：`navigator.clipboard.writeText` + `execCommand('copy')` fallback。

**保存机制**：即时保存（PATCH/DELETE/POST 立即生效）。

**响应式**：Naive UI `NDataTable` 自带 `scroll-x` 水平滚动，`fixed:'right'` 固定操作列。

---

## 二、共性模式（所有竞品都做的）

| 维度 | 共性做法 |
|---|---|
| **列表展示** | 表格/卡片二选一；显示 name/label、掩码 key、enabled 状态、realm、models 白名单、有效期、创建时间 |
| **新增交互** | 弹窗（Modal/Dialog）表单；创建后明文一次性展示 + 复制按钮 |
| **编辑交互** | 同一个弹窗复用；PATCH 部分更新语义 |
| **启用/禁用** | 行内 Switch 开关，即时保存（乐观更新） |
| **删除确认** | 确认弹窗（Dialog/Modal），防误删 |
| **密钥掩码** | `前4 + ******** + 后4` 格式；复制按钮复制明文 |
| **models 白名单** | 多选下拉 或 自由文本 + 校验；空=不限（ARP 做了三态：null/[]/[a,b]） |
| **realm 下拉** | Select 下拉 或 胶囊开关（cn/global/不限） |
| **即时保存** | 无批量保存按钮，每次操作立即生效 |
| **分页/排序** | 数据量大时分页（ARP 后端分页；manager 前端全量+分组） |

---

## 三、差异化设计

| 维度 | workbuddy-manager | workbuddy2api-hub | ARP |
|---|---|---|---|
| **列表形态** | 表格（shadcn Table） | **卡片式**（.key-card） | 表格（NDataTable，13列） |
| **分页** | 前端全量 + Tab 分组 | 前端全量 | **后端分页**（POST /page） |
| **密钥存储** | **只存哈希**，明文仅创建时返回一次 | 明文存储，前端脱敏显示 | 明文存储，前端脱敏显示 |
| **软删除** | 无（硬删） | **有**（deleted_at + secret 清空） | 无（硬删） |
| **models 校验** | **失焦校验**（调后端 /check-models） | 无 | 无 |
| **models 三态** | 空=不限 | 空=不限 | **null/[]/[a,b] 三态** |
| **调用日志** | 有（独立页面） | 无 | **行内点击打开弹窗**（分页+解析 usage） |
| **导出/导入** | **有**（cc-switch/ZCode 配置片段 + 一键导入本机） | 无 | 无 |
| **额度** | token + credit 双额度 | 无 | credit 额度 |
| **指定账号** | 无 | 无 | **有**（designatedAccountId + 消耗方式） |
| **realm UI** | Select 下拉 | **胶囊式彩色 badge** | 无 realm（单协议） |
| **空状态区分** | **取数失败 ≠ 没有密钥**（防误重建） | 无 | 无 |
| **提交锁** | **useRef 同步锁**（防连点建重复） | 无 | 无 |
| **前端框架** | React + Next.js | **原生 HTML/CSS/JS 单文件** | Vue 3 + Naive UI |
| **UI 库** | shadcn/ui | 自写 CSS | Naive UI |

---

## 四、对 YG Gateway 的建议

### 建议 1：列表形态——**卡片式**（对齐 hub，适配 go:embed 单文件）

理由：
1. YG 的 `app.js` 是 go:embed 单文件前端，**不能引入 npm 依赖**（shadcn/ui 要 React + npm build，Naive UI 要 Vue + npm build）。卡片式用纯 CSS `.key-card` 即可实现，不依赖任何 UI 库。
2. 卡片式天然适配移动端（`flex-wrap:wrap`），hub 的 `@media (max-width:860px)` 断点可直接参考。
3. YG 的 Key 规模小（几十把顶天），不需要 ARP 那种 13 列后端分页表格——卡片式更直观。
4. 表格列多了在窄屏会挤成一团（ARP 要 `fixed:'right'` 固定操作列 + 水平滚动），卡片式没有这个问题。
5. hub 的 `dashboard.html` 已经验证了单文件可以做出完整的卡片式 Key 管理 UI（618KB 内嵌全部 CSS+JS）。

### 建议 2：密钥展示——**创建时明文一次 + 掩码列表**（对齐 manager，符合现有脱敏设计）

理由：
1. YG 现有 `loadConfigMasked` 已经做了脱敏（`api_key` 留空 + `key_configured` 状态），多 Key 化后应该沿用：列表里 `api_keys[].key` 留空 + `key_configured` 状态。
2. **明文只在创建那一次返回**（manager 模式）：`POST /panel/api/config` 创建 Key 时返回明文，之后 `GET /panel/api/config` 永远脱敏。这比 hub/ARP 的「后端始终返回明文，前端脱敏」更安全——前端 JS 里不会有明文残留。
3. 列表里显示 `prefix`（前 12 位）供认领，不显示完整明文。
4. 创建后弹窗展示明文 + 复制按钮（`navigator.clipboard.writeText` + `execCommand` fallback）。

### 建议 3：新增/编辑——**弹窗表单 + 三态 models + realm 胶囊**

表单字段（裁剪自三家，适合 YG 的字段）：
- `name`：Input 文本，必填
- `realm`：**胶囊式开关**（`cn` / `global` / 不限），对齐 hub 的 `.realm-switch-group`。比 Select 下拉更直观，颜色区分（红=国内，蓝=国际）。
- `models`：**多选 + 手填混合**。参考 ARP 的 `NSelect multiple`（从模型清单选）+ 手填自由文本。YG 可用 `<select multiple>` + `<input>` fallback。
- `enabled`：Switch 开关（新建时默认开）
- `expires_at`：可选，datetime-local 输入（参考 ARP 的 `toDateTimeLocal`）

**不建议照搬**：
- manager 的 `max_ips` / `ip_allowlist` / `quota` / `quota_credit`（B 端能力，YG 不需要）
- ARP 的 `consumption` / `designatedAccountId`（指定账号绑定，YG 二期不做）
- ARP 的 `creditLimit`（YG 没有积分额度概念）

### 建议 4：删除/禁用——**软删除 + 确认弹窗**（对齐 hub 的软删除）

理由：
1. 一期笔记已定「采纳 hub 的软删除」（`deleted_at` + secret 清空 + enabled=False，id 保留）。
2. 确认弹窗：用 `confirm()` 或自写 `.modal` 确认框，防误删。
3. 启用/禁用：行内 Switch 开关，即时保存（`PATCH` enabled 字段）。

### 建议 5：保存机制——**即时保存**（对齐现有 POST /panel/api/config 热生效）

理由：
1. YG 现有 `POST /panel/api/config` 已经是「校验→落盘→热应用」即时生效模式，多 Key 化后沿用。
2. 不做批量保存按钮——三家都没做，即时保存是行业惯例。
3. 每次操作（新增/编辑/删除/启停）都调 `POST /panel/api/config` 提交完整配置（含 `api_keys` 数组），后端 `SaveConfig` 闭包统一处理。

### 建议 6：models 三态——**采纳 ARP 的 null/[]/[a,b] 三态语义**

理由（一期笔记已定）：
- `null` = 未配置 = 回退全集（向后兼容存量 Key）
- `[]` = 严格全拒
- `["a","b"]` = 白名单

这比「空=不限」更严谨，避免「空数组到底是不限还是全拒」的歧义。UI 上：
- `null` → 显示「全部」灰色 badge
- `[]` → 显示「不放行」红色 badge
- `["a","b"]` → 显示「N 个」+ tooltip 列出 id

### 建议 7：空状态区分——**采纳 manager 的「取数失败 ≠ 没有密钥」**

理由：
1. YG 的 `GET /panel/api/config` 如果失败，不应该显示「没有密钥」+「新建」按钮——用户会误以为密钥被删了而重建重复密钥。
2. 取数失败显示错误提示 + 重试按钮；没有密钥显示空状态 + 新建按钮。

### 建议 8：提交锁——**采纳 manager 的 useRef 同步锁**

理由：
1. `fetch` 是异步的，`disabled` 状态要等重渲染才生效，连点会建重复密钥。
2. YG 的 app.js 可用 `let submitting = false` 模块级变量做同步锁（不依赖 React 的 ref）。

### 建议 9：前端实现——**纯 HTML/CSS/JS，不引入框架**

理由：
1. YG 的 `app.js` 是 go:embed 单文件，不能引入 npm 依赖。
2. hub 的 `dashboard.html` 证明了单文件可以做出完整的卡片式 Key 管理 UI（618KB 内嵌全部 CSS+JS）。
3. 用原生 `fetch` + `innerHTML` 渲染 + `classList` 切换状态即可，不需要 React/Vue 的响应式。
4. 弹窗用 `.modal` + `.modal-mask.show` 切换 `display:flex`；Switch 用 `<input type="checkbox">` + CSS `.switch-track`；多选用 `<select multiple>` 或自写 chip 组件。

---

## 五、二期 Key 管理页 UI 规格建议（汇总）

```
页面结构：
  <section class="keys-section">
    <h2>API Key 管理</h2>
    <button class="add-key-btn">+ 新建 Key</button>
    <div class="key-list">
      <!-- 卡片式列表 -->
      <div class="key-card [disabled]">
        <div class="key-card-main">
          <span class="key-name">{name}</span>
          <code class="key-prefix">{prefix}...</code>
          <span class="realm-badge [cn|intl]">{realm}</span>
          <span class="models-badge">{全部|不放行|N个}</span>
          <label class="switch"><input type="checkbox" {checked}><span class="switch-track"></span></label>
          <div class="key-actions">
            <button class="key-action-btn edit">编辑</button>
            <button class="key-action-btn danger delete">删除</button>
          </div>
        </div>
      </div>
      <div class="empty-state">暂无 API Key</div>  <!-- 或 LoadError -->
    </div>
  </section>

  <!-- 新增/编辑弹窗 -->
  <div class="modal-mask [show]">
    <div class="modal">
      <div class="modal-hd"><h2>{新建|编辑} Key</h2><button class="modal-x">×</button></div>
      <div class="modal-bd">
        <label>名称 <input type="text" name="name" required></label>
        <fieldset class="realm-switch-group">
          <button class="realm-pill [active]" data-realm="cn">国内版</button>
          <button class="realm-pill [active]" data-realm="global">国际版</button>
          <button class="realm-pill [active]" data-realm="">不限</button>
        </fieldset>
        <label>模型白名单
          <select multiple name="models">
            <option value="model-a">model-a</option>
          </select>
          <span class="hint">留空 = 不限；选择 = 白名单</span>
        </label>
        <label>有效期 <input type="datetime-local" name="expires_at"></label>
        <label>启用 <input type="checkbox" name="enabled" checked></label>
      </div>
      <div class="modal-ft">
        <button class="sec cancel">取消</button>
        <button class="save">保存</button>
      </div>
    </div>
  </div>

  <!-- 创建后明文展示弹窗 -->
  <div class="modal-mask [show]">
    <div class="modal">
      <div class="modal-hd"><h2>Key 已创建</h2></div>
      <div class="modal-bd">
        <code class="key-plaintext">{full_key}</code>
        <button class="copy-btn">复制</button>
        <p class="hint">请立即复制保存，关闭后无法再次查看完整密钥。</p>
      </div>
    </div>
  </div>
```

**交互流程**：
1. 新建：点「+ 新建 Key」→ 弹窗表单 → 保存 → 后端生成 Key + 返回明文 → 明文弹窗 → 关闭 → 刷新列表
2. 编辑：点「编辑」→ 弹窗表单（填充现有值）→ 保存 → 刷新列表
3. 启停：切 Switch → 即时 PATCH enabled → 乐观更新
4. 删除：点「删除」→ 确认弹窗 → 确认 → 软删除（deleted_at + secret 清空）→ 刷新列表

**保存接口**：复用现有 `POST /panel/api/config`，提交完整配置（含 `api_keys` 数组），后端 `SaveConfig` 闭包统一处理校验→落盘→热应用。

---

## 六、与前版推断的差异说明

本文件前版（基于 API 推断）的几个结论在源码取证后修正：

| 前版结论 | 源码取证后修正 |
|---|---|
| manager「推断硬删（无 deleted_at 字段）」 | ✅ 确认硬删（`DELETE /api/keys/{id}` 直接删行） |
| manager「密钥创建后一次性展示」 | ✅ 确认（`created.key` 返回明文，之后只存哈希） |
| hub「明文可查看（因为明文存）」 | ✅ 确认（后端始终返回明文，前端 `maskApiKey` 脱敏显示） |
| ARP「supportedModels 三态」 | ✅ 确认（`null`/`[]`/`[a,b]` 三态，UI 三态渲染） |
| ARP「一键生成 Key」 | ✅ 确认（`generateApiKey` 前缀+随机字母表） |
| 前版建议「不做 enabled 开关」 | ❌ **修正**：三家都做了 enabled 开关，建议采纳（行内 Switch 即时保存） |
| 前版建议「列表 + 行内编辑，不做弹窗」 | ❌ **修正**：三家都用弹窗表单，建议用弹窗（`.modal` 纯 CSS 可实现） |
| 前版建议「不做软删除」 | ❌ **修正**：一期笔记已定采纳 hub 软删除，二期应实现 |
