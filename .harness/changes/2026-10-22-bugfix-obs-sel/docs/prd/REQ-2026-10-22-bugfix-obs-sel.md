# REQ-2026-10-22-bugfix-obs-sel：全仓体检修复批（usage WAL 重试 / SSE 聚合上限 / degrade 冷却展示 / LoadBinds 迭代错误）

## 必要性（阶段 0）

1. **真实问题**：版块 B 只读体检发现 4 处已定位缺陷——
   - usage WAL 追加失败后 flush 提前清空脏键集合，失败批永久丢失且注释谎称「下轮补写」；
   - SSE 非流式聚合的 16MiB 上限只量 content，reasoning/tool_calls arguments 无界；
   - /status 的 Cooling 布尔漏算 degradeUntil 第三截止；
   - LoadBinds 的 SCAN 迭代错误未检查（R10）。
2. **不做会怎样**：①观测台账在磁盘故障窗口内静默丢增量（月配额计数器重启后从旧基线复活）；
   ②畸形纯推理流可无界吃内存；③降权号在面板显示"不冷却"误导运维；④启动恢复静默吞错。
   均不报错、不告警——正是本项目最危险的一类（静默偏差）。
3. **现状是否已变**：问题代码逐行读过、测试基线实跑通过（go1.27.2 绕行），现状成立。
4. **更便宜手段**：均为小补丁（锁内集合保留 + 闭包计量 + 布尔或门 + iter.Err()），无更便宜替代。

**结论：做**（L2 轻量路径：范围已冻结，只动四包 + 测试 + 本文档）。

## 范围

| 编号 | 文件 | 验收（Given-When-Then） |
|---|---|---|
| P2-1 | internal/usage/wal.go, usage.go | Given WAL 目录写入失败；When flush；Then walDirty 保留、walCount 不推进、dirty 保持 true；消除失败条件后下轮 flush 重发成功且 WAL 含记录 |
| P2-6 | internal/usage/wal.go:19 | 注释去掉 "durable" 过强表述，写明"进程崩溃可恢复/掉电不保证" |
| P2-2 | internal/upstream/sse.go | Given 纯 reasoning 流累计超 16MiB；When Aggregate；Then 返回 errFrameTooLarge（此前 content=0 不报警） |
| P2-4 | internal/pool/state.go | Given 连败达阈 degrade 期内；When statusOf；Then Cooling=true 且 CoolKind="degrade"、CoolRemaining>0；NoteSuccess 后 Cooling=false |
| F-01 | internal/redisstore/redisstore.go | Given SCAN 中途/开局报错；When LoadBinds；Then 不 panic、返回部分映射/空映射（iter.Err() WARN 不阻断启动） |

## 不做（显式排除）

- **F-1（EWMA 样本不足输出数字）不改代码**：ADR-007 取舍一明确反转过该决策（"标注而非隐藏"），
  且 cost_evidence_test.go:115 / ewma_test.go:334 两条测试锁定现状。与 AGENTS.md:91 顶层契约
  存在真实冲突，回报组织层裁定（见 summary.md）。
- r8_golden、面板、server 等域不动。

## 回滚

`git revert <本提交>`；无数据迁移、无配置变更、无上游协议变更，回滚零副作用。
