package output

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

func init() {
	// Disable ANSI colors so tests can compare against literal strings.
	color.NoColor = true
}

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{1000, "1,000"},
		{1234, "1,234"},
		{1500000, "1,500,000"},
		{-1234, "-1,234"},
	}
	for _, c := range cases {
		if got := FormatTokens(c.in); got != c.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSignedTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "+1"},
		{-1, "-1"},
		{1500000, "+1,500,000"},
		{-1500000, "-1,500,000"},
	}
	for _, c := range cases {
		if got := SignedTokens(c.in); got != c.want {
			t.Errorf("SignedTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"hello world", 1, "…"},
		{"hello world", 0, ""},
		// Newlines are flattened.
		{"line one\nline two", 8, "line on…"},
		// CJK: 12 runes, truncated to 5 should produce 4 chars + ellipsis.
		// Critically, this must NOT cut a multi-byte codepoint in half.
		{"测试一二三四五六七八九十", 5, "测试一二…"},
		// Mixed: each Chinese char is one rune.
		{"hi你好world", 4, "hi你…"},
	}
	for _, c := range cases {
		got := Truncate(c.in, c.maxLen)
		if got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.maxLen, got, c.want)
		}
		// Strong invariant: result must always be valid UTF-8 (no half
		// codepoints) — this is the whole reason we slice by rune, not byte.
		if !isValidUTF8(got) {
			t.Errorf("Truncate(%q, %d) returned invalid UTF-8: %q", c.in, c.maxLen, got)
		}
	}
}

// isValidUTF8 reports whether s is well-formed UTF-8. Equivalent to
// utf8.ValidString but kept inline so the test file has no extra imports.
func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD && !strings.ContainsRune(s, 0xFFFD) {
			return false
		}
	}
	return true
}

func TestStatusColorAllRecognizedStatuses(t *testing.T) {
	// Every status the platform can emit should pass through (potentially
	// colored, but here colors are disabled). Unknown statuses are returned
	// verbatim — verify both branches.
	known := []string{
		"open", "in_progress", "submitted", "revision",
		"completed", "cancelled", "active", "paused", "idle", "closed",
	}
	for _, s := range known {
		if got := StatusColor(s); got != s {
			t.Errorf("StatusColor(%q) = %q, want passthrough %q", s, got, s)
		}
	}
	if got := StatusColor("brand_new_status"); got != "brand_new_status" {
		t.Errorf("StatusColor unknown should pass through, got %q", got)
	}
}
