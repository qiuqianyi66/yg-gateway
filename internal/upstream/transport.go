// transport.go 出站连接构造的单一事实来源（Transport + http.Client，吸收
// kongjianguan 4 连击实测经验的前三件，见 .claude/reports/fork-scan-absorb.md T-2）：
// 真正禁 h2 / TLS 握手超时 / 短 keepalive 探测 / 显式重定向策略，全参数集中定义
// 可测试可调整。第四件 DisableKeepAlives 按报告 trade-off 不吸收（每请求 TLS
// 握手开销与连接复用意图相反），分析见 .claude/reports/transport-hardening.md。
package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"yg-gateway/internal/logfmt"
)

// 连接层参数集中定义（与 server/backoff.go 同风格：一处定义，测试可回读断言）。
const (
	// dialTimeout TCP 连接建立上限。半死连接的第一道闸：连不上就快速失败
	// 轮转换号，不再干等系统 TCP 重传窗口（kongjianguan 实测半开 TCP 单次
	// TTFB 卡 936s——默认 Dialer 无超时上限）。
	dialTimeout = 10 * time.Second
	// dialKeepAlive TCP keepalive 探测周期。默认 Dialer 2h 才发首个探测——
	// NAT 黑洞里 2h 足够连接半死且被复用。15s 周期让死连接在 15~30s 内被
	// 内核掐掉（RST/ETIMEDOUT），复用侧立即感知而非卡到重传窗口。
	dialKeepAlive = 15 * time.Second
	// tlsHandshakeTimeout TLS 握手上限。现役此前完全缺失——握手挂起时无任何
	// 层兜底（ResponseHeaderTimeout 只在请求写完后才计时），只能干等到
	// HTTP.Client.Timeout(120s)。
	tlsHandshakeTimeout = 10 * time.Second
	// idleConnTimeout 空闲连接池保留时长。从 90s 收到 30s：WAF 风暴后上游
	// NGI 常态性掐闲置连接，90s 池里的连接多半已死（kongjianguan 同款取值）；
	// 复用侧仍有 15s keepalive 兜底识别。
	idleConnTimeout = 30 * time.Second
	// responseHeaderTimeout 聊天 SSE 首字节前（响应头）硬上限。从 120s 收到
	// 60s：kongjianguan 笔记实录成功请求 TTFB 曾到 16s（慢模型冷启动），
	// 60s ≈ 3.75× 观测最坏健康首包，留足慢冷启动余量；同时把半死连接场景的
	// 单请求卡死从 2 分钟压到 1 分钟（MaxRotate 默认 3 次的最坏轮转从 6 分钟
	// 压到 3 分钟）。不取 kongjianguan 的 20s：其 20s 是 DisableKeepAlives+
	// timedConn 20s 写超时组合的取值，我们保留连接复用，须按自身慢冷启动
	// 观测留余量。语义核对（任务书设计纪律）：ResponseHeaderTimeout 只计响应
	// 头到达前的时长，头到达后 SSE 长流不受影响（流中空闲由 IdleTimeout 监控，
	// 见 idle.go），不误杀长流——transport_test.go 有显式回归。
	//
	// 注意：本常量只是 newTransport 的构造默认，main.go 会按 config
	// upstream.header_timeout_seconds 无条件覆盖。因此生产生效值 = config
	// 解析值（未配置时 normalize 回落 timeout_seconds，默认 120），本 60s 仅作
	// 「Config 未接线/测试裸用」时的安全网——与 config.example.json 的取值
	// 对齐避免三处口径漂移（transport 60 / config 回落 120 / example 60）。
	responseHeaderTimeout = 60 * time.Second
)

// maxIdleConns / maxIdleConnsPerHost 连接池容量（既有值，一并集中定义）。
const (
	maxIdleConns        = 100
	maxIdleConnsPerHost = 20
)

// newDialer 构造出站拨号器（DialContext 的 Timeout/KeepAlive 参数集中于此，
// 供测试回读断言）。
func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: dialKeepAlive,
	}
}

// newTransport 构造共享出站 Transport（HTTP 与 ChatHTTP 同一实例，连接池不重复）。
// 分两层防半死连接：
//   - TLS 层：空 TLSNextProto 真正禁 h2（kongjianguan 二次修正的实证：ForceAttemptHTTP2=false
//     只对自定义 Dial 生效，默认 TLS 经 ALPN 仍协商出 h2，半死 h2 流复用表现为
//     "http2: timeout awaiting response headers"——唯一正确写法是置空映射，让 ALPN
//     完成后无 h2 协议可用，连接退回 HTTP/1.1）。
//   - TCP 层：DialContext 10s 建连上限 + 15s keepalive 探测，半开连接在建立期
//     和复用期都能被快速识别（见 dialTimeout/dialKeepAlive 注释）。
func newTransport() *http.Transport {
	tr, _ := newTransportWithProxy("")
	return tr
}

// redactProxyURL 抹掉代理 URL 里的 userinfo（`user:pass@`），只保留 scheme://host。
//
// 为什么必须抹（2026-11-02 独立复审 🔴-1，R1/R2 红线）：ProxyURL 按设计就是含凭据的
// 形态（见 internal/auth/auth.go 的注释示例 `http://user:pass@proxy:8080`），原样打进
// WARN 日志等于把账号代理密码写进 stderr / 日志文件，而运维巡检与归档都会读它。
//
// 解析失败时**连原文都不能打**：此时无法判断是否含 userinfo，一律按"可能含凭据"处理。
//
// 脱敏逻辑的单一实现已下沉 internal/logfmt（auth 视图 / pool.Status / 本日志三处共用），
// 本函数只是薄包装——禁止在此另起实现（漂移即泄密）。
func redactProxyURL(raw string) string {
	return logfmt.RedactProxyURL(raw)
}

// proxyEffective 账号出站代理的生效状态（/status 观测三态，P0-2）。
//
// 为什么需要第三态 proxyFailed（而非只有"配了/没配"）：fail-closed 之后，代理构造失败
// 的账号**出站全失败**（请求错误冒泡换号）——运维必须能从观测看出「这个号为什么一直
// 在换号失败」，否则会误判成账号本身的问题。magpie REA 第④节结论：显式代理必须
// fail-closed，且 per-account 观测是 yg 自己能补的（magpie 自身也是 unknown）。
type proxyEffective int

const (
	// proxyDirect 未配置代理（直连，ProxyURL 为空）。
	proxyDirect proxyEffective = iota
	// proxyActive 代理已配置且 Transport 构造成功（连接期失败由 dial 错误冒泡，不在此态）。
	proxyActive
	// proxyFailed 代理已配置但构造失败（fail-closed，该账号出站必失败）。
	proxyFailed
)

// errProxyConfig 显式代理配置错误的哨兵（fail-closed 语义，见 failDial）。
// 显式配置了代理的账号，代理构造失败 = 该账号出站失败（请求错误冒泡 → chatrun
// 网络层抖动分支换号），**不**静默回退直连——直连会让多账号共用一个出口 IP，
// per-account 代理想切断的风控关联重新出现，且不报错（最危险的形态）。
// 直连路径（proxyURL == ""）不受影响，仍是唯一合法的直连来源。
var errProxyConfig = errors.New("proxy config error (fail-closed)")

// failDial 构造一个 dial 必然失败的 DialContext，作为代理配置错误的载体。
// 错误消息必须经 redactProxyURL 脱敏（R1/R2：代理 URL 可含 userinfo，绝不出日志/错误）。
func failDial(cause error) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, cause
	}
}

// newTransportWithProxy 构造带 HTTP/SOCKS5 代理的出站 Transport。
// proxyURL 为空 = 直连（与 newTransport 等价）。
//
// 支持的代理 scheme：
//   - http:// 或 https:// → HTTP 代理（CONNECT 隧道）
//   - socks5:// → SOCKS5 代理
//
// 用途：per-account 代理出口，切断多账号共用 IP 的风控关联。
// 每个不同的 proxyURL 对应一个独立的 Transport 实例（连接池隔离）。
//
// 日志纪律：本函数只打印 redactProxyURL(proxyURL)——绝不打印含 userinfo 的原文（R1/R2）。
// newTransportWithProxy 构造带 HTTP/SOCKS5 代理的出站 Transport，并返回代理生效状态
// （P0-2 /status 观测三态）：proxyURL 为空 = proxyDirect；构造失败 = proxyFailed
// （fail-closed，该账号出站必失败）；构造成功 = proxyActive。
func newTransportWithProxy(proxyURL string) (*http.Transport, proxyEffective) {
	dialer := newDialer()
	tr := &http.Transport{
		DialContext: dialer.DialContext,
		// 空 TLSNextProto（非 nil）真正禁 h2：见函数注释。必须 make 而非 nil——
		// nil 表示「让标准库注入默认 h2 映射」（kongjianguan 实测：设
		// ForceAttemptHTTP2=false 后日志仍报 h2 timeout，正是这个陷阱）。
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
	if proxyURL == "" {
		return tr, proxyDirect
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		log.Printf("WARN: [upstream] 代理 URL 解析失败（fail-closed，该账号出站将失败）: %s (%v)", redactProxyURL(proxyURL), err)
		tr.DialContext = failDial(fmt.Errorf("%w: url parse %s: %v", errProxyConfig, redactProxyURL(proxyURL), err))
		return tr, proxyFailed
	}
	switch u.Scheme {
	case "http", "https":
		// HTTP 代理：用 Transport.Proxy 方式
		tr.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		// SOCKS5 代理：用 x/net/proxy 构造 Dialer
		// 变量名 socksAuth 而非 auth：本仓大量文件 import yg-gateway/internal/auth，
		// 同名局部变量会遮蔽包名（独立复审 🔵-2）。
		socksAuth := &proxy.Auth{}
		if u.User != nil {
			socksAuth.User = u.User.Username()
			// 忽略：URL 未带密码时 Password() 返回空串+nil，不影响 SOCKS5 认证
			socksAuth.Password, _ = u.User.Password()
		}
		socksDialer, err := proxy.SOCKS5("tcp", u.Host, socksAuth, dialer)
		if err != nil {
			log.Printf("WARN: [upstream] SOCKS5 代理初始化失败（fail-closed，该账号出站将失败）: %s (%v)", redactProxyURL(proxyURL), err)
			tr.DialContext = failDial(fmt.Errorf("%w: socks5 init %s: %v", errProxyConfig, redactProxyURL(proxyURL), err))
			return tr, proxyFailed
		}
		// 类型断言：确保是 ContextDialer
		if cd, ok := socksDialer.(proxy.ContextDialer); ok {
			tr.DialContext = cd.DialContext
		}
	default:
		log.Printf("WARN: [upstream] 不支持的代理 scheme %q（fail-closed，该账号出站将失败）: %s", u.Scheme, redactProxyURL(proxyURL))
		tr.DialContext = failDial(fmt.Errorf("%w: unsupported proxy scheme %q", errProxyConfig, u.Scheme))
		return tr, proxyFailed
	}
	return tr, proxyActive
}

// newUpstreamHTTP 构造出站 http.Client：共享 Transport + 显式重定向策略。
// CheckRedirect 必须显式给出（不依赖 net/http 的默认跟随行为）：上游 base 是配置
// 固定的受信端点，重定向只应发生在同 host 内（http→https 升级、路径调整）；
// 跨 host 跳转一律拒绝。与 newTransport 同属「出站连接单一事实来源」。
//
// 出站节奏双闸在此统一挂载：全部出站 client（直连 HTTP/ChatHTTP + per-proxy
// proxyClients）的 Transport 外层都过同一套闸——
//   - concurrencyRoundTripper（外层）：在途并发闸，限"同时几个"；
//   - rpmRoundTripper（内层）：速率闸，限"多快"。
//
// 两闸开关都在 RoundTrip 时判（字段 > 0 才生效），故 New() 时零值也安全（直接放行）；
// 装配后设值即生效。
func (c *Client) newUpstreamHTTP(timeout time.Duration, tr http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &concurrencyRoundTripper{
			c:    c,
			next: &rpmRoundTripper{c: c, next: tr},
		},
		CheckRedirect: sameHostRedirect,
	}
}

// sameHostRedirect 重定向策略：仅同 host（hostname 大小写不敏感 + 生效端口相等）
// 放行；跨 host 拒绝。via[0] 是原始请求，req 是待跳转目标。
//
// 拒绝时打 WARN 并返回 http.ErrUseLastResponse：调用方拿到 3xx 响应本体，走既有
// 的状态码分类路径，而不是被静默引到一个未审计的 host（SSRF 纵深防御）。
// 不返回普通 error——那会把它推进传输层错误路径，丢失「上游到底回了什么」的线索。
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		// 理论不可达（net/http 首次调用 CheckRedirect 必带原始请求）。防御性放行：
		// 宁可回到默认跟随，也不要在这里误拒合法请求。
		return nil
	}
	orig := via[0]
	if sameHostPort(req.URL, orig.URL) {
		return nil
	}
	log.Printf("WARN: [upstream] 拒绝跨 host 重定向 %s -> %s（上游 base 应为固定受信端点）",
		orig.URL.Redacted(), req.URL.Redacted())
	return http.ErrUseLastResponse
}

// sameHostPort 比较两个 URL 的 hostname（EqualFold）与生效端口。端口必须按
// scheme 补缺省值再比——否则 "https://host" 与 "https://host:443"（同一端点）
// 会被误判成跨 host，而 "host:8443"（换了端口=换了服务）必须判为跨 host。
func sameHostPort(a, b *url.URL) bool {
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return effectivePort(a) == effectivePort(b)
}

// effectivePort URL 的生效端口：显式端口优先，否则按 scheme 取缺省值。
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// closeIdler 实现该接口的 RoundTripper 支持清空空闲连接池（*http.Transport、
// http2.Transport 等均满足；测试注入的自定义 RoundTripper 可选择性实现）。
type closeIdler interface {
	CloseIdleConnections()
}

// roundTripCloseIdle 在传输层请求失败后清掉 rt 所属 Transport 的空闲连接池
// （kongjianguan 第 4 件：失败连接可能仍留在空闲池里，等 IdleConnTimeout 才
// 过期，下一个请求会继续捡到它）。
//
// 挂载点（任务书「评估挂载点：错误分类处理处」的结论）：错误分类（Classify）
// 只见业务信封——传输层失败根本没有 body 可分类（见 doJSON/ChatStreamContext
// 对 read body 失败的处理：不进 Classify、不罚号）。这类失败的正确处理正是
// 连接层的池清理，故挂在与 Do 并列的传输层出口（ChatStreamContext 的 Do 错误
// 分支），而非 applyErrorPolicy。
//
// 关闭是 best-effort：rt 为 nil 或未实现 closeIdler（如测试注入的 rtFunc）时
// 静默跳过。CloseIdleConnections 只关空闲连接，不影响在途请求；瞬时代价是
// 下个请求多一次 TCP+TLS 握手，与半死连接被复用卡 60s 的风险完全不成比例。
func roundTripCloseIdle(rt http.RoundTripper) {
	if rt == nil {
		return
	}
	if ci, ok := rt.(closeIdler); ok {
		ci.CloseIdleConnections()
	}
}
