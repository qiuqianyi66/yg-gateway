# 架构地基 + Evidence/Unknown 语义 — 变更总结

- 日期：2026-10-15
- 分支：main
- 相关：`docs/prd/REQ-yg-gateway-architecture-foundation-evidence-2026-10-15.md`、
  `docs/adr/ADR-007-evidence-and-unknown.md`、`AGENTS.md`
- 起因：复核豆包基于 morluto/rea 源码深读给出的移植清单

## 一、复核结论（为什么不是照单执行）

| 原清单项 | 复核后处理 |
|---|---|
| P0-1 抽 `UpstreamClient` interface | **降到 P1 并暂缓**：`internal/upstream` 已存在且是事实 provider 层，不是"新建"；且零用户可见收益、要动两个 2000+ 行文件的核心类型 |
| P0-2 依赖方向守卫 | **保留但降级为 XS**：实测 `server→pool` 单向依赖**现在就已经正确**，脚本的价值是冻结现状而非修复问题 |
| P2 全部（MCP 全家桶） | **整体延后**：rea 的 MCP 是结果不是原因；yg-gateway 是 7×24 服务，MCP 的 ROI 远低于 Evidence/Unknown |
| （清单漏项） | **提到 P0**：四态标记 + Unknown 消解实体 —— 这是 rea 最值钱的东西 |

## 二、改了什么

### F-801 依赖方向守卫（XS）
`scripts/check-boundaries.ps1`：白名单式（只允许列出的方向），新包默认拒绝。
白名单由 `-Fix` **实测生成**，不是拍脑袋写的。

为什么白名单而非黑名单：新包被随手引入时白名单会默认拒绝，迫使显式声明归属层；
黑名单则默认放行，直到有人记得来更新。

### F-802 顶层 AGENTS.md（XS）
- Product Direction：四条定位 + **明确不是什么**（不是计费平台/不是通用网关/
  不是一次性分析工具）
- Boundary Contracts：R8（上游协议数据不改名）等红线上提为顶层硬约束
- **三态语义纪律**（本轮核心新增）+ 已知推断/未知清单

### F-803 事实四态标记（`internal/evidence/`）
`Observe` / `Derive` / `Infer` / `Unknown` / `UnknownWithSamples` 五个构造函数；
序列化自带 provenance（kind/basis/samples）；**零值即 unknown**。

### F-804 成本 tier 与 EWMA 接入四态
- `ModelCostStatus.Provenance`：tier 0/2 标 `inference` + 样本数 + basis
- `Status.EwmaProvenance`：样本达标标 `derivation`，不足标 `unknown`

### F-805 Unknown 消解链路（`internal/unknown/` + pool 接入）
- 带 revision 的注册表：`requires` 必填、消解需证据 ID、stale revision 报错、
  内容哈希幂等去重、`List`/`All` 分离
- pool：`TrackCostUnknown`（请求路径登记，`sync.Map` 快路径）+ `NoteModelCost`
  自动消解；全程可选，未注入 registry 时 no-op（零回归）
- handler：选号成功后调用（**不在 pick 临界区内**）
- `/status` 的 `accounts[]` 带 `unknowns`；`/metrics` 带 `yg_gateway_unknowns_open`

### F-806 技能边界重写（S）
11 个 `.harness/skills/*/SKILL.md` 的 description 补「【跳过】」条件 +
frontmatter 加 `version: 1`。解决"只改文档的任务也被拉进十阶段全流程"。

### F-807 upstream 收窄为 interface —— 未做（需 YG 确认）

## 三、怎么验证的

### 有牙齿的测试（还原改动必须 FAIL）
| 测试 | 覆盖 |
|---|---|
| `TestFactZeroValueUseful` | 零值 Fact 落 unknown（不是 observation） |
| `TestFactUnknownKeepsPartialSamples` | unknown 带 2/5 样本时不能丢样本数 |
| `TestModelCostProvenanceInference` | tier 标 inference + samples |
| `TestEwmaProvenanceUnknownWhenCold` | 冷启动标 unknown（**但数值仍透出**） |
| `TestResolveStaleRevision` | 过期 revision 必须报错（禁盲重试） |
| `TestResolveNeedsEvidence` | 无证据不能消解 |
| `TestRecordRequiresNonEmpty` | 说不清需要什么就不给登记 |
| `TestTrackCostUnknownRecordsOnce` | 同组合只登记一条 |
| `TestNoteModelCostResolvesUnknown` | 实测 credit 自动消解 |
| `TestUnknownRegistryOptional` | 未注入时全部 no-op（零回归） |
| `TestMetricsUnknownsGauge` | gauge 出现在 /metrics |

### 一次性踩坑（已修正）
最初实现是"EWMA 样本不足就不输出数字"，**立刻被 `TestEwmaStatusExposure`
抓到**——它打破 ADR-005 的既有透出契约。修正为"保留数值 + 标注 unknown"，
理由见 ADR-007 取舍一（rea 的 "Preserve partial facts"）。

### 门禁
```
go test ./... -count=1   → 20 包全绿（较上轮 +2：evidence / unknown）
go vet ./...             → 无输出
gofmt -l ./cmd ./internal → 仅历史文件漂移（pre-existing）
scripts/check-boundaries.ps1 → 退出码 0
```
边界脚本的**牙齿**也验证过：故意在 `internal/pool/` 里 `import
"yg-gateway/internal/server"` → 退出码 1 并报 `pool -> server`；还原后回 0。

### 真机验证（D:\yg-gateway，PID 24572）
| 验证项 | 结果 |
|---|---|
| healthz | `{"healthy":3,"realm_servable":{"cn":true,"global":false},"total":4}` |
| 流式请求 | 全链路正常，含 `[DONE]`，无 ping 帧（默认关，零回归） |
| 非流式 | 200 |
| `/metrics` | `yg_gateway_unknowns_open` 族存在 |
| `/status` provenance | **同快照内 `samples=1` 与 `samples=145` 并存**（本 ADR 要解决问题的直接证据） |
| 未消解未知 | 6 发冷门模型全成功 → `unknowns_open 0`（登记→消解闭环，无残留） |
| 未消解未知（负例） | 请求不存在的模型（上游 400、无 credit）→ `unknowns_open 3`，三账号各一条，Q/R/rev 正确 |

## 四、部署与回滚

- 二进制备份：`D:\yg-gateway\yg-gateway.exe.pre-evidence.bak`（R15）
- **无需改配置**：全部改动缺省即旧行为；unknown registry 由 main 自动装配
- 回滚：回退二进制即可；**不改任何落盘格式**（state.json / usage.json / 归档
  JSONL 均未动），无数据迁移
- 注意：`unknown.Registry` 是进程级状态，重启归零（有意设计，见 ADR-007 取舍三）

## 五、下一轮候选

1. **上游收窄为 interface**（原 P0-1，需 YG 拍板：零用户可见收益、风险最高）
2. `/v1/models` 的 `supported_efforts` 空 / `supports_images` 缺失标 `unknown`
   —— 本轮只把这套机制建好，尚未接到模型目录输出（涉及是否换 UA 拿完整目录，
   触碰 R8 边界，需单独决策）
3. unknown 持久化（ID 已是内容派生，可直接落盘）
4. `internal/tools/doccheck` 增加 lint：新暴露的事实缺 provenance 时提示
5. pick 侧 scratch 切片的 `sync.Pool` 复用（收益待 bench 量化）

## 六、已知问题（本轮未修，不在范围）

- `internal/pool` 的 `TestEwmaLatencyPenalizesSlowInPick` 偶发 flake
  （8 次跑 1 次失败；单独跑必过）。根因是**时间敏感**：测试用
  `setLastUsed(time.Now())` + idle 补偿，三次调用跨毫秒边界会让权重微变。
  与本轮改动无关（不触碰 pick 时间逻辑），记录待后续单独处理。
- `gofmt` 的 pre-existing 漂移（`internal/httpauth/index.go`、`internal/panel/panel.go`）。
- 本机无 gcc，`-race` 不可用；并发正确性靠设计 + 单测保证。
