package upstream

import "testing"

// TestProtocolConstantsR8 把 R8 协议常量的**字面值**钉死。
//
// 为什么需要（R8 此前只有人读评审，没有机器检查）：这些字符串是**报文内容**——
// 上游认的是它们的字面值，改名即改变指纹、有风控风险。集中到 protocol.go 之后，
// 任何人"顺手优化"一下大小写、版本号或域名，本测试立刻红。
//
// 如果确实要升级版本（如上游 CLI 升级到 2.64.x），正确流程是：
//  1. 改 protocol.go 的字面量；
//  2. 同步改本测试的期望值（这一步是有意的摩擦，不是障碍）；
//  3. 重跑真实账号冒烟（AGENTS.md：上游协议改动必须端到端验证）。
func TestProtocolConstantsR8(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"LoginBaseCN", LoginBaseCN, "https://copilot.tencent.com"},
		{"LoginBaseGlobal", LoginBaseGlobal, "https://www.workbuddy.ai"},
		{"OriginRefererCN", OriginRefererCN, "https://www.codebuddy.cn"},
		{"OriginRefererGlobal", OriginRefererGlobal, "https://www.workbuddy.ai"},
		{"CLIUserAgent", CLIUserAgent, "CLI/2.63.2 CodeBuddy/2.63.2"},
		// 包内别名必须与单一来源一致（防止有人把别名改回字面量）。
		{"originRefererCN(alias)", originRefererCN, OriginRefererCN},
		{"originRefererGlobal(alias)", originRefererGlobal, OriginRefererGlobal},
		{"codeBuddyCLIUA(alias)", codeBuddyCLIUA, CLIUserAgent},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("R8 协议常量 %s = %q，期望 %q（改这个值等于改上游指纹，需同步冒烟）", c.name, c.got, c.want)
		}
	}
}
