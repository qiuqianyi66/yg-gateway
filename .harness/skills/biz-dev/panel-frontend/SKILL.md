---
name: panel-frontend
version: 1
description: 面板前端开发——go:embed 单文件 + 严格 CSP + esc() 转义，改 app.js/index.html 必读（R3/R13 红线 + 测试切片契约）。【跳过】纯后端改动且不碰 app.js/index.html；只改 CSS 变量取值（无新渲染函数）——但改完仍必须重新编译二进制（R13）。
---

# 面板前端（YG Gateway）

## 触发条件

- 改 `internal/panel/app.js` / `index.html`
- 新增面板视图 / 渲染函数 / 样式

## 架构约束（铁律）

1. **go:embed 单文件**：资源打进二进制，改 JS/HTML 后**必须重编译**（R13），否则线上不生效。
2. **严格 CSP**：`default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'`。
   禁内联脚本、禁内联事件处理器。
3. **esc() 转义**：所有用户输入渲染前过 `esc()`（转义 `& < > " '` 五字符）。
   尤其属性插值（`title="..."`）——引号闭合是 XSS 高危点。
4. **设计令牌**：颜色走 CSS 变量（`:root` / `[data-theme]`），禁裸色值散落。
5. **零上游请求**：打开视图不打上游（逐账号实时查会卡死面板），显式按钮才触发。

## 测试切片契约（写测试必读）

前端测试用 `node` + `vm` 执行代码切片（见 `frontend_test.go`），三条铁律：

1. **函数外常量不进切片**：`indexOf` 定位切片，模块级常量落在切片外会 ReferenceError。
2. **注释里禁写函数声明字面量**：`indexOf` 先命中注释，切片被从函数中间切断。
3. **日期用本地时区**：`new Date(dateStr+'T00:00:00')` 本地解析，探针别用 toISOString。

## 视觉验收标准（反模板）

- 信息层级：首屏先回答"健康吗 / 积分够吗 / 何时到期"，主指标大、次要指标小
- 数字成列对齐（等宽 mono + tabular-nums），扫读可沿列比较
- 状态用语义色（ok/warn/bad），危险度用色条而非仅色点
- 真机截图验证（Playwright 传 colorScheme，否则 auto 主题两张图字节相同）

## 自检

- [ ] esc() 覆盖所有动态插值（含属性）
- [ ] 无内联脚本/事件处理器（CSP 兼容）
- [ ] 已重编译（R13）
- [ ] XSS 探针 SAFE
- [ ] 新增渲染函数配了牙齿测试

## 下一步

改完 → `unit-test-ci` → `expert-reviewer`（安全维度重点审 XSS）。
