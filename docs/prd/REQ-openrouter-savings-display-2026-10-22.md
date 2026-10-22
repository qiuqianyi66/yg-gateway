# REQ：OpenRouter 费率折算面板展示（估算 inference 标注 + 未估算三值化）

> **状态：v1（2026-10-22）。本需求落地 PRD v2 §4.4 债务清单 P2 项「OpenRouter 费率
> 折算 / 增量用量落库」的面板展示半边：后端折算早已实现，缺的是 inference 标注与
> 缺失三值化展示。**
> 落地记录见 `.harness/changes/2026-10-22-openrouter-savings-display/`。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

PRD v2 §2.2 P2 需求「OpenRouter 费率折算：用户想知道『我省了多少钱』——对标
workbuddy2api-hub」。2026-10-22 实测现状：

1. **后端折算已实现但标注缺位**：`Agg.USDValue`（OpenRouter 公开参考价折算，纯
   展示 inference，`internal/usage/usage.go:465-468`）已在用量接口返回；app.js 已在
   明细表与「扣除积分」KPI 副标题展示折算值（app.js:3189/3414）。
2. **无明确 inference 标注**：现有文案「若按 OpenRouter 公开价 ≈ $X」隐含推断语义，
   但没有显式「估算（inference）」字样——违反 AGENTS.md 四态纪律：对外暴露的每个
   事实必须标注 observation/derivation/inference/unknown。
3. **缺失三值化不到位**：`usd_value` 带 omitempty（无定价部署响应体不含此键），但
   现有 `fmtUSD` 用 `Number(v || 0)` 把缺失坍缩为 0 显示「—」——「缺失 = unknown」
   的三值处理在面板展示层没有显式表达。

**Q2 不做会怎样？** 不报错，但展示层把 inference 当 observation 用：用户把折算美元
值当账单实测值看，进而在「到底省了多少钱」上得出错误结论。这正是 AGENTS.md 四态
纪律要防的那类静默错误。

**Q3 现状是不是已经变了？** 变了一半：后端 wire（含 omitempty）与 app.js 既有渲染
不动（app.js 4378 非空行已顶 check-depth 基线，一行都不能增）；本需求只加一块
带明确标注的独立说明行（`savings_note.js` + index.html 静态容器）。

**Q4 有没有更便宜的等效手段？** 有，本需求即最便宜路径：一个新 JS 文件自取
`/panel/api/usage?hours=72`（与后端默认窗口同口径）渲染说明行；不改后端、不改
app.js、不改 wire 形状。

**结论：做，L1（前端展示）。** 明确不做：增量用量落库（同债务清单后续项）、后端
pricing 表扩充、app.js 既有渲染的改动。

---

## 二、实测基线（2026-10-22）

| 事实 | 出处 |
|---|---|
| `Agg.USDValue` 折算值，`json:"usd_value,omitempty"` | `internal/usage/usage.go:465-468` |
| omitempty 语义：无定价部署响应体不含此键（0 = 全部模型无定价不折算） | 同上 466-467 注释 |
| app.js 已有两处折算展示（明细表列 / KPI 副标题），但无显式 inference 标注 | `internal/panel/app.js:3189,3414-3416` |
| 四态纪律：usage USD 折算价 = inference，必须标注 | AGENTS.md「已知的推断/未知清单」 |
| 三值纪律：字段缺失 = unknown，禁止 `!field` 判否 | AGENTS.md「实现口径」 |

---

## 三、范围边界

**做**
- 用量总览区加 `#savingsLine` 说明行容器（index.html 静态文案：估算值非账单实测）。
- `savings_note.js`：用量视图激活时取 `/panel/api/usage?hours=72`，按三值渲染：
  - `totals.usd_value` 键存在且 >0 → 「本窗口折算 ≈ $X（按 OpenRouter 公开参考价估算 · inference，非上游账单实测）」
  - 键缺失 → 「本窗口未估算：无模型定价或窗口内无计费用量（usd_value 字段缺失 = unknown，不按 0 计）」
- index.go 嵌入 + 路由挂载该静态资源。

**不做（明写）**
- 不改后端 wire 形状（`internal/usage/*` 只读；omitempty 语义保持）。
- 不改 app.js 既有渲染（check-depth 基线压线）。
- 增量聚合/落库、pricing 表扩充（债务清单后续项，另立 REQ）。
- 不渲染 0 美元——缺失即「未估算」。

---

## 四、需求项（Given-When-Then 验收）

### F-1 折算金额渲染 + inference 标注

- **Given** 用量视图（#usage）激活，且该窗口 totals 里存在 `usd_value` 键且 >0
- **When** `savings_note.js` 完成取数
- **Then** `#savingsLine` 显示折算金额，且文案含「估算 · inference，非上游账单实测」

### F-2 字段缺失显示「未估算」（禁止 !field 判否）

- **Given** 无模型定价的部署，响应体不含 `usd_value` 键
- **When** `savings_note.js` 渲染
- **Then** 显示「未估算」，不渲染 0 美元；判据用 `hasOwnProperty('usd_value')`，不用 `!totals.usd_value`

### F-3 静态说明行（取数失败兜底）

- **Given** 401/5xx/网络错误
- **When** 取数失败
- **Then** `#savingsLine` 保留静态说明文案（估算值非账单实测），不弹窗、不报错

### F-4 可测证据

- **Given** `go test ./internal/panel`
- **When** 运行
- **Then** 含 R-106 文案断言测试（serve index.html 断言 savingsLine 容器/inference 文案/脚本外链）+ JS 路由可取 + node --check 通过。

---

## 五、验证方式

| 项 | 分级 | 实际验证 |
|---|---|---|
| index.html 容器与静态文案 / 脚本外链 | L1 | `TestIndexHTMLFeatureCopy` |
| JS 路由可下载且含「未估算」文案 | L1 | `TestFeatureJSServed` |
| JS 语法 | L1 | `TestFeatureJSSyntax` |
| 门禁总闸 | — | `scripts/pre-commit.ps1` 全绿；`check-depth.ps1` 无回退 |

回滚：纯前端展示，`git checkout` 即可；无二进制/凭证/数据改动，wire 形状零变化。

---

## 六、未完成 / 待补（诚实清单）

- [ ] 浏览器实机冒烟（有定价 vs 无定价两种部署各看一眼渲染）requires：一份带 pricing 的 config；本轮仅到 DOM 断言级证据。
- [ ] 增量用量落库（同债务清单后续 P2 项）requires：另立 REQ + ADR（数据模型演进成本高）。
