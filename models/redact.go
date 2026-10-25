package models

import "regexp"

type redaction struct {
	re   *regexp.Regexp
	repl string
}

// redactions run in order on every journal message before it leaves the
// daemon (docs/API.md, Logs). Best effort: they catch credentials printed in
// a URL or a key/value pair, not every secret.
var redactions = []redaction{
	{regexp.MustCompile(`(?i)://[^/\s@:]+:[^@\s/]+@`), `://***@`},
	{regexp.MustCompile(`(?i)([?&])(\w*(?:pass|pwd|token|key|secret|session|auth|user)\w*)=[^&\s"']*`), `${1}${2}=***`},
	{regexp.MustCompile(`(?i)\b(cn_pass|cn_user|calaos_password|calaos_user|password|passwd|pass|pwd|secret|token|api_key|apikey|auth_token|mcp_token|mcp_service_token|smtp_password|influxdb_token|admin_pass|new_pass|admin_session|x-calaos-admin-session|session|psk|private_key|push_key|code)["']?\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`), `${1}=***`},
	{regexp.MustCompile(`(?i)authorization:\s*bearer\s+\S+`), `authorization: bearer ***`},
	{regexp.MustCompile(`(?i)Calaos admin code \d{6}`), `Calaos admin code ******`},
}

// Redact masks the credentials a log line may carry.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
