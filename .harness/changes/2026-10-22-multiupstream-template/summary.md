# 2026-10-22-multiupstream-template 变更摘要

> 需求：`docs/prd/REQ-multiupstream-template-2026-10-22.md`
> 分级：L2（业务逻辑 / 配置形态）+ L3（provider 接入模板）
> ADR：`docs/adr/ADR-014-fallback-cascade.md`

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `internal/upstream/openai_compat.go`（新增） | OpenAICompatEndpoint 接入模板：ChatCompletionsURL + NewChatRequest（URL/Bearer/Accept 收敛一处） |
| `cmd/server/config.go`（修改） | Fallback 增 `endpoints` 列表字段；新增包级 `FallbackEndpointConfig` 类型 |
| `cmd/server/main.go`（修改） | buildFallbackConfig 优先列表、空则回退单字段合成（向后兼容） |
| `internal/server/handler.go`（修改） | FallbackConfig 改 `Endpoints []FallbackEndpoint` + Covers()；fallbackForward 改级联循环；转发体抽 forwardToOne 用模板 |
| `internal/server/fallback_test.go`（修改） | 迁移新列表形态 + 新增级联顺序/跳端两个用例 |
| `cmd/server/coverage_test.go`（修改） | buildFallbackConfig 断言迁移到 Endpoints 形态 |
| `README.md`（修改） | 路线图「更多上游接入」更新为已实现 |

**没做的（明写）**：非 OpenAI 兼容 provider；fallback 权重/负载均衡；面板 fallback 展示。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 单测 | `go test ./internal/server/ ./cmd/server/` | 全绿 |
| 门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 全绿 |

**负向探针**：TestFallbackCascadeOrder——把循环改成只用第一个端点 → 必红（牙齿在）。

## 三、未完成 / 待补（诚实清单）

- [ ] 旧单端点 config.json 端到端实测：requires 一次真实旧配置加载（单测已覆盖合成逻辑）。

## 四、怎么回滚

`git revert <本提交>`。新增 openai_compat.go 删除；config/main/handler/test/README 还原。旧 config.json 无 schema 破坏，回滚后旧字段仍生效。

## 五、Learning

根因：fallback 单端点硬编码 → 列表化 + 模板收敛 URL/鉴权，向后兼容靠「空列表回退单字段合成」。
