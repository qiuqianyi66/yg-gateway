# 2026-10-22-f101-panel-notice 变更摘要

需求：`docs/prd/REQ-f101-panel-acl-notice-2026-10-22.md`
分级：L1（前端展示）
ADR：不需要（纯展示文案 + 静态资源挂载；无难逆转决策、无真实权衡）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/panel/acl_notice.js` | R-101 面板显式提示：MutationObserver 挂 `#addDone`/`#importDone`，成功后追加 ACL 自动收紧说明行；不做任何写/收紧操作 |
| `修改：internal/panel/index.html` | ①账号池凭证管理区加静态说明（含 lock-auths.ps1 兜底指引）；②「添加账号」弹窗底部加新增/导入凭证落盘后自动收紧说明；③底部引入 acl_notice.js |
| `修改：internal/panel/index.go` | go:embed acl_notice.js；新增 aclNoticeScript 处理器（与 appScript 同口径：同源静态、安全头、免鉴权） |
| `修改：internal/panel/panel.go` | 路由注册 `GET /panel/acl_notice.js` |
| `新增：internal/panel/panel_features_test.go` | R-101/R-106 展示断言（serve index.html 文案 + JS 路由 + node --check） |

**没做的（明写，免得下轮重提）**：
- 只读 ACL 状态端点（本轮明确不做：收紧结果不落盘，现查增益低）。
- 收紧动作/icacls 参数/auths 目录结构的任何改动（`internal/auth/*` 只读）。
- app.js 一行改动（4378 非空行顶 check-depth 基线 maxFrontendLines，零增长）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 面板包测试 | `go test ./internal/panel` | ok 45.654s（含新增 TestIndexHTMLFeatureCopy / TestFeatureJSServed / TestFeatureJSSyntax） |
| 门禁总闸 | `powershell -NoProfile -File scripts/pre-commit.ps1` | （见提交记录，全绿） |

**负向探针**：TestIndexHTMLFeatureCopy 断言文案逐字存在——删任一句文案即测试红。
**「已检查 / 已验证」必须真跑过**：上述两命令实跑，输出见上。

## 三、未完成 / 待补（诚实清单）

- [ ] 浏览器实机冒烟（OAuth 添加走一遍）requires：可交互 Windows 桌面会话；本轮到 DOM 断言级证据。
- [ ] 只读 ACL 状态端点 requires：幂等查询端点设计评审（未来项）。

## 四、怎么回滚

纯前端展示 + 静态资源挂载：`git checkout -- internal/panel/index.html internal/panel/index.go internal/panel/panel.go`；新增文件 `acl_notice.js` / `panel_features_test.go` 随 R-106 同提交，整体 revert 即可。无二进制/凭证/数据改动；`auths/` 不随二进制回滚。

## 五、Learning

根因：后端闭环早已落地（写回/启动自动收紧），面板侧零提示是「最后一公里」缺失——治理债要追到展示层才算闭环。
同一根因在 ≥2 个切片复现（R-106 同：后端折算早已实现、面板标注缺位）→ 按经验三级进化提级 pattern 建议：**PRD v2 债务清单逐条落地时，先盘点「后端已做/前端未做」的半边缺口**，禁自动跨级。
