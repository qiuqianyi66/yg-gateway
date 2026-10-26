# 2026-10-26 opendesign-lessons-absorb（OpenDesign 源码学习吸收计划）

> **状态：全部 6 个任务完成/确认。**
> 来源：对 github.com/nexu-io/open-design（104k star）的源码级对照分析。

## 背景

对照 OpenDesign（消费端多 CLI 编排器）与 YG Gateway（服务端单上游账号池）的源码后，
发现 6 个可吸收的工程思想。不是照搬代码——上下文不同，只取可移植的纪律和模式。

详细 PRD：`docs/prd/REQ-001.md`

## 任务清单（按优先级）

| # | 优先级 | 任务 | 分级 | 状态 |
|---|---|---|---|---|
| P0-1 | 最高 | R6 红线机器检查（pre-commit grep） | L1 | ✅ 已完成 |
| P0-2 | 最高 | 错误分类加 evidence_level 标签 | L2 | ✅ 已完成 |
| P1-1 | 高 | mocks 录制回放（零 token 测试） | L2 | ✅ 已确认（现状已符合） |
| P1-2 | 高 | SSE goroutine 回收路径检查 | L1 | ✅ 已确认（现状已符合） |
| P2-1 | 中 | 主动探测（账号健康巡检） | L2 | 📝 已设计，暂不实现 |
| P2-2 | 中 | 请求指纹追踪（reqlog 加 hash） | L2 | ✅ 已完成 |

## P0-1 完成记录

**做了什么**：
- 新建 `scripts/check-upstream-isolation.ps1`：扫描 internal/ 下除 upstream/ 外的包，检出站请求调用（http.NewRequest/http.Get/http.Post/http.DefaultClient/.Do(req）），白名单放行合法例外。
- 挂进 `scripts/pre-commit.ps1` 作为检查 11/11。
- 白名单：`internal/panel/login.go`（OAuth 设备授权流程，合法直连上游 auth 端点）。
- 负向探针验证：在 internal/server/ 临时加 http.Get 到 workbuddy.ai，检查正确报红后删除。

**怎么验证的**：
- `powershell -NoProfile -File scripts/check-upstream-isolation.ps1` → exit 0（通过）
- 负向探针 → exit 1（正确检出违规）
- `powershell -NoProfile -File scripts/pre-commit.ps1 -Fast` → 全绿（11 项检查全部通过）

**怎么回滚**：
- 删掉 `scripts/check-upstream-isolation.ps1`
- 从 `scripts/pre-commit.ps1` 删掉检查 11/11 的 section
- 把所有「/11」改回「/10」（检查编号分母）

## P0-2 完成记录

**做了什么**：
- 新增 `EvidenceLevel` 类型（high/medium/low 三级），标注错误判定的证据强度。
- 在 `Error` 结构体加 `Evidence EvidenceLevel` 字段。
- 新增 `ClassifyError(status, body) *Error` 函数：返回完整 Error 对象（含 evidence level）。
- 新增 `inferEvidenceLevel()` 辅助函数：根据 ErrKind + status + body 反推证据强度。

**设计考虑（安全性/可维护性/扩展性）**：
- **安全性**：高确定性错误（402/429/精确业务码）可以直接执行严厉操作（禁用账号、长冷却）；低确定性错误（纯文案猜测、兜底）保守处理（降级重试、不罚账号）。把猜测当事实会导致基于噪声误禁健康号。
- **可维护性**：旧 `Classify()` 函数保留不动，向后兼容；新代码用 `ClassifyError()`。现有调用点零改动。
- **扩展性**：后续如果需要精确到每条判定路径的 evidence level，可以把 Classify 拆成 classifyWithEvidence，逐个分支带 evidence level。当前方案是「事后推断」，大部分情况准确，边界情况有偏差但不影响正确性。

**怎么验证的**：
- `go build ./internal/upstream/` → 通过
- `go test ./internal/upstream/ -count=1` → 全部通过（2.958s）

**怎么回滚**：
- 删掉 `EvidenceLevel` 类型定义
- 从 `Error` 结构体删掉 `Evidence` 字段
- 删掉 `ClassifyError()` 和 `inferEvidenceLevel()` 函数
- 现有调用点零改动（因为旧 Classify 保留了）

## P1-1 完成记录（mocks 录制回放）

**结论：现状已符合思想，无需额外改动。**

**做了什么**：
- 检查了 upstream 包的测试用例（client_test.go 等）。
- 现状：测试都是用硬编码 JSON 字符串模拟上游响应，不真的调上游 API，零 token 消耗。
- 这正是 OpenDesign mocks 思想的核心：测试不依赖真实上游，用录制好的 fixture 回放。

**为什么不做额外改动**：
- 现在的测试用例已经是手写的"录制好的 fixture"，覆盖了各种边界情况（11102/11115/11135/402/429 等）。
- 再搞一套"录制工具"是过度工程——手写 fixture 已经足够好，而且更可控。

## P1-2 完成记录（SSE goroutine 回收路径检查）

**结论：现状已符合 R9 红线，无需额外改动。**

**做了什么**：
- 检查了 sse.go 里的 keepalive goroutine。
- 回收路径完整：
  - `kaDone.Add(1)` 启动前计数
  - goroutine 内 `defer kaDone.Done()` 结束时计数减一
  - `stopKa` channel 作为停止信号
  - `defer close(stopKa); kaDone.Wait()` 函数返回时关闭停止信号并等待 goroutine 退出
  - 定时器有 `defer t.Stop()`
- 符合 R9「goroutine 必须有回收路径」的要求。

## P2-1 完成记录（主动探测，已设计暂不实现）

**结论：设计方案已就绪，暂不实现代码。**

**为什么暂不实现**：
- 主动探测本身会烧 token、可能触发上游风控。
- 需要先跟 CodeBuddy 的限流策略对齐：探测频率多少合适？探测用什么端点最便宜？
- 用户明确说过"不要为了验证配置而随意调用付费 AI API，避免浪费 token/额度"。

**设计方案（后续要做时参考）**：
1. **触发时机**：账号冷却结束后，先探测一下再放回池里（不是定时全池巡检）。
2. **探测方式**：用最轻量的端点（比如 GET /v1/models），不是 chat 请求。
3. **失败处理**：探测失败 → 继续冷却，不进池。
4. **配置开关**：`probe_on_cooldown_end`，默认关。
5. **频率限制**：每个账号每小时最多探测一次，避免探测风暴。

## P2-2 完成记录（请求指纹追踪）

**做了什么**：
- 在 `upstream.Error` 结构体加 `UpstreamRequestID` 字段。
- 在 `reqlog.Event` 结构体加 `UpstreamRequestID` 字段。
- 新增 `extractRequestID()` 函数：从错误 body 里解析上游的 requestId 字段。
- `ClassifyError()` 自动填充 UpstreamRequestID。

**设计考虑**：
- **可观测性**：看到网关日志里的错误时，可以拿着上游 requestId 去查上游日志，关联网关请求和上游侧的 trace。
- **安全性**：只记录上游返回的 requestId，不记录用户敏感信息（R1/R2）。
- **可维护性**：omitempty 字段，旧日志行没有这个字段也能正常解析。

**怎么验证的**：
- `go build ./internal/...` → 通过
- `go test ./internal/upstream/ ./internal/reqlog/ -count=1` → 全部通过

**怎么回滚**：
- 从 `upstream.Error` 删掉 `UpstreamRequestID` 字段
- 从 `reqlog.Event` 删掉 `UpstreamRequestID` 字段
- 删掉 `extractRequestID()` 函数
- `ClassifyError()` 里删掉 UpstreamRequestID 填充逻辑

## 不做的

- 不引入 Electron/sidecar 多进程
- 不做 30+ CLI 适配抽象
- 不做 prompt 双轨灰度
- 不做用户指导文案体系

## 怎么验证（总闸）

每个子任务完成后跑：
```
powershell -NoProfile -File scripts/pre-commit.ps1
go test ./...
go vet ./...
gofmt -l ./cmd ./internal
```

## 怎么回滚

全部是增量改动：
- pre-commit 脚本：删掉新加的检查段
- Error/reqlog 结构：omitempty 字段，双向兼容
- mocks 测试和 fixture：删掉目录即可
- scheduler 探测任务：配置开关关掉

## 诚实记录

- 本 plan 基于一次源码通读，未做逐行验证。动工前每个子任务需重新确认受影响文件清单。
- evidence_level 的三级标签（high/medium/low）是初步设计，实际做时可能调整为其他粒度。
- mocks 回放的 fixture 格式尚未设计（JSON vs Go testdata），动工前先定。
- 主动探测的频率和风控边界需要先跟上游 CodeBuddy 的限流策略对齐，避免探测本身触发风控。
