# 模板：功能开发（需求 → 方案 → 拆分 → 编码）

> 用法：整段粘贴，替换 `<...>`。适用于 L2/L3 改动（业务逻辑 / 接口 / 调度 / 架构）。
> 纯文档或纯样式改动走 L0/L1 轻量路径，见 `.harness/rules/开发流程规范.md`「流程路径分级」。

---

你在 YG Gateway 仓库（`%USERPROFILE%\Desktop\yg-gateway`）里做需求：**<需求一句话>**。

## 硬约束（先读，违反即返工）

- 顶层规则：`AGENTS.md`；细则：`.harness/rules/{编码规范,工程结构,开发流程规范,ERROR-HANDLING}.md`
- 动手前必读 `.harness/LESSONS.md` 索引，命中哪条读 `.harness/lessons/L-NN.md` 正文
- 红线：R1 凭证不出 `auths/`、R2 日志脱敏、R5 密钥比较走 `internal/httpauth`、
  R6 出站集中 `internal/upstream`、R8 上游协议数据不改名、R9 goroutine 有回收、
  R10 错误不静默吞、R14 Go ≥1.27.2、R15 换二进制先备份
- 依赖方向以 `scripts/check-boundaries.ps1` 的 `allow` 表为唯一真相（`pkg` 层是纯叶子）

## 第 1 段：必要性（阶段 0，不答完不许往下）

按 `.harness/rules/开发流程规范.md` §零 四问逐条回答，**每条都要给出处**
（代码行 / 日志 / 用户症状 / 实测命令输出）：

1. 要解决的真实问题是什么？（禁止用"待办清单上写着"当理由）
2. 不做会怎样？（可观测性 × 严重度）
3. 现状是否已变？（旧文档说的"未做/不存在"是否仍成立——**实跑验证，别信转述**）
4. 有没有更便宜的等效手段？

结论四选一：**做 / 不做 / 等条件 / 降级做**，并写明放弃什么、重评条件。

> 纪律：否定性断言（不存在/从不/全仓搜不到）必须穷尽证据源（AGENTS.md + L-15）。
> 做不到穷尽就写 unknown，不许写成否定。

## 第 2 段：需求（阶段 1）

产出 `docs/prd/REQ-<slug>-<YYYY-MM-DD>.md`，必须含：

- `## 必要性` 段（机器校验：`check-req-necessity.ps1`，标题匹配 `必要性` 即可）
- F-编号需求 + 每条 **Given-When-Then** 验收
- 非功能约束（性能/并发/可观测/回滚）
- 明确"非目标"（写清不做什么，比写做什么更能防蔓延）

## 第 3 段：方案（阶段 2）

- 列 D-编号决策点，每个给**备选 + 取舍 + 结论**（真实权衡才写 ADR，判据见
  `.harness/rules/开发流程规范.md` 与 `docs/adr/` 现有编号）
- 红线冲突自查：逐条对照上面硬约束，冲突清零再进编码
- 影响面：列出将改的包、是否会动承重墙（选号 / 冷却熔断 / 凭证链路 / 出站改写管线）

## 第 4 段：拆分（阶段 3）

- 任务粒度 ≤ 4h，标注依赖顺序与可并行项
- 每个任务写"完成判据"（能被命令验证，不是"看起来对"）

## 第 5 段：编码（阶段 4）

- 定点编辑（L-10），不做字符串手术
- 新增共享逻辑先查 `internal/pkg/{timex,jsonsafe}` 是否已有原语；**没有第二个消费者
  就不建新抽象**（本仓已拒绝过 2 个无证据的候选包，见
  `docs/prd/REQ-yg-gateway-code-reuse-2026-11-02.md`）
- JSON 取值一律走 `internal/pkg/jsonsafe`（缺失 ≠ 零值）；可取消等待走 `timex.Sleep:tx`

## 收尾（必跑，贴原始输出）

```powershell
go build ./...
go vet ./...
gofmt -l ./cmd ./internal          # 必须无输出
go test ./...
powershell -NoProfile -File scripts/pre-commit.ps1
```

落 `.harness/changes/<feat-name>/summary.md`：改了什么 / 怎么验证（原始输出）/
怎么回滚。**没有原始输出 = 没有验证**。
