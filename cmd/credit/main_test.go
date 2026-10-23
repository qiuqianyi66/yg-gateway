package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D-04：损坏/不可读的 auth 文件必须被识别出来（broken=true），由 main 循环补一条
// Error 行——静默 continue 会把坏文件从输出里整体抹掉，总行数与目录文件数对不上。
func TestLoadAuthFileBroken(t *testing.T) {
	dir := t.TempDir()

	// 1) JSON 语法损坏。
	bad := filepath.Join(dir, "workbuddy-bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, broken := loadAuthFile(bad); !broken {
		t.Error("JSON 损坏文件必须报 broken")
	}

	// 2) 文件根本不存在（os.ReadFile 失败分支）。
	missing := filepath.Join(dir, "workbuddy-missing.json")
	if _, broken := loadAuthFile(missing); !broken {
		t.Error("缺失文件必须报 broken")
	}

	// 3) 正常文件解析出来（反向确认：没把好文件误判成坏）。
	good := filepath.Join(dir, "workbuddy-good.json")
	if err := os.WriteFile(good, []byte(`{"account":{"uid":"u-1","nickname":"n"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	af, broken := loadAuthFile(good)
	if broken {
		t.Error("正常文件不得报 broken")
	}
	if af.Account.UID != "u-1" {
		t.Errorf("UID = %q, want u-1", af.Account.UID)
	}
}

// billingBaseFor 按 realm/domain 选域：显式 realm=global 或 workbuddy.ai 家族 → 国际域。
func TestBillingBaseFor(t *testing.T) {
	cn := &authFile{}
	if got := billingBaseFor(cn); got != billingBaseCN {
		t.Errorf("默认（无 realm/domain）= %s, want %s", got, billingBaseCN)
	}
	global := &authFile{}
	global.Auth.Realm = "GLOBAL" // EqualFold：大小写不敏感
	if got := billingBaseFor(global); got != billingBaseGlobal {
		t.Errorf("realm=global = %s, want %s", got, billingBaseGlobal)
	}
	dom := &authFile{}
	dom.Auth.Domain = "foo.workbuddy.ai"
	if got := billingBaseFor(dom); got != billingBaseGlobal {
		t.Errorf("domain=foo.workbuddy.ai = %s, want %s", got, billingBaseGlobal)
	}
	cnDom := &authFile{}
	cnDom.Auth.Domain = "codebuddy.cn"
	if got := billingBaseFor(cnDom); got != billingBaseCN {
		t.Errorf("domain=codebuddy.cn = %s, want %s", got, billingBaseCN)
	}
}

// billingPathFor：global 域无 /v2 前缀，其余带 /v2。
func TestBillingPathFor(t *testing.T) {
	if got := billingPathFor(billingBaseGlobal); got != "/billing/meter/get-user-resource" {
		t.Errorf("global path = %s, want 无 /v2 前缀", got)
	}
	if got := billingPathFor(billingBaseCN); got != "/v2/billing/meter/get-user-resource" {
		t.Errorf("cn path = %s, want 带 /v2 前缀", got)
	}
}

// fetchUserResourceAt 的聚合与错误分支（httptest 注入假上游；真实域名无法单测）。
func TestFetchUserResourceAt(t *testing.T) {
	// 成功：两个 package（一个 cycle 容量、一个基础容量）+ TotalDosage 提升 size 下限。
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":{"Response":{"Data":{
			"TotalDosage": 1000,
			"Accounts":[
				{"CapacityRemain":50,"CapacityUsed":10,"CapacitySize":60},
				{"CycleCapacityRemain":200,"CycleCapacityUsed":50,"CycleCapacitySize":300}
			]}}}}`)
	}))
	defer srv.Close()

	af := &authFile{}
	af.Auth.AccessToken = "tok"
	af.Auth.Domain = "workbuddy.ai"
	af.Account.UID = "u-1"
	af.Account.EnterpriseID = "e-1"

	remain, used, size, packs, err := fetchUserResourceAt(srv.URL, af, srv.Client())
	if err != nil {
		t.Fatalf("fetchUserResourceAt: %v", err)
	}
	if packs != 2 {
		t.Errorf("packs = %d, want 2", packs)
	}
	if remain != 250 { // 50 + 200
		t.Errorf("remain = %d, want 250", remain)
	}
	if size != 1000 { // 60+300=360 < TotalDosage 1000 → 取 1000
		t.Errorf("size = %d, want 1000", size)
	}
	if used != 750 { // size-remain = 1000-250 = 750 > 60(10+50) → 取 750
		t.Errorf("used = %d, want 750", used)
	}
	if gotPath != billingPathFor(srv.URL) {
		t.Errorf("path = %s, want %s（测试注入 base 非 global → 带 /v2 前缀）", gotPath, billingPathFor(srv.URL))
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}
}

// fetchUserResourceAt 错误分支：HTTP >=400 / code!=0 / body 非 JSON。
func TestFetchUserResourceAtErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
		want string
	}{
		{"http500", `{}`, 500, "http 500"},
		{"codeNonZero", `{"code":7,"msg":"expired"}`, 200, "code=7 expired"},
		{"badJSON", `not json`, 200, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
				fmt.Fprint(w, c.body)
			}))
			defer srv.Close()
			af := &authFile{}
			af.Auth.AccessToken = "tok"
			_, _, _, _, err := fetchUserResourceAt(srv.URL, af, srv.Client())
			if c.want == "" {
				if err == nil {
					t.Error("bad JSON 必须报错")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want contains %q", err, c.want)
			}
		})
	}
}

// fetchUserResourceAt：client.Do 失败（网络错误）分支。
func TestFetchUserResourceAtDoError(t *testing.T) {
	af := &authFile{}
	af.Auth.AccessToken = "tok"
	// 指向不可达地址，连接必然失败。
	badClient := &http.Client{}
	_, _, _, _, err := fetchUserResourceAt("http://127.0.0.1:1", af, badClient)
	if err == nil {
		t.Error("连接失败必须报错")
	}
}

// run 全流程：损坏文件 / 缺 token / fetch 失败 / fetch 成功 / 汇总 / 排序。
func TestRunFullFlow(t *testing.T) {
	dir := t.TempDir()
	// 两个文件按文件名排序（b 在 a 前）。
	mustWrite(t, filepath.Join(dir, "workbuddy-b.json"),
		`{"auth":{"accessToken":"tok-b","domain":"codebuddy.cn"},"account":{"uid":"uid-bbbbb1","nickname":"B"}}`)
	mustWrite(t, filepath.Join(dir, "workbuddy-a.json"),
		`{"auth":{"accessToken":"tok-a","domain":"codebuddy.cn"},"account":{"uid":"uid-aaaaa2","nickname":"A"}}`)
	// 一个损坏文件 + 一个无 token 文件。
	mustWrite(t, filepath.Join(dir, "workbuddy-bad.json"), `{broken`)
	mustWrite(t, filepath.Join(dir, "workbuddy-notok.json"),
		`{"account":{"uid":"uid-notok3"}}`)

	var gotUIDs []string
	fetch := func(af *authFile) (int64, int64, int64, int, error) {
		gotUIDs = append(gotUIDs, af.Account.UID)
		switch af.Account.UID {
		case "uid-aaaaa2":
			return 10, 90, 100, 1, nil
		case "uid-bbbbb1":
			return 5, 5, 10, 1, fmt.Errorf("code=9 nope")
		}
		return 0, 0, 0, 0, nil
	}

	var out, errOut bytes.Buffer
	code := run([]string{"credit"}, dir, fetch, &out, &errOut)
	if code != 0 {
		t.Fatalf("run 退出码 = %d, want 0；stderr=%s", code, errOut.String())
	}

	var got struct {
		Total struct {
			Remain   int64 `json:"remain"`
			Used     int64 `json:"used"`
			Size     int64 `json:"size"`
			Accounts int   `json:"accounts"`
			OK       int   `json:"ok"`
			Failed   int   `json:"failed"`
		} `json:"total"`
		Accounts []accountResult `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, out.String())
	}
	// 4 个文件（坏/无 token 各 1）；只有 A 成功。
	if got.Total.Accounts != 4 || got.Total.OK != 1 || got.Total.Failed != 3 {
		t.Errorf("total = %+v, want accounts=4 ok=1 failed=3", got.Total)
	}
	if got.Total.Remain != 10 || got.Total.Used != 90 || got.Total.Size != 100 {
		t.Errorf("total 数值 = %+v, want remain=10 used=90 size=100", got.Total)
	}
	// fetch 按文件名升序被调用：a 先于 b。
	if len(gotUIDs) != 2 || gotUIDs[0] != "uid-aaaaa2" || gotUIDs[1] != "uid-bbbbb1" {
		t.Errorf("fetch 顺序 = %v, want [uid-aaaaa2 uid-bbbbb1]", gotUIDs)
	}
	// 坏文件/无 token 各补一条 Error 行。
	for _, a := range got.Accounts {
		if a.UID == "uid-notok3" && a.Error != "no accessToken" {
			t.Errorf("无 token 账号 error = %q, want no accessToken", a.Error)
		}
	}
	if !strings.Contains(out.String(), "unreadable/corrupt: workbuddy-bad.json") {
		t.Error("损坏文件必须出现在输出 Error 行")
	}
}

// run 的 -pretty 分支与 glob 失败分支。
func TestRunPrettyAndGlobError(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "workbuddy-a.json"),
		`{"auth":{"accessToken":"tok","domain":"codebuddy.cn"},"account":{"uid":"uid-pppppp1","nickname":"P"}}`)
	fetch := func(af *authFile) (int64, int64, int64, int, error) { return 30, 70, 100, 1, nil }

	var out, errOut bytes.Buffer
	code := run([]string{"credit", "-pretty"}, dir, fetch, &out, &errOut)
	if code != 0 {
		t.Fatalf("pretty 分支退出码 = %d", code)
	}
	text := out.String()
	for _, want := range []string{"📊 WorkBuddy 积分日报", "账号: 1/1", "总计: 30/100", "剩余: 30%"} {
		if !strings.Contains(text, want) {
			t.Errorf("pretty 输出缺 %q：\n%s", want, text)
		}
	}

	// glob 失败：authDir 含 '[' 元字符 → 退出码 1 + stderr 说明。
	var out2, errOut2 bytes.Buffer
	code2 := run([]string{"credit"}, filepath.Join(dir, "bad[dir"), fetch, &out2, &errOut2)
	if code2 != 1 {
		t.Errorf("glob 失败退出码 = %d, want 1", code2)
	}
	if !strings.Contains(errOut2.String(), "枚举 auths 失败") {
		t.Errorf("stderr = %q, want 含 枚举 auths 失败", errOut2.String())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
