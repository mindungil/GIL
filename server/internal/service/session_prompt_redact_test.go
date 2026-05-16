package service

import (
	"strings"
	"testing"
)

// iter93a regression: dotenv values with provider-named keys (no
// "key"/"token"/"secret" substring) must still be redacted when the
// value itself has a known secret-prefix shape.
func TestLooksLikeSecretValue(t *testing.T) {
	cases := []struct {
		val  string
		want bool
		name string
	}{
		// Synthetic prefixed values — never copy real keys into tests.
		{"sk-or-v1-" + strings.Repeat("X", 32), true, "openrouter shape"},
		{"sk_" + strings.Repeat("X", 32), true, "pollinations sk_ shape"},
		{"sk-ant-" + strings.Repeat("X", 32), true, "anthropic shape"},
		{"ghp_" + strings.Repeat("X", 20), true, "github personal shape"},
		{"AIza" + strings.Repeat("X", 32), true, "google api shape"},
		{"eyJ" + strings.Repeat("X", 32), true, "jwt shape"},
		{"glpat-" + strings.Repeat("X", 20), true, "gitlab pat shape"},

		// Negative cases: ordinary config values must not be flagged.
		{"vim", false, "EDITOR"},
		{"en_US.UTF-8", false, "LANG"},
		{"sk-tooshort", false, "prefix but under 16 chars"},
		{"https://example.com/api/v1/resource", false, "URL"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeSecretValue(c.val); got != c.want {
				t.Fatalf("looksLikeSecretValue(%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}
}
