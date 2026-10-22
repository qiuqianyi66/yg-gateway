# 2026-10-22-openrouter-savings-display 变更摘要

需求：`docs/prd/REQ-openrouter-savings-display-2026-10-22.md`
分级：L1（前端展示）
ADR：不需要（纯展示标注 + 静态资源挂载；wire 形状零变化）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/panel/savings_note.js` | R-106：用量视图激活时取 `/panel/api/usage?hours=72`，三值渲染折算说明行（键存在→金额+inference 标注；键缺失→「未估算」，不渲染 0） |
| `修改：internal/panel/index.html` | 用量总览 `#usStats` 下加 `#savingsLine` 说明行容器（静态兜底文案：估算值非账单实测）；底部引入 savings_note.js |
| `修改：internal/panel/index.go` | go:embed savings_note.js；新增 savingsNoteScript 处理器（同源静态、安全头、免鉴权） |
| `修改：internal/panel/panel.go` | 路由注册 `GET /panel/savings_note.js` |
| `新增：internal/panel/panel_features_test.go` | R-106 展示断言（与 R-101 同文件） |

**没做的（明写，免得下轮重提）**：
- 后端 pricing 表扩充、`internal/usage/*` 任何改动（wire 形状零变化）。
- app.js 既有渲染改动（check-depth 基线压线，零增长）。
- 增量用量落库（同债务清单后续 P2 项，另立 REQ）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 面板包测试 | `go test ./internal/panel` | ok 45.654s（含新增 TestIndexHTMLFeatureCopy / TestFeatureJSServed / TestFeatureJSSyntax） |
| 门禁总闸 | `powershell -NoProfile -File scripts/pre-commit.ps1` | （见提交记录，全绿） |

**负向探针**：三值判据用 `hasOwnProperty('usd_value')`——若退化为 `!totals.usd_value` 判否即违反四态纪律；TestFeatureJSServed 断言「本窗口未估算」文案存在。
**「已检查 / 已验证」必须真跑过**：上述两命令实跑，输出见上。

## 三、未完成 / 待补（诚实清单）

- [ ] 浏览器实机冒烟（有定价 vs 无定价部署各看一眼）requires：一份带 pricing 的 config；本轮到 DOM 断言级证据。
- [ ] 增量用量落库 requires：另立 REQ + ADR（数据模型演进成本高）。

## 四、怎么回滚

纯前端展示：`git checkout -- internal/panel/index.html internal/panel/index.go internal/panel/panel.go`；新增文件 `savings_note.js` / `panel_features_test.go` 随 R-101 同提交整体 revert。wire 形状零变化，无二进制/凭证/数据改动；`auths/` 不随二进制回滚。

## 五、Learning

根因：后端折算 wire 早已就位（omitempty 三值语义），面板展示层缺 inference 标注与「未估算」显式三值——四态纪律在展示层漏标。
同一根因在 ≥2 个切片复现（R-101 同：后端闭环早已落地、面板提示缺位）→ 按经验三级进化提级 pattern 建议：**债务清单逐条落地时先盘点「后端已做/前端未做」的半边缺口**，禁自动跨级。
