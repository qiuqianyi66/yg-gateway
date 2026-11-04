package upstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProxyConfigFailClosed 显式代理配置失败 = 该账号出站失败，不静默回退直连。
//
// 背景（#11 ban-risk）：newTransportWithProxy 三处失败分支（URL 解析 / SOCKS5 初始化 /
// 不支持 scheme）此前 log WARN 后返回直连 Transport——显式配代理的账号在代理配错时
// 走共享出口 IP，多账号风控关联重新出现且不报错（最危险的形态）。fail-closed：
// 构造失败 → dial 必然失败 → 请求错误冒泡（chatrun.handleTransportError 网络层抖动
// 分支 → rotateBackoff 换号，见 chatrun.go:683-695）。
//
// 牙齿：旧行为（回退直连）对本地可达目标会成功 204；本测试断言必须失败。
func TestProxyConfigFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cases := []struct {
		name, proxyURL string
	}{
		{"bad-url", "://missing-scheme"},
		{"unsupported-scheme", "ftp://proxy.example:8080"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newTransportWithProxy(c.proxyURL)
			client := newUpstreamHTTP(5*time.Second, tr)
			resp, err := client.Get(srv.URL)
			if err == nil {
				resp.Body.Close()
				t.Fatalf("fail-open：代理配置错误 %q 仍成功出站（回退直连），status=%d", c.proxyURL, resp.StatusCode)
			}
			if !strings.Contains(err.Error(), "proxy config error") {
				t.Errorf("错误应可定位为代理配置错误（/status 观测与运维排查依赖此信号），got: %v", err)
			}
		})
	}
}

// TestProxyDialFailureSurfaces 代理构造成功但连接不可达：dial 期错误必须冒泡（请求失败），
// 不静默直连。fail-closed 的另一面——构造期与连接期的失败都不得降级为直连。
func TestProxyDialFailureSurfaces(t *testing.T) {
	tr := newTransportWithProxy("socks5://127.0.0.1:1")
	client := newUpstreamHTTP(5*time.Second, tr)
	_, err := client.Get("http://127.0.0.1:65500")
	if err == nil {
		t.Fatal("代理不可达时请求仍成功（dial 错误未冒泡）")
	}
}
