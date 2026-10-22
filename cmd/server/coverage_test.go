// coverage_test.go cmd/server 覆盖率补强（工程深度治理 Task 5，纯增量）。
//
// 覆盖重点：config.go 的 normalize/校验/默认补齐、密钥索引构建（buildAuthSnapshot）、
// 配置保存/密钥轮换的原子写入与热应用、以及 main.go / wiring.go 里可纯测的装配函数。
//
// 明确不覆盖：main() 本体、warmModelRates 的真实上游抓取路径（需真账号 + 真上游，
// 见交付报告「遗留」段）。所有测试无网络、无真实时间依赖、无后台 goroutine。
//
// 命名约定：本文件所有辅助声明一律 cmdserver 前缀，测试名一律 TestCmdServer 前缀，
// 避免与同包其他测试文件（以及并行 agent 的产出）重复声明。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/scheduler"
	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// ---------------------------------------------------------------------------
// helpers（全部 cmdserver 前缀）
// ---------------------------------------------------------------------------

// cmdserverEnv saveConfig / resetAPIKey 需要的一整套运行时依赖。
// pool.New("")：stateFp 为空 → 不起后台 flusher、不落盘；
// upstream.New()：只构造 HTTP client，不发任何请求；
// scheduler.New：纯结构体，不启动循环。
type cmdserverEnv struct {
	path string
	live *livecfg.Holder
	p    *pool.Pool
	up   *upstream.Client
	sch  *scheduler.Scheduler
}

func cmdserverNewEnv(t *testing.T, body string) *cmdserverEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("种子配置必须可加载: %v", err)
	}
	p := pool.New("")
	return &cmdserverEnv{
		path: path,
		live: livecfg.New(buildAuthSnapshot(cfg)),
		p:    p,
		up:   upstream.New(),
		sch:  scheduler.New(scheduler.Config{Pool: p}),
	}
}

func (e *cmdserverEnv) save(t *testing.T, raw string) ([]string, error) {
	t.Helper()
	return saveConfig([]byte(raw), e.path, e.live, e.p, e.up, e.sch)
}

func (e *cmdserverEnv) reset(t *testing.T, id, key string) error {
	t.Helper()
	return resetAPIKey(e.path, id, key, e.live, e.p, e.up, e.sch)
}

// cmdserverFileMap 把磁盘上的配置读回 map[string]any（面板保存后的落盘形状）。
func cmdserverFileMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("落盘配置必须仍是合法 JSON: %v", err)
	}
	return m
}

func cmdserverNested(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	sub, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%q 应为嵌套对象，got %#v", key, m[key])
	}
	return sub
}

// cmdserverBearerReq 构造带 Bearer 头的假请求（不发起任何网络调用）。
func cmdserverBearerReq(key string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/v1/models", nil)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return r
}

func cmdserverAuthKey(t *testing.T, idx *httpauth.Index, single, key string) (*httpauth.Key, bool) {
	t.Helper()
	return httpauth.Authenticate(cmdserverBearerReq(key), idx, single)
}

// cmdserverCountingRT 拦截一切出站请求并计数：使「不发请求」成为可断言的事实。
type cmdserverCountingRT struct{ calls int }

func (rt *cmdserverCountingRT) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls++
	return nil, errors.New("cmdserver: 测试禁止真实网络")
}

func cmdserverAccount(uid, domain string) *auth.Auth {
	return &auth.Auth{UID: uid, AccessToken: "tok-" + uid, Domain: domain}
}

// sha256("abc") 的十六进制摘要（独立手写常量，不用被测实现算）。
const cmdserverSHA256Abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

// ---------------------------------------------------------------------------
// 不变量：Default() 的结果必然通过 normalize
// ---------------------------------------------------------------------------

// TestCmdServerDefaultNormalizeInvariantAndIdempotent
// 钉住两条不变量：(1) Default() 必须能通过 normalize（WriteDefault 依赖它——
// 生成的推荐配置必须能被 Load 回来）；(2) normalize 幂等（第二遍不再改任何字段）。
// 破坏方式：任何把「回落默认」写成「每遍都覆盖用户值」的改动，或 Default() 塞进
// 一个 normalize 会拒绝的值（如负时长、非法 mode），本测试即 FAIL。
func TestCmdServerDefaultNormalizeInvariantAndIdempotent(t *testing.T) {
	a := Default()
	if err := a.normalize(); err != nil {
		t.Fatalf("Default().normalize() 必须成功（写默认配置依赖它）: %v", err)
	}
	b := Default()
	if err := b.normalize(); err != nil {
		t.Fatalf("Default().normalize() 必须成功: %v", err)
	}
	if err := b.normalize(); err != nil {
		t.Fatalf("normalize 必须幂等（第二遍不该报错）: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("normalize 不幂等：二遍结果与一遍不同\nonce=%+v\ntwice=%+v", a, b)
	}

	// 归一化后的默认配置必须能被序列化 + 重新解析（面板保存/首启生成的同一路径）。
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("归一化默认配置必须可被重新解析: %v", err)
	}
	if back.SoftRateDur != a.SoftRateDur ||
		back.ServerReadTimeoutDur != a.ServerReadTimeoutDur ||
		back.MaxBodyBytesEffective != a.MaxBodyBytesEffective ||
		back.BreakerProbeGapDur != a.BreakerProbeGapDur ||
		back.CostExploreIntervalDur != a.CostExploreIntervalDur ||
		back.BalanceRefreshInterval != a.BalanceRefreshInterval ||
		back.ExpiringSoonDur != a.ExpiringSoonDur ||
		len(back.Usage.Pricing) != len(a.Usage.Pricing) {
		t.Fatalf("默认配置经 JSON 往返后语义漂移: back=%+v", back)
	}
}

// TestCmdServerWriteDefaultOmitsAPIKeysAndUsesRawURLKey
// 两条真实行为：(1) 生成的推荐配置里绝不能出现 api_keys 键——一旦写成
// "api_keys": null，Load 会把它判成「显式 null」fail fast（阻断项 4 回归）；
// (2) api_key 必须是 sk- + base64 RawURL(18B) = 27 字符，且与落盘值一致。
// 破坏方式：删掉 APIKeys 的 omitempty（本条 1 失败），或改用 StdEncoding（本条 2 失败）。
func TestCmdServerWriteDefaultOmitsAPIKeysAndUsesRawURLKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	key, err := WriteDefault(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 27 {
		t.Fatalf("生成的 key 长度=%d（%q）want 27 = 3 + base64.RawURL(18B)", len(key), key)
	}
	if !strings.HasPrefix(key, "sk-") {
		t.Fatalf("生成的 key=%q want sk- 前缀", key)
	}
	for i := 3; i < len(key); i++ {
		c := key[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			t.Fatalf("key 第 %d 位 %q 不在 base64 RawURL 字母表内（是否误用了 StdEncoding？）", i, c)
		}
	}

	m := cmdserverFileMap(t, path)
	if _, present := m["api_keys"]; present {
		t.Fatalf("生成的推荐配置不得含 api_keys 键（显式 null 会让 Load fail fast）: %#v", m["api_keys"])
	}
	if m["api_key"] != key {
		t.Fatalf("落盘 api_key=%#v want %q", m["api_key"], key)
	}
	if c, err := Load(path); err != nil || c.APIKey != key {
		t.Fatalf("生成的推荐配置必须能被 Load 回来: err=%v api_key=%q", err, c.APIKey)
	}
}

// ---------------------------------------------------------------------------
// Load / ParseConfigInto 边界
// ---------------------------------------------------------------------------

// TestCmdServerLoadMissingFileKeepsNotExistSentinel
// main() 的「首启自动生成配置」完全依赖 errors.Is(err, fs.ErrNotExist) 能看穿
// Load 里的 fmt.Errorf("%w") 包装。破坏方式：把 %w 改成 %v（或换用 os.IsNotExist
// 的旧检查）→ 本测试 FAIL，且首启会自动生成失效。
func TestCmdServerLoadMissingFileKeepsNotExistSentinel(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "config.json")
	_, err := Load(missing)
	if err == nil {
		t.Fatal("不存在的 config 必须报错")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("错误链必须保留 fs.ErrNotExist（首启自动生成依赖）: %v", err)
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("错误信息应带 read config 前缀: %v", err)
	}
}

// TestCmdServerParseConfigRejectsNonObjectJSON
// 非对象 JSON（数组/标量/空）必须报 parse config 错误而不是静默产出默认配置——
// ParseConfigInto 的第一步是用 map 探测 api_keys，探测失败必须让后面的 unmarshal 兜住。
// 破坏方式：把 unmarshal 错误吞掉（例如 `_ = json.Unmarshal(raw, c)`）→ 本测试 FAIL。
func TestCmdServerParseConfigRejectsNonObjectJSON(t *testing.T) {
	for _, body := range []string{`[1,2]`, `"just-a-string"`, `42`, ``, `{`} {
		if _, err := ParseConfig([]byte(body)); err == nil {
			t.Errorf("非对象/非法 JSON %q 必须报错", body)
		} else if !strings.Contains(err.Error(), "parse config") {
			t.Errorf("%q 的错误信息应为 parse config 前缀，got %v", body, err)
		}
	}
}

// ---------------------------------------------------------------------------
// normalize：默认补齐、回落、拒绝
// ---------------------------------------------------------------------------

// TestCmdServerNormalizePricingMerge
// usage.pricing 的合并语义：内置默认表为底，用户同键覆盖、异键补充；未给 pricing
// 时用整套默认价。破坏方式：把合并方向反过来（内置覆盖用户）→ 用户值 9 丢失 FAIL；
// 或改成「用户给了表就整体替换」→ 自建模型之外的默认键消失 FAIL。
func TestCmdServerNormalizePricingMerge(t *testing.T) {
	c, err := ParseConfig([]byte(`{"usage":{"pricing":{
		"glm-5.3-flash":{"input":9,"output":8,"cache_read":7},
		"my-own-model":{"input":1,"output":2,"cache_read":3}
	}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Usage.Pricing["glm-5.3-flash"]; got != (ModelPrice{Input: 9, Output: 8, CacheRead: 7}) {
		t.Fatalf("用户配置必须覆盖内置默认价，got %+v", got)
	}
	// 内置默认键仍在（用户没配的那些模型照常有参考价）。
	for _, k := range []string{"glm-5.2", "deepseek-v4-pro", "kimi-k2.6"} {
		if _, ok := c.Usage.Pricing[k]; !ok {
			t.Fatalf("内置默认价键 %q 不该被用户表替换掉: %d 项", k, len(c.Usage.Pricing))
		}
	}
	if _, ok := c.Usage.Pricing["my-own-model"]; !ok {
		t.Fatalf("用户自建模型价必须保留: %#v", c.Usage.Pricing)
	}

	// 未配置 pricing → 整套内置默认价（非空）。
	c2, err := ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Usage.Pricing) == 0 {
		t.Fatal("未配置 usage.pricing 时必须回落内置默认价（否则面板不显示 $ 折算）")
	}
}

// TestCmdServerNormalizeDurationFallbacks 各时长字段的空值回落、负值钳制与非法拒绝。
// 每行钉一个具体字段的归一结果；破坏对应回落 → 该行 FAIL。
func TestCmdServerNormalizeDurationFallbacks(t *testing.T) {
	type want struct {
		body    string
		field   string
		check   func(t *testing.T, c *Config)
		wantErr bool
	}
	cases := []want{
		{body: `{"cooldown":{"soft_rate_max":""}}`, field: "soft_rate_max 空串→2h", check: func(t *testing.T, c *Config) {
			if c.SoftRateMaxDur != 2*time.Hour || c.Cooldown.SoftRateMax != "2h" {
				t.Fatalf("got %v/%q want 2h", c.SoftRateMaxDur, c.Cooldown.SoftRateMax)
			}
		}},
		{body: `{"pool":{"breaker_probe_gap":""}}`, field: "breaker_probe_gap 空串→30s", check: func(t *testing.T, c *Config) {
			if c.BreakerProbeGapDur != 30*time.Second {
				t.Fatalf("got %v want 30s", c.BreakerProbeGapDur)
			}
		}},
		{body: `{"pool":{"cost_explore_interval":""}}`, field: "cost_explore_interval 空串→30m", check: func(t *testing.T, c *Config) {
			if c.CostExploreIntervalDur != 30*time.Minute {
				t.Fatalf("got %v want 30m", c.CostExploreIntervalDur)
			}
		}},
		{body: `{"pool":{"cost_explore_interval":"-5m"}}`, field: "cost_explore_interval 负值→0（关停）", check: func(t *testing.T, c *Config) {
			if c.CostExploreIntervalDur != 0 {
				t.Fatalf("got %v want 0（负值无合理语义，钳 0 关停）", c.CostExploreIntervalDur)
			}
		}},
		{body: `{"pool":{"cost_explore_interval":"abc"}}`, field: "cost_explore_interval 非法→报错", wantErr: true},
		{body: `{"pool":{"expiring_soon":"abc"}}`, field: "expiring_soon 非法→报错", wantErr: true},
		{body: `{"pool":{"breaker_cooldown_max":"abc"}}`, field: "breaker_cooldown_max 非法→报错", wantErr: true},
		{body: `{"pool":{"degrade_cooldown":"abc"}}`, field: "degrade_cooldown 非法→报错", wantErr: true},
		{body: `{"pool":{"degrade_cooldown_max":"abc"}}`, field: "degrade_cooldown_max 非法→报错", wantErr: true},
		{body: `{"session_sticky":{"gc_interval":"abc"}}`, field: "session_sticky.gc_interval 非法→报错", wantErr: true},
		{body: `{"security":{"read_only_rate_limit":{"limit":10,"window":"0s"}}}`, field: "限流窗口非正→报错", wantErr: true},
	}
	for _, tc := range cases {
		c, err := ParseConfig([]byte(tc.body))
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: %s 必须报错", tc.field, tc.body)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %s 不该报错: %v", tc.field, tc.body, err)
			continue
		}
		tc.check(t, c)
	}
}

// TestCmdServerNormalizeNumericAndScheduleFallbacks
// 数值/排程类归一：未设置或非正 → 回落文档化默认值；非法小时 fail fast 且错误信息
// 指向正确的 *_enabled 开关（用户不用猜哨兵值）。
func TestCmdServerNormalizeNumericAndScheduleFallbacks(t *testing.T) {
	c, err := ParseConfig([]byte(`{
		"pool":{"breaker_threshold":0,"degrade_threshold":0,"max_in_flight_global":0,
		        "idle_weight_per_hour":0,"idle_weight_max":-1},
		"upstream":{"timeout_seconds":0},
		"schedule":{"balance_refresh_minutes":-5,
		            "blackcat_hours":[],"growth_hours":[]},
		"alerting":{"dedup_minutes":0,"min_credit_threshold":-5,"consecutive_failures":-3}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pool.BreakerThreshold != 3 {
		t.Errorf("breaker_threshold=%d want 3", c.Pool.BreakerThreshold)
	}
	if c.Pool.DegradeThreshold != 5 {
		t.Errorf("degrade_threshold=%d want 5", c.Pool.DegradeThreshold)
	}
	if c.Pool.MaxInFlightGlobal != 2 {
		t.Errorf("max_in_flight_global=%d want 2（global 域 WAF 分档默认）", c.Pool.MaxInFlightGlobal)
	}
	if c.Pool.IdleWeightPerHour != 0.5 || c.Pool.IdleWeightMax != 5.0 {
		t.Errorf("idle weights=%v/%v want 0.5/5.0", c.Pool.IdleWeightPerHour, c.Pool.IdleWeightMax)
	}
	if c.Upstream.TimeoutSeconds != 120 {
		t.Errorf("timeout_seconds=%d want 120", c.Upstream.TimeoutSeconds)
	}
	if c.Schedule.BalanceRefreshMinutes != 5 || c.BalanceRefreshInterval != 5*time.Minute {
		t.Errorf("balance refresh=%d/%v want 5/5m", c.Schedule.BalanceRefreshMinutes, c.BalanceRefreshInterval)
	}
	if len(c.Schedule.BlackcatHours) != 1 || c.Schedule.BlackcatHours[0] != 23 {
		t.Errorf("blackcat_hours=%v want [23]", c.Schedule.BlackcatHours)
	}
	if len(c.Schedule.GrowthHours) != 1 || c.Schedule.GrowthHours[0] != 1 {
		t.Errorf("growth_hours=%v want [1]", c.Schedule.GrowthHours)
	}
	if c.Alerting.DedupMinutes != 60 {
		t.Errorf("alerting dedup_minutes=%d want 60（去重窗口兜底）", c.Alerting.DedupMinutes)
	}
	if c.Alerting.MinCreditThreshold != 0 || c.Alerting.ConsecutiveFailures != 0 {
		t.Errorf("alerting 阈值负数应钳 0，got %d/%d", c.Alerting.MinCreditThreshold, c.Alerting.ConsecutiveFailures)
	}

	// 非法小时：fail fast 并把用户指向正确的开关。
	for _, tc := range []struct{ body, wantSwitch string }{
		{`{"schedule":{"blackcat_hours":[24]}}`, "blackcat_enabled"},
		{`{"schedule":{"growth_hours":[-1]}}`, "growth_enabled"},
	} {
		_, err := ParseConfig([]byte(tc.body))
		if err == nil {
			t.Fatalf("%s 必须报错（非法小时不能静默吞掉）", tc.body)
		}
		if !strings.Contains(err.Error(), tc.wantSwitch) {
			t.Errorf("%s 的错误应指向 schedule.%s，got %v", tc.body, tc.wantSwitch, err)
		}
	}
}

// TestCmdServerNormalizeListenAddress
// listen 归一：裸端口补 ":"，已带主机/端口原样保留。破坏方式：去掉空/裸端口判断
// → "8080" 不被补冒号 → 本测试 FAIL（监听地址会变成非法值）。
func TestCmdServerNormalizeListenAddress(t *testing.T) {
	if c, err := ParseConfig([]byte(`{"listen":"8080"}`)); err != nil {
		t.Fatal(err)
	} else if c.Listen != ":8080" {
		t.Fatalf("裸端口应补冒号，got %q", c.Listen)
	}
	for _, in := range []string{":8080", "0.0.0.0:7863", "127.0.0.1:9000"} {
		c, err := ParseConfig([]byte(`{"listen":"` + in + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		if c.Listen != in {
			t.Errorf("listen %q 应原样保留，got %q", in, c.Listen)
		}
	}
}

// TestCmdServerNormalizePromptAppendMode
// prompt.mode 三态解析：append 是合法值（issue #129），且 custom/append 都要加载
// PromptText（空 file → 内置默认，非空）。大小写/空白归一。
// 破坏方式：normalizePrompt 的 switch 去掉 "append" 分支 → 本测试 FAIL
// （append 会被判非法值，直接启动失败）。
func TestCmdServerNormalizePromptAppendMode(t *testing.T) {
	c, err := ParseConfig([]byte(`{"prompt":{"mode":" APPEND "}}`))
	if err != nil {
		t.Fatalf("append 必须是合法 mode: %v", err)
	}
	if c.Prompt.Mode != "append" {
		t.Fatalf("mode=%q want append（应去空白 + 转小写）", c.Prompt.Mode)
	}
	if c.PromptText == "" {
		t.Fatal("append 模式必须加载提示词文本（file 空 → 内置默认）")
	}
}

// ---------------------------------------------------------------------------
// applyEnv：覆盖顺序与垃圾值不被采信
// ---------------------------------------------------------------------------

// TestCmdServerApplyEnvOverridesFileAndIgnoresGarbage
// 两条真实契约：(1) env 在文件之后应用（env 优先级更高）；(2) 数值/布尔解析失败的
// env 值必须被忽略、保留既有值（不是被 ParseBool/Atoi 的零值悄悄写进去）。
// 破坏方式：把 applyEnv 挪到文件解析之前 → soft_rate 断言失败；
// 去掉 `if err == nil` 守卫 → prefer_expiring 被垃圾值写成 false → 断言失败。
func TestCmdServerApplyEnvOverridesFileAndIgnoresGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cooldown":{"soft_rate":"5m"},"pool":{"prefer_expiring":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WB2A_AUTH_DIR", "./env-auths")
	t.Setenv("WB2A_STATE_FILE", "./env-data/state.json")
	t.Setenv("WB2A_SOFT_RATE", "77s")
	t.Setenv("WB2A_SOFT_RATE_MAX", "3h")
	t.Setenv("WB2A_TIMEOUT_SECONDS", "45")
	t.Setenv("WB2A_HEADER_TIMEOUT_SECONDS", "30")
	t.Setenv("WB2A_IDLE_TIMEOUT_SECONDS", "600")
	t.Setenv("WB2A_CLIENT_VERSION", "9.9.9")
	t.Setenv("WB2A_CLI_VERSION", "1.2.3")
	t.Setenv("WB2A_CLIENT_NAME", "WorkBuddy")
	t.Setenv("WB2A_DEVICE_TOKEN", "dt-env")
	t.Setenv("WB2A_DEVICE_TOKEN_FILE", "dt.txt")
	t.Setenv("WB2A_PASSTHROUGH_IP", "true")
	t.Setenv("WB2A_SANITIZE_FINGERPRINTS", "false")
	t.Setenv("WB2A_EXPIRING_SOON", "24h")
	t.Setenv("WB2A_PREFER_EXPIRING", "not-a-bool") // 垃圾：必须忽略（默认 true 保留）

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != "./env-auths" || c.StateFile != "./env-data/state.json" {
		t.Errorf("auth_dir/state_file env 未生效: %q/%q", c.AuthDir, c.StateFile)
	}
	if c.SoftRateDur != 77*time.Second {
		t.Errorf("soft_rate=%v want 77s（env 必须覆盖文件里的 5m）", c.SoftRateDur)
	}
	if c.SoftRateMaxDur != 3*time.Hour {
		t.Errorf("soft_rate_max=%v want 3h", c.SoftRateMaxDur)
	}
	if c.Upstream.TimeoutSeconds != 45 {
		t.Errorf("timeout_seconds=%d want 45", c.Upstream.TimeoutSeconds)
	}
	if c.Upstream.HeaderTimeoutSeconds != 30 {
		t.Errorf("header_timeout_seconds=%d want 30（env 必须覆盖）", c.Upstream.HeaderTimeoutSeconds)
	}
	if c.Upstream.IdleTimeoutSeconds != 600 {
		t.Errorf("idle_timeout_seconds=%d want 600（env 必须覆盖）", c.Upstream.IdleTimeoutSeconds)
	}
	if c.Upstream.PassthroughIP != true {
		t.Error("passthrough_ip env=true 未生效")
	}
	if c.Features.SanitizeBlacklistFingerprints {
		t.Error("sanitize_fingerprints env=false 未生效")
	}
	if c.ExpiringSoonDur != 24*time.Hour {
		t.Errorf("expiring_soon=%v want 24h", c.ExpiringSoonDur)
	}
	if !c.Pool.PreferExpiring {
		t.Error("垃圾 prefer_expiring env 必须被忽略（保留默认 true）")
	}
	if c.Upstream.ClientVersion != "9.9.9" || c.Upstream.CliVersion != "1.2.3" || c.Upstream.ClientName != "WorkBuddy" {
		t.Errorf("版本/归属头 env 未生效: %q/%q/%q",
			c.Upstream.ClientVersion, c.Upstream.CliVersion, c.Upstream.ClientName)
	}
	if c.Upstream.DeviceToken != "dt-env" || c.Upstream.DeviceTokenFile != "dt.txt" {
		t.Errorf("device token env 未生效: %q/%q", c.Upstream.DeviceToken, c.Upstream.DeviceTokenFile)
	}
}

// TestCmdServerApplyEnvPromptFileAndValidFalse
// 合法布尔 env 必须真正生效（false 也要能改掉默认 true），且 WB2A_PROMPT_FILE
// 指向的文件内容会被加载进 PromptText。
func TestCmdServerApplyEnvPromptFileAndValidFalse(t *testing.T) {
	pf := filepath.Join(t.TempDir(), "prompt.md")
	const want = "env 指定的提示词正文"
	if err := os.WriteFile(pf, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB2A_PROMPT_MODE", "custom")
	t.Setenv("WB2A_PROMPT_FILE", pf)
	t.Setenv("WB2A_PREFER_EXPIRING", "false")

	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Prompt.Mode != "custom" {
		t.Fatalf("prompt.mode=%q want custom", c.Prompt.Mode)
	}
	if c.PromptText != want {
		t.Fatalf("PromptText=%q want %q（env 指定的提示词文件必须被加载）", c.PromptText, want)
	}
	if c.Pool.PreferExpiring {
		t.Error("env prefer_expiring=false 必须真正关闭（合法值不能被当成垃圾忽略）")
	}
}

// ---------------------------------------------------------------------------
// 鉴权快照构建（密钥索引）
// ---------------------------------------------------------------------------

// TestCmdServerBuildAuthSnapshotSingleKeyInIndex
// 单 api_key 必须作为合成条目（ID="single"）进入索引，摘要 = sha256(明文)——
// 否则多 Key 索引命中路径上单 Key 就失效（只剩 Authenticate 的兜底分支）。
// 破坏方式：不把单 Key append 进 keys → 索引里查不到 single 条目 → FAIL。
func TestCmdServerBuildAuthSnapshotSingleKeyInIndex(t *testing.T) {
	c, err := ParseConfig([]byte(`{"api_key":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	snap := buildAuthSnapshot(c)
	if snap.APIKey != "abc" {
		t.Fatalf("快照 api_key=%q want abc", snap.APIKey)
	}
	if len(snap.APIKeys) != 1 || snap.APIKeys[0].ID != httpauth.SingleKeyID {
		t.Fatalf("单 Key 必须成为索引里的合成条目 %q，got %#v", httpauth.SingleKeyID, snap.APIKeys)
	}
	if got := hex.EncodeToString(snap.APIKeys[0].Digest); got != cmdserverSHA256Abc {
		t.Fatalf("摘要=%s want sha256(\"abc\")=%s", got, cmdserverSHA256Abc)
	}
	if snap.AuthIndex == nil {
		t.Fatal("有单 Key 时必须构建鉴权索引")
	}
	k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "abc")
	if !ok || k.ID != httpauth.SingleKeyID {
		t.Fatalf("用正确 Bearer 必须命中单 Key 合成身份，got ok=%v key=%+v", ok, k)
	}
	if _, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "wrong"); ok {
		t.Fatal("错误 Bearer 不得命中")
	}
}

// TestCmdServerBuildAuthSnapshotDisabledKeyExcludedButKeptInConfig
// enabled:false 的 Key 不进鉴权索引（无法用它调用），但配置里必须保留该条目
// （面板要能重新启用它）。破坏方式：去掉 Enabled 判断 → 禁用 Key 仍可鉴权 → FAIL。
func TestCmdServerBuildAuthSnapshotDisabledKeyExcludedButKeptInConfig(t *testing.T) {
	c, err := ParseConfig([]byte(`{"api_key":"","api_keys":[
		{"id":"k-on","key":"sk-on"},
		{"id":"k-off","key":"sk-off","enabled":false}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.APIKeys) != 2 {
		t.Fatalf("配置侧必须保留禁用条目（面板可重新启用），got %d 项", len(c.APIKeys))
	}
	snap := buildAuthSnapshot(c)
	if len(snap.APIKeys) != 1 || snap.APIKeys[0].ID != "k-on" {
		t.Fatalf("禁用 Key 不得进索引，got %#v", snap.APIKeys)
	}
	if k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-on"); !ok || k.ID != "k-on" {
		t.Fatalf("启用 Key 必须可鉴权，got ok=%v key=%+v", ok, k)
	}
	if _, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-off"); ok {
		t.Fatal("被禁用的 Key 必须无法鉴权（配置里保留 ≠ 运行时可用）")
	}
}

// TestCmdServerBuildAuthSnapshotCarriesKeyConstraintsAndHotFields
// 快照必须把每把 Key 的全部约束（models/realm/rpm/月配额/分组）与热生效字段
// （soft_cooldown/脱敏开关/来源记录/模型别名）带进运行时。
// 破坏方式：漏传 Group（Key↔账号组隔离失效）或 MonthlyTokenQuota（月配额硬上限失效）
// → 对应断言 FAIL。
func TestCmdServerBuildAuthSnapshotCarriesKeyConstraintsAndHotFields(t *testing.T) {
	c, err := ParseConfig([]byte(`{
		"api_key":"sk-single",
		"api_keys":[{"id":"k1","key":"sk-k1","name":"甲","models":["m1","glm-*"],
		             "realm":" Global ","rpm":30,"monthly_token_quota":1000000,"group":"team-a"}],
		"cooldown":{"soft_rate":"42s"},
		"features":{"sanitize_blacklist_fingerprints":false},
		"logging":{"request_client_info":false},
		"model_aliases":{"glm":"glm-5.3-flash"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	snap := buildAuthSnapshot(c)
	if len(snap.APIKeys) != 2 {
		t.Fatalf("应有单 Key 合成条目 + 1 把多 Key，got %d", len(snap.APIKeys))
	}
	var multi *httpauth.Key
	for i := range snap.APIKeys {
		if snap.APIKeys[i].ID == "k1" {
			multi = &snap.APIKeys[i]
		}
	}
	if multi == nil {
		t.Fatalf("索引里必须有多 Key k1: %#v", snap.APIKeys)
	}
	if multi.Name != "甲" {
		t.Errorf("name=%q want 甲", multi.Name)
	}
	if len(multi.Models) != 2 || multi.Models[0] != "m1" || multi.Models[1] != "glm-*" {
		t.Errorf("models 白名单未透传: %#v", multi.Models)
	}
	if multi.Realm != "global" {
		t.Errorf("realm=%q want global（应去空白 + 转小写）", multi.Realm)
	}
	if multi.RPM != 30 {
		t.Errorf("rpm=%d want 30", multi.RPM)
	}
	if multi.MonthlyTokenQuota != 1000000 {
		t.Errorf("monthly_token_quota=%d want 1000000（月 token 硬上限）", multi.MonthlyTokenQuota)
	}
	if multi.Group != "team-a" {
		t.Errorf("group=%q want team-a（Key↔账号组隔离）", multi.Group)
	}
	if len(multi.Digest) != sha256.Size {
		t.Errorf("摘要长度=%d want %d", len(multi.Digest), sha256.Size)
	}
	if snap.SoftCooldown != 42*time.Second {
		t.Errorf("soft_cooldown=%v want 42s", snap.SoftCooldown)
	}
	if snap.SanitizeFingerprints {
		t.Error("sanitize_fingerprints=false 未进快照")
	}
	if snap.RecordClientInfo {
		t.Error("request_client_info=false 未进快照")
	}
	if snap.ModelAliases["glm"] != "glm-5.3-flash" {
		t.Errorf("model_aliases 未进快照: %#v", snap.ModelAliases)
	}
	// 约束必须经索引命中后仍可见（不只是躺在 APIKeys 里）。
	k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-k1")
	if !ok {
		t.Fatal("sk-k1 必须能通过索引命中")
	}
	if k.ID != "k1" || k.Group != "team-a" || k.Realm != "global" || k.MonthlyTokenQuota != 1000000 {
		t.Fatalf("索引命中的 Key 必须带全约束，got %+v", k)
	}
}

// TestCmdServerBuildAuthSnapshotNoKeysMeansNoAuth
// 无 api_key 且无 api_keys → 不启用鉴权：索引为 nil、快照 api_key 空。
// 破坏方式：给空配置也构建非空索引 → 未鉴权部署会开始拒绝请求 → FAIL。
func TestCmdServerBuildAuthSnapshotNoKeysMeansNoAuth(t *testing.T) {
	c, err := ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	snap := buildAuthSnapshot(c)
	if snap.APIKey != "" || snap.AuthIndex != nil || len(snap.APIKeys) != 0 {
		t.Fatalf("无任何 Key 时应为免鉴权态，got %+v", snap)
	}
	if k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "whatever"); !ok || k != nil {
		t.Fatalf("免鉴权态必须恒放行（返回 nil,true），got ok=%v key=%+v", ok, k)
	}
}

// ---------------------------------------------------------------------------
// saveConfig：合并、校验先于落盘、热应用
// ---------------------------------------------------------------------------

// TestCmdServerSaveConfigMergesDeepAndHotApplies
// 面板保存的三条契约：(1) 未提交的键（含用户手写未知键、api_keys）必须保留；
// (2) 嵌套对象逐键覆盖（同段兄弟键不被整体替换）；(3) 能热生效的字段立即生效，
// 并返回需要重启的字段清单。
// 破坏方式：mergeConfigMaps 改成整体覆盖 → api_keys/未知键/兄弟键丢失 FAIL；
// 去掉 live.Store → soft_cooldown 热生效断言 FAIL。
func TestCmdServerSaveConfigMergesDeepAndHotApplies(t *testing.T) {
	env := cmdserverNewEnv(t, `{
		"api_key":"sk-seed",
		"api_keys":[{"id":"k1","key":"sk-k1"}],
		"listen":":7863",
		"custom_hand_written":{"note":"keep-me"},
		"cooldown":{"soft_rate":"600s"},
		"pool":{"max_in_flight":3,"breaker_threshold":3}
	}`)

	if got := env.live.Load().SoftCooldown; got != 600*time.Second {
		t.Fatalf("种子快照 soft_cooldown=%v want 600s", got)
	}

	restart, err := env.save(t, `{"listen":":8623","cooldown":{"soft_rate":"45s"},"pool":{"max_in_flight":7}}`)
	if err != nil {
		t.Fatalf("保存应成功: %v", err)
	}
	if !cmdserverContains(restart, "listen") {
		t.Fatalf("改动 listen 必须出现在需重启字段里，got %v", restart)
	}
	if !cmdserverContains(restart, "server.read_timeout") {
		t.Fatalf("需重启清单应含 server.read_timeout，got %v", restart)
	}
	if env.live.Load().SoftCooldown != 45*time.Second {
		t.Fatalf("soft_rate 必须热生效，got %v", env.live.Load().SoftCooldown)
	}
	if env.live.Load().APIKey != "sk-seed" {
		t.Fatalf("未提交的 api_key 在热快照里也必须保持，got %q", env.live.Load().APIKey)
	}

	m := cmdserverFileMap(t, env.path)
	if m["listen"] != ":8623" {
		t.Errorf("listen=%#v want :8623", m["listen"])
	}
	if m["api_key"] != "sk-seed" {
		t.Errorf("未提交的 api_key 必须保留，got %#v", m["api_key"])
	}
	if _, ok := m["api_keys"]; !ok {
		t.Errorf("未提交的 api_keys 必须保留（否则面板保存会误删多 Key），got %#v", m)
	}
	if _, ok := m["custom_hand_written"]; !ok {
		t.Errorf("用户手写未知键必须保留，got %#v", m)
	}
	pool := cmdserverNested(t, m, "pool")
	if pool["max_in_flight"] != float64(7) {
		t.Errorf("pool.max_in_flight=%#v want 7", pool["max_in_flight"])
	}
	if pool["breaker_threshold"] != float64(3) {
		t.Errorf("未提交的兄弟键 pool.breaker_threshold 必须保留，got %#v", pool["breaker_threshold"])
	}
	// 落盘内容必须仍可被 Load（与启动同一套校验）。
	if c, err := Load(env.path); err != nil {
		t.Fatalf("落盘配置必须可加载: %v", err)
	} else if c.SoftRateDur != 45*time.Second || c.Listen != ":8623" {
		t.Fatalf("落盘配置语义不符: soft=%v listen=%q", c.SoftRateDur, c.Listen)
	}
}

// TestCmdServerSaveConfigValidationBlocksWriteAndHotApply
// 校验失败必须「不落盘、不热应用」：文件字节级不变，热快照保持旧值。
// 破坏方式：把落盘/热应用挪到校验之前 → 本测试 FAIL（这是面板保存唯一的回归保护）。
func TestCmdServerSaveConfigValidationBlocksWriteAndHotApply(t *testing.T) {
	env := cmdserverNewEnv(t, `{"api_key":"sk-seed","cooldown":{"soft_rate":"600s"},"pool":{"max_in_flight":3}}`)
	before, err := os.ReadFile(env.path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = env.save(t, `{"pool":{"ewma":{"alpha":1.5}}}`)
	if err == nil {
		t.Fatal("alpha>1 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "pool.ewma.alpha") {
		t.Errorf("错误信息应指明字段 pool.ewma.alpha，got %v", err)
	}
	after, err := os.ReadFile(env.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("校验失败不得改写配置\nbefore=%s\nafter=%s", before, after)
	}
	if env.live.Load().SoftCooldown != 600*time.Second {
		t.Fatalf("校验失败不得热应用，soft_cooldown=%v want 600s", env.live.Load().SoftCooldown)
	}
}

// TestCmdServerSaveConfigReadAndParseErrors
// 读不到现配置 / 提交体不是合法 JSON 都必须报错且不改文件。
func TestCmdServerSaveConfigReadAndParseErrors(t *testing.T) {
	// 现配置不存在 → read current config
	env := cmdserverNewEnv(t, `{"api_key":"sk-seed"}`)
	missing := filepath.Join(t.TempDir(), "gone.json")
	if _, err := saveConfig([]byte(`{"listen":":1"}`), missing, env.live, env.p, env.up, env.sch); err == nil {
		t.Fatal("现配置读不到必须报错")
	} else if !strings.Contains(err.Error(), "read current config") {
		t.Errorf("错误信息应为 read current config，got %v", err)
	}

	// 提交体是数组 → parse submitted config，且文件不变
	before, err := os.ReadFile(env.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.save(t, `["not","an","object"]`); err == nil {
		t.Fatal("提交体必须是 JSON 对象")
	} else if !strings.Contains(err.Error(), "parse submitted config") {
		t.Errorf("错误信息应为 parse submitted config，got %v", err)
	}
	after, err := os.ReadFile(env.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("解析失败不得改写配置文件")
	}
}

// ---------------------------------------------------------------------------
// resetAPIKey：只动目标条目
// ---------------------------------------------------------------------------

// TestCmdServerResetAPIKeyRotatesOnlyTargetEntry
// 重置某把 Key 的密钥时：其他多 Key 的原钥、顶层单 api_key、手写未知键都必须原样
// 保留；热快照必须立即反映新钥可用 / 旧钥失效（否则旧钥泄漏后仍能调用）。
// 重置目标是**第二把** Key——若实现按「数组第一项」而不是按 id 定位，本测试即 FAIL。
// 破坏方式：整份 api_keys 重写（丢其他条目）或忘记 live.Store → 对应断言 FAIL。
func TestCmdServerResetAPIKeyRotatesOnlyTargetEntry(t *testing.T) {
	env := cmdserverNewEnv(t, `{
		"api_key":"sk-single",
		"api_keys":[{"id":"k1","key":"sk-old-1"},{"id":"k2","key":"sk-old-2"}],
		"custom_hand_written":42
	}`)
	if _, ok := cmdserverAuthKey(t, env.live.Load().AuthIndex, env.live.Load().APIKey, "sk-old-2"); !ok {
		t.Fatal("前置断言：旧钥 sk-old-2 应可用")
	}

	if err := env.reset(t, "k2", "sk-new-2"); err != nil {
		t.Fatalf("重置应成功: %v", err)
	}

	m := cmdserverFileMap(t, env.path)
	if m["api_key"] != "sk-single" {
		t.Errorf("顶层单 api_key 必须原样保留，got %#v", m["api_key"])
	}
	if _, ok := m["custom_hand_written"]; !ok {
		t.Errorf("手写未知键必须保留，got %#v", m)
	}
	keys, ok := m["api_keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("api_keys 应仍是 2 项，got %#v", m["api_keys"])
	}
	got := map[string]string{}
	for _, k := range keys {
		km := k.(map[string]any)
		got[km["id"].(string)], _ = km["key"].(string)
	}
	if got["k2"] != "sk-new-2" {
		t.Errorf("k2 的密钥应被替换，got %q", got["k2"])
	}
	if got["k1"] != "sk-old-1" {
		t.Errorf("k1 的密钥绝不能被连带改动（必须按 id 定位而非取第一项），got %q", got["k1"])
	}

	snap := env.live.Load()
	if k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-new-2"); !ok || k.ID != "k2" {
		t.Fatalf("新钥必须立即生效并归因到 k2，got ok=%v key=%+v", ok, k)
	}
	if _, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-old-2"); ok {
		t.Fatal("旧钥必须立即失效（否则泄漏后仍能调用）")
	}
	if k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-old-1"); !ok || k.ID != "k1" {
		t.Fatalf("未被重置的 k1 必须照常可用，got ok=%v key=%+v", ok, k)
	}
	if k, ok := cmdserverAuthKey(t, snap.AuthIndex, snap.APIKey, "sk-single"); !ok || k.ID != httpauth.SingleKeyID {
		t.Fatalf("单 api_key 必须照常可用，got ok=%v key=%+v", ok, k)
	}
}

// TestCmdServerResetAPIKeyRejections
// 参数守卫：空 id / 空新钥 / 保留 id / 目标不存在 / config 无 api_keys 都必须报错
// 且不碰文件。破坏方式：去掉保留名守卫 → "single" 会被当成普通 id 改写（身份冲突）。
func TestCmdServerResetAPIKeyRejections(t *testing.T) {
	env := cmdserverNewEnv(t, `{"api_key":"sk-single","api_keys":[{"id":"k1","key":"sk-k1"}]}`)
	before, err := os.ReadFile(env.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, key, want string }{
		{"", "sk-x", "id 不能为空"},
		{"   ", "sk-x", "id 不能为空"},
		{"k1", "", "新密钥不能为空"},
		{"k1", "   ", "新密钥不能为空"},
		{httpauth.SingleKeyID, "sk-x", "保留名"},
		{"nope", "sk-x", "未找到"},
	} {
		err := env.reset(t, tc.id, tc.key)
		if err == nil {
			t.Errorf("id=%q key=%q 必须被拒绝", tc.id, tc.key)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("id=%q key=%q 的错误应含 %q，got %v", tc.id, tc.key, tc.want, err)
		}
	}
	after, err := os.ReadFile(env.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("任何被拒绝的重置都不得改写配置")
	}

	// config 里没有 api_keys 键（旧版单 Key 部署）→ 明确报错。
	env2 := cmdserverNewEnv(t, `{"api_key":"sk-single"}`)
	if err := env2.reset(t, "k1", "sk-x"); err == nil {
		t.Fatal("无 api_keys 时重置必须报错")
	} else if !strings.Contains(err.Error(), "无 api_keys") {
		t.Errorf("错误信息应说明 config 无 api_keys，got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 纯函数：合并、路径、装配
// ---------------------------------------------------------------------------

// TestCmdServerMergeConfigMapsNonMapReplacement
// 深合并的边界分支：cur 里同键是标量/数组而 incoming 是对象时，必须整体替换（不能崩、
// 不能保留标量）。破坏方式：删掉类型断言的回落分支 → cur 的标量键不会被覆盖 → FAIL。
func TestCmdServerMergeConfigMapsNonMapReplacement(t *testing.T) {
	cur := map[string]any{"pool": "手写成了标量", "a": map[string]any{"x": 1}}
	incoming := map[string]any{"pool": map[string]any{"y": 2}, "a": map[string]any{"z": 3}}
	merged := mergeConfigMaps(cur, incoming)

	pool, ok := merged["pool"].(map[string]any)
	if !ok {
		t.Fatalf("cur 同键为标量时必须被 incoming 对象替换，got %#v", merged["pool"])
	}
	if pool["y"] != 2 || len(pool) != 1 {
		t.Fatalf("替换后应只含 incoming 的键，got %#v", pool)
	}
	a := cmdserverNested(t, merged, "a")
	if a["x"] != 1 || a["z"] != 3 {
		t.Fatalf("两侧都是对象时应逐键合并，got %#v", a)
	}
	if cur["a"].(map[string]any)["z"] != 3 {
		t.Fatal("深合并应原地更新 cur 的嵌套对象（不新建 map）")
	}
}

// TestCmdServerStateSiblingAndUsagePath
// state 文件同目录派生路径（usage.json / 探测文件都靠它），含无目录名的回落。
// 破坏方式：去掉 dir=="." 的回落 → 相对 state 文件会派生出 ".\usage.json" → FAIL。
func TestCmdServerStateSiblingAndUsagePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"state.json", "usage.json"},
		{"", "usage.json"},
		{filepath.Join("data", "state.json"), filepath.Join("data", "usage.json")},
		{filepath.Join("a", "b", "state.json"), filepath.Join("a", "b", "usage.json")},
	}
	for _, tc := range cases {
		if got := usagePathFor(tc.in); got != tc.want {
			t.Errorf("usagePathFor(%q)=%q want %q", tc.in, got, tc.want)
		}
		if got := stateSibling(tc.in, "output_probes.json"); got != filepath.Join(filepath.Dir(tc.want), "output_probes.json") {
			t.Errorf("stateSibling(%q)=%q 与 usage 同目录规则不一致", tc.in, got)
		}
	}
}

// TestCmdServerPanelListenPath
// 启动日志的面板 URL 端口提取：必须取**最后一个**冒号（IPv6 字面量 "[::1]:7863"
// 用第一个冒号会切出 "::1]:7863"），无冒号时原样返回。
// 破坏方式：改成取第一个冒号 → IPv6 用例 FAIL。
func TestCmdServerPanelListenPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{":7863", ":7863"},
		{"0.0.0.0:7863", ":7863"},
		{"127.0.0.1:80", ":80"},
		{"[::1]:7863", ":7863"},
		{"7863", "7863"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := panelListenPath(tc.in); got != tc.want {
			t.Errorf("panelListenPath(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// TestCmdServerBuildFallbackConfig
// P2 备用级联装配：三要件（enabled/base_url/api_key）缺一即关闭（零回归），
// 齐备时模型列表转成集合、超时透传。
// 破坏方式：去掉 api_key 空的判断 → 配了 base 却忘了 key 时会向备用源发无凭证请求 → FAIL。
func TestCmdServerBuildFallbackConfig(t *testing.T) {
	if got := buildFallbackConfig(Default()); got != nil {
		t.Fatalf("默认（enabled=false）应返回 nil，got %+v", got)
	}
	for _, body := range []string{
		`{"fallback":{"enabled":true}}`,
		`{"fallback":{"enabled":true,"base_url":"https://api.deepseek.com/v1"}}`,
		`{"fallback":{"enabled":true,"api_key":"sk-x"}}`,
	} {
		c, err := ParseConfig([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if got := buildFallbackConfig(c); got != nil {
			t.Errorf("%s 三要件不齐必须返回 nil，got %+v", body, got)
		}
	}

	c, err := ParseConfig([]byte(`{"fallback":{"enabled":true,"base_url":"https://api.deepseek.com/v1",
		"api_key":"sk-x","models":["deepseek-chat","deepseek-chat","deepseek-reasoner"],"timeout_seconds":33}}`))
	if err != nil {
		t.Fatal(err)
	}
	fb := buildFallbackConfig(c)
	if fb == nil {
		t.Fatal("三要件齐备时必须启用备用级联")
	}
	if len(fb.Endpoints) != 1 {
		t.Fatalf("单端点旧配置应合成 1 个级联端点，got %d", len(fb.Endpoints))
	}
	ep := fb.Endpoints[0]
	if ep.BaseURL != "https://api.deepseek.com/v1" || ep.APIKey != "sk-x" || ep.TimeoutSeconds != 33 {
		t.Fatalf("装配结果不符: %+v", ep)
	}
	if len(ep.Models) != 2 || !ep.Models["deepseek-chat"] || !ep.Models["deepseek-reasoner"] {
		t.Fatalf("模型白名单应是去重集合，got %#v", ep.Models)
	}
	if ep.Models["deepseek-v4-pro"] {
		t.Fatal("未配置的模型不得进白名单（否则 CodeBuddy 专属模型会被转发到备用源 404）")
	}
}

// TestCmdServerBuildFallbackEndpointsList（R-107）：endpoints 列表配置优先于
// 单端点旧字段；列表里空 base/key 的项被跳过；顺序即优先级。
func TestCmdServerBuildFallbackEndpointsList(t *testing.T) {
	body := `{"fallback":{"enabled":true,
		"endpoints":[
			{"base_url":"https://a.example/v1","api_key":"ka","models":["m1"],"timeout_seconds":11},
			{"base_url":"","api_key":"kbad","models":["m1"]},
			{"base_url":"https://b.example/v1","api_key":"kb","models":["m1","m2"]}
		]}}`
	c, err := ParseConfig([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	fb := buildFallbackConfig(c)
	if fb == nil || len(fb.Endpoints) != 2 {
		t.Fatalf("列表应装配 2 个有效端点（空 base 的项被跳过），got %+v", fb)
	}
	if fb.Endpoints[0].BaseURL != "https://a.example/v1" || fb.Endpoints[0].APIKey != "ka" {
		t.Fatalf("端点 1 装配错: %+v", fb.Endpoints[0])
	}
	if fb.Endpoints[0].TimeoutSeconds != 11 || !fb.Endpoints[0].Models["m1"] {
		t.Fatalf("端点 1 字段错: %+v", fb.Endpoints[0])
	}
	if fb.Endpoints[1].BaseURL != "https://b.example/v1" || len(fb.Endpoints[1].Models) != 2 {
		t.Fatalf("端点 2 装配错: %+v", fb.Endpoints[1])
	}
}

// TestCmdServerRunDriftProbeEarlyExit：空池 + 已取消 ctx → 探测不报错、立即退出（R9）。
func TestCmdServerRunDriftProbeEarlyExit(t *testing.T) {
	p, err := newPool(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	mon := upstream.NewDriftMonitor("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 取消后首轮 probe 与 ticker 都立即退出，不挂起
	runDriftProbe(ctx, nil, p, mon, time.Hour)
	// 空池观测不到任何东西 → 状态保持 unknown。
	if got := mon.Status().(map[string]any)["state"]; got != "unknown" {
		t.Fatalf("空池探测后 state=%v want unknown", got)
	}
}

// TestCmdServerBuildDriftMonitor（R-102）：未启用=nil；启用=装配观测器；快照损坏不阻断。
func TestCmdServerBuildDriftMonitor(t *testing.T) {
	if mon := buildDriftMonitor(Default()); mon != nil {
		t.Fatal("默认 drift.enabled=false 应返回 nil")
	}
	dir := t.TempDir()
	c := Default()
	c.Drift.Enabled = true
	c.StateFile = filepath.Join(dir, "state.json")
	mon := buildDriftMonitor(c)
	if mon == nil {
		t.Fatal("启用后应返回观测器")
	}
	// 快照缺失 = unknown（不报错）。
	if got := mon.Status().(map[string]any)["state"]; got != "unknown" {
		t.Fatalf("缺失快照 state=%v want unknown", got)
	}
	// 快照损坏：Load 报错 → WARN 但观测器仍返回（不阻断启动），回 unknown 重新播种。
	bad := Default()
	bad.Drift.Enabled = true
	bad.Drift.SnapshotPath = filepath.Join(dir, "bad-snapshot.json")
	os.WriteFile(bad.Drift.SnapshotPath, []byte("{not json"), 0o644)
	mon2 := buildDriftMonitor(bad)
	if mon2 == nil {
		t.Fatal("快照损坏不应阻断装配")
	}
	if got := mon2.Status().(map[string]any)["state"]; got != "unknown" {
		t.Fatalf("损坏快照 state=%v want unknown（重新播种）", got)
	}
}

// TestCmdServerDriftProbeOnceSeeds（R-102）：池里有可用号 + 假 fetch 返回 60 条目录
// → 观测器播种快照并落盘。
func TestCmdServerDriftProbeOnceSeeds(t *testing.T) {
	p, err := newPool(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	// 替换可测接缝：假目录 60 条成功。
	orig := driftCatalogFetch
	driftCatalogFetch = func(c *upstream.Client, a *auth.Auth) (int, error) { return 60, nil }
	defer func() { driftCatalogFetch = orig }()

	snap := filepath.Join(t.TempDir(), "snap.json")
	mon := upstream.NewDriftMonitor(snap)
	driftProbeOnce(nil, p, mon)
	st := mon.Status().(map[string]any)
	if st["state"] != "ok" {
		t.Fatalf("成功探测后 state=%v want ok", st["state"])
	}
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("快照未落盘: %v", err)
	}
}

// TestCmdServerBuildUSDPriceMap
// 价格表跨包转换：三档价格（含 cache_read）必须逐字段搬到 usage.USDPrice；
// 空表返回 nil（前端据此不显示 $ 列，不伪造折算）。
func TestCmdServerBuildUSDPriceMap(t *testing.T) {
	if got := buildUSDPriceMap(nil); got != nil {
		t.Fatalf("nil 输入应返回 nil，got %#v", got)
	}
	if got := buildUSDPriceMap(map[string]ModelPrice{}); got != nil {
		t.Fatalf("空表应返回 nil（不伪造美元折算），got %#v", got)
	}
	out := buildUSDPriceMap(map[string]ModelPrice{
		"glm-5.3-flash": {Input: 0.15, Output: 0.6, CacheRead: 0.015},
	})
	if len(out) != 1 {
		t.Fatalf("转换后应 1 项，got %#v", out)
	}
	if want := (usage.USDPrice{Input: 0.15, Output: 0.6, CacheRead: 0.015}); out["glm-5.3-flash"] != want {
		t.Fatalf("转换结果=%+v want %+v（cache_read 不可漏）", out["glm-5.3-flash"], want)
	}
}

// TestCmdServerRestartRequiredFieldsUpstashConditional
// 需重启清单里的 upstash 项只在真配了 URL/token 时出现——否则面板会对纯内存部署
// 反复提示「需重启」。破坏方式：把条件判断去掉（恒加入）→ 空 upstash 用例 FAIL。
func TestCmdServerRestartRequiredFieldsUpstashConditional(t *testing.T) {
	plain, err := ParseConfig([]byte(`{"listen":":7863"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cmdserverContains(restartRequiredFields(plain), "upstash") {
		t.Fatalf("未配 upstash 时不应提示重启，got %v", restartRequiredFields(plain))
	}
	redis, err := ParseConfig([]byte(`{"upstash":{"url":"https://x.upstash.io"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cmdserverContains(restartRequiredFields(redis), "upstash") {
		t.Fatalf("配了 upstash 必须提示重启，got %v", restartRequiredFields(redis))
	}
}

// ---------------------------------------------------------------------------
// wiring.go：realm 感知的可用账号闭包
// ---------------------------------------------------------------------------

// TestCmdServerRealmAwareAvailableForModel
// 会话粘性分配用的可用账号闭包：带前缀的模型名必须先剥前缀再按 realm 过滤，
// 否则 global 号会被分配给 CN 前缀请求（跨 realm 泄漏 / 计费与风控串域）。
// 破坏方式：忽略 ResolveModel 的 realm（恒传 ""）→ 本测试的 cn/global 用例 FAIL。
func TestCmdServerRealmAwareAvailableForModel(t *testing.T) {
	p := pool.New("")
	p.Add(cmdserverAccount("u-cn", "www.codebuddy.cn"))
	p.Add(cmdserverAccount("u-global", "www.workbuddy.ai"))
	avail := realmAwareAvailableForModel(p)

	cases := []struct {
		model string
		want  []string
	}{
		{"global:gpt-5.4", []string{"u-global"}},
		{"cn:glm-5.2", []string{"u-cn"}},
		{"glm-5.2", []string{"u-cn"}},
		{"foo:glm-5.2", []string{"u-cn"}}, // 非 cn/global 前缀 → 视为裸名，走 cn
	}
	for _, tc := range cases {
		if got := avail(tc.model); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("avail(%q)=%v want %v", tc.model, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// main.go：模型倍率预热（只测可确定性判定的守卫）
// ---------------------------------------------------------------------------

// TestCmdServerWarmModelRatesNeverBlocksShutdownAndSkipsIdlePool
// 两条可断言的守卫：(1) ctx 已取消时预热必须立刻放弃（不得在上游请求上拖住进程退出）；
// (2) 池里没有可用账号时不得发起任何上游请求（避免无谓调用）。
// 出站请求经计数 RoundTripper 拦下——测试无需真实网络，且「发了请求」是可观测事实。
// 破坏方式：删掉开头的 ctx.Err() 守卫 → 已取消场景会真的发起探测请求 → calls>0 FAIL。
func TestCmdServerWarmModelRatesNeverBlocksShutdownAndSkipsIdlePool(t *testing.T) {
	// 场景 1：ctx 已取消 + 池里有可用 CN 账号（若守卫失效，下面会计到出站请求）。
	p := pool.New("")
	p.Add(cmdserverAccount("u-cn", "www.codebuddy.cn"))
	up := upstream.New()
	rt := &cmdserverCountingRT{}
	up.HTTP = &http.Client{Transport: rt}
	up.ChatHTTP = up.HTTP

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	warmModelRates(ctx, up, p)
	if rt.calls != 0 {
		t.Fatalf("ctx 已取消时预热不得发起任何上游请求（会拖住进程退出），calls=%d", rt.calls)
	}

	// 场景 2：ctx 正常但池为空 → 无可用账号，同样不得发请求。
	rt2 := &cmdserverCountingRT{}
	up2 := upstream.New()
	up2.HTTP = &http.Client{Transport: rt2}
	up2.ChatHTTP = up2.HTTP
	warmModelRates(context.Background(), up2, pool.New(""))
	if rt2.calls != 0 {
		t.Fatalf("无可用账号时不得发起上游请求，calls=%d", rt2.calls)
	}
}

// cmdserverContains 报告切片是否含某字符串。
func cmdserverContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
