# multi-provider-presets

## 改了什么

- `internal/provider/provider.go`：新增 `Preset`/`Presets`/`LookupPreset`，登记
  Cerebras（OpenAI 兼容、`https://api.cerebras.ai/v1`、`gpt-oss-120b` /
  `qwen-3.8-27b`；注释明确免费额度有限、模型需核对）。
- `cmd/server/config.go`：`ProviderConfig.preset` 展开，显式字段优先；未知 preset
  fail fast。
- `cmd/server/provider_wiring.go`：`ensureProviderAccounts` 在 provider 级
  `api_key` 且池内无该 provider 账号时创建内存账号 `provider:<id>`；已有 auths
  账号不覆盖。
- `internal/server/chatrun.go`：只有 codebuddy 账号走 `NeedsRefresh`；API Key
  provider 不再误走 OAuth refresh。
- `internal/server/handler_provider.go`：provider 出站失败时写入 `run.lastErr`，
  末端不再退化成 `no_healthy_account`；客户端取消走 499/interrupted，不罚 provider
  账号。
- `internal/upstream/errors.go` + `internal/server/chatflow.go`：新增 `ErrProvider`，
  provider 5xx 映射 503、4xx 映射 502，统一 `upstream_error` 并透传原文。
- `internal/server/chatflow.go`：OpenAI 风格 error 内层的 requestId 也进入可读
  message。
- `config.example.json` / `README.md`：补 Cerebras preset 示例与免费额度限制说明。
- 测试：preset 解析/覆盖/未知 preset、虚拟账号数量/不重复、仅配置 provider 的
  入站→出站端到端（Bearer + model）、provider 5xx 原文透传、provider 客户端
  取消不罚号。

## 怎么验证

- `go test ./cmd/server ./internal/server -count=1`
- `scripts/pre-commit.ps1`（11/11）
- 生产验证：代码部署后，用启用 provider 的配置发一条非流式请求；本机无
  Cerebras API key，未做真实上游调用（unknown，不冒充成功）。

## 怎么回滚

- 配置：删除 provider 条目或 `enabled=false`；虚拟账号不落盘，重启即消失。
- 代码：还原上述文件并删除新增测试；无数据迁移。
