package upstream

// ArkEndpoint 火山方舟 Ark 的 OpenAI 兼容端点（豆包模型官方正门）。
//
// 协议形状与 OpenAICompatEndpoint 完全一致（/chat/completions + Bearer），差异只在
// 模型目录与额度口径——那些归 provider.Descriptor（静态形状）与 quota（运行态）管，
// 不在本类型里重复。
//
// 因此本类型**复用** OpenAICompatEndpoint 的 URL 拼装 / Bearer 鉴权 / Accept 透传，
// 不另起 HTTP 客户端（R6：出站收口在 internal/upstream，不新增旁路）。未来 Ark 专属
// 逻辑（如额度查询端点）挂在这里，而不是膨胀 OpenAICompatEndpoint 成万能适配层。
type ArkEndpoint struct {
	OpenAICompatEndpoint
	// Models 该 Ark 实例接管的模型 ID（显式白名单路由判定用）。空 = 未知
	// （四态纪律：空 ≠ 不支持，装配层按 unknown 处理，禁止 len==0 判不支持）。
	Models []string
}
