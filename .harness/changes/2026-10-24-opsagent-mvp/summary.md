# 2026-10-24 opsagent-mvp

## 背景与必要性

依据 `docs/prd/REQ-opsagent-2026-10-24.md`（立项）+ `docs/adr/ADR-017-opsagent-position.md`
（编排层位置拍板）。MVP = 观察-建议闭环，不自动行动；config 默认 off（零回归）。

## 改了什么

- `internal/opsagent/`（新包，271 行 + 247 行测试，覆盖率 97.1%）：
  - 骨架复用：unknown.Registry（假设登记，requires 强制非空、幂等去重）+
    evidence 四态（每轮 Facts 标注 observation/derivation，推断不得裸奔）；
  - 两条 MVP 规则：capacity（healthy/total < 阈值且池 ≥3，derivation 判据）与
    credit-402-contradiction（402 硬冷却 vs 本地正余额口径矛盾 → 登记 unknown 等权威对账）；
  - 动作白名单护栏：`Executor` 接口 + 默认执行器仅放行 `suggest`；被拒动作落入
    `Round.Rejected` 审计（teeth 测试锚定：非白名单必须可见地拒绝，不静默）；
  - Run 走 ctx 退出（R9）；历史环默认 50 轮；Round 可手动触发（测试/未来面板按钮）。
- `cmd/server/config_opsagent.go`（新）：OpsAgentConfig（enabled/interval_sec/healthy_frac），
  独立成文件因 config.go 压在 maxFileLines 棘轮上限（1088/1089），只许加一行字段。
- `cmd/server/opsagent_wiring.go`（新）：观察面从 pool 闭包投影（ADR-017 依赖倒置——
  opsagent 包不 import pool/metrics）；建议输出口 = WARN 日志 + 账号级建议走 alerting
  webhook（复用去重窗口）。
- `cmd/server/main.go`：alerting 实例提升为变量（opsSink 复用同一实例）；ctx 处启动一行。
- `scripts/check-boundaries.ps1`：allow 表加 `'opsagent' = @('evidence','logfmt','unknown')`
  （理由注释：ADR-017）。
- `config.example.json`：opsagent 段（默认 false）。
- `docs/REQ-INDEX.md`：opsagent 待办行改「MVP 已落地」。

## 怎么验证的

`go test ./internal/opsagent/ -cover` = ok 97.1%；`go test ./cmd/server/ -cover` = 74.0%
（> 全仓棘轮 min 73.3，未拖低）；`go build ./...`、vet、gofmt 干净；全量 pre-commit
（含边界检查新条目、深度棘轮 config.go 1089=基线不回退、main.go 995 未过千）通过。
护栏 teeth：TestGuardRejectsNonWhitelistAction 断言 cost-explore 被拒且 Sink 不被调用。

**两个实测教训（本变更内闭环）**：
1. **词表口径**：pool 的冷却值是 `hard_credit` 而非 `hard`——opsagent 规则若直接匹配
   细词表会**永假**（规则静默死亡）。投影测试（TestOpsAgentObserversProjection，真实
   pool 上断言投影）当场抓住；修法：装配层 `coolKindOf` 归一成粗口径（hard/soft），
   opsagent 只认粗口径，pool 未来加细分零影响。
2. **覆盖率棘轮的传导**：先写了装配代码后补测试，cmd/server 覆盖率 73.6→71.8 拖低全仓
   min（棘轮红）；把观察面构造从 startOpsAgent 内联改为独立函数后闭包体可直测，
   恢复到 74.0。新代码必须连同其装配闭包一起可测。

## 怎么回滚

单提交 revert。config 默认 off，未配置 opsagent 键的既有部署行为零变化
（json 忽略未知键 + enabled=false 双保险）。生产启用需在 D:\yg-gateway\config.json
加 `"opsagent": {"enabled": true}` 后重启（换版走 deploy-prod.ps1）。
