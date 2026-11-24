# 模板：harness 自改（规则 / 技能 / 门禁 / 检查项）

> 用法：整段粘贴，替换 `<...>`。改动对象是 `.harness/` 与 `AGENTS.md` 的治理面，**不是业务代码**。
> 对应流水线阶段 10 / 收尾；流程细则引 `.harness/rules/开发流程规范.md`，经验门引 `.harness/LESSONS.md`，不在这里复制。

---

你在 YG Gateway 仓库（`%USERPROFILE%\Desktop\yg-gateway`）里改治理面：**<改哪条规则/技能/检查项，一句话>**。

## 硬约束（先读，违反即返工）

- 常驻预算：`powershell -NoProfile -File scripts/check-context-budget.ps1` 必须绿；新增一行常驻内容必须配一条删除（只增不减 = 返工）。
- 新教训进 `.harness/LESSONS.md` 前必须有证据（commit / 命令输出 / 实测日期）；Seen 计数照旧，Seen≥2 未升级由 `check-harness.ps1` 检项 [7] 报红。
- 新增检查项按 `scripts/check-harness.ps1` 头注释判据：真问题 ≥1 且误报 ≤1 才补，否则不加。
- 改前先读 `.harness/LESSONS.md` 索引，命中哪条读 `.harness/lessons/L-NN.md` 正文。

## 第 1 段：基线（k≥1，先跑再动）

同命令同环境先跑一次，落 `.harness/changes/<feat>/baseline.md`：命令、退出码、关键输出、日期。
**k=1 只当一次抽样**——单次结果不能证明改动有效。

## 第 2 段：双轨候选

给两条候选，各写适用情境：

- **invariant**：所有切片/任务通用（例：定点编辑、否定断言穷尽证据源）
- **domain**：本仓独有（例：上游协议不改名、凭证不出 `auths/`、选号与熔断链路）

一次只改一面，禁止把两轨混进同一处。

## 第 3 段：选择纪律

- k 次基线当分布读；单 rollout 是弱证据；变化落在历史波动内不算改进。
- **下行与上行同权**：门禁变绿时同时查覆盖是否缩水（检查项减少、豁免变多、分母缩水）——只报增益不报缩水=假改进。
- 关联产出为空 / 未落盘 = UNSAFE，不算证据。

## 第 4 段：对照复跑

改动后同命令同环境再跑 k 次，报 median + 范围；变化 < 波动即不采纳，回滚改动。

## 第 5 段：leave-one-out

停用新增条目再跑一次：增益消失 → 是噪声，删掉；仍有增益 → 采纳并写进证据。

## 第 6 段：落盘与回滚

- `.harness/changes/<feat>/summary.md`（照 `changes/_template/summary.md`）+ `rollback.md`。
- 纯治理改动回滚 = `git checkout` 对应文件；新增文件直接删除。
- 负向探针：至少做一次预期报红的演示，证明新检查有牙齿；探针用完删除。

## 第 7 段：成本口径

总 token = 执行 + 编辑 + 选择 + 基线 k 路。`check-context-budget.ps1` 只看常驻层，**不替代**本口径；k>1 时把基线路数写进 summary，别只报单路增益。
