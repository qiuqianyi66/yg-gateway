# ADR-014：fallback 单端点扩为列表级联的配置形态

- 日期：2026-10-22
- 关联：R-107（`docs/prd/REQ-multiupstream-template-2026-10-22.md`）
- 状态：已采纳

## 背景

现有 fallback 是单端点硬编码（`config.fallback.base_url/api_key` 单值 + handler
写死单端点透传）。要支持多备用源按序级联。配置形态与兼容策略存在真实权衡。

## 候选

1. **新增 `endpoints` 列表字段，旧单字段保留做兼容回退**（采纳）：
   `endpoints` 非空时按序级联；为空时合成旧单字段成一个端点。
2. **直接把 `fallback` 改成列表，旧配置迁移脚本**：破坏既有 config.json，
   违反「单二进制零依赖 + 双击即用」定位（老用户升级后 fallback 静默失效）。
3. **每个端点单独一个配置块（fallback_1/fallback_2...）**：枚举式，加第三个又要改。

## 决策

采纳方案 1。`config.Fallback.Endpoints []FallbackEndpointConfig`；装配时
`buildFallbackConfig` 优先列表、空则回退单字段合成。运行时 `server.FallbackConfig`
统一为 `Endpoints []FallbackEndpoint`，handler 按序级联循环。

理由：
- **向后兼容零回归**：旧 config.json 不改动仍生效，`buildFallbackConfig` 里
  `len(Endpoints)==0` 时回退单字段——老用户无感升级。
- **模板收敛**：URL 拼装/Bearer 鉴权/Accept 透传三件事收进新文件
  `internal/upstream/openai_compat.go` 的 `NewChatRequest`，级联每一项复用同一模板，
  不会出现「某端点漏透传 Accept」的漂移。
- **边界克制**：只做 OpenAI 兼容族（BaseURL+APIKey+模型白名单），不做「接所有
  provider」——AGENTS.md Product Direction 明确不是通用 AI 网关。

## 代价与边界

- 触发判定（`Covers(model)`）与级联转发（每端点 Models 白名单）用同一口径，
  避免「触发命中、转发漏判」。
- 级联顺序即优先级，不做权重/负载均衡（够用即止）。
- 非 OpenAI 兼容 provider 需另立适配文件，不在本模板内膨胀。
