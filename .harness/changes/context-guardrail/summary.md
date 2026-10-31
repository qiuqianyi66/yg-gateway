# context-guardrail — 上下文护栏（设计 + 实现切片）

> 状态：**设计 + 实现完成**（代码已通过全量门禁，未提交时以本文件为准回填验证）。
> 需求：`docs/prd/REQ-context-guardrail-2026-10-31.md`
> ADR：`docs/adr/ADR-019-context-guardrail.md`
> 分级：L2（业务逻辑）

## 一、改了什么

### 设计凭据（上一切片已交付）

| 文件 | 作用 / 改动 |
|---|---|
| `新增：.harness/changes/context-guardrail/docs/prd/REQ-context-guardrail-2026-10-31.md` | 立项 PRD：必要性（request-logs 实弹数据）+ 范围 G1–G8（Given-When-Then）+ 默认值策略 + 估算口径 + 实现契约（config 形状 / reqlog 字段 / 413·429 契约 / /status 形状 / 测试映射）+ 不做 + 回滚 |
| `新增：docs/adr/ADR-019-context-guardrail.md` | 决策 D1–D6：请求前估算（derivation）/ 双阈值默认 50 万·80 万 / 413 硬拒语义 / 会话限流默认关 / webhook 去重 / reqlog+status 观测 |

### 实现切片（本切片）

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/server/guardrail.go` | 核心护栏：`Guardrail`（warn/maxTokens/sessionRPM + 会话窗口表 + stats + 可注入时钟）；`Check`（先 record 后 allow、散客不计、窗口分钟翻转、惰性 GC 128 键）；`SetContextWarnTokens/SetContextMaxTokens/SetSessionRPM`（热生效、关档清窗口）；`RecordCalibration`（≤20 FIFO、upstream≤0 不补）；`Snapshot`（锁内拷贝）；`applyGuardrail(w,r,sessKey,reqView,run)`（估算三段 → 429 session_rate_limit / 413 context_length_exceeded / warn 头 + trace 标记） |
| `新增：internal/server/guardrail_test.go` | 14 个单测全绿（零回归恒放行 / 阈值边界差 1 / 窗口翻转 / 会话隔离 / 散客 / 关档清空 / sweep bounds / Calibration FIFO / 并发安全 / 413 / 429 / warn 头 / status 省略与不省略） |
| `新增：cmd/server/config_guardrail.go` | `GuardrailsConfig` 结构体 + `validate()`（0=关零回归、负值 fail fast）。独立文件理由：config.go 处于 maxFileLines 棘轮上限，同 config_opsagent.go 先例 |
| `新增：cmd/server/config_test.go` 追加 | `TestGuardrailsValidate`（空配置通过 / 正数通过 / 三个负值各 fail fast） |
| 修改：`internal/server/handler.go` | Config 增 `Guardrail *Guardrail`、Handler 增字段、NewHandler 挂载；挂点调用 `applyGuardrail`（返回 true 即 return，不选号不打上游）；`rotateAndServe` 成功收尾补 `RecordCalibration`；`/status` 增 `guardrail_stats`（无命中整块省略） |
| 修改：`internal/reqlog/reqlog.go` | Event 增 `GuardrailHit string json:"guardrail_hit,omitempty"`（三值 warn\|blocked\|session_rpm，空=未命中） |
| 修改：`internal/server/logging.go` | requestTrace 增 guardrailHit + `setGuardrailHit` + event() 写入 |
| 修改：`internal/server/chatrun.go` | chatRun 增 `guardrailEst int64`（供成功收尾补对照样本） |
| 修改：`cmd/server/config.go` | Config 增 `Guardrails GuardrailsConfig` 字段（仅 1 行，守棘轮） |
| 修改：`cmd/server/main.go` | 装配：`cfg.Guardrails.validate()` fail fast → `NewGuardrail()` + 三个 Set + 注入 Config |

**没做的（明写，免得下轮重提）**：不改 dsh 客户端侧会话治理（压缩/拆新/图片即弃）；不做按
agent_kind 差异化阈值；`/status` guardrail_stats 尚未在面板展示（数据已聚合，待面板切片）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 护栏单测（14 个） | `go test ./internal/server/ -run 'Guardrail\|ApplyGuardrail' -v` | 14/14 PASS（含 413/429/warn 头/status 两态） |
| 全量单测 | `go test ./...` | 全过（check-depth 自跑覆盖率口径：最低包 cmd/server 73.0%） |
| 静态检查 | `go vet ./...` | 通过（exit 0） |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出（干净） |
| 依赖方向 | `powershell -NoProfile -File scripts/check-boundaries.ps1` | ✅ 边界检查通过 |
| 深度棘轮 | `powershell -NoProfile -File scripts/check-depth.ps1` | ✅ 未回退；maxFileLines 1089→1070 变好 |
| 治理自检 | `powershell -NoProfile -File scripts/check-harness.ps1` | 8/8 OK（ADR 001–019 无重号、REQ 46 篇合规、120 feat 目录 summary 齐） |
| 提交前总闸 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 11/11 全过（含 REQ 必要性段、常驻上下文预算 15820/15820 压线、R6 出站隔离） |

**负向探针（本切片已带）**：阈值边界差 1（est = threshold 放行 / est = threshold+1 拦截）；
零回归（未装配护栏的 Handler `applyGuardrail` 恒返回 false，行为与改动前逐位一致）；
校验失败路径（config 三个负值各 fail fast）；status 无命中时整块省略（未知不冒充 0）。
**「已检查 / 已验证」声明纪律**：以上结果均为实跑输出回填，不冒充。

## 三、未完成 / 待补（诚实清单）

- [x] 设计三文档（PRD 含实现契约 / ADR D1–D6 / summary）
- [x] 代码实现（guardrail.go / handler 挂点 / config / main / reqlog 字段 / /status 聚合 / 单测）——门禁全绿
- [ ] `/status` guardrail_stats 面板展示——requires：面板切片（数据已聚合就位）
- [ ] dsh 客户端侧会话治理（压缩/拆新/图片即弃）——requires：另开切片，本网关不做
- [ ] 估算权重真机校准——requires：护栏上线后积累估算 vs 上游 usage 对照样本（RecordCalibration ≤20 FIFO 已埋）
- [ ] 提交后生产部署（build + deploy-prod.ps1 冒烟）——requires：本切片合入后另行操作

## 四、怎么回滚

**配置热回退（首选）**：`guardrails` 块三项全部设 0（默认）→ 重启即关，护栏零干预；
负数配错会在启动时 fail fast 拒绝启动（不会带病运行）。
**代码回滚**：`git revert <实现提交>` 即可；新增文件一并删除。生产按 deploy-prod.ps1 换版
（自动备份 + 冒烟 + 失败回滚）。详见 PRD「回滚」节。

## 五、Learning

根因：**上下文无界增长 + 多会话并发无控**是「图片识别失败 / agent-busy / 费用爆炸」的共同病根，
模型与 max_tokens 只是触发器——归因须从使用模式出发，不停留在参数层。

同根因在 ≥2 个切片复现（观测/设计切片 + 实现切片）→ 已提级为规则建议：**「单会话 prompt
无界增长」应纳入网关运行告警基线**（warn 50 万即触发；本切片已实现 warn 打标与 /status 聚合，
告警动作待运维接线）。

另记一条实现期经验：**深度棘轮口径是「非空行」且基线文件行数会随每次微调漂移**——config.go
从 998 加 26 行就跨过 1000 线被 check-depth 判定回退；独立配置域拆独立文件（config_opsagent.go
先例）+ 校验由 main 装配侧调用（不占 Normalize 行数），是守住棘轮的干净做法，不要用
`-Update` 抬棘轮掩盖。
