# 面板 Key 管理页 — 执行计划

> PRD：`docs/prd/REQ-yg-gateway-panel-key-management-2026-10-11.md`
> ADR：`docs/adr/ADR-002-panel-key-management.md`
> 策略：先测试后实现（ADR-002 承重墙声明：零后端改动，仅前端 index.html + app.js）

---

## 任务拆分

### T0：HTML 骨架（index.html） ✅
- 导航栏插入「API Key」项（`data-view="keys"`，在「配置」之前）
- `view-keys` section：表头 + 卡片容器 + 空状态 div + 新增/刷新按钮
- 不改现有 `view-config` 的任何内容

### T1：JS 路由 + 数据加载（app.js） ✅
- `TITLES` 加 `keys: 'API Key 管理'`
- `go()` 加 `if (v === 'keys') loadKeys();`
- `loadKeys()`：GET config → 取 `api_keys` + `api_key_configured` → 调 `renderKeyList()`
- `renderKeyList(keys)`：生成卡片 HTML，空状态切换

### T2：列表渲染 + 弹窗编辑/删除（app.js） ✅
- 最终采用卡片式列表 + 弹窗表单（ADR-002 D-303 修订为弹窗，非行内编辑）
- `keyCardHTML(k)`：id/name/realm/models 徽标/启停开关/编辑/删除按钮
- `openKeyDialog(id)`：弹窗新增/编辑（id 编辑时只读）
- `deleteKey(id)`：confirm → 移除条目 → saveKeys

### T3：新增 + 生成 + 保存（app.js） ✅
- `btnKeyAdd` onclick：打开新增弹窗
- `genApiKey()`：crypto.getRandomValues 生成 sk- + 32 字符 base62
- `saveKeyFromDialog()`：收集表单 → POST config `{ api_keys: payload }` → 成功 toast + loadKeys 刷新
- 新增成功后展示明文一次性弹窗（showKeyPlaintext）
- 删除最后一条 → 提交 null（退化为单 Key 模式，D-304）

### T4：前端语法校验 + 人工验证 ✅
- `node --check app.js` 语法通过
- HTTP 冒烟：view-keys/key-card/key-dlg/keyEditVeil/keyShowVeil 全部 OK
- CSP 兼容：无行内 onclick
- 浏览器人工验证：待 YG 在 http://127.0.0.1:7863/panel/#keys 实际点一遍

---

## 执行顺序

T0 → T1 → T2 → T3 → T4（严格顺序，每步可独立验证）
