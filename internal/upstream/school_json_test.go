package upstream

// schoolJSON 的 R10 钉子：marshal 失败必须上报，不得静默发空 body。
// 旧实现 `raw, _ = json.Marshal(body)` 把「参数不可序列化」伪装成上游拒绝——
// 排障时全查错方向；同文件 ReportMPEvent 早就做了检查，schoolJSON 是唯一漏检的。

import (
	"net/http"
	"testing"

	"yg-gateway/internal/auth"
)

func TestSchoolJSONMarshalFailureIsNotSilent(t *testing.T) {
	hit := false
	c := &Client{
		HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			hit = true
			return jsonResp(200, `{"code":0}`), nil
		})},
	}
	// chan 是 json.Marshal 明确不支持的类型 → 必然 marshal 失败。
	err := c.schoolJSON(&auth.Auth{UID: "u1"}, http.MethodPost, "/tasks/share-complete",
		map[string]any{"bad": make(chan int)}, nil)
	if err == nil {
		t.Fatal("marshal 失败必须返回 error（R10：不得静默发空 body）")
	}
	if hit {
		t.Fatal("marshal 失败后不应发出任何请求（空 body 会把锅甩给上游）")
	}
}

func TestSchoolJSONMarshalOKStillSends(t *testing.T) {
	// 对照：正常 body 仍照旧发出（修复不得把路堵死）。
	var gotPath, gotBody string
	c := &Client{
		BillingBaseCN: "https://fake.example",
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			if r.Body != nil {
				buf := make([]byte, 256)
				n, _ := r.Body.Read(buf)
				gotBody = string(buf[:n])
			}
			return jsonResp(200, `{"code":0,"data":{}}`), nil
		})},
	}
	if err := c.schoolJSON(&auth.Auth{UID: "u1"}, http.MethodPost, "/tasks/share-complete",
		map[string]any{"channel": "wechat"}, nil); err != nil {
		t.Fatalf("正常 body 应成功: %v", err)
	}
	if gotPath != "/portal/activity/school/tasks/share-complete" {
		t.Errorf("path=%q", gotPath)
	}
	if gotBody != `{"channel":"wechat"}` {
		t.Errorf("body=%q want 原样序列化", gotBody)
	}
}
