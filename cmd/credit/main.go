// credit.go — WorkBuddy 积分查询（全部账号 + 总计），JSON 输出到 stdout。
//
// 用法:
//
//	go run ./cmd/credit        # 或编译后 ./credit
//
// 输出结构:
//
//	{"service":"workbuddy","ts":N,
//	 "total":{"remain":N,"used":N,"size":N,"accounts":N,"ok":N,"failed":N},
//	 "accounts":[{"uid","nickname","remain","used","size","packages","ok","error?"}]}
//
// 接口与聚合逻辑：POST codebuddy.cn/v2/billing/meter/get-user-resource，聚合所有 package 的
// Cycle* 字段，TotalDosage 作 size 下限。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const billingBaseCN = "https://www.codebuddy.cn"

// billingBaseGlobal 国际版计费域。global 账号打 CN 域会得到 401：
// www.codebuddy.cn 不认 workbuddy.ai 的 token（实测 401，workbuddy.ai 同 token 为 code=0）。
const billingBaseGlobal = "https://www.workbuddy.ai"

// billingBaseFor 按账号 realm 选择计费域。
//
// 判定口径与 internal/auth.Realm() 一致：显式 realm=global 或 domain 落在
// workbuddy.ai 家族，都按国际版处理。cmd/credit 早先对所有账号硬编码 CN 域，
// 导致 global 账号余额查询恒返回 401（面板显示的是池内缓存值，不是实时的）。
func billingBaseFor(af *authFile) string {
	if strings.EqualFold(strings.TrimSpace(af.Auth.Realm), "global") {
		return billingBaseGlobal
	}
	d := strings.ToLower(strings.TrimSpace(af.Auth.Domain))
	if d == "workbuddy.ai" || strings.HasSuffix(d, ".workbuddy.ai") {
		return billingBaseGlobal
	}
	return billingBaseCN
}

type authFile struct {
	Auth struct {
		AccessToken string `json:"accessToken"`
		Domain      string `json:"domain"`
		Realm       string `json:"realm"`
	} `json:"auth"`
	Account struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	} `json:"account"`
}

// loadAuthFile 读单个 auth 文件。损坏/不可读时返回 broken=true，由调用方补一条
// Error 行——不许静默跳过（否则总行数对不上目录文件数，D-04）。
func loadAuthFile(path string) (af authFile, broken bool) {
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &af) != nil {
		return authFile{}, true
	}
	return af, false
}

type accountResult struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Remain   *int64 `json:"remain"`
	Used     *int64 `json:"used"`
	Size     *int64 `json:"size"`
	Packages int    `json:"packages,omitempty"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

type resourcePackage struct {
	CapacityRemain      int64 `json:"CapacityRemain"`
	CapacityUsed        int64 `json:"CapacityUsed"`
	CapacitySize        int64 `json:"CapacitySize"`
	CycleCapacityRemain int64 `json:"CycleCapacityRemain"`
	CycleCapacityUsed   int64 `json:"CycleCapacityUsed"`
	CycleCapacitySize   int64 `json:"CycleCapacitySize"`
}

// packageRemainUsed 与 billing.go:203-258 一致
func packageRemainUsed(a resourcePackage) (remain, used, size int64) {
	if a.CycleCapacitySize > 0 {
		remain = a.CycleCapacityRemain
		size = a.CycleCapacitySize
		if remain < 0 {
			remain = 0
		}
		if remain > size {
			remain = size
		}
		used = size - remain
		if a.CycleCapacityUsed > used {
			used = a.CycleCapacityUsed
			if size >= used {
				remain = size - used
			}
		}
		return remain, used, size
	}
	remain = a.CapacityRemain
	used = a.CapacityUsed
	size = a.CapacitySize
	if used == 0 && size > remain {
		used = size - remain
	}
	return remain, used, size
}

// fetchUserResource 按账号 realm 选定计费域后查询积分（main 的默认 fetch）。
func fetchUserResource(af *authFile) (remain, used, size int64, packs int, err error) {
	return fetchUserResourceAt(billingBaseFor(af), af, &http.Client{Timeout: 20 * time.Second})
}

// billingPathFor 按计费域选查询路径：global 域无 /v2 前缀（与
// internal/upstream 的 billingMeterPaths 同口径）。独立函数以便单测。
func billingPathFor(base string) string {
	if base == billingBaseGlobal {
		return "/billing/meter/get-user-resource"
	}
	return "/v2/billing/meter/get-user-resource"
}

// fetchUserResourceAt 向指定 base 域查询积分。base/HTTP 客户端参数化，供测试
// 用 httptest 注入假上游（真实域名 + 硬编码 client 无法单测成功路径）。
// 接口与聚合逻辑：POST {base}{path} billing/meter/get-user-resource，聚合所有
// package 的 Cycle* 字段，TotalDosage 作 size 下限。
func fetchUserResourceAt(base string, af *authFile, client *http.Client) (remain, used, size int64, packs int, err error) {
	now := time.Now()
	body, _ := json.Marshal(map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	path := billingPathFor(base)
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, 0, 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+af.Auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if af.Account.UID != "" {
		req.Header.Set("X-User-Id", af.Account.UID)
	}
	if af.Account.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", af.Account.EnterpriseID)
		req.Header.Set("X-Tenant-Id", af.Account.EnterpriseID)
	}
	if af.Auth.Domain != "" {
		req.Header.Set("X-Domain", af.Auth.Domain)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, 0, 0, 0, fmt.Errorf("http %d", resp.StatusCode)
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Response struct {
				Data struct {
					TotalDosage int64             `json:"TotalDosage"`
					Accounts    []resourcePackage `json:"Accounts"`
				} `json:"Data"`
			} `json:"Response"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return 0, 0, 0, 0, err
	}
	if env.Code != 0 {
		return 0, 0, 0, 0, fmt.Errorf("code=%d %s", env.Code, env.Msg)
	}
	for _, a := range env.Data.Response.Data.Accounts {
		r, u, s := packageRemainUsed(a)
		remain += r
		used += u
		size += s
	}
	packs = len(env.Data.Response.Data.Accounts)
	if size > 0 {
		if derived := size - remain; derived > used {
			used = derived
		}
	}
	if dosage := env.Data.Response.Data.TotalDosage; dosage > size {
		size = dosage
		if derived := size - remain; derived > used {
			used = derived
		}
	}
	return remain, used, size, packs, nil
}

// fetchFn 单账号积分查询的注入点（run 的第三个参数，测试传假实现）。
type fetchFn func(af *authFile) (remain, used, size int64, packs int, err error)

// run 执行 credit 命令主体（main 的委托对象）。args/authDir/fetch/stdout/stderr
// 全部参数化：main 传真实值，测试传临时目录 + 假 fetch + 缓冲捕获输出。
// 返回进程退出码（0 = 正常；1 = 枚举 auths 失败）。
func run(args []string, authDir string, fetch fetchFn, stdout, stderr io.Writer) int {
	pretty := len(args) > 1 && args[1] == "-pretty"
	// Glob 只在 pattern 非法时失败（如 authDir 含 '[' 等元字符）。静默吞掉会让命令
	// 报「0 个账号」并以 0 退出——把「配置写错」伪装成「确实没有账号」，脚本据此
	// 判断会把 0 积分当成事实（ERROR-HANDLING.md 判据④「影响计量」）。
	files, err := filepath.Glob(filepath.Join(authDir, "workbuddy-*.json"))
	if err != nil {
		fmt.Fprintln(stderr, "credit: 枚举 auths 失败:", err)
		return 1
	}
	sort.Strings(files)

	accounts := make([]accountResult, 0, len(files))
	for _, f := range files {
		af, broken := loadAuthFile(f)
		if broken {
			// 损坏/不可读文件不能静默跳过：否则输出条数与 auths 目录文件数对不上，
			// 把「配置写错」伪装成「这个账号不存在」（与 cmd/signin 的 LOAD_ERR 同口径）。
			accounts = append(accounts, accountResult{Error: "unreadable/corrupt: " + filepath.Base(f)})
			continue
		}
		res := accountResult{UID: af.Account.UID, Nickname: af.Account.Nickname}
		if af.Auth.AccessToken == "" {
			res.Error = "no accessToken"
			accounts = append(accounts, res)
			continue
		}
		remain, used, size, packs, err := fetch(&af)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Remain = &remain
			res.Used = &used
			res.Size = &size
			res.Packages = packs
			res.OK = true
		}
		accounts = append(accounts, res)
		time.Sleep(200 * time.Millisecond)
	}

	var totalRemain, totalUsed, totalSize int64
	okCount := 0
	for _, a := range accounts {
		if a.OK {
			okCount++
			if a.Remain != nil {
				totalRemain += *a.Remain
			}
			if a.Used != nil {
				totalUsed += *a.Used
			}
			if a.Size != nil {
				totalSize += *a.Size
			}
		}
	}
	out := map[string]any{
		"service": "workbuddy",
		"ts":      time.Now().Unix(),
		"total": map[string]any{
			"remain":   totalRemain,
			"used":     totalUsed,
			"size":     totalSize,
			"accounts": len(accounts),
			"ok":       okCount,
			"failed":   len(accounts) - okCount,
		},
		"accounts": accounts,
	}
	if pretty {
		printPretty(stdout, accounts, totalRemain, totalUsed, totalSize, okCount)
		return 0
	}
	raw, _ := json.Marshal(out)
	fmt.Fprintln(stdout, string(raw))
	return 0
}

func main() {
	authDir := "./auths"
	if v := os.Getenv("WB2A_AUTH_DIR"); v != "" {
		authDir = v
	}
	code := run(os.Args, authDir, fetchUserResource, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

// printPretty 人类可读日报：四行汇总，无账号明细。
func printPretty(w io.Writer, accounts []accountResult, totalRemain, totalUsed, totalSize int64, okCount int) {
	withBalance := 0
	var failed []string
	for _, a := range accounts {
		if a.OK && a.Remain != nil && *a.Remain > 0 {
			withBalance++
		}
		if !a.OK {
			name := a.Nickname
			if name == "" && len(a.UID) >= 8 {
				name = a.UID[:8]
			}
			failed = append(failed, name+" "+a.Error)
		}
	}
	pct := int64(0)
	if totalSize > 0 {
		pct = totalRemain * 100 / totalSize
	}
	fmt.Fprintf(w, "📊 WorkBuddy 积分日报\n")
	fmt.Fprintf(w, "账号: %d/%d\n", withBalance, len(accounts))
	fmt.Fprintf(w, "总计: %d/%d\n", totalRemain, totalSize)
	fmt.Fprintf(w, "剩余: %d%%\n", pct)
	for _, f := range failed {
		fmt.Fprintf(w, "⚠️ %s\n", f)
	}
}
