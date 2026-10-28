package fname

import "testing"

func TestRenderBasic(t *testing.T) {
	rec := map[string]interface{}{"项目编号": "AP-001"}
	got := Render("{{项目编号}}_审计报告", rec, 0, "audit-report")
	if got != "AP-001_审计报告" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderIllegalChars(t *testing.T) {
	rec := map[string]interface{}{"p": `a/b\c:d*e?f"g<h>i|j`}
	got := Render("{{p}}", rec, 2, "c")
	for _, bad := range []string{`\`, "/", ":", "*", "?", `"`, "<", ">", "|"} {
		if contains(got, bad) {
			t.Fatalf("illegal char %q remains in %q", bad, got)
		}
	}
}

func TestRenderMissingVarFallback(t *testing.T) {
	got := Render("{{不存在}}_报告", map[string]interface{}{}, 3, "audit-report")
	if got != "3_报告" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderEmptyRuleFallback(t *testing.T) {
	got := Render("", map[string]interface{}{}, 7, "audit-report")
	if got != "audit-report_007" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderLongTruncate(t *testing.T) {
	rec := map[string]interface{}{"p": stringRepeat("中", 200)}
	got := Render("{{p}}", rec, 0, "c")
	if len([]rune(got)) > 120 {
		t.Fatalf("not truncated: %d", len([]rune(got)))
	}
}

func TestWithExt(t *testing.T) {
	if WithExt("a", "pdf") != "a.pdf" {
		t.Error("pdf ext")
	}
	if WithExt("a", "both") != "a.docx" {
		t.Error("both ext")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func stringRepeat(c string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += c
	}
	return out
}
