// protocol.go 出站协议常量（R8 红线的**单一来源**）。
//
// 为什么集中在这里（2026-11-02 代码复用体系，实测依据见
// docs/prd/REQ-yg-gateway-code-reuse-2026-11-02.md）：
//   - `CLI/2.63.2 CodeBuddy/2.63.2` 这一串在三处各有一份字面量
//     （`internal/upstream/models_fetch.go` 的 codeBuddyCLIUA、`internal/panel/login.go`
//     与 `cmd/login/main.go` 的 clientUA）；
//   - 两个 Origin 与两个登录基址同样在 panel/login.go 与 cmd/login/main.go 各一份。
//
// 这些字符串属于**报文内容**（AGENTS.md R8：上游协议数据不得改名）——散成多份时，
// 改一处忘一处就是风控特征漂移，而且不会报错。集中后由 `TestProtocolConstantsR8`
// 把值钉死：任何人改这些字面量，测试立刻红。
//
// 纪律：本文件只放**协议事实**（上游认的字符串），不放业务策略；改这里的值等于改对外
// 契约，必须同步 R8 影响面（headers.go / client.go / useragent*）并重跑真实账号冒烟。
package upstream

const (
	// LoginBaseCN 设备授权（OAuth device flow）CN 域基址。
	LoginBaseCN = "https://copilot.tencent.com"
	// LoginBaseGlobal 设备授权国际版基址。
	LoginBaseGlobal = "https://www.workbuddy.ai"
	// OriginRefererCN 出站 Origin/Referer 的 CN 域（三段式指纹的一部分）。
	OriginRefererCN = "https://www.codebuddy.cn"
	// OriginRefererGlobal 出站 Origin/Referer 的国际版域。
	OriginRefererGlobal = "https://www.workbuddy.ai"
	// CLIUserAgent 登录链路使用的 CLI UA 字面量（R8：报文内容，不得改名）。
	CLIUserAgent = "CLI/2.63.2 CodeBuddy/2.63.2"

	// DefaultBillingBaseCN 计费域（积分/套餐查询）CN 默认基址。
	// 与 OriginRefererCN 字面相同但**语义不同**：那是 Origin/Referer 头，这是请求目标域。
	DefaultBillingBaseCN = "https://www.codebuddy.cn"
	// DefaultBillingBaseGlobal 计费域国际版默认基址（global 账号打 CN 域实测 401）。
	DefaultBillingBaseGlobal = "https://www.workbuddy.ai"
)
