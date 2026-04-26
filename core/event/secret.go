package event

import (
	"regexp"
	"strings"
)

// secretPatterns matches common secret formats. Keep this list small and
// high-precision to avoid false positives.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-(?:api|oat)\d?-[A-Za-z0-9_-]{20,}`),  // Anthropic API + OAuth tokens
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{32,}`),                      // OpenAI / generic sk-* keys
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),                       // GitHub personal access token
	regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[:=]\s*["']?([^\s"']{8,})`),
	regexp.MustCompile(`(?i)bearer\s+([A-Za-z0-9._\-+/=]{20,})`),     // Authorization: Bearer <token>
}

const secretReplacement = "<secret_hidden>"

// MaskSecrets returns s with detected secrets replaced by <secret_hidden>.
// For patterns with capture groups (password=..., bearer ...), only the
// secret value is masked; the surrounding context (e.g., "password=") is kept.
func MaskSecrets(s string) string {
	out := s
	for _, re := range secretPatterns {
		// If pattern has a capture group, replace only the captured portion.
		if re.NumSubexp() > 0 {
			out = re.ReplaceAllStringFunc(out, func(match string) string {
				// Find the submatch and replace it within the match
				sub := re.FindStringSubmatch(match)
				if len(sub) < 2 {
					return secretReplacement
				}
				return strings.Replace(match, sub[1], secretReplacement, 1)
			})
		} else {
			out = re.ReplaceAllString(out, secretReplacement)
		}
	}
	return out
}
