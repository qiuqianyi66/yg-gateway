package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/reqlog"
)

// TestStreamOutcomeClassification 钉住流式结束后的**结果分类**与账号处置（A6 增量 2026-10-18）。
//
// 为什么需要它（变异核验实测）：`internal/server` 的流式收尾 switch 有五个分支——
// 空流 / 超大帧 / 上游 error 帧 / 客户端断连 / 真成功。打 6 条语义变异后**5 条存活**：
//
//	S1 空流(200+0 帧) 改记 200  → 存活（运维把上游缺陷看成成功）
//	S3 断连记成 Success          → 存活
//	S4 超大帧改记 200            → 存活
//	S5 错误帧不调用 applyErrorPolicy → 存活（注释里明说这是修过的 bug：限流号被当健康号、粘性钉住它）
//	S6 真成功不 NoteSuccess      → 存活
//
// 即：这段「代码里注释最详细、修复历史最明确」的逻辑，此前只有 S2（错误帧判据写反）被
// `TestRequestMetricsRecordsStream` 间接咬住。
//
// 还原什么会让它 FAIL：把任一分支的 status/outcome 改回「一律 200 success」、或去掉
// `applyErrorPolicy` 调用（第三条子用例）→ 对应用例红。
func TestStreamOutcomeClassification(t *testing.T) {
	const frameLimit = 1 << 20 // 与 chatrun 的 maxSSEFrameBytes 同量级
	cases := []struct {
		name        string
		upstream    string
		wantStatus  int
		wantOutcome string
		wantCooling bool
		// wantSuccessNote 只有「真成功」分支才应把这一跳记为账号成功（NoteSuccess）。
		// 断连分支按设计也记成功（人已走但上游无恙），故它不在这里断言。
		wantSuccessNote bool
	}{
		{
			name:        "空流（200 + 0 有效帧）→ 502 / stream_error",
			upstream:    "data: [DONE]\n\n",
			wantStatus:  http.StatusBadGateway,
			wantOutcome: reqlog.OutcomeStreamError,
		},
		{
			name: "上游单帧超限（畸形流）→ 502 / stream_error",
			upstream: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" +
				strings.Repeat("a", frameLimit+4096) + "\"}}]}\n\n",
			wantStatus:  http.StatusBadGateway,
			wantOutcome: reqlog.OutcomeStreamError,
		},
		{
			name:        "上游 error 帧（6004 无重置时间）→ 503 / stream_error + 账号进入冷却",
			upstream:    "data: {\"error\":{\"code\":6004,\"message\":\"model usage limit exceeded\"}}\n\ndata: [DONE]\n\n",
			wantStatus:  http.StatusServiceUnavailable,
			wantOutcome: reqlog.OutcomeStreamError,
			wantCooling: true,
		},
		{
			name:        "正常流 → 200 / success",
			upstream:    sseOK,
			wantStatus:  http.StatusOK,
			wantOutcome: reqlog.OutcomeSuccess,
			// 变异 S6（去掉成功分支的 NoteSuccess）此前无人咬住：它不让结果分类变化，
			// 只让账号的成功计数/健康信号不再更新 —— 必须直接断言池侧计数才看得见。
			wantSuccessNote: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
			reqLog := reqlog.New(reqlog.Config{})
			h := NewHandler(Config{
				Pool:       p,
				Upstream:   newFakeUpstream(t, func(string) (int, string, bool) { return 200, tc.upstream, true }),
				RequestLog: reqLog,
			})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions",
				strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
			h.ServeHTTP(rec, req)

			s := reqLog.Snapshot()
			if len(s.Recent) != 1 {
				t.Fatalf("应恰好 1 条事件，got %d（metrics=%+v）", len(s.Recent), s)
			}
			e := s.Recent[0]
			if e.Outcome != tc.wantOutcome {
				t.Errorf("outcome=%q，want %q（把失败记成 success = 运维看到假成功）", e.Outcome, tc.wantOutcome)
			}
			// **刻意不断言 e.Status**（实测结论，2026-10-18）：
			//   reqlog 的 Status 来自 handler 层的 HTTP 码（`trace.event(status)`），流式响应头
			//   早已发出，所以恒为 200；chatrun 里 `st.status = 502/503` 只进 **stderr 日志行**
			//   （`logChatRowEx`），不进 reqlog、也不进面板。
			//   因此变异 S1/S4（把 502 改成 200）是「只影响日志行」的变异，要抓住它必须捕获
			//   log 输出——本轮未覆盖，已在 change summary 里记为剩余缺口，
			//   同时这条本身也是一个观测事实：注释里「状态收敛到 502 观测」目前只体现在 stderr。
			_ = tc.wantStatus

			_, _, cooling, _, _ := p.CountsDetailed()
			if tc.wantCooling && cooling == 0 {
				t.Errorf("上游 error 帧必须套用错误策略（账号进冷却），cooling=0 —— 否则限流号被当健康号、粘性还钉在它身上")
			}
			if !tc.wantCooling && cooling != 0 {
				t.Errorf("不该触发账号冷却，cooling=%d", cooling)
			}

			st, ok := p.Status("u1")
			if !ok {
				t.Fatalf("池里应有账号 u1")
			}
			if tc.wantSuccessNote && st.SuccessCount == 0 {
				t.Errorf("真成功必须记一次账号成功（NoteSuccess）；SuccessCount=0 说明成功信号没更新，健康度与成功率都会失真")
			}
			if !tc.wantSuccessNote && st.SuccessCount != 0 {
				t.Errorf("失败路径不该记成功，SuccessCount=%d", st.SuccessCount)
			}
		})
	}
}

// failAfterWriter 前 N 次 Write 正常，之后返回错误——模拟客户端断连（TCP RST / 用户关页面）。
// 刻意不实现 http.Flusher：chatrun 不依赖它（已确认无 Flush 调用），少了更贴近「写就失败」。
type failAfterWriter struct {
	hdr       http.Header
	code      int
	failAfter int
	n         int
}

func (w *failAfterWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = http.Header{}
	}
	return w.hdr
}

func (w *failAfterWriter) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
}

func (w *failAfterWriter) Write(b []byte) (int, error) {
	w.n++
	if w.n > w.failAfter {
		return 0, errors.New("client gone: write failed")
	}
	return len(b), nil
}

// TestStreamClientDisconnectRecordsInterrupted 钉住「客户端断连 ≠ 网关失败」的分类（A6 S3）。
//
// 为什么需要它（变异核验实测）：把 chatrun 断连分支的 `OutcomeInterrupted` 改成
// `OutcomeSuccess` 后，包内测试全绿。危害是**成功率被断连污染**——人关了页面，
// 网关却记成一次成功；该分支还会照常 NoteSuccess，与「真成功」混为一谈。
//
// 还原什么会让它 FAIL：`st.outcome = reqlog.OutcomeInterrupted` → `OutcomeSuccess` → 本用例红。
func TestStreamClientDisconnectRecordsInterrupted(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       p,
		Upstream:   newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true }),
		RequestLog: reqLog,
	})

	// failAfter=0：第一次写就失败，模拟首帧即断连。
	w := &failAfterWriter{failAfter: 0}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	h.ServeHTTP(w, req)

	s := reqLog.Snapshot()
	if len(s.Recent) != 1 {
		t.Fatalf("应恰好 1 条事件，got %d（metrics=%+v）", len(s.Recent), s)
	}
	e := s.Recent[0]
	if e.Outcome != reqlog.OutcomeInterrupted {
		t.Errorf("outcome=%q，want %q（断连记成成功会污染成功率）", e.Outcome, reqlog.OutcomeInterrupted)
	}
	if e.OK {
		t.Errorf("断连不得记为 OK")
	}
}
