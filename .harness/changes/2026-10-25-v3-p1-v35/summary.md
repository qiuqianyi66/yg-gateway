# 2026-10-25 v3-p1-v35（config 表驱动归一化 + APIKeys 迁出）

## 背景与必要性

ROADMAP v3 P1 首项（V3-5）。动工前实测推翻了立项假设：原 GWT「净减 ≥150 行」
没算过 79 处模式的真实构成——大头是 struct 注释（即文档，删了违背注释文化）与
Default()（不可缩）；normalize() 内真正同构可表的只有时长解析（10 块）与排程
默认（6 块）。按「研究明白再动工」纪律重定 scope：**目标从行数改为顶格解除**。

## 改了什么

1. `cmd/server/config_keys.go`（新）：normalizeAPIKeys + validKeyID 整段自 config.go
   迁出（逐字节保留原注释与逻辑，纯机械搬移）——config.go 不再依赖 net/httpauth。
2. `cmd/server/config.go`：
   - normalize() 10 个时长解析块 → 统一表（dst/src/name 行，错误路径包装不变）；
     特殊语义不进表：ReadTimeout/ProbeGap 负值检查（表后就地）、ExpiringSoon/
     CostExplore 三态、SoftRateMax/ProbeGap/Degrade 空值回落（保留原顺序语义）。
   - 6 个排程默认块 → 表。
   - import 清理（net/httpauth 随迁移块离开）。
3. config.go **1089 → 977**（顶格解除），config_keys.go 117 行。

## 怎么验证的

GWT 核心条款兑现：config 相关测试全绿且**断言未改一行**；build/gofmt 干净；
全量 pre-commit（棘轮：maxFileLines 1089 基线下 maxFile 不再是 config.go；
覆盖/预算不受影响）。

## 怎么回滚

单提交 revert（纯搬移+表驱动，无行为面）。

## 诚实记录

- PRD GWT 已同步修正（「净减 ≥150」→「顶格解除 + 同构块表驱动」，理由在案）。
- Degrade 空值回落块在原实现里位于 parse 之后（实际不可达的死兜底），本次保持
  原位置未动——清理它属于行为考古，另行记录不混入本变更。
