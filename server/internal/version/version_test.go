package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		why  string
	}{
		{"3.4.0", "3.4.0", 0, "同版本"},
		{"3.4.1", "3.4.0", 1, "补丁位"},
		{"3.5.0", "3.4.9", 1, "次版本优先于补丁位（按字符串比会得出 3.4.9 > 3.5.0）"},
		{"4.0.0", "3.99.99", 1, "主版本"},
		{"v3.4.1", "3.4.1", 0, "v 前缀必须被忽略，否则线上 tag 与注入版本永远不等"},
		{"V3.4.1", "3.4.1", 0, "大写 V 也算前缀"},
		{"3.4", "3.4.0", 0, "缺段按 0 处理"},
		{"3.4.0-rc1", "3.4.0", -1, "预发布小于同号正式版（semver）；漏了这条会把降级当升级"},
		{"3.4.0", "3.4.0-rc1", 1, "反向"},
		{"3.4.0-rc2", "3.4.0-rc1", 1, "预发布之间比较"},
		{"3.4.0+build7", "3.4.0", 0, "构建元数据不参与比较"},
		{" 3.4.0 ", "3.4.0", 0, "前后空白"},
		{"10.0.0", "9.9.9", 1, "多位数按数值比，不能按字符比"},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q)=%d want %d —— %s", c.a, c.b, got, c.want, c.why)
		}
	}
}

// 这一条防的是「比大小不对称」：如果 a>b 与 b<a 不同时成立，
// 更新提示会在某些版本对上下摇摆。
func TestCompareAntisymmetry(t *testing.T) {
	vs := []string{"3.4.0", "3.4.1", "3.5.0", "3.4.0-rc1", "v3.4.10", "4.0"}
	for _, a := range vs {
		for _, b := range vs {
			x, y := Compare(a, b), Compare(b, a)
			if x != -y {
				t.Errorf("Compare(%q,%q)=%d 但反向是 %d", a, b, x, y)
			}
		}
	}
}

func TestCurrent_DevWhenEmpty(t *testing.T) {
	old := Version
	Version = ""
	defer func() { Version = old }()

	info := Current()
	if info.Version != "dev" || !info.Dev {
		t.Fatalf("Version 为空时应归一成 dev，实际 %+v", info)
	}
}

func TestCurrent_NormalizesWhitespace(t *testing.T) {
	old := Version
	Version = "  3.4.0\n"
	defer func() { Version = old }()

	info := Current()
	if info.Version != "3.4.0" {
		t.Fatalf("应去掉空白，实际 %q", info.Version)
	}
	if info.Dev {
		t.Fatal("3.4.0 不是 dev 构建")
	}
}
