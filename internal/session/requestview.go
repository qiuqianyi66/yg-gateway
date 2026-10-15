// requestview.go 请求体的单次解析视图（热路径去重解析，F-701）。
//
// 背景：一个 /v1/chat/completions 请求体在热路径上此前被**全文档** json.Unmarshal
// 六次以上（peek(model/stream) / 日志取 model / 工具名单 / 会话键 / 轮级键 /
// conversationId / 是否带图），大上下文请求下这是纯 CPU 与 GC 支出。
//
// 方案：顶层只解析一次成「字段名 → 原始字节」的视图，子树（messages / tools /
// metadata）**按需**解析——用到才解析，不用不付代价。解析失败（body 不是 JSON
// 对象）时视图退化为全空，与旧实现「各消费方各自解析失败」的语义逐位一致。
//
// 为什么顶层保留 RawMessage 而不是 typed struct：typed struct 在**任一字段类型
// 不符**时整体解析失败（如 `{"user_id":123}` 会让整个 body 解析失败，连 model
// 都拿不到），而旧实现是逐字段 strOrEmpty 容错。RawMessage 永不因字段类型
// 失败，逐字段取用时才判定，语义与旧实现一致（见派生开关测试 ids_test）。
package session

import "encoding/json"

// RequestView 一个请求体的惰性解析视图。零值可用（全空 = 解析失败语义）。
//
// 并发：视图只在单个请求的处理 goroutine 内流转，无需加锁；但子树解析结果做了
// 「解析一次即缓存」处理，重复取用不重复解析。
type RequestView struct {
	top        map[string]json.RawMessage
	meta       map[string]json.RawMessage
	metaLoaded bool
	msgs       []json.RawMessage
	msgsLoaded bool
	tools      []json.RawMessage
	toolsLd    bool
	funcs      []json.RawMessage
	funcsLd    bool
}

// ParseRequestView 解析请求体。ok=false 表示 body 不是 JSON 对象（此时返回零值
// 视图，各消费方按既有空值语义回落）。
func ParseRequestView(body []byte) (v RequestView, ok bool) {
	if len(body) == 0 {
		return RequestView{}, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return RequestView{}, false
	}
	return RequestView{top: top}, true
}

// Raw 取顶层字段的原始字节（缺失返回 nil）。
func (v *RequestView) Raw(key string) json.RawMessage {
	if v == nil || v.top == nil {
		return nil
	}
	return v.top[key]
}

// Str 取顶层字符串字段；缺失 / 非字符串 / 空串一律返回 ""（对齐 strOrEmpty 口径）。
func (v *RequestView) Str(key string) string {
	return rawString(v.Raw(key))
}

// Model 返回 model 字段的原始字节（供类型四态判定：missing/null/string/other）。
func (v *RequestView) Model() json.RawMessage { return v.Raw("model") }

// ModelStr 返回 model 的字符串值（非字符串返回 ""）。
func (v *RequestView) ModelStr() string { return rawString(v.Raw("model")) }

// Stream 报告 stream 字段是否为 JSON true。
//
// 与旧实现（json.Unmarshal 到 bool）的等价性：旧实现只在值确实是 true 时置 true，
// 其他形态（false / 字符串 / 缺失 / 类型错误）一律 false——本实现用字节比较达到
// 同样效果，且不因类型错误牵连整个 body 的解析。
func (v *RequestView) Stream() bool {
	raw := v.Raw("stream")
	return len(raw) > 0 && string(raw) == "true"
}

// Meta 返回 metadata 子对象（缺失 / 非对象 → nil）。
func (v *RequestView) Meta() map[string]json.RawMessage {
	if v == nil || v.metaLoaded {
		return v.meta
	}
	v.metaLoaded = true
	raw, ok := v.top["metadata"]
	if !ok {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	v.meta = m
	return v.meta
}

// MetaStr 取 metadata 下的字符串字段（缺失 / 非字符串 → ""）。
func (v *RequestView) MetaStr(key string) string {
	m := v.Meta()
	if m == nil {
		return ""
	}
	return rawString(m[key])
}

// Messages 返回 messages 数组的各条目原始字节（缺失 / 非数组 → nil）。
//
// 惰性 + 缓存：只有真正需要消息内容的消费方（会话键 / 轮级键 / 工具名单 / 带图
// 判定）才付这份解析代价，且同一请求内只解析一次。
func (v *RequestView) Messages() []json.RawMessage {
	if v == nil || v.msgsLoaded {
		return v.msgs
	}
	v.msgsLoaded = true
	raw, ok := v.top["messages"]
	if !ok {
		return nil
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil
	}
	v.msgs = msgs
	return v.msgs
}

// Tools 返回 tools 数组各条目原始字节（缺失 / 非数组 → nil），惰性 + 缓存。
func (v *RequestView) Tools() []json.RawMessage {
	if v == nil || v.toolsLd {
		return v.tools
	}
	v.toolsLd = true
	v.tools = rawArray(v.top["tools"])
	return v.tools
}

// Functions 返回旧版 functions 数组各条目原始字节，惰性 + 缓存。
func (v *RequestView) Functions() []json.RawMessage {
	if v == nil || v.funcsLd {
		return v.funcs
	}
	v.funcsLd = true
	v.funcs = rawArray(v.top["functions"])
	return v.funcs
}

func rawArray(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// rawString 把原始字节解成字符串；非字符串 / 解析失败 / 空串一律 ""。
// 与 strOrEmpty 同口径（JSON null 也落 ""）。
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
