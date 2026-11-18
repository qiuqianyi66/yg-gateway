package server

import "testing"

// TestFetchGlobalModelsEmptyPoolMakesNoUpstreamCall 守 fetchGlobalModels 的核心契约：
// 池中无 global 账号时返回 (nil, nil) 且**零上游调用**。
//
// 为什么这条值得单独钉住：`global/xxx` 这类模型只有 global 号能服务。若某个部署
// 一个 global 号都没有（纯 CN 池），每次 /v1/models 刷新都会走进这个函数。
// 若这里漏判空池直接去打上游，就是**每次客户端刷新模型列表都白打一次上游**，
// 结果必然为空 —— 纯浪费。
//
// 断言用 fake upstream 的调用计数做反证：必须是 0 次。
func TestFetchGlobalModelsEmptyPoolMakesNoUpstreamCall(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 200, `{"data":[]}`, false
	})
	// 空池（只有 CN 号或完全没有号）都不该触发 global 探测。
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: up})

	names, acct := h.fetchGlobalModels()

	if names != nil {
		t.Errorf("空池应返回 nil 名单（未探测），实际 %v (len=%d)", names, len(names))
	}
	if acct != nil {
		t.Errorf("空池应返回 nil 账号，实际 %+v", acct)
	}
	if calls != 0 {
		t.Errorf("池中无 global 账号时不许打上游，实际调用 %d 次", calls)
	}
}
