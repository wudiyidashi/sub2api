package runner

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const failureMessageLimit = 512

var (
	failureURLPattern   = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"']+`)
	failureAuthPattern  = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[^\s,;"']+`)
	failureTokenPattern = regexp.MustCompile(`\b[A-Za-z0-9_.~+/=-]{24,}\b`)
)

// Diagnostics originate as fixed classifications in the driver. Redaction here
// is a second boundary: never expose raw endpoints, credentials or RPC identity.
func safeFailureMessage(message string) string {
	message = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) || unicode.IsSpace(value) || unicode.Is(unicode.Cf, value) {
			return ' '
		}
		return value
	}, message)
	message = failureURLPattern.ReplaceAllString(message, "[endpoint]")
	message = failureAuthPattern.ReplaceAllString(message, "[credential]")
	message = logredact.RedactText(message, "authorization", "proxy-authorization", "api_key", "api-key", "token", "chatgpt-account-id", "session_id", "credential_fingerprint")
	message = failureTokenPattern.ReplaceAllString(message, "[redacted]")
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > failureMessageLimit {
		message = string(runes[:failureMessageLimit-3]) + "..."
	}
	return message
}
