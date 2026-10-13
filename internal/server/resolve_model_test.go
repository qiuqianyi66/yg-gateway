package server

import "testing"

func TestApplyModelAliases(t *testing.T) {
	cases := []struct {
		name    string
		bare    string
		aliases map[string]string
		want    string
	}{
		{"nil map 零回归", "glm-5.3", nil, "glm-5.3"},
		{"空 map 零回归", "glm-5.3", map[string]string{}, "glm-5.3"},
		{"命中替换", "glm", map[string]string{"glm": "glm-5.3-flash"}, "glm-5.3-flash"},
		{"未命中原样返回", "deepseek-v4-flash", map[string]string{"glm": "glm-5.3-flash"}, "deepseek-v4-flash"},
		{"空值映射忽略", "glm", map[string]string{"glm": ""}, "glm"},
	}
	for _, c := range cases {
		if got := applyModelAliases(c.bare, c.aliases); got != c.want {
			t.Errorf("%s: applyModelAliases(%q)=%q want %q", c.name, c.bare, got, c.want)
		}
	}
}

func TestModelAllowedByWhitelist(t *testing.T) {
	cases := []struct {
		name      string
		whitelist []string
		bare      string
		want      bool
	}{
		{"nil = 不限", nil, "anything", true},
		{"空切片 = 全拒", []string{}, "glm-5.3", false},
		{"精确命中", []string{"glm-5.3", "deepseek-v4-flash"}, "glm-5.3", true},
		{"精确未命中", []string{"glm-5.3"}, "deepseek-v4-flash", false},
		{"后缀通配命中", []string{"glm-*"}, "glm-5.3-flash", true},
		{"后缀通配不命中其他族", []string{"glm-*"}, "deepseek-v4-flash", false},
		{"裸 * 放行全部", []string{"*"}, "anything", true},
		{"星号在中间不视为通配", []string{"glm-*flash"}, "glm-5.3-pro", false},
		{"混合精确与通配", []string{"deepseek-v4-flash", "glm-*"}, "glm-5.3", true},
	}
	for _, c := range cases {
		if got := modelAllowedByWhitelist(c.whitelist, c.bare); got != c.want {
			t.Errorf("%s: allowed(%q, %q)=%v want %v", c.name, c.whitelist, c.bare, got, c.want)
		}
	}
}
