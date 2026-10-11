# 面板 Key 管理页 — 执行计划

> PRD：`docs/prd/REQ-yg-gateway-panel-key-management-2026-10-11.md`
> ADR：`docs/adr/ADR-002-panel-key-management.md`
> 策略：先测试后实现（ADR-002 承重墙声明：零后端改动，仅前端 index.html + app.js）

---

## 任务拆分

### T0：HTML 骨架（index.html）
- 导航栏插入「API Key」项（`data-view="keys"`，在「配置」之前）
- `view-keys` section：表头 + 表格 thead + 空状态 div + 新增/刷新按钮
- 不改现有 `view-config` 的任何内容

### T1：JS 路由 + 数据加载（app.js）
- `TITLES` 加 `keys: 'API Key 管理'`
- `go()` 加 `if (v === 'keys') loadKeys();`
- `loadKeys()`：GET config → 取 `api_keys` + `api_key_configured` → 调 `renderKeyList()`
- `renderKeyList(keys)`：生成表格行 HTML，空状态切换

### T2：列表渲染 + 行内编辑/删除（app.js）
- `keyRowHTML(k, editing)`：普通模式（展示）+ 编辑模式（表单 input）
- 普通行：id / name / key_configured 状态 / realm / models / 编辑+删除按钮
- 编辑行：name input / key password / realm select / models text / 保存+取消
- `deleteKey(id)`：confirm → collectKeyList 移除条目 → saveKeys
- `editKey(id)`：行切换 editing 状态 + 重渲染

### T3：新增 + 生成 + 保存（app.js）
- `btnKeyAdd` onclick：列表顶部插入空白编辑行
- `genApiKey()`：crypto.getRandomValues 生成 sk- + 32 字符 base62
- `collectKeyList()`：从 DOM 逐行收集 id/name/key/realm/models
  - 密钥空 = 不带 key 字段（编辑时）；新增时必填
  - models 空输入 = 不带 models 字段（缺失=不限）
  - 长度 0 → 返回 null（D-304）
- `saveKeys(keys)`：POST config `{ api_keys: keys }` → 成功 toast + loadKeys 刷新

### T4：前端语法校验 + 人工验证
- `node --check app.js` 语法
- 浏览器打开 #keys 验收 13 个场景（PRD §七）
- 重点：删除最后一条提交 null + 热生效

---

## 执行顺序

T0 → T1 → T2 → T3 → T4（严格顺序，每步可独立验证）
