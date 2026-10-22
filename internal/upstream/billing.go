// billing.go 积分/账单与签到：CreditPackages、UserResource*、DailyCheckin。
// 从 client.go 机械拆出（2026-10-22 R-201），零行为变化。
package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/logfmt"
)

// UserResource 查询账号积分余额与总额度（所有套餐聚合）。remain 负值钳 0；
// total 取与 remain 同源的额度字段（CycleCapacitySize 优先，无周期额度退
// CapacitySize），上游缺 size 的套餐按 remain 兜底，保证百分比不超 100%。
// CreditPackage 单个积分包的构成明细（面板「积分构成」用）。
//
// 两个账号即使任务完成度完全一致，余额也可能相差上千——差别藏在包的**面额与
// 来源**里（「国内运营裂变包」「拉新权益包」按次发放，面额 6~1500 不等）。
// 只看聚合值看不出这件事，所以把逐包明细暴露出来。
type CreditPackage struct {
	Name   string `json:"name"`
	Remain int64  `json:"remain"`
	Used   int64  `json:"used"`
	Size   int64  `json:"size"`
	// EndTime 该包的失效时刻：优先 DeductionEndTime（可抵扣窗口结束，真「用不完
	// 就没了」），缺失依次回落 ExpiredTime / PackageEndTime / CycleEndTime（周期
	// 边界，仅兜底）。RFC3339 或上游墙钟字符串，前端取日期部分展示。
	EndTime string `json:"end_time,omitempty"`
	// ExpiresAt 与 EndTime 同源的 Unix 毫秒时间戳，供面板按精确剩余天数聚合。
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// CreatedAt 发放时刻，RFC3339。**这是区分「首登赠送」与「活动奖励」的唯一依据**：
	// 两类包的 PackageName 与 PackageCode 完全相同（例如都是「国内运营裂变包」+
	// TCACA_code_007_*），只看名字无法区分，只有时间能说明它是不是账号首次授权那刻发的。
	CreatedAt string `json:"created_at,omitempty"`
	// PackageCode / SubProductCode 上游的包类型标识。同 Name 不同 Code 的包可能
	// 是不同来源；同 Code 不同面额则是同来源分批发放（首登 1500 与活动 300 即如此）。
	PackageCode    string `json:"package_code,omitempty"`
	SubProductCode string `json:"sub_product_code,omitempty"`
	SubProductName string `json:"sub_product_name,omitempty"`
	// Cycle 为 true 表示按周期发放的包（读 Cycle* 字段），否则读 Capacity*。
	Cycle bool `json:"cycle,omitempty"`
}

// CreditPackages 返回账号当前的逐包构成。remain/size 为各包求和。
//
// 字段选择与 UserResourceDetailed 的聚合口径一致：CycleCapacitySize > 0 时按
// 周期字段算，否则按 Capacity 字段算——两条路径不能混，否则同一个包会被算两次。
func (c *Client) CreditPackages(a *auth.Auth) ([]CreditPackage, int64, int64, error) {
	now := time.Now()
	body := map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format(packageEndLayout),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format(packageEndLayout),
	}
	data, err := c.billingMeterJSON(a, c.billingMeterPaths(a), http.MethodPost, body)
	if err != nil {
		return nil, 0, 0, err
	}
	// 注意层级：doJSON 已经解过 apiEnvelope 并返回 env.Data，所以这里从
	// Response 开始解析——**不能**再套一层 Code/Data，否则 Accounts 恒为空，
	// 表现为「每个号都 0 个包」（实测踩过）。
	var resp struct {
		Response struct {
			Data struct {
				Accounts []struct {
					PackageName         string `json:"PackageName"`
					CapacityRemain      int64  `json:"CapacityRemain"`
					CapacityUsed        int64  `json:"CapacityUsed"`
					CapacitySize        int64  `json:"CapacitySize"`
					CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
					CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
					CycleCapacitySize   int64  `json:"CycleCapacitySize"`
					// 到期时间字段名在上游存在三种口径：ExpiredTime / PackageEndTime
					// 在 CN/global 实测字段全集里均恒 miss（见 UserResourceDetailed
					// 处注释），真实下发的是 CycleEndTime——三者都读，谁有值用谁。
					ExpiredTime    string `json:"ExpiredTime"`
					PackageEndTime string `json:"PackageEndTime"`
					CycleEndTime   string `json:"CycleEndTime"`
					// DeductionEndTime 可抵扣窗口结束（epoch 毫秒）——「这个包什么时候
					// 不能再花」的真失效时刻。CycleEndTime 是周期边界（额度重置点），
					// 两者语义不同：判「用不完就没了」以本字段为准，CycleEndTime 兜底
					//（OkRoromori 分支实测结论：请求参数叫 PackageEndTimeRange*，但
					// 响应里 ExpiredTime 恒空，真正的失效时刻只有这里下发）。
					DeductionEndTime int64 `json:"DeductionEndTime"`
					// 发放时刻（epoch 毫秒）。
					CreateTime     int64  `json:"CreateTime"`
					PackageCode    string `json:"PackageCode"`
					SubProductCode string `json:"SubProductCode"`
					SubProductName string `json:"SubProductName"`
				} `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, 0, 0, fmt.Errorf("packages parse: %w", err)
	}
	packs := resp.Response.Data.Accounts
	out := make([]CreditPackage, 0, len(packs))
	var sumRemain, sumSize int64
	for _, p := range packs {
		cp := CreditPackage{
			Name:           p.PackageName,
			PackageCode:    p.PackageCode,
			SubProductCode: p.SubProductCode,
			SubProductName: p.SubProductName,
		}
		switch {
		case p.DeductionEndTime > 0:
			// 真失效时刻（可抵扣窗口结束），语义见上方字段注释：判「用不完就没了」
			// 用它而不是周期边界。epoch 毫秒 → RFC3339，与 CycleEndTime 字符串口径
			// 共存（前端统一 slice(0,10) 取日期）。ExpiresAt 直接用原始毫秒——
			// RFC3339 不是 packageEndLayout 形态，交给下方解析会静默失败得 0。
			cp.EndTime = time.UnixMilli(p.DeductionEndTime).Format(time.RFC3339)
			cp.ExpiresAt = p.DeductionEndTime
		case p.ExpiredTime != "":
			cp.EndTime = p.ExpiredTime
		case p.PackageEndTime != "":
			cp.EndTime = p.PackageEndTime
		default:
			cp.EndTime = p.CycleEndTime
		}
		if cp.ExpiresAt == 0 && cp.EndTime != "" {
			if end, perr := time.ParseInLocation(packageEndLayout, cp.EndTime, softRateResetLoc); perr == nil {
				cp.ExpiresAt = end.UnixMilli()
			}
		}
		// CreateTime 是 epoch 毫秒；0 表示上游没给，留空而不是伪造 1970。
		if p.CreateTime > 0 {
			cp.CreatedAt = time.UnixMilli(p.CreateTime).Format(time.RFC3339)
		}
		if p.CycleCapacitySize > 0 {
			cp.Cycle = true
			cp.Remain, cp.Size = p.CycleCapacityRemain, p.CycleCapacitySize
			cp.Used = cp.Size - cp.Remain
			if p.CycleCapacityUsed > cp.Used {
				cp.Used = p.CycleCapacityUsed
				cp.Remain = cp.Size - cp.Used
			}
			if cp.Remain < 0 {
				cp.Remain = 0
			}
		} else {
			cp.Remain, cp.Used, cp.Size = p.CapacityRemain, p.CapacityUsed, p.CapacitySize
			if cp.Used == 0 && cp.Size > cp.Remain {
				cp.Used = cp.Size - cp.Remain
			}
		}
		sumRemain += cp.Remain
		sumSize += cp.Size
		out = append(out, cp)
	}
	// 面额降序：大包一眼可见，正是差异最可能出现的地方。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out, sumRemain, sumSize, nil
}

func (c *Client) UserResource(a *auth.Auth) (remain, total int64, err error) {
	remain, total, _, err = c.UserResourceDetailed(a, 0)
	return remain, total, err
}

// packageEndLayout 上游套餐到期时间的墙钟格式（UTC+8，与 softRateResetLoc 同口径）。
const packageEndLayout = "2006-01-02 15:04:05"

// parsePackageEndTime 统一解析上游套餐到期时间。空值、格式异常返回 false，
// 调用方据此保守地不把该包计入最早到期路由。
func parsePackageEndTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(packageEndLayout, raw, softRateResetLoc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// UserResourceDetailed 在 UserResource 基础上额外返回「快过期」积分子集：
// soon > 0 且套餐 CycleEndTime 解析成功且到期时刻 ≤ now+soon 的余额计入 expiring
// （pool 据此优先消耗，避免官方活动赠送的奖励积分到期作废）；soon ≤ 0 时 expiring
// 恒 0（禁用分桶，行为与引入前一致）。expiring 是 remain 的一部分。
//
// 到期时间判据是 CycleEndTime（上游实测：CN/global 两域字段全集均无 PackageEndTime，
// 旧判据恒 miss 致 expiring 恒 0；CycleEndTime 是上游真实下发的到期时刻——
// global Bonus Pack 14 天赠送积分的到期时间即此字段）。解析失败/缺失的套餐保守
// 不计入 expiring（不误标为快过期而插队）。
// 单套餐取数统一调 packageRemainUsed（与 CreditPackages 同一事实来源，含 remain
// 钳 [0,size] 与 used 修正；消除双份逻辑漂移——旧中间 switch 只钳负值，上游脏数据
// CycleRemain>Size 时会高估）。
func (c *Client) UserResourceDetailed(a *auth.Auth, soon time.Duration) (remain, total, expiring int64, err error) {
	remain, total, expiring, _, _, err = c.UserResourceDetailedWithExpiry(a, soon)
	return remain, total, expiring, err
}

// enterpriseUnlimitedRemain / enterpriseUnlimitedTotal 企业版「不限量」在池内的等价表示
// （上游 limitNum == -1；桌面端同判为 unlimited）。
//
// 池内没有"不限"状态位，而 credits 同时参与两处判定——路由权重（credits/maxCredits）与
// credit_floor（余额低于阈值则不接收费模型）——不限量必须两处都"不构成限制"，故剩余量
// 取一个足够大的值；total 记 -1 供面板显示「不限」。total 仅用于展示与快照，
// 不参与任何判定（已核对全仓库引用面）。
const (
	enterpriseUnlimitedRemain int64 = 1 << 40
	enterpriseUnlimitedTotal  int64 = -1
)

// enterpriseResource 查询**企业版账号**的分配额度，并归一到个人口径的
// (remain, total, expiring, earliestAt, earliestRemaining)。
//
// 为什么必须分流：企业额度不在「个人资源包」体系内——/billing/meter/get-user-resource*
// 对 enterpriseId 非空的账号恒返回空 Accounts（实测 code 0 且 Accounts null），
// 面板因此长期显示 0/未知（额度其实是有的）。
//
// 上游口径（实测 2026-10-07，企业号 a6239ec8）：
//
//	POST /v2/billing/meter/get-enterprise-user-usage   （X-Enterprise-Id 由 BillingHeaders 注入）
//	{"credit":812.45,"limitNum":2000,
//	 "cycleStartTime":"2026-09-27 00:00:00","cycleEndTime":"2026-10-26 23:59:59",
//	 "cycleResetTime":"2026-10-27 00:00:00"}
//
// credit = **本周期已用**（与个人口径 CapacityRemain「剩余」语义相反），
// limitNum = **分配给本账号的额度**。故 remain = limitNum - credit、total = limitNum。
// 注意：企业**池**总额度是另一回事，成员账号无权查询（实测所有池端点 403 not_authorized），
// 本函数只反映该账号被分配的额度。
//
// 分桶：企业配额按周期重置、未用完即作废，与个人「奖励积分到期作废」同性质，故把
// cycleEndTime 作为唯一到期批次——周期末企业号会被 prefer_expiring 优先选中（期望行为）。
// limitNum < 0（不限量）无作废语义，不参与分桶。
func (c *Client) enterpriseResource(a *auth.Auth, soon time.Duration) (remain, total, expiring int64, earliestAt time.Time, earliestRemaining int64, err error) {
	// 与个人口径一致：余额是签到后的紧邻调用，偶发 500 值得有界重试。
	var data json.RawMessage
	err = c.retryBillingTransient(func() error {
		var e error
		data, e = c.billingJSON(a, http.MethodPost, "/v2/billing/meter/get-enterprise-user-usage", map[string]any{})
		return e
	})
	if err != nil {
		return 0, 0, 0, time.Time{}, 0, err
	}
	// 兼容上游两套字段命名（桌面端两条解析路径分别读 camelCase 与 snake_case）。
	var resp struct {
		Credit         float64 `json:"credit"`
		LimitNum       int64   `json:"limitNum"`
		LimitNumSnake  int64   `json:"limit_num"`
		UsedNum        float64 `json:"used_num"`
		CycleEndTime   string  `json:"cycleEndTime"`
		CycleResetTime string  `json:"cycleResetTime"`
	}
	if uerr := json.Unmarshal(data, &resp); uerr != nil {
		return 0, 0, 0, time.Time{}, 0, fmt.Errorf("enterprise resource parse: %w", uerr)
	}
	limit := resp.LimitNum
	if limit == 0 && resp.LimitNumSnake != 0 {
		limit = resp.LimitNumSnake
	}
	used := resp.Credit
	if used == 0 && resp.UsedNum != 0 {
		used = resp.UsedNum
	}
	// 不限量：池内无对应状态位，用大剩余量让路由权重与 credit_floor 都不构成限制。
	if limit < 0 {
		return enterpriseUnlimitedRemain, enterpriseUnlimitedTotal, 0, time.Time{}, 0, nil
	}
	// credit 是浮点（如 812.45），池内 credits 是整数：四舍五入到最近整数
	// （向下取整会低报剩余额度，与"额度还有多少"的展示意图相悖）。
	usedInt := int64(used)
	if used-float64(usedInt) >= 0.5 {
		usedInt++
	}
	remain = limit - usedInt
	if remain < 0 {
		remain = 0
	}
	total = limit
	// 周期到期批次：cycleEndTime 优先，缺失回落 cycleResetTime。
	now := time.Now()
	end, ok := parsePackageEndTime(resp.CycleEndTime)
	if !ok {
		end, ok = parsePackageEndTime(resp.CycleResetTime)
	}
	if ok && remain > 0 && end.After(now) {
		earliestAt, earliestRemaining = end, remain
		if soon > 0 && !end.After(now.Add(soon)) {
			expiring = remain
		}
	}
	return remain, total, expiring, earliestAt, earliestRemaining, nil
}

// UserResourceDetailedWithExpiry 在 UserResourceDetailed 基础上返回最早未来到期批次：
// earliestAt 是最早的可用到期时刻，earliestRemaining 是同一时刻所有正余额包的剩余量之和。
// 已过期、剩余为 0、缺少或无法解析到期时间的包都不会成为最早批次；无有效批次时返回零值。
func (c *Client) UserResourceDetailedWithExpiry(a *auth.Auth, soon time.Duration) (remain, total, expiring int64, earliestAt time.Time, earliestRemaining int64, err error) {
	// 企业版额度不在「个人资源包」体系内：get-user-resource* 对 enterpriseId 非空账号
	// 恒返回空 Accounts（实测 code 0 且 Accounts null），故分流到企业口径端点。
	if a.IsEnterprise() {
		return c.enterpriseResource(a, soon)
	}
	now := time.Now()
	body := map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format(packageEndLayout),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format(packageEndLayout),
	}
	// 余额查询同样做瞬时错误有界重试（签到后紧接着的 user-resource 偶发 500 会让
	// 该账号错过本次解冻/到期快照更新，只能等下一个刷新周期）。
	var data json.RawMessage
	err = c.retryBillingTransient(func() error {
		var e error
		data, e = c.billingMeterJSON(a, c.billingMeterPaths(a), http.MethodPost, body)
		return e
	})
	if err != nil {
		return 0, 0, 0, time.Time{}, 0, err
	}
	var resp struct {
		Response struct {
			Data struct {
				Accounts []struct {
					PackageName         string `json:"PackageName"`
					CycleEndTime        string `json:"CycleEndTime"` // "2006-01-02 15:04:05"，缺省/空 = 无到期
					CapacitySize        int64  `json:"CapacitySize"`
					CapacityRemain      int64  `json:"CapacityRemain"`
					CapacityUsed        int64  `json:"CapacityUsed"`
					CycleCapacitySize   int64  `json:"CycleCapacitySize"`
					CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
					CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
				} `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, 0, 0, time.Time{}, 0, fmt.Errorf("resource parse: %w", err)
	}
	for _, acct := range resp.Response.Data.Accounts {
		r, _, size := packageRemainUsed(respAccount{
			CapacityRemain:      acct.CapacityRemain,
			CapacityUsed:        acct.CapacityUsed,
			CapacitySize:        acct.CapacitySize,
			CycleCapacityRemain: acct.CycleCapacityRemain,
			CycleCapacityUsed:   acct.CycleCapacityUsed,
			CycleCapacitySize:   acct.CycleCapacitySize,
		})
		if r < 0 {
			r = 0
		}
		if size < r {
			size = r
		}
		remain += r
		total += size
		if r <= 0 {
			continue
		}
		end, ok := parsePackageEndTime(acct.CycleEndTime)
		if !ok || !end.After(now) {
			continue
		}
		if earliestAt.IsZero() || end.Before(earliestAt) {
			earliestAt = end
			earliestRemaining = r
		} else if end.Equal(earliestAt) {
			earliestRemaining += r
		}
		// 分桶：仅 soon>0 且确实在窗口内 → expiring。
		if soon > 0 && !end.After(now.Add(soon)) {
			expiring += r
		}
	}
	return remain, total, expiring, earliestAt, earliestRemaining, nil
}

// respAccount 供 packageRemainUsed 解析的套餐字段（CreditPackages 的逐包结构同构）。
type respAccount struct {
	CapacityRemain      int64
	CapacityUsed        int64
	CapacitySize        int64
	CycleCapacityRemain int64
	CycleCapacityUsed   int64
	CycleCapacitySize   int64
}

// packageRemainUsed 聚合单套餐的 remain/used/size（与 CreditPackages/cmd/credit 的
// 历史口径一致，收敛至此作为单一事实来源）。Cycle 期套餐优先：用 CycleCapacity
// 三字段，used 取 CycleUsed 与 size-remain 的较大者；否则回退 Capacity 三字段。
func packageRemainUsed(a respAccount) (remain, used, size int64) {
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

// DailyCheckin 执行每日签到。已签到（业务 code 非 0）也返回错误，调用方按 msg 区分。
// 偶发上游 5xx（code 10000）做有界重试（见 retryBillingTransient）——单次抖动不再
// 让该账号整天漏签；「已签到」等业务错误不重试。
func (c *Client) DailyCheckin(a *auth.Auth) error {
	return c.retryBillingTransient(func() error {
		_, err := c.billingMeterJSON(a, c.checkinMeterPaths(a), http.MethodPost, map[string]any{})
		return err
	})
}

// IsAlreadyCheckin 报告 err 是否表示"今天已签到"（上游幂等拒绝重复签到）。
// 只认带分类的 *Error（业务 code 或 HTTP 错误）：网络层/解析层错误不得当作幂等成功，
// 否则停机补签遇到抖动会误记为 already，账号当天实际未签到却被判定正常。
func IsAlreadyCheckin(err error) bool {
	var ue *Error
	if !errors.As(err, &ue) {
		return false
	}
	for _, m := range alreadyCheckinMarkers {
		if strings.Contains(ue.Msg, m) || strings.Contains(strings.ToLower(ue.Msg), strings.ToLower(m)) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	return logfmt.Truncate(s, n)
}
