package main

import "testing"

func TestNormalizeProvidersEmpty(t *testing.T) {
	if _, err := ParseConfig([]byte(`{}`)); err != nil {
		t.Fatalf("空 providers 应零回归：%v", err)
	}
}

func TestNormalizeProvidersValidArk(t *testing.T) {
	raw := `{"providers":[{"id":"ark","kind":"ark","quota":"metered","base_url":"https://ark.cn-beijing.volces.com/api/v3","models":["doubao-seed-2-1-pro"],"enabled":true}]}`
	c, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("合法 ark 配置应通过：%v", err)
	}
	if len(c.Providers) != 1 || c.Providers[0].ID != "ark" || !c.Providers[0].Enabled {
		t.Errorf("Providers 解析/保留错误: %+v", c.Providers)
	}
}

func TestNormalizeProvidersInvalidKind(t *testing.T) {
	raw := `{"providers":[{"id":"p1","kind":"bogus","quota":"metered","base_url":"https://x","enabled":true}]}`
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Error("启用条目 kind 非法应报错（fail fast，不静默禁用）")
	}
}

func TestNormalizeProvidersCreditsQuotaRejected(t *testing.T) {
	raw := `{"providers":[{"id":"p1","kind":"ark","quota":"credits","base_url":"https://x","enabled":true}]}`
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Error("quota=credits 应被拒绝（credits 是 CodeBuddy 专属，不搬家）")
	}
}

func TestNormalizeProvidersDisabledSkipsValidation(t *testing.T) {
	raw := `{"providers":[{"id":"p1","kind":"bogus","quota":"bogus","enabled":false}]}`
	if _, err := ParseConfig([]byte(raw)); err != nil {
		t.Errorf("未启用条目应跳过 kind/quota 校验（占位不生效）：%v", err)
	}
}

func TestNormalizeProvidersRejectsCodeBuddy(t *testing.T) {
	raw := `{"providers":[{"id":"codebuddy","kind":"ark","quota":"metered","base_url":"https://x","enabled":true}]}`
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Error("codebuddy 是内置默认，不可重定义")
	}
}

func TestNormalizeProvidersDuplicateID(t *testing.T) {
	raw := `{"providers":[{"id":"ark","kind":"ark","quota":"metered","base_url":"https://x","enabled":true},{"id":"ark","kind":"ark","quota":"metered","base_url":"https://y","enabled":true}]}`
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Error("重复 id 应报错")
	}
}

func TestNormalizeProvidersMissingBaseURL(t *testing.T) {
	raw := `{"providers":[{"id":"ark","kind":"ark","quota":"metered","enabled":true}]}`
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Error("启用条目缺 base_url 应报错")
	}
}
