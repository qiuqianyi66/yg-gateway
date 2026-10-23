# summary：2026-10-22 全仓体检修复批（P2-1/P2-2/P2-4/P2-6/F-01）

## 改了什么

| 编号 | 文件 | 改动 |
|---|---|---|
| P2-1 | internal/usage/wal.go | `appendWAL` 改返回 error；各失败分支记日志并返回（R10：不再 `continue` 吞 marshal 失败、不再丢弃 `bw.Write` 错误） |
| P2-1 | internal/usage/usage.go | `flush()`：仅在 appendWAL **成功**后才按 recs 逐条从 walDirty/walMonthly/walDeletes 摘除并推进 walCount、重算 dirty；失败保留集合下轮自动重发。基线重写路径独立保留——即使 WAL 失败，基线从活内存全量重建仍是自愈终点（写完截断 WAL + 清集合） |
| P2-6 | internal/usage/wal.go:19 | 注释去掉 "durable" 过强表述：「进程崩溃可恢复（flush 到 OS）；掉电 durability 不保证——无 fsync」 |
| P2-2 | internal/upstream/sse.go | Aggregate 上限判定改为 `trackedAggregateBytes()` = content + reasoning + 全部 tool_calls arguments 合计；超限照旧 `errFrameTooLarge` |
| P2-4 | internal/pool/state.go:619 | `Cooling` 或门纳入 `degradeUntil`；CoolRemaining/CoolKind 分支补 degrade 路（CoolKind="degrade"，取剩余更晚者） |
| F-01 | internal/redisstore/redisstore.go:263 | LoadBinds 循环后判 `iter.Err()`，出错 WARN 不阻断启动恢复（该 +4 行随本批一并纳入） |

## 没改什么（重要）

**F-1（EWMA 样本不足仍输出数字）不改代码**——消费方核验发现这是真实契约冲突：

- AGENTS.md:91 顶层契约：「ewma_success/ewma_latency_ms 样本不足时 kind=unknown，**不输出数字**」；
- 但 ADR-007 取舍一（docs/adr/ADR-007-evidence-and-unknown.md:62-73）记录：最初实现就是"样本不足不输出数字"，
  被 ADR-005 契约测试 TestEwmaStatusExposure 打回后**有意反转**为「保留数值 + unknown 标注」（rea 的
  "Preserve partial facts"：标注不确定性而非藏值）；
- 测试锁定现状：cost_evidence_test.go:115-117 冷启动断言 `EwmaSuccess != nil`；
  ewma_test.go:334-341 单样本断言数字输出。
- 面板 app.js 仅展示数字（sCooling 旁账号行），无"缺失即报错"逻辑，但改输出形状会破上述 2 条测试。

→ 冲突双方都有明确文档与测试牙齿，按任务书「回报组织层裁定」处理，本批不动。裁定选项：
(a) 维持现状（改 AGENTS.md:91 表述对齐 ADR-007）；(b) 反转代码 + 改 2 条测试 + ADR-007 补"再反转"段。

## 怎么验证的

- 工具链绕行：本机 Defender 删 go1.27.3 二进制，用现存 go1.27.2 toolchain + 临时 go.mod 降 `go 1.27.2`
  实跑（提交前已还原 go.mod）。
- `go build ./...` 0 error；`go vet` 四包 0 issue；`gofmt -l` 干净。
- 四包 `go test -count=1` 全绿；新增 5 条测试逐条 -v PASS（见下）。
- 新测试断言清单：
  - `TestFlushRetainsDirtyAndRetriesOnWALFailure`：失败保留 walDirty=1、walCount=0、dirty=true；
    修复冲突目录后重发成功（walDirty=0、walCount=1、WAL 含 u1）。
  - `TestAggregateReasoningAndArgsCountedInBudget`：纯 reasoning 流 >16MiB → errFrameTooLarge。
  - `TestStatusCoolingIncludesDegrade`：degrade 期 Cooling=true/CoolKind=degrade/CoolRemaining>0；
    NoteSuccess 后 Cooling=false。
  - `TestFRSLoadBindsScanMidErrorReturnsPartial`：SCAN 中途错 → 部分映射保留、不 panic。
  - `TestFRSLoadBindsScanImmediateErrorReturnsEmpty`：SCAN 开局错 → 空映射、不 panic。
- 未实跑项：`pre-commit.ps1` 全量（本机主 go 1.27.0 < go.mod 1.27.3，gate 验证待工具链恢复）。

## 怎么回滚

`git revert <本提交>`。无数据迁移、无配置变更、无上游报文变更；WAL 格式零变化（记录结构未动）。

---

**提交说明（2026-10-23 补）**：本批次改动已随 764303a（组织层 alerting/redisstore 提交）进入 main——共享 index 并行竞态所致，commit 归属在 alerting/redisstore 消息下；代码与测试内容完整（git show --stat 764303a 可核 17 files），组织层审查报告已作第六节竞态标注。
