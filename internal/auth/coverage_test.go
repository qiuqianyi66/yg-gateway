package auth

// coverage_test.go — internal/auth 关键路径补强（纯增量：不改生产代码、不改既有测试）。
//
// 覆盖重点：
//   - 落盘形状（SaveAtomic 的键白名单 / tmp+rename 目标 / 0o600 / 拒绝写回）
//   - 凭证读取的加锁语义（AccessTokenValue / RefreshTokenValue / DomainValue / Lock / Unlock）
//   - 解析错误分类（空输入 / 类型不符 / 空白 token）
//   - token 过期边界（NeedsRefresh 的 >= 死线）与 realm 显式写入（BackfillRealmFor）
//   - LoadDir 的错误传播与「已有标识的文件不被重写」

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// 落盘内容里绝不允许出现的错误类文本（R1：凭证文件只放凭证，错误信息走日志）。
var authForbiddenDumpSubstrings = []string{"error", "err=", "failed", "panic", "stack"}

// authSortedKeys 返回 map 顶层键的字典序切片（精确断言落盘 JSON 的键集合）。
func authSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// authAssertKeys 断言 m 的键集合恰好等于 wantCSV（逗号分隔、字典序）。
func authAssertKeys(t *testing.T, label string, m map[string]any, wantCSV string) {
	t.Helper()
	got := strings.Join(authSortedKeys(m), ",")
	if got != wantCSV {
		t.Errorf("%s keys = %q want %q", label, got, wantCSV)
	}
}

// authSaveAndDecode 落盘 a.FilePath 并解码为顶层 JSON 对象，同时返回原始文本。
func authSaveAndDecode(t *testing.T, a *Auth) (map[string]any, string) {
	t.Helper()
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic(%s): %v", a.FilePath, err)
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		t.Fatalf("read %s: %v", a.FilePath, err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal saved doc %s: %v", a.FilePath, err)
	}
	return top, string(raw)
}

// authNested 取出顶层子对象（类型不符直接 fatal）。
func authNested(t *testing.T, top map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := top[key].(map[string]any)
	if !ok {
		t.Fatalf("top-level %q is %T want JSON object", key, top[key])
	}
	return v
}

// authWrite 写入测试 fixture 文件（失败即 fatal）。
func authWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// authFixtures: 旧 token 哨兵串——SaveAtomic 必须以「整体替换」而非追加/合并方式写回。
const authOldTokenSentinel = "OLD-TOKEN-SENTINEL-MUST-VANISH"

// ---------------------------------------------------------------------------
// Parse：错误分类
// ---------------------------------------------------------------------------

// TestAuthParseRejectsEmptyStorage 空输入（nil / 零长）必须报 "empty auth storage"，
// 而不是落到 json.Unmarshal 的 "unexpected end of JSON input"。
func TestAuthParseRejectsEmptyStorage(t *testing.T) {
	for _, raw := range [][]byte{nil, {}} {
		a, err := Parse(raw)
		if err == nil {
			t.Fatalf("Parse(%v) err=nil want error", raw)
		}
		if err.Error() != "empty auth storage" {
			t.Errorf("Parse(%v) err=%q want %q", raw, err.Error(), "empty auth storage")
		}
		if a != nil {
			t.Errorf("Parse(%v) auth=%+v want nil", raw, a)
		}
	}
}

// TestAuthParseTypeMismatchWrappedError JSON 结构类型不符（嵌套形 auth 非对象 /
// 扁平形 accessToken 是数字）必须被归类为 storage_parse_error 且不返回半成品 Auth。
func TestAuthParseTypeMismatchWrappedError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"nested-auth-not-object", `{"auth":"nope"}`},
		{"nested-account-not-object", `{"auth":{"accessToken":"at"},"account":42}`},
		{"flat-accessToken-number", `{"accessToken":123}`},
		{"flat-uid-array", `{"accessToken":"at","uid":["u1"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := Parse([]byte(c.raw))
			if err == nil {
				t.Fatalf("Parse(%s) err=nil want storage_parse_error", c.raw)
			}
			if !strings.Contains(err.Error(), "storage_parse_error") {
				t.Errorf("Parse(%s) err=%q want storage_parse_error prefix", c.raw, err.Error())
			}
			if a != nil {
				t.Errorf("Parse(%s) auth=%+v want nil on error", c.raw, a)
			}
		})
	}
}

// TestAuthParseRejectsBlankAccessToken 纯空白 accessToken 视为缺失（核对 TrimSpace 而非 == ""），
// 嵌套形与扁平形两条路径都要拦。
func TestAuthParseRejectsBlankAccessToken(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"flat-space", `{"accessToken":"   ","refreshToken":"rt","uid":"u1"}`},
		{"flat-tab", `{"accessToken":"\t","refreshToken":"rt","uid":"u1"}`},
		{"nested-space", `{"auth":{"accessToken":"  ","refreshToken":"rt"},"account":{"uid":"u1"}}`},
		{"nested-empty-key", `{"auth":{"refreshToken":"rt"},"account":{"uid":"u1"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := Parse([]byte(c.raw))
			if err == nil {
				t.Fatalf("Parse(%s) err=nil want missing accessToken", c.raw)
			}
			if !strings.Contains(err.Error(), "missing accessToken") {
				t.Errorf("Parse(%s) err=%q want missing accessToken", c.raw, err.Error())
			}
			if a != nil {
				t.Errorf("Parse(%s) auth=%+v want nil", c.raw, a)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SaveAtomic：落盘形状 / tmp+rename / 拒绝写回
// ---------------------------------------------------------------------------

// TestAuthSaveAtomicRefusesBlankAccessToken 空 token 必须拒绝写回，且**不得触碰原文件**
// （绝不能出现「刷新失败后用空凭证覆盖有效文件」），也不得留下 tmp 残留。
func TestAuthSaveAtomicRefusesBlankAccessToken(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-u1.json")
	old := `{"auth":{"accessToken":"at-valid","refreshToken":"rt","expiresAt":1,"domain":""},"account":{"uid":"u1"}}`
	authWrite(t, fp, old)

	a := &Auth{AccessToken: "   ", UID: "u1", FilePath: fp}
	err := a.SaveAtomic()
	if err == nil {
		t.Fatal("空 accessToken 应拒绝写回")
	}
	if !strings.Contains(err.Error(), "empty accessToken") {
		t.Errorf("err=%q want 含 empty accessToken", err.Error())
	}
	if !strings.Contains(err.Error(), "uid=u1") {
		t.Errorf("err=%q want 含 uid 便于定位", err.Error())
	}
	raw, rerr := os.ReadFile(fp)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if string(raw) != old {
		t.Errorf("拒绝写回时原文件被改动:\n got %s\nwant %s", raw, old)
	}
	if _, serr := os.Stat(fp + ".tmp"); !os.IsNotExist(serr) {
		t.Error("拒绝写回时不得留下 .tmp 残留")
	}
}

// TestAuthSaveAtomicRequiresFilePath 未设置 FilePath 时报错且不在进程工作目录留下 .tmp。
func TestAuthSaveAtomicRequiresFilePath(t *testing.T) {
	// 工作目录下若出现 .tmp，说明实现退化成了「先落盘再校验路径」。
	if _, err := os.Stat(".tmp"); err == nil {
		t.Fatal("precondition: 工作目录不应存在 .tmp")
	}
	a := &Auth{AccessToken: "at", UID: "u1"}
	err := a.SaveAtomic()
	if err == nil {
		t.Fatal("FilePath 为空应报错")
	}
	if err.Error() != "no FilePath set" {
		t.Errorf("err=%q want %q", err.Error(), "no FilePath set")
	}
	if _, serr := os.Stat(".tmp"); serr == nil {
		_ = os.Remove(".tmp")
		t.Error("FilePath 为空时不得写任何文件（工作目录出现 .tmp）")
	}
}

// TestAuthSaveAtomicPermissionHintAndTmpTarget 写目标不可写时（只读 .tmp）：
//  1. 错误必须带 Docker 权限指引（chown / 10001）；
//  2. 写入目标必须是 FilePath+".tmp"（否则本测试的只读文件拦不住，SaveAtomic 会成功）；
//  3. 原文件保持原样（rename 未发生）。
func TestAuthSaveAtomicPermissionHintAndTmpTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 可无视权限位，无法构造 EACCES")
	}
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-u1.json")
	tmp := fp + ".tmp"
	old := `{"auth":{"accessToken":"at-valid","refreshToken":"rt","expiresAt":1,"domain":""},"account":{"uid":"u1"}}`
	authWrite(t, fp, old)
	authWrite(t, tmp, "sentinel")
	// 只读属性（Windows: read-only attribute；POSIX: 0444）→ WriteFile 返回 fs.ErrPermission。
	if err := os.Chmod(tmp, 0o444); err != nil {
		t.Fatalf("chmod tmp: %v", err)
	}

	a := &Auth{AccessToken: "at-new", UID: "u1", FilePath: fp}
	err := a.SaveAtomic()
	if err == nil {
		t.Fatal("只读 .tmp 应使 SaveAtomic 失败（若成功说明 tmp 路径不是 FilePath+\".tmp\"，破坏了同目录 rename 原子性）")
	}
	if !strings.Contains(err.Error(), tmp) {
		t.Errorf("err=%q want 含写入目标 %q", err.Error(), tmp)
	}
	if !strings.Contains(err.Error(), "chown") || !strings.Contains(err.Error(), "10001") {
		t.Errorf("权限错误应含 Docker 指引（chown/10001），实际: %v", err)
	}
	raw, rerr := os.ReadFile(fp)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if string(raw) != old {
		t.Errorf("写失败时原文件被改动: %s", raw)
	}
}

// TestAuthSaveAtomicKeyWhitelistNoErrorText 落盘 JSON 的键集合必须严格等于白名单
// （R1：凭证文件只放凭证字段，绝不夹带错误信息/调试字段），可选键缺省时不得出现空键。
func TestAuthSaveAtomicKeyWhitelistNoErrorText(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{
		AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: 42,
		Domain: "www.workbuddy.ai", realm: "global",
		UID: "u-1", EnterpriseID: "ent-1", Nickname: "nick-1",
		FilePath: filepath.Join(dir, "workbuddy-u1.json"),
	}
	top, text := authSaveAndDecode(t, a)

	authAssertKeys(t, "top-level", top, "account,auth")
	authObj := authNested(t, top, "auth")
	authAssertKeys(t, "auth", authObj, "accessToken,domain,expiresAt,realm,refreshToken")
	acctObj := authNested(t, top, "account")
	authAssertKeys(t, "account", acctObj, "enterpriseId,nickname,uid")

	// expiresAt 必须以 JSON 数字落盘（Parse 侧按 int64 读；写成字符串会静默丢过期时间）。
	if got, ok := authObj["expiresAt"].(float64); !ok || got != 42 {
		t.Errorf("auth.expiresAt = %#v want JSON number 42", authObj["expiresAt"])
	}
	if got := authObj["realm"]; got != "global" {
		t.Errorf("auth.realm = %#v want \"global\"", got)
	}
	// 可选键缺省 → 键必须缺席（不是 ""），否则旧文件被塞入空键。
	if _, ok := top["device_token"]; ok {
		t.Error("DeviceToken 为空时顶层不得出现 device_token 键")
	}
	if _, ok := top["group"]; ok {
		t.Error("Group 为空时顶层不得出现 group 键")
	}
	// 落盘文本里绝不能出现任何错误类字样。
	low := strings.ToLower(text)
	for _, bad := range authForbiddenDumpSubstrings {
		if strings.Contains(low, bad) {
			t.Errorf("落盘 JSON 含错误类字样 %q（R1：凭证文件不放错误信息）:\n%s", bad, text)
		}
	}
}

// TestAuthSaveAtomicReplacesOldContentAndMode SaveAtomic 必须整体替换（先写 tmp 再 rename），
// 不残留旧 token；POSIX 下新文件权限必须是 0o600。
func TestAuthSaveAtomicReplacesOldContentAndMode(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-u1.json")
	authWrite(t, fp, `{"auth":{"accessToken":"`+authOldTokenSentinel+`","refreshToken":"rt-old"},"account":{"uid":"u1"}}`)

	a := &Auth{AccessToken: "at-new", RefreshToken: "rt-new", ExpiresAt: 1753600000,
		Domain: "www.codebuddy.cn", realm: "cn", UID: "u1", EnterpriseID: "e1",
		Nickname: "n1", FilePath: fp}
	top, text := authSaveAndDecode(t, a)
	if strings.Contains(text, authOldTokenSentinel) {
		t.Errorf("旧 token 残留（落盘应为整体替换而非追加/合并）:\n%s", text)
	}
	b, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if b.AccessToken != "at-new" || b.RefreshToken != "rt-new" || b.ExpiresAt != 1753600000 {
		t.Errorf("roundtrip tokens: %+v", b)
	}
	if b.UID != "u1" || b.EnterpriseID != "e1" || b.Nickname != "n1" {
		t.Errorf("roundtrip account: %+v", b)
	}
	if got := authNested(t, top, "auth")["domain"]; got != "www.codebuddy.cn" {
		t.Errorf("auth.domain = %#v want www.codebuddy.cn", got)
	}
	if _, err := os.Stat(fp + ".tmp"); !os.IsNotExist(err) {
		t.Error("rename 后不得留下 .tmp")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(fp)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("落盘权限 = %#o want 0600", perm)
		}
	}
}

// TestAuthSaveAtomicGroupAndDeviceTokenRoundTrip 非空的 group / device_token 必须写到**顶层**
// 且两种磁盘形态都能解析回来（嵌套形与扁平形共用顶层键）。
func TestAuthSaveAtomicGroupAndDeviceTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-u1.json")
	authWrite(t, fp, `{"auth":{"accessToken":"at-1","refreshToken":"rt-1","expiresAt":1,"realm":"cn"},`+
		`"account":{"uid":"u1"},"device_token":"dev-old","group":"team-old"}`)

	parsed, err := Parse([]byte(authMustRead(t, fp)))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if parsed.Group != "team-old" || parsed.DeviceToken != "dev-old" {
		t.Fatalf("nested fixture parse group=%q device=%q", parsed.Group, parsed.DeviceToken)
	}
	parsed.FilePath = fp // Parse 不设来源文件（LoadDir 才设），写回前需显式指定

	parsed.Group = "team-a"
	parsed.DeviceToken = "dev-new"
	top, _ := authSaveAndDecode(t, parsed)
	if got := top["group"]; got != "team-a" {
		t.Errorf("顶层 group = %#v want \"team-a\"", got)
	}
	if got := top["device_token"]; got != "dev-new" {
		t.Errorf("顶层 device_token = %#v want \"dev-new\"", got)
	}

	b, err := Parse([]byte(authMustRead(t, fp)))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if b.Group != "team-a" || b.DeviceToken != "dev-new" {
		t.Errorf("roundtrip group=%q device=%q want team-a/dev-new", b.Group, b.DeviceToken)
	}

	// 扁平形也要读顶层 group（手写 auth 直接写 "group"）。
	flat, err := Parse([]byte(`{"accessToken":"at","refreshToken":"rt","uid":"u2","group":"team-flat"}`))
	if err != nil {
		t.Fatalf("flat parse: %v", err)
	}
	if flat.Group != "team-flat" {
		t.Errorf("flat Group = %q want team-flat", flat.Group)
	}
}

// authMustRead 读取文件内容（失败即 fatal）。
func authMustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// 加锁访问器：Lock / Unlock / AccessTokenValue / RefreshTokenValue / DomainValue
// ---------------------------------------------------------------------------

// TestAuthAccessorsNilReceiverSafe nil 接收者必须返回空串（调用点形如
// `a == nil || a.AccessTokenValue() == ""` 与 `if a.RefreshTokenValue() == ""` 混用）。
func TestAuthAccessorsNilReceiverSafe(t *testing.T) {
	var a *Auth
	if got := a.AccessTokenValue(); got != "" {
		t.Errorf("nil.AccessTokenValue()=%q want empty", got)
	}
	if got := a.RefreshTokenValue(); got != "" {
		t.Errorf("nil.RefreshTokenValue()=%q want empty", got)
	}
	if got := a.DomainValue(); got != "" {
		t.Errorf("nil.DomainValue()=%q want empty", got)
	}
}

// TestAuthAccessorsReflectLockedWrites 三个访问器必须各读各自字段（不串字段），
// 且能看到更新（upstream.RefreshToken 在 Lock/Unlock 之间改写 token 字段的写路径）。
func TestAuthAccessorsReflectLockedWrites(t *testing.T) {
	a := &Auth{AccessToken: "at-v1", RefreshToken: "rt-v1", Domain: "domain-v1", UID: "u1"}
	if got := a.AccessTokenValue(); got != "at-v1" {
		t.Errorf("AccessTokenValue()=%q want at-v1", got)
	}
	if got := a.RefreshTokenValue(); got != "rt-v1" {
		t.Errorf("RefreshTokenValue()=%q want rt-v1", got)
	}
	if got := a.DomainValue(); got != "domain-v1" {
		t.Errorf("DomainValue()=%q want domain-v1", got)
	}

	// 模拟出站刷新：持锁写回新凭证后，访问器必须返回新值（锁正常释放）。
	a.Lock()
	a.AccessToken, a.RefreshToken, a.Domain = "at-v2", "rt-v2", "domain-v2"
	a.Unlock()

	if got := a.AccessTokenValue(); got != "at-v2" {
		t.Errorf("after locked write AccessTokenValue()=%q want at-v2", got)
	}
	if got := a.RefreshTokenValue(); got != "rt-v2" {
		t.Errorf("after locked write RefreshTokenValue()=%q want rt-v2", got)
	}
	if got := a.DomainValue(); got != "domain-v2" {
		t.Errorf("after locked write DomainValue()=%q want domain-v2", got)
	}
}

// TestAuthAccessorsBlockWhileLockHeld 访问器必须真正走 a.mu：持锁期间读必须阻塞，
// 解锁后立刻返回。直接读字段的实现会立即返回（这正是生产上 -race 抓到的数据竞争）。
func TestAuthAccessorsBlockWhileLockHeld(t *testing.T) {
	a := &Auth{AccessToken: "at-locked", RefreshToken: "rt-locked", Domain: "domain-locked"}
	accessors := []struct {
		name string
		get  func() string
		want string
	}{
		{"AccessTokenValue", a.AccessTokenValue, "at-locked"},
		{"RefreshTokenValue", a.RefreshTokenValue, "rt-locked"},
		{"DomainValue", a.DomainValue, "domain-locked"},
	}
	for _, acc := range accessors {
		a.Lock()
		done := make(chan string, 1)
		go func(get func() string) { done <- get() }(acc.get)

		select {
		case got := <-done:
			a.Unlock()
			t.Fatalf("%s 在持锁期间返回 %q —— 访问器未与 Lock/Unlock 同步", acc.name, got)
		case <-time.After(100 * time.Millisecond):
			// 期望：仍未返回（被锁挡住）。
		}

		a.Unlock()
		select {
		case got := <-done:
			if got != acc.want {
				t.Errorf("%s=%q want %q", acc.name, got, acc.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s 在 Unlock 后仍未返回（锁未释放 / 死锁）", acc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// global 开关 / 过期边界 / BackfillRealmFor
// ---------------------------------------------------------------------------

// TestAuthSetGlobalEnabledEscapeHatch SetGlobalEnabled/GlobalEnabled 必须真实读写同一开关：
// 关掉后恒判 cn（逃生门），开回来恢复 global 判定。
func TestAuthSetGlobalEnabledEscapeHatch(t *testing.T) {
	if !GlobalEnabled() {
		t.Fatal("前置条件：global 开关缺省应为开启态")
	}
	t.Cleanup(func() { SetGlobalEnabled(true) })

	g := &Auth{realm: "global", Domain: "www.workbuddy.ai"}

	SetGlobalEnabled(false)
	if GlobalEnabled() {
		t.Error("SetGlobalEnabled(false) 后 GlobalEnabled() 仍为 true")
	}
	if got := g.Realm(); got != "cn" {
		t.Errorf("逃生门关闭时 Realm()=%q want cn", got)
	}
	if g.IsGlobal() {
		t.Error("逃生门关闭时 IsGlobal()=true，应恒 false")
	}

	SetGlobalEnabled(true)
	if !GlobalEnabled() {
		t.Error("SetGlobalEnabled(true) 后 GlobalEnabled() 仍为 false")
	}
	if got := g.Realm(); got != "global" {
		t.Errorf("开关恢复后 Realm()=%q want global", got)
	}
}

// TestAuthNeedsRefreshBoundary 过期边界：expiresAt 恰好等于 now+within 时必须判需刷新
// （实现是 `>=`，写成 `>` 会让死线当刻的 token 漏刷）。
func TestAuthNeedsRefreshBoundary(t *testing.T) {
	const within = 30 * time.Minute

	deadline := &Auth{ExpiresAt: time.Now().Add(within).Unix()}
	if !deadline.NeedsRefresh(within) {
		t.Errorf("ExpiresAt=now+within 必须判需刷新（>= 边界）")
	}
	insideWindow := &Auth{ExpiresAt: time.Now().Add(within + 3*time.Second).Unix()}
	if insideWindow.NeedsRefresh(within) {
		t.Errorf("ExpiresAt=now+within+3s 不应判需刷新")
	}
	justExpired := &Auth{ExpiresAt: time.Now().Unix() - 1}
	if !justExpired.NeedsRefresh(within) {
		t.Error("已过期应判需刷新")
	}
	noExpiry := &Auth{ExpiresAt: 0}
	if !noExpiry.NeedsRefresh(within) {
		t.Error("无 expiry 应判需刷新")
	}
}

// TestAuthBackfillRealmForValidation 显式写入 realm 的入参校验：nil / 非法值必须报错
// 且不写脏（"防写脏"）；合法值里的首尾空白被归一化接受。
func TestAuthBackfillRealmForValidation(t *testing.T) {
	if changed, err := BackfillRealmFor(nil, "global"); err == nil || changed {
		t.Errorf("BackfillRealmFor(nil)=%v,%v want error,false", changed, err)
	} else if !strings.Contains(err.Error(), "nil auth") {
		t.Errorf("nil auth err=%q want 含 nil auth", err.Error())
	}

	for _, bad := range []string{"", "   ", "eu", "Global", "GLOBAL", "cn;global", "gl0bal"} {
		a := &Auth{UID: "u1"}
		changed, err := BackfillRealmFor(a, bad)
		if err == nil {
			t.Errorf("BackfillRealmFor(%q) err=nil want error", bad)
			continue
		}
		if changed {
			t.Errorf("BackfillRealmFor(%q) changed=true want false（非法值不得写入）", bad)
		}
		if got := a.RealmStored(); got != "" {
			t.Errorf("BackfillRealmFor(%q) 写入脏值 realm=%q want empty", bad, got)
		}
	}

	ws := &Auth{UID: "u2"}
	if _, err := BackfillRealmFor(ws, "  global  "); err != nil {
		t.Errorf("带首尾空白的合法 realm 应被接受: %v", err)
	} else if got := ws.Realm(); got != "global" {
		t.Errorf("ws.Realm()=%q want global", got)
	}
}

// TestAuthBackfillRealmForWritesAndIdempotent 显式 realm 优先于 domain 回落；同值再写为
// no-op（幂等）；切换 cn/global 视为变更。
func TestAuthBackfillRealmForWritesAndIdempotent(t *testing.T) {
	withGlobalEnabled(t)
	// cn 域上显式写 global：显式值必须赢（面板登录已知用户选了 global）。
	a := &Auth{UID: "u1", Domain: "www.codebuddy.cn"}

	changed, err := BackfillRealmFor(a, "global")
	if err != nil || !changed {
		t.Fatalf("BackfillRealmFor(global)=%v,%v want true,nil", changed, err)
	}
	if got := a.RealmStored(); got != "global" {
		t.Errorf("RealmStored()=%q want global", got)
	}
	if got := a.Realm(); got != "global" {
		t.Errorf("Realm()=%q want global（显式优先于 cn domain 回落）", got)
	}

	if changed, err := BackfillRealmFor(a, "global"); err != nil || changed {
		t.Errorf("同值重写 = %v,%v want false,nil（幂等）", changed, err)
	}
	if changed, err := BackfillRealmFor(a, "cn"); err != nil || !changed {
		t.Errorf("global→cn = %v,%v want true,nil", changed, err)
	}
	if got := a.Realm(); got != "cn" {
		t.Errorf("切换后 Realm()=%q want cn", got)
	}
	if changed, err := BackfillRealmFor(a, "cn"); err != nil || changed {
		t.Errorf("cn 重写 = %v,%v want false,nil", changed, err)
	}
}

// ---------------------------------------------------------------------------
// LoadDir：错误传播 / 非文件条目 / 不做无谓重写
// ---------------------------------------------------------------------------

// TestAuthLoadDirBadPatternReturnsError 非法 glob 模式必须把错误抛给调用方，
// 而不是静默返回空账号列表（那会让整个账号池无声变空）。
func TestAuthLoadDirBadPatternReturnsError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad[dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	list, err := LoadDir(dir)
	if err == nil {
		t.Fatalf("非法 glob 模式应报错，实际 err=nil list=%d", len(list))
	}
	if !errors.Is(err, filepath.ErrBadPattern) {
		t.Errorf("err=%v want filepath.ErrBadPattern", err)
	}
	if list != nil {
		t.Errorf("出错时 list=%d want nil", len(list))
	}
}

// TestAuthLoadDirSkipsNonFileEntry 目录项名形如 workbuddy*.json（如误建的目录）时读失败，
// 必须跳过而不是把空 Auth 塞进结果集、也不阻断其他账号加载。
func TestAuthLoadDirSkipsNonFileEntry(t *testing.T) {
	dir := t.TempDir()
	good := `{"auth":{"accessToken":"at-1","refreshToken":"rt-1","expiresAt":1,"realm":"cn"},"account":{"uid":"u-ok"}}`
	authWrite(t, filepath.Join(dir, "workbuddy-ok.json"), good)
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-dir.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	list, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir err=%v want nil（单个条目不可读不应阻断启动）", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 账号（目录条目应被跳过），got %d", len(list))
	}
	if list[0].UID != "u-ok" || list[0].AccessTokenValue() != "at-1" {
		t.Errorf("loaded=%+v want uid=u-ok/at-1", list[0])
	}
}

// TestAuthLoadDirDoesNotRewriteMigratedFile 只有缺 realm 标识的文件被 backfill 重写；
// 已有标识的文件必须逐字节保持原样（幂等，不做无谓落盘）。
func TestAuthLoadDirDoesNotRewriteMigratedFile(t *testing.T) {
	dir := t.TempDir()
	already := `{"auth":{"accessToken":"at-1","refreshToken":"rt-1","expiresAt":1,"domain":"www.workbuddy.ai","realm":"global"},"account":{"uid":"u-1"}}`
	needs := `{"auth":{"accessToken":"at-2","refreshToken":"rt-2","expiresAt":1,"domain":"www.workbuddy.ai"},"account":{"uid":"u-2"}}`
	fpAlready := filepath.Join(dir, "workbuddy-a.json")
	fpNeeds := filepath.Join(dir, "workbuddy-b.json")
	authWrite(t, fpAlready, already)
	authWrite(t, fpNeeds, needs)

	list, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 账号, got %d", len(list))
	}
	if got := authMustRead(t, fpAlready); got != already {
		t.Errorf("已有 realm 标识的文件被重写:\n got %s\nwant %s", got, already)
	}
	if got := authMustRead(t, fpNeeds); got == needs {
		t.Error("缺 realm 标识的文件未被 backfill 重写")
	}
	migrated, err := Parse([]byte(authMustRead(t, fpNeeds)))
	if err != nil {
		t.Fatalf("reparse migrated: %v", err)
	}
	if migrated.RealmStored() != "global" {
		t.Errorf("migrated realm=%q want global", migrated.RealmStored())
	}
}
