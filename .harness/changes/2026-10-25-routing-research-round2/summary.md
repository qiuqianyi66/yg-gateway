# 2026-10-25 routing-research-round2（研究轮：不动产品代码）

## 背景与必要性

用户指令「啥时候研究明白了啥时候再动工」——REQ-routing-improvements 的 P1/P2 各有
一处拍脑袋参数（×0.5 系数、替换 idle 补偿），pace 公式挂 unknown。本轮把研究做透：
读了 magpie 源码 `internal/routes/strategies.go`（Smart/Pace/LeastUsed/Weighted 实现）
+ `internal/gateway/routing.go`（Seat 组装），三问全部消解。**零产品代码改动。**

## 研究结论（实锤，回写进两份文档）

1. **pace 公式**（消解 unknown）：`pace = 周剩余份额 ÷ 距 reset 小时数`——统一
   least-used（浪费）与 smart（打光）两个失败模式的「每小时流失风险」度量；分带
   用十分之一带宽，注释专门论证非传递性问题的「先排序后画带」解法。
2. **90% 带**（修正 P1 依据）：magpie 是**三档字典序分层**（fine→low→spent 拼接），
   **无权重系数**；near-spent 层 Credits 多者优先（spentBefore）。YG 的加权抽签
   体系的正确翻译 = 带内权重降档（×0.5 保留但标注为翻译），bandMult 改为「按典型
   请求扣费定义 N=2 次」而非拍 1.2。
3. **半衰期**（修正 P2 方案）：实锤 `exp2(-Δt/1h)` 作用于 **token 记账维度**
   （served 先衰减再累加），是次级排序键**不是权重乘子**——原「替换线性 idle
   补偿」是误读，废弃；改为「衰减 token 账 × 乘性因子」（RecordTokenUsage 已有
   累计值，补时间戳即可）。1h 适配论证：CodeBuddy 签到 12h 间隔下，1h 半衰期使
   12h 后历史用量 ≈ 1/4096 归零 = 新签到周期语义，**1h 合适**。
4. 顺带佐证：count_tokens 在 magpie 网关层原生实现（BACKLOG A1 验证）；
   Soon 比较截断到小时（"a few minutes don't reorder"）——防抖小技巧可借。

## 改了什么（仅文档）

- `docs/reference/magpie-deep-read-2026-10-25.md` §五：unknown → 消解，三问源码级
  答案（含关键代码引用）。
- `docs/prd/REQ-routing-improvements-2026-10-25.md`：方案段整体重写（标注二轮修正
  依据），GWT 随之更新。状态仍「已立项待执行」——研究已明白，动工等用户口令。

## 怎么验证的

源码引用均来自 `internal/routes/strategies.go`（gh api 拉取，base64 解码直读）；
公式可复核（tier 常量 LowShare=90、exp2 形式）。全量 pre-commit 通过。

## 怎么回滚

单提交 revert（纯文档）。
