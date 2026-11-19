// body_shape.go 入口层的请求体形状判定。
//
// 只做**一件**事：在读到 body 之后、进入任何解析/改写/选号之前，把「顶层不是
// JSON 对象」的请求体就地 400。不是校验器，不做完整 JSON 解析。
package server

import "net/http"

// bodyIsJSONObject 报告 body 的顶层值是否以 JSON 对象开头（首个非空白字节为 `{`）。
//
// 为什么只看首字节：需要的是 O(1) 形状判定，不是完整校验。以 `{` 开头的畸形
// JSON 仍会在后续 unmarshal 处按原语义报错，误判成本为零；反过来，`null` / `[]` /
// `"x"` / `7` / 空 body 一定不是对象，后面跟什么都不改变这个结论。
//
// 特别说明 `null`：`json.Unmarshal` 到 map/struct 时把零值置空且**不返回错误**
// （`err == nil`、目标是 nil map）。所以「解一次看有没有报错」这种判法在这里是
// 盲的——上游 prepareBody 正是踩这个坑对 nil map 赋值 panic 的（2026-11-19）。
func bodyIsJSONObject(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// rejectIfNotJSONObject 在入口处拦下非 JSON 对象的请求体，返回 true 表示已写出
// 响应、调用方应立即 return。
//
// 依据（行为变更，见 docs/prd/REQ-entry-body-shape-2026-11-19.md）：
//   - `null` / `[]` / `"x"` / `7` 此前一路透传到上游才被 11101 拒：白占一个账号
//     名额、白跑一次往返，且把「我们本来就不接受的 body」记成上游的错；
//   - `countTokens` 对 `null` 会解成零值结构体，静默返回 0 token，看起来像成功。
//
// 错误码取 invalid_request（与 read body / 畸形 JSON 同族），不冒充上游 bad_params。
func rejectIfNotJSONObject(w http.ResponseWriter, body []byte) bool {
	if bodyIsJSONObject(body) {
		return false
	}
	writeOpenAIError(w, http.StatusBadRequest, "invalid_request",
		"request body must be a JSON object")
	return true
}
