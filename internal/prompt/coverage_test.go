package prompt

// coverage_test.go —— Task 5 覆盖率补强（纯增量：不改生产代码、不改既有 prompt_test.go）。
//
// 重点覆盖既有 prompt_test.go 未触及的分支：
//   - Rewrite：messages 缺失/类型不符/空数组、非 map 消息元素、缺 role 消息、
//     中段与末段 system/developer 删除（既有测试只覆盖开头删除）、幂等性、
//     user 消息零触碰（指纹清洗由 upstream/sanitize 负责，两层不互相替代）、
//     JSON 转义敏感字符的提示词往返。
//   - Append：全路径 0% → 守卫、插入点（开头连续 system/developer 块末尾）、
//     首条非 system 角色/非 map 元素/缺 role 元素、messages 缺失或类型不符、
//     插入角色恒为 system（上游 role 白名单不含 developer）。
//   - 组合：append 模式降级退化为 Rewrite(Degraded) 后 system 必须收敛为一条。
//   - Load：内嵌 markdown 真实生效、空文件覆盖语义、错误标注文件路径。
//
// 变异补牙（第二批，针对变异实测 SURVIVED 的真空洞）：
//   - TestDegradedContentAnchor：Degraded 文案的独立字面量 + 长度上限 + 反指纹反向断言
//     （旧断言把 Degraded 同时当输入与期望，内容零约束）。
//   - TestDefaultPromptContentAnchor：defaultprompt.md 正文逐行字面量 + 全文 SHA256
//     （旧断言只比对 embed 与磁盘两侧，同一来源，删正文只留标题也全绿）。
//   - TestRewrite/TestAppendPassesThroughAllTopLevelFields：非 messages 顶层字段
//     个数/集合/逐字透传 + tools 显式断言（旧测试只覆盖 model/stream/metadata，
//     tools/tool_choice/temperature/response_format/parallel_tool_calls 全为盲区）。

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cpGW 网关自有提示词的测试字面量（含非 ASCII，顺带验证序列化不改内容）。
const cpGW = "网关自有提示词-GW-SENTINEL"

// cpUnmarshal 解析 body 为顶层对象；非合法 JSON 对象即 Fatal。
func cpUnmarshal(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON 对象: %v (body=%s)", err, body)
	}
	return obj
}

// cpMessages 返回 messages 数组的原始元素（不做类型收窄）。
func cpMessages(t *testing.T, body []byte) []any {
	t.Helper()
	obj := cpUnmarshal(t, body)
	msgs, ok := obj["messages"].([]any)
	if !ok {
		t.Fatalf("messages 不是数组: %#v (body=%s)", obj["messages"], body)
	}
	return msgs
}

// cpMsgMap 取第 i 条消息为对象；类型不符即 Fatal。
func cpMsgMap(t *testing.T, msgs []any, i int) map[string]any {
	t.Helper()
	mm, ok := msgs[i].(map[string]any)
	if !ok {
		t.Fatalf("messages[%d] 不是对象: %#v", i, msgs[i])
	}
	return mm
}

// cpContent 取第 i 条消息的 content 字符串；类型不符即 Fatal。
func cpContent(t *testing.T, m any) string {
	t.Helper()
	mm, ok := m.(map[string]any)
	if !ok {
		t.Fatalf("消息不是对象: %#v", m)
	}
	s, ok := mm["content"].(string)
	if !ok {
		t.Fatalf("content 不是字符串: %#v", mm["content"])
	}
	return s
}

// cpRoles 返回 messages 逐条角色序列：非 map 元素记为 "<non-map>"，map 缺 role 记为 ""。
func cpRoles(t *testing.T, body []byte) []string {
	t.Helper()
	msgs := cpMessages(t, body)
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out = append(out, "<non-map>")
			continue
		}
		r, _ := mm["role"].(string)
		out = append(out, r)
	}
	return out
}

// cpWantRoles 断言角色序列精确等于 want（长度 + 逐位）。
func cpWantRoles(t *testing.T, body []byte, want []string) {
	t.Helper()
	got := cpRoles(t, body)
	if len(got) != len(want) {
		t.Fatalf("角色序列=%v want %v (body=%s)", got, want, body)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("角色序列=%v want %v (body=%s)", got, want, body)
		}
	}
}

// cpCountSystemContent 统计 role=system 且 content==want 的消息条数。
func cpCountSystemContent(t *testing.T, body []byte, want string) int {
	t.Helper()
	n := 0
	for _, m := range cpMessages(t, body) {
		mm, ok := m.(map[string]any)
		if !ok || mm["role"] != "system" {
			continue
		}
		if s, _ := mm["content"].(string); s == want {
			n++
		}
	}
	return n
}

// cpAssertAbsent 断言输出 raw 文本中不含指定串（残留泄漏检查）。
func cpAssertAbsent(t *testing.T, body []byte, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(string(body), n) {
			t.Errorf("输出残留 %q（本应被删除）: %s", n, body)
		}
	}
}

// cpSameJSON 断言两条消息 JSON 序列化逐字一致（marshal 对 map 键排序，比较是规范的）。
func cpSameJSON(t *testing.T, label string, in, out map[string]any) {
	t.Helper()
	a, errA := json.Marshal(in)
	b, errB := json.Marshal(out)
	if errA != nil || errB != nil {
		t.Fatalf("%s marshal 失败: %v / %v", label, errA, errB)
	}
	if string(a) != string(b) {
		t.Errorf("%s 消息未逐字保留:\n in=%s\nout=%s", label, a, b)
	}
}

// ---------- Rewrite ----------

// 既有 prompt_test.go 只测开头 system/developer；本用例钉住「任意位置」都被删除。
func TestRewriteRemovesSystemAndDeveloperAnywhere(t *testing.T) {
	in := []byte(`{"messages":[
		{"role":"system","content":"CLIENT-SYS"},
		{"role":"user","content":"q1"},
		{"role":"developer","content":"CLIENT-DEV"},
		{"role":"assistant","content":"a1"},
		{"role":"system","content":"CLIENT-SYS-2"},
		{"role":"user","content":"q2"}
	]}`)
	out := Rewrite(in, cpGW)
	cpWantRoles(t, out, []string{"system", "user", "assistant", "user"})
	msgs := cpMessages(t, out)
	if got := cpContent(t, msgs[0]); got != cpGW {
		t.Fatalf("头部 system content=%q want %q", got, cpGW)
	}
	if cpContent(t, msgs[1]) != "q1" || cpContent(t, msgs[2]) != "a1" || cpContent(t, msgs[3]) != "q2" {
		t.Fatalf("user/assistant 内容或顺序被改动: %s", out)
	}
	cpAssertAbsent(t, out, "CLIENT-SYS-2", "CLIENT-SYS", "CLIENT-DEV")
}

// Rewrite 必须清理用户消息里的指纹串（由 upstream/sanitize 负责），不得越界清洗。
func TestRewriteLeavesUserFingerprintForSanitizer(t *testing.T) {
	const fp = "You are Claude Code, Anthropic's official CLI for Claude"
	in := []byte(`{"messages":[
		{"role":"system","content":"` + fp + `"},
		{"role":"user","content":"请复述: ` + fp + `"}
	]}`)
	out := Rewrite(in, cpGW)
	cpWantRoles(t, out, []string{"system", "user"})
	msgs := cpMessages(t, out)
	wantUser := "请复述: " + fp
	if got := cpContent(t, msgs[1]); got != wantUser {
		t.Errorf("user content 被改动: %q want %q（user 侧指纹由 sanitize 清洗，Rewrite 不得越界）", got, wantUser)
	}
	if n := strings.Count(string(out), fp); n != 1 {
		t.Errorf("指纹串出现 %d 次 want 1（system 侧应被删除，仅剩 user 侧）: %s", n, out)
	}
}

func TestRewriteInsertsSystemWhenMessagesMissing(t *testing.T) {
	in := []byte(`{"model":"glm-5.2","stream":true}`)
	out := Rewrite(in, cpGW)
	obj := cpUnmarshal(t, out)
	if obj["model"] != "glm-5.2" || obj["stream"] != true {
		t.Errorf("其余字段被改动: %v", obj)
	}
	cpWantRoles(t, out, []string{"system"})
	if got := cpContent(t, cpMessages(t, out)[0]); got != cpGW {
		t.Errorf("插入的 system content=%q want %q", got, cpGW)
	}
}

func TestRewriteReplacesNonArrayMessages(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"字符串类型", `{"model":"m","messages":"oops"}`},
		{"对象类型", `{"model":"m","messages":{"0":{"role":"user"}}}`},
		{"null", `{"model":"m","messages":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := Rewrite([]byte(tc.in), cpGW)
			obj := cpUnmarshal(t, out)
			if obj["model"] != "m" {
				t.Errorf("其余字段被改动: %v", obj)
			}
			cpWantRoles(t, out, []string{"system"})
		})
	}
}

func TestRewriteInsertsSystemIntoEmptyMessagesArray(t *testing.T) {
	out := Rewrite([]byte(`{"messages":[]}`), cpGW)
	cpWantRoles(t, out, []string{"system"})
	if got := cpContent(t, cpMessages(t, out)[0]); got != cpGW {
		t.Errorf("content=%q want %q", got, cpGW)
	}
}

// 非 map 元素与缺 role 元素必须原样保留：Rewrite 不能静默丢消息。
func TestRewriteKeepsNonMapAndRolelessMessageElements(t *testing.T) {
	in := []byte(`{"messages":[
		"RAW-ELEMENT",
		{"role":"user","content":"hi"},
		{"content":"no-role"},
		{"role":"developer","content":"CLIENT-DEV"}
	]}`)
	out := Rewrite(in, cpGW)
	cpWantRoles(t, out, []string{"system", "<non-map>", "user", ""})
	msgs := cpMessages(t, out)
	if s, ok := msgs[1].(string); !ok || s != "RAW-ELEMENT" {
		t.Errorf("非 map 元素丢失或被改写: %#v", msgs[1])
	}
	if cpContent(t, msgs[2]) != "hi" || cpContent(t, msgs[3]) != "no-role" {
		t.Errorf("元素被改写: %s", out)
	}
	cpAssertAbsent(t, out, "CLIENT-DEV")
}

// 幂等性：Rewrite 自身输出再喂 Rewrite 不得累积 system 消息（出站重试路径会重复调用）。
func TestRewriteIsIdempotent(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"CLIENT"},{"role":"user","content":"hi"},{"role":"system","content":"CLIENT-2"}]}`)
	once := Rewrite(in, cpGW)
	twice := Rewrite(once, cpGW)
	cpWantRoles(t, twice, []string{"system", "user"})
	if n := cpCountSystemContent(t, twice, cpGW); n != 1 {
		t.Errorf("二次 Rewrite 后网关提示词出现 %d 条 want 1（幂等性破坏）: %s", n, twice)
	}
	cpAssertAbsent(t, twice, "CLIENT-2", "CLIENT")
	if got := cpContent(t, cpMessages(t, twice)[1]); got != "hi" {
		t.Errorf("user content=%q want %q", got, "hi")
	}
}

// 提示词含 JSON 敏感字符时必须正确转义，往返后内容逐字不变且 body 仍合法。
func TestRewriteEscapesJSONSensitivePrompt(t *testing.T) {
	p := "行1\n行2 \"引号\" <tag> & \\ 反斜杠\t制表"
	out := Rewrite([]byte(`{"messages":[{"role":"user","content":"q"}]}`), p)
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON: %s", out)
	}
	if got := cpContent(t, cpMessages(t, out)[0]); got != p {
		t.Errorf("提示词往返失真:\n got=%q\nwant=%q", got, p)
	}
}

// ---------- Append ----------

func TestAppendGuardReturnsBodyUntouched(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		p    string
	}{
		{"空 body", []byte{}, cpGW},
		{"空 prompt", []byte(`{"messages":[{"role":"system","content":"CLIENT"}]}`), ""},
		{"坏 JSON 对象", []byte(`{"messages":[`), cpGW},
		{"JSON 标量", []byte(`"just a string"`), cpGW},
		{"JSON 数组", []byte(`[1,2,3]`), cpGW},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Append(tc.body, tc.p)
			if string(out) != string(tc.body) {
				t.Fatalf("守卫失效：原样返回要求，got=%s want=%s", out, tc.body)
			}
		})
	}
}

// Append 核心行为：插在「开头连续 system/developer 块」末尾，既有消息逐字不动。
func TestAppendInsertsAfterLeadingSystemDeveloperBlock(t *testing.T) {
	in := []byte(`{"messages":[
		{"role":"system","content":"CLIENT-A","cache_control":{"type":"ephemeral"}},
		{"role":"developer","content":"CLIENT-B"},
		{"role":"user","content":"q1"},
		{"role":"assistant","content":"a1","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","name":"f","content":"RESULT"},
		{"role":"system","content":"MID-SYS"}
	]}`)
	out := Append(in, cpGW)
	cpWantRoles(t, out, []string{"system", "developer", "system", "user", "assistant", "tool", "system"})

	inMsgs := cpMessages(t, in)
	outMsgs := cpMessages(t, out)
	if got := cpContent(t, outMsgs[2]); got != cpGW {
		t.Fatalf("插入位置错误：out[2].content=%q want %q (body=%s)", got, cpGW, out)
	}
	// 逐条 JSON 序列化对比：除插入的那条外，既有消息必须逐字不动（含 tool_calls/tool_call_id）。
	for i, j := range []int{0, 1, 3, 4, 5, 6} { // out[j] 对应 in[i]
		cpSameJSON(t, "既有消息", cpMsgMap(t, inMsgs, i), cpMsgMap(t, outMsgs, j))
	}
}

// 开头块边界：首条不是 system/developer（含非 map、null、缺 role、role 非字符串）→ 插到最前。
func TestAppendInsertionPointAtLeadingBlockEnd(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     []string
		wantGWAt int // 网关消息应落的索引（= 开头连续块长度，手写常量）
	}{
		{"首条 user", `{"messages":[{"role":"user","content":"q"}]}`, []string{"system", "user"}, 0},
		{"首条 assistant", `{"messages":[{"role":"assistant","content":"a"}]}`, []string{"system", "assistant"}, 0},
		{"首条 tool", `{"messages":[{"role":"tool","tool_call_id":"t1","content":"r"}]}`, []string{"system", "tool"}, 0},
		{"首条非对象元素", `{"messages":["RAW",{"role":"user","content":"q"}]}`, []string{"system", "<non-map>", "user"}, 0},
		{"首条 null 元素", `{"messages":[null,{"role":"user","content":"q"}]}`, []string{"system", "<non-map>", "user"}, 0},
		{"首条缺 role", `{"messages":[{"content":"q"}]}`, []string{"system", ""}, 0},
		{"首条 role 非字符串", `{"messages":[{"role":7,"content":"q"}]}`, []string{"system", ""}, 0},
		{"messages 空数组", `{"messages":[]}`, []string{"system"}, 0},
		{"仅一条 system", `{"messages":[{"role":"system","content":"c"}]}`, []string{"system", "system"}, 1},
		{"长度 1 的 system 块", `{"messages":[{"role":"system","content":"c"},{"role":"user","content":"q"}]}`, []string{"system", "system", "user"}, 1},
		{"长度 2 的 system/developer 块", `{"messages":[{"role":"system","content":"a"},{"role":"developer","content":"b"},{"role":"user","content":"q"}]}`, []string{"system", "developer", "system", "user"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Append([]byte(tc.in), cpGW)
			cpWantRoles(t, out, tc.want)
			msgs := cpMessages(t, out)
			if got := cpContent(t, msgs[tc.wantGWAt]); got != cpGW {
				t.Fatalf("插入位置错误: msgs[%d].content=%q want %q (body=%s)", tc.wantGWAt, got, cpGW, out)
			}
			if n := cpCountSystemContent(t, out, cpGW); n != 1 {
				t.Fatalf("单次 Append 插入了 %d 条网关提示词 want 1: %s", n, out)
			}
		})
	}
}

func TestAppendMessagesMissingOrWrongType(t *testing.T) {
	cases := []struct{ name, in string }{
		{"无 messages 字段", `{"model":"m","stream":true}`},
		{"messages 为字符串", `{"model":"m","messages":"oops"}`},
		{"messages 为对象", `{"model":"m","messages":{"0":{"role":"user"}}}`},
		{"messages 为 null", `{"model":"m","messages":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Append([]byte(tc.in), cpGW)
			obj := cpUnmarshal(t, out)
			if obj["model"] != "m" {
				t.Errorf("其余字段被改动: %v", obj)
			}
			cpWantRoles(t, out, []string{"system"})
			if got := cpContent(t, cpMessages(t, out)[0]); got != cpGW {
				t.Errorf("content=%q want %q", got, cpGW)
			}
		})
	}
}

// 网关消息角色恒为 system：上游 role 白名单不含 developer，插 developer 会制造一次
// 必然归一与多余的 11-128 风险窗口（设计约束，写在 Append doc comment 里）。
func TestAppendInsertedRoleIsAlwaysSystem(t *testing.T) {
	in := []byte(`{"messages":[{"role":"developer","content":"CLIENT-DEV"},{"role":"user","content":"q"}]}`)
	out := Append(in, cpGW)
	cpWantRoles(t, out, []string{"developer", "system", "user"})
	msgs := cpMessages(t, out)
	if role := cpMsgMap(t, msgs, 1)["role"]; role != "system" {
		t.Fatalf("网关消息 role=%v want system", role)
	}
	devs := 0
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "developer" {
			devs++
		}
	}
	if devs != 1 {
		t.Errorf("developer 消息 %d 条 want 1（只允许客户端自己那条）", devs)
	}
}

// Append 的插入点是位置判定而非内容去重：客户端已带同内容 system 时仍会再插一条。
func TestAppendInsertsEvenWhenGatewayPromptAlreadyPresent(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"` + cpGW + `"},{"role":"user","content":"q"}]}`)
	out := Append(in, cpGW)
	cpWantRoles(t, out, []string{"system", "system", "user"})
	if n := cpCountSystemContent(t, out, cpGW); n != 2 {
		t.Errorf("网关提示词 %d 条 want 2（Append 不做内容去重，插入点为位置判定）: %s", n, out)
	}
}

// append 模式降级 = 退化为 replace：Append 的输出再经 Rewrite(Degraded)，
// system 必须收敛为唯一一条 Degraded，客户端 system/developer 与网关提示词全部消失。
func TestAppendThenDegradeRewriteCollapsesToSingleSystem(t *testing.T) {
	in := []byte(`{"messages":[
		{"role":"system","content":"CLIENT-FINGERPRINT"},
		{"role":"developer","content":"CLIENT-DEV"},
		{"role":"user","content":"q"},
		{"role":"assistant","content":"a"}
	]}`)
	appended := Append(in, cpGW)
	cpWantRoles(t, appended, []string{"system", "developer", "system", "user", "assistant"})

	degraded := Rewrite(appended, Degraded)
	cpWantRoles(t, degraded, []string{"system", "user", "assistant"})
	msgs := cpMessages(t, degraded)
	if got := cpContent(t, msgs[0]); got != Degraded {
		t.Fatalf("降级后首条 system content=%q want Degraded", got)
	}
	if cpContent(t, msgs[1]) != "q" || cpContent(t, msgs[2]) != "a" {
		t.Fatalf("降级重写改动了 user/assistant: %s", degraded)
	}
	cpAssertAbsent(t, degraded, cpGW, "CLIENT-FINGERPRINT", "CLIENT-DEV")
	if n := cpCountSystemContent(t, degraded, Degraded); n != 1 {
		t.Errorf("Degraded 消息 %d 条 want 1: %s", n, degraded)
	}
}

// ---------- Load ----------

// 内置提示词必须来自内嵌 defaultprompt.md（不是硬编码桩、不是 Degraded），且与磁盘原文一致。
func TestLoadReturnsEmbeddedMarkdownVerbatim(t *testing.T) {
	disk, err := os.ReadFile("defaultprompt.md")
	if err != nil {
		t.Fatalf("读取内嵌源文件失败: %v", err)
	}
	if len(disk) == 0 {
		t.Fatal("defaultprompt.md 为空")
	}
	for _, mode := range []string{"custom", "append", "passthrough"} {
		got, err := Load(mode, "")
		if err != nil {
			t.Fatalf("Load(%q,\"\") err=%v", mode, err)
		}
		if got != string(disk) {
			t.Errorf("Load(%q,\"\") 未返回内嵌 defaultprompt.md 内容 (len=%d vs %d)", mode, len(got), len(disk))
		}
	}
	got, _ := Load("custom", "")
	if got == Degraded {
		t.Error("默认提示词退化成 Degraded")
	}
	if !strings.Contains(got, "# 系统提示词") {
		t.Errorf("默认提示词缺少标题锚点，疑似被替换: %.60s", got)
	}
}

// 空文件覆盖 = 空提示词（无隐式回退）；空提示词触发 Rewrite 守卫 → 客户端 system 原样透传。
func TestLoadEmptyOverrideFileDisablesRewrite(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(fp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load("custom", fp)
	if err != nil {
		t.Fatalf("空文件不应报错: %v", err)
	}
	if got != "" {
		t.Errorf("空文件覆盖应返回空串（无隐式回退到内置提示词）: %q", got)
	}
	in := []byte(`{"messages":[{"role":"system","content":"CLIENT"}]}`)
	if out := Rewrite(in, got); string(out) != string(in) {
		t.Errorf("空提示词应不改写 body: got=%s", out)
	}
}

// 读文件失败必须带 "prompt file <路径>" 前缀，运维才能定位是哪个文件（fail fast）。
func TestLoadWrapsReadErrorWithFilePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "prompt.md")
	_, err := Load("custom", missing)
	if err == nil {
		t.Fatal("缺失文件应返回 error（fail fast）")
	}
	if !strings.Contains(err.Error(), "prompt file "+missing) {
		t.Errorf("错误未标注文件路径: %v", err)
	}
	// 目录不是有效提示词文件，同样必须失败且带同一前缀。
	dir := t.TempDir()
	if _, err := Load("custom", dir); err == nil {
		t.Error("路径为目录时应失败")
	} else if !strings.Contains(err.Error(), "prompt file "+dir) {
		t.Errorf("目录错误未标注路径: %v", err)
	}
}

// ---------- 内容锚点①-a：Degraded 文案（变异实测真空洞） ----------
//
// 旧断言 TestAppendThenDegradeRewriteCollapsesToSingleSystem 把 Degraded 同时当
// 输入与期望（Rewrite(appended, Degraded) 后断言 content == Degraded），只证明
// "Rewrite 逐字拷贝了传进来的那串"，对 Degraded 的内容零约束：把常量整句换成
// 客户端指纹句，测试依然全绿。下面用独立手写字面量钉死内容。

// cpDegradedLiteral 是 Degraded 的独立手写字面量（与 prompt.go 里的常量分开手写，
// 不引用任何被测符号），任何单字改动都必须让 TestDegradedContentAnchor 红。
const cpDegradedLiteral = "You are a helpful assistant. Respond in the user's language, follow the user's instructions, and be direct and concise."

// cpDegradedMaxLen 降级提示词长度上限（手写常量）：降级提示词一旦膨胀，就从
// "最小中性"变成新的可逐字匹配模板，误报面反而扩大。
const cpDegradedMaxLen = 160

// cpClientFingerprints 客户端指纹片段（手写字面量）：降级提示词必须完全中性，
// 混入任一即等于把误报源搬到降级路径上。
var cpClientFingerprints = []string{"Claude Code", "Anthropic", "CLI tool", "Codex", "WorkBuddy"}

func TestDegradedContentAnchor(t *testing.T) {
	// 用 Errorf 而非 Fatalf：文案漂移时后面的长度/指纹断言仍会执行并各自报错，
	// 三条断言都能独立观察到（Fatalf 会让后两条变成永不执行的死代码）。
	if Degraded != cpDegradedLiteral {
		t.Errorf("Degraded 文案漂移:\n got=%q\nwant=%q", Degraded, cpDegradedLiteral)
	}
	if n := len(Degraded); n > cpDegradedMaxLen {
		t.Errorf("Degraded 长度 %d > 上限 %d（降级提示词必须保持极简，膨胀即成为新的可匹配模板）", n, cpDegradedMaxLen)
	}
	low := strings.ToLower(Degraded)
	for _, fp := range cpClientFingerprints {
		if strings.Contains(low, strings.ToLower(fp)) {
			t.Errorf("Degraded 含客户端指纹片段 %q（降级后会重新落回上游逐字匹配误报面）: %q", fp, Degraded)
		}
	}
}

// ---------- 内容锚点①-b：defaultprompt.md 正文（变异实测真空洞） ----------
//
// 旧断言 TestLoadReturnsEmbeddedMarkdownVerbatim 只比对「内嵌内容 == 磁盘同名文件」，
// 两侧是同一来源、同一份字节：把正文删到只留标题（两端同步变短）依然全绿。
// 下面把正文逐行与全文 SHA256 写成独立字面量。

// cpDefaultPromptLines 是 defaultprompt.md 全部非空行的独立手写字面量副本，顺序即
// 文件顺序（31 行 = 1 个一级标题 + 1 行引言 + 6 个二级标题 + 23 条原则）。
var cpDefaultPromptLines = []string{
	"# 系统提示词",
	"你是一名工程助手，帮助用户完成软件工程任务。以下原则指导你的行为。",
	"## 核心立场",
	"- 你的价值是让用户的工程目标更快达成，而非展示你自己的能力边界。",
	"- 当用户的方向有更优解时，直接指出并给出替代方案；不必逢迎。",
	"- 对不确定的事保持诚实：宁可说\"我不确定，需要验证\"，也不编造看似合理的答案。",
	"## 语言与风格",
	"- 跟随用户的提问语言：用户用中文则用中文，用英文则用英文。",
	"- 简洁直接，不说废话；不用客套开场与总结，不重复用户已说过的内容。",
	"- 技术术语精确，不为了通俗而牺牲准确性。",
	"## 工程行为",
	"- 先看代码再动手：理解上下文、既有模式与约定，避免破坏一致性。",
	"- 最小改动：只改必要的部分，不做无关重构或风格统一。",
	"- 改动后验证闭环：运行测试或构建确认结果，不假设\"应该没问题\"。",
	"- 遇到不确定的边界，先确认再执行，不擅自扩大范围或假设需求。",
	"- 修改共享代码前，先看它被谁依赖，避免连锁影响。",
	"## 任务分解",
	"- 复杂任务先拆步骤，按依赖顺序推进；每步可独立验证。",
	"- 给出改动清单与影响面，让用户能判断是否继续。",
	"- 失败时如实报告原因，给出下一步建议，不掩盖、不粉饰。",
	"## 输出格式",
	"- 用 Markdown 组织结构。",
	"- 代码块标注语言（```go / ```bash / ```json 等）。",
	"- 复杂度与任务匹配：简单问题一句话答完，复杂问题分步骤说明。",
	"- 关键决策给出依据，不堆砌理由；不写你已经知道答案却还要绕的解释。",
	"- 引用代码时用 `file:line` 形式，便于用户跳转。",
	"## 边界",
	"- 不臆造未给定的 API、字段或行为；不确定时如实说明并给出验证路径。",
	"- 安全敏感操作（删除、覆盖、发布）先确认，除非已被明确授权。",
	"- 错误与失败如实报告，不为了让结果\"好看\"而省略或美化。",
	"- 保留对方案的质疑空间：如果用户的方案有明显问题，指出并提供更优替代。",
}

// cpDefaultPromptSHALiteral 是 defaultprompt.md 全文（含空行与行尾符）的 SHA256，
// 手写常量。行断言对空行/行尾符/不可见字符无感知，这里补齐逐字节锚点。
const cpDefaultPromptSHALiteral = "140150d658e967866566333f57b332101b8aedd207fbfdb2c1b96b77376c22a6"

func TestDefaultPromptContentAnchor(t *testing.T) {
	got, err := Load("custom", "")
	if err != nil {
		t.Fatalf("Load(\"custom\",\"\") err=%v", err)
	}

	// ① 逐行钉死正文：截成"只留标题"、删段、换序、改字都红。
	lines := make([]string, 0, len(cpDefaultPromptLines))
	for _, l := range strings.Split(got, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, strings.TrimSpace(l))
	}
	if len(lines) != len(cpDefaultPromptLines) {
		t.Fatalf("正文非空行 %d 行 want %d 行（正文被增删）:\n got=%q", len(lines), len(cpDefaultPromptLines), lines)
	}
	for i, want := range cpDefaultPromptLines {
		if lines[i] != want {
			t.Errorf("第 %d 行正文漂移:\n got=%q\nwant=%q", i+1, lines[i], want)
		}
	}

	// ② 逐字节锚点：空行、行尾符、不可见字符改动同样必须红。
	if gotSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(got))); gotSHA != cpDefaultPromptSHALiteral {
		t.Errorf("defaultprompt.md 全文 SHA256 漂移: got=%s want=%s（若为有意的正文修订，必须同步更新本测试的字面量与 SHA256 锚点）", gotSHA, cpDefaultPromptSHALiteral)
	}

	// ③ 反向断言：网关自有提示词不得掺入任何客户端指纹片段。
	low := strings.ToLower(got)
	for _, fp := range cpClientFingerprints {
		if strings.Contains(low, strings.ToLower(fp)) {
			t.Errorf("默认提示词含客户端指纹片段 %q: 网关自有提示词必须是自有文案", fp)
		}
	}
}

// ---------- 顶层字段透传②：Rewrite / Append 丢弃非 messages 字段（变异实测真空洞） ----------
//
// 变异实测：删掉 Rewrite/Append 对非 messages 顶层字段的透传（只回填 messages）后
// 全部测试仍绿。后果是 function calling（tools/tool_choice/parallel_tool_calls）
// 静默全废——上游收到没有 tools 的请求，返回纯文本而不是工具调用，且不报错。

// cpTopBody 顶层字段全集：model/stream/temperature/response_format/tool_choice/
// parallel_tool_calls/tools/metadata + messages。tools 内联规范序列化，
// 便于对输出 body 做逐字子串断言。
const cpToolsCanonicalJSON = `{"function":{"name":"get_weather","parameters":{"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}},"type":"function"}`

const cpTopBody = `{
	"model":"glm-5.2",
	"stream":true,
	"temperature":0.25,
	"response_format":{"type":"json_object"},
	"tool_choice":"auto",
	"parallel_tool_calls":false,
	"tools":[` + cpToolsCanonicalJSON + `],
	"metadata":{"conversation_id":"wb2api-conv-1","trace_id":"T-1"},
	"messages":[
		{"role":"system","content":"CLIENT-SYS"},
		{"role":"developer","content":"CLIENT-DEV"},
		{"role":"user","content":"weather?"}
	]
}`

// cpTopKeys 是 cpTopBody 的顶层字段集合（手写、已排序）：既是最小字段数锚点，
// 也是「下次新增顶层字段又被透传漏掉」时的第一道红灯。
var cpTopKeys = []string{
	"messages",
	"metadata",
	"model",
	"parallel_tool_calls",
	"response_format",
	"stream",
	"temperature",
	"tool_choice",
	"tools",
}

// cpTopRawFragments 是 cpTopBody 里各顶层字段的原始字节片段（手写字面量）：
// ③ 的比较在「解析后的值」层面，float64 失真之类的改动看不出来，这里补一层
// 输出 body 的逐字节子串断言。
var cpTopRawFragments = []string{
	`"model":"glm-5.2"`,
	`"stream":true`,
	`"temperature":0.25`,
	`"response_format":{"type":"json_object"}`,
	`"tool_choice":"auto"`,
	`"parallel_tool_calls":false`,
	`"metadata":{"conversation_id":"wb2api-conv-1","trace_id":"T-1"}`,
}

// cpSortedKeys 返回 map 的键排序副本（避免依赖 map 迭代顺序）。
func cpSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cpAssertTopLevelPassthrough 断言 out 相对 in：顶层字段个数、字段集合、除 messages
// 外每个字段的 JSON 序列化逐字相同，且 tools 存在且等于手写规范序列化。
func cpAssertTopLevelPassthrough(t *testing.T, label string, in, out []byte) {
	t.Helper()
	inObj := cpUnmarshal(t, in)
	outObj := cpUnmarshal(t, out)

	// 输入自检：cpTopBody 的字段集合必须与 cpTopKeys 逐位一致（防用例被改瘦后断言变弱）。
	inKeys := cpSortedKeys(inObj)
	if strings.Join(inKeys, ",") != strings.Join(cpTopKeys, ",") {
		t.Fatalf("%s: 测试输入字段集合=%v want %v（用例自身漂移）", label, inKeys, cpTopKeys)
	}

	// ① 顶层字段个数相同：透传丢字段（如整块 tools 消失）立刻红。
	if len(outObj) != len(inObj) {
		t.Fatalf("%s: 输出顶层字段 %d 个 want %d 个（透传丢字段）:\n in=%v\nout=%v\nbody=%s",
			label, len(outObj), len(inObj), inKeys, cpSortedKeys(outObj), out)
	}
	// ② 字段集合逐位相同：新增字段、改名、丢字段都红。
	if outKeys := cpSortedKeys(outObj); strings.Join(outKeys, ",") != strings.Join(inKeys, ",") {
		t.Errorf("%s: 输出顶层字段集合=%v want %v\nbody=%s", label, outKeys, inKeys, out)
	}
	// ③ 除 messages 外每个顶层字段逐字透传（JSON 序列化后比较，键序由 marshal 规范）。
	for k, v := range inObj {
		if k == "messages" {
			continue
		}
		gotVal, ok := outObj[k]
		if !ok {
			t.Errorf("%s: 顶层字段 %q 丢失（Rewrite/Append 只允许改 messages）: %s", label, k, out)
			continue
		}
		wantRaw, errW := json.Marshal(v)
		gotRaw, errG := json.Marshal(gotVal)
		if errW != nil || errG != nil {
			t.Fatalf("%s: %q marshal 失败: %v / %v", label, k, errW, errG)
		}
		if string(gotRaw) != string(wantRaw) {
			t.Errorf("%s: 顶层字段 %q 未逐字透传:\n got=%s\nwant=%s", label, k, gotRaw, wantRaw)
		}
	}

	// ④ 输出 body 逐字节包含每个非 messages 顶层字段的原始片段（bytes 层，不经再解析）。
	for _, frag := range cpTopRawFragments {
		if !strings.Contains(string(out), frag) {
			t.Errorf("%s: 输出 body 缺少原文片段 %s\nbody=%s", label, frag, out)
		}
	}

	// ⑤ tools 显式断言：必须存在、内容等于手写规范序列化、内部结构完好。
	toolsVal, ok := outObj["tools"]
	if !ok {
		t.Fatalf("%s: 输出丢失顶层 tools 字段 → function calling 静默全废 (body=%s)", label, out)
	}
	toolsRaw, err := json.Marshal(toolsVal)
	if err != nil {
		t.Fatalf("%s: tools marshal 失败: %v", label, err)
	}
	if wantTools := "[" + cpToolsCanonicalJSON + "]"; string(toolsRaw) != wantTools {
		t.Errorf("%s: tools 内容被改写:\n got=%s\nwant=%s", label, toolsRaw, wantTools)
	}
	arr, ok := toolsVal.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("%s: tools 结构被改写: %#v", label, toolsVal)
	}
	fn, ok := cpMsgMap(t, arr, 0)["function"].(map[string]any)
	if !ok {
		t.Fatalf("%s: tools[0].function 丢失或类型不符: %#v", label, arr[0])
	}
	if fn["name"] != "get_weather" {
		t.Errorf("%s: tools[0].function.name=%v want %q", label, fn["name"], "get_weather")
	}
	// 输出 body 里 tools 子串逐字存在（bytes 层，不经再解析）。
	if !strings.Contains(string(out), cpToolsCanonicalJSON) {
		t.Errorf("%s: 输出 body 未逐字包含 tools 内容:\nbody=%s\nwant 子串=%s", label, out, cpToolsCanonicalJSON)
	}
}

// Rewrite 只允许改 messages：其余顶层字段（含 tools/metadata）必须逐字透传。
func TestRewritePassesThroughAllTopLevelFields(t *testing.T) {
	in := []byte(cpTopBody)
	out := Rewrite(in, cpGW)
	if string(out) == string(in) {
		t.Fatal("Rewrite 未改写 body（应替换 system 消息）")
	}
	cpAssertTopLevelPassthrough(t, "Rewrite", in, out)
	cpWantRoles(t, out, []string{"system", "user"}) // 客户端 system/developer 被删，注入网关 system
	cpAssertAbsent(t, out, "CLIENT-SYS", "CLIENT-DEV")
}

// Append 同理：插入点后的重组不得丢掉 tools 等顶层字段。
func TestAppendPassesThroughAllTopLevelFields(t *testing.T) {
	in := []byte(cpTopBody)
	out := Append(in, cpGW)
	if string(out) == string(in) {
		t.Fatal("Append 未改写 body（应插入网关 system）")
	}
	cpAssertTopLevelPassthrough(t, "Append", in, out)
	cpWantRoles(t, out, []string{"system", "developer", "system", "user"}) // 网关消息插在开头块末尾
	if n := cpCountSystemContent(t, out, cpGW); n != 1 {
		t.Errorf("网关提示词 %d 条 want 1: %s", n, out)
	}
}
