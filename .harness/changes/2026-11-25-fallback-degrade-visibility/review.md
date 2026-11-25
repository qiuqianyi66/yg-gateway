# 2026-11-25-fallback-degrade-visibility 自评审

> 评审对象：本轮工作区 diff（前序已有 4 文件实现 + 本轮修复/测试/文档）。
> 评审者：实现者本人。**本文只是自评审，不能替代 L2 要求的独立复审。**

## 六维检查

### 1. 正确性

- **🟡 已修复（本轮主要发现）**：旧实现 `forwardToOne` 在拿到上游 `resp` 后就写 `X-YG-*` 三头；非流式 `io.ReadAll` 失败返回 `false` 时头已留在 `w.Header()`，级联到下一端点若连接失败、最终走 `writeOpenAIErrorHint`，客户端会收到带 `X-YG-Route: fallback` / `X-YG-Served-By: <失败端点>` 的**网关错误**。已把设置延后到两个唯一写点：流式 `WriteHeader` 前、非流式读完 body 并解析 model 后；新增 `TestFallbackFailureDoesNotLeakHeaders` 钉住（修复前实测红）。
- 流式：三头在 `WriteHeader` 前设置，`committed` 置位后级联终止语义未变；`X-YG-Upstream-Model` 不设置（body 未解析=unknown），已有断言。
- 非流式：`io.ReadAll` → usage 解析 → model 解析 → 三头 + Content-Type → `WriteHeader` → `Write`，顺序保证失败时未提交响应头。
- `endpointHost`：`url.Parse` 成功且有 Host 才返回 Host；invalid/空回落 `fallback-endpoint`，已用带 userinfo/path/query 的用例钉住只取 host。

### 2. 性能

仅在 fallback 路径执行（主池正常时不进入），增加 3~4 次 `Header.Set` 与一次 `url.Parse`；不在选号/SSE 热路径，无锁、无新分配压力。

### 3. 安全

- `X-YG-Served-By` 只暴露 `host[:port]`，不含 userinfo/path/query/fragment；`endpointHost` 有单测。
- 不新增凭证读取/透出；`X-YG-Upstream-Model` 来自上游 body，Go 的 `net/http` 在写头时会把换行替换为空格，不构成头注入。
- CORS 只暴露四个固定头名，不扩大 `Allow-Origin` 策略，不放行新请求头。
- 唯一新增暴露面是「备用源 host」——这是本变更的目的（客户端要知道谁服务），且 host 由运营者配置。

### 4. 一致性

- 代码、PRD 契约表、README 口径三处一致：**带头 = 备用源服务；不带头 = 未标记（可能是主池，也可能是中间层剥离）**，符合四态纪律（缺省不是否定证据）。
- 不改 ADR-015 的级联顺序/白名单/`realm=fallback` 归因；既有 fallback 测试（级联、4xx 透传、半截流）全绿。

### 5. 可维护性

- `setFallbackHeaders` 收口三头设置，避免流式/非流式两处漂移；注释写明「为什么必须延后到写点」。
- 测试覆盖成功（流式/非流式）、失败不残留、host 裁剪、CORS 暴露四类行为。

### 6. 架构合规

- 改动只在 `internal/server`；出站仍走 `upstream.OpenAICompatEndpoint`（R6）；未改上游请求报文/UA/协议常量（R8）；无新依赖、无新配置项、无数据模型变更。

## 结论

自评审未发现新的 🔴/🟡；本轮发现的 🟡（失败路径残留头）已修复并由常驻测试钉住。🟢 记录两条：CORS 暴露头在无 Origin/http 源时也会下发（浏览器无 ACAO 时忽略，无害）；流式中途失败后已写出的头无法撤回（响应确为备用源的部分响应，语义成立）。

## 未验证（不影响本轮机械结论，但不能按更强保证表述）

- 真实上游 fallback 端到端、浏览器跨域实际读取、生产部署：见 `summary.md` §三。
- **独立复审未执行**：L2 要求另一只读会话/子代理按 `doubao-coding-review-code` 复跑；本轮结论强度为「自评审 + 机械门禁绿」。
