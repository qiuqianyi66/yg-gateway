# 2026-11-25-fallback-degrade-visibility 变更摘要

> 目录：`.harness/changes/2026-11-25-fallback-degrade-visibility/`。
> 需求：`docs/prd/REQ-fallback-degrade-visibility-2026-11-25.md`
> 分级：L2（对外响应契约）
> ADR：不适用（沿用 ADR-015 的 fallback 级联，无新架构/数据模型/上游协议）

---

## 基线（先写清，避免把前序工作算成本轮成果）

本轮**开始前工作区已有未提交实现**（2026-11-21 补的降级响应头，4 文件 +72/−10：
`README.md`、`internal/server/fallback_test.go`、`handler.go`、`handler_fallback.go`）。
本轮不重复实现，做的是：补 PRD/变更记录、修掉复审发现的失败路径残留头缺陷、
补测试、跑门禁、提交。下表的「本轮」列按这个口径写。

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：internal/server/handler_fallback.go` | `setFallbackHeaders` 统一设置三头；**只在真正写响应头前调用**（流式写流前 / 非流式读完 body 后），修掉「读体失败返回 false 但头已残留」的缺陷；`endpointHost` 只取 host。 |
| `修改：internal/server/handler.go` | CORS 增加 `Access-Control-Expose-Headers: X-YG-Route, X-YG-Degraded, X-YG-Served-By, X-YG-Upstream-Model`，浏览器跨域客户端才读得到。 |
| `修改：internal/server/fallback_test.go` | 现有非流式/流式成功断言；新增 `TestFallbackFailureDoesNotLeakHeaders`（全部端点失败不得残留头）与 `TestEndpointHostOnlyHost`（只暴露 host）；流式断言 `X-YG-Upstream-Model` 为空=unknown。 |
| `修改：internal/server/cors_test.go` | 预检断言 `Expose-Headers` 精确列出四个头。 |
| `修改：README.md` | 能力表补降级可见口径；把「没有这些头 = 不是备用源」改成「带 = 备用源；没有 = 未标记（不能反推主池）」。 |
| `新增：docs/prd/REQ-fallback-degrade-visibility-2026-11-25.md` | 必要性四问、契约表、GWT、回滚。 |
| `新增：.harness/changes/2026-11-25-fallback-degrade-visibility/summary.md` | 本文件。 |
| `新增：.harness/changes/2026-11-25-fallback-degrade-visibility/review.md` | 六维自评审 + 复审发现与处置。 |

**没做的（明写，免得下轮重提）**：

- 不改响应 body、不加 JSON 字段、不加配置开关（OpenAI 兼容与「零新依赖」优先）。
- 不改 reqlog/usage 的 `realm=fallback` 归因（聚合事实另有一套，不重复造）。
- 不做面板可视化/告警（本项只解决 per-response 客户端可见）。
- 不把 `X-YG-Served-By` 扩成完整 URL（路径/查询/凭证不出网关）。
- README 的「模型/provider 声明口径」段是工作区**既有未提交文档**，按 L-21 不并入本任务提交，仍留在工作区待单独处理。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 先红（牙齿） | `go test ./internal/server -run TestFallbackFailureDoesNotLeakHeaders -count=1` | **FAIL**：`X-YG-Route="fallback"` 残留在失败响应（旧实现 `handler_fallback.go` 在拿到 resp 后就写头）。 |
| 修复后定向 | `go test ./internal/server -run 'TestFallback\|TestEndpointHostOnlyHost\|TestCORSPreflightHTTPSOriginReflected' -count=1` | `ok yg-gateway/internal/server 0.126s` |
| 全量测试 | `go test ./... -count=1` | exit 0（68.1s；`internal/panel` 61.2s、`internal/scheduler` 19.8s、`internal/server` 11.0s） |
| 静态检查 | `go vet ./...` | exit 0 |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出（clean） |
| 提交总闸 | `powershell -NoProfile -File scripts/pre-commit.ps1` | **通过（exit 0，90.5s）**：8 个改动文件；gofmt/R10/PS 地形/垃圾文件/build+vet/test/边界+深度/必要性/治理/预算/R6 共 11 项检查全绿；`go test ./...` 28 包 ok。 |
| 治理一致性 | `powershell -NoProfile -File scripts/check-harness.ps1` | 11 检项全 OK；`[5]` 未测由 pre-commit 检查 8 承担；`changes/*/summary.md` 208/208，REQ 58 篇命名合规。 |
| 推送后 CI | push `ecb23ce` → release `45624746884` / e2e `45624747148` | `test` / `gate` / `race` 与 `e2e` 全绿；`build-image` 红在已知 GHCR `write_package` 权限（`continue-on-error`，非本变更面）；`publish` 非 tag 跳过 |

**负向探针**：`TestFallbackFailureDoesNotLeakHeaders` 在修复前实测红（见上），修复后绿；探针即该常驻测试，未删除。CORS 暴露的牙齿由 `TestCORSPreflightHTTPSOriginReflected` 的精确断言承担。

## 三、未完成 / 待补（诚实清单）

- [ ] **真机 fallback 端到端未验**。本轮只有 `httptest` 边界（成功/失败/级联/半截流）。`requires`：一个真实可用的备用端点 + 主池全部不可用的一次窗口；不能为验证随意打生产账号。
- [ ] **浏览器跨域实际读取未验**。已钉响应头存在与 `Expose-Headers` 名单，但「浏览器在 https 源下确实能读到 X-YG-*」需要真实浏览器 + 跨域页面。`requires`：一个 https 源页 + 本机网关。
- [ ] **生产未部署**。当前生产仍是 2026-11-21 的二进制；本提交已推送 `origin/main`（2026-11-25），但未换版。`requires`：换版窗口（`deploy-prod.ps1 -Build`）。
- [ ] **独立复审未执行**。L2 要求另一只读会话/子代理复审；本轮只有自评审 + 机械门禁，结论强度按仓库惯例降级。`requires`：另一只读会话按 `doubao-coding-review-code` 复跑。

## 四、怎么回滚

`git revert <本提交>`。无 `state.json`/`usage.json` 迁移，无配置变更；客户端对未知响应头天然忽略。
生产回滚按 `deploy-prod.ps1` 的备份与退出码处理（本提交未部署，暂不涉及）。

## 五、Learning

根因：可见性标记在「拿到上游响应」时写入，而不是「真正写出响应」时写入；失败路径复用同一个 `ResponseWriter`，标记会残留，把网关错误伪装成「备用源已服务」。
同类风险：任何在 `ResponseWriter` 上提前写 header、又允许返回 `false` 继续级联或走错误路径的代码，都应把标记延后到唯一写点。
