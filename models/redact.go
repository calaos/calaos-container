package models

import "regexp"

type redaction struct {
	re   *regexp.Regexp
	repl string
}

// The value of a key/value pair: quoted, or up to the next blank.
const redactValue = `["']?\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`

// redactions run in order on every journal message before it leaves the
// daemon (docs/API.md, Logs). Best effort: they catch credentials printed in
// a URL, a header or a key/value pair, not every secret.
var redactions = []redaction{
	// The password runs to the last "@" of the token, so a "/" or an "@"
	// in it is masked too.
	{regexp.MustCompile(`(?i)://[^/\s]+:\S+@`), `://***@`},
	{regexp.MustCompile(`(?i)([?&])(\w*(?:pass|pwd|token|key|secret|session|auth|user)\w*)=[^&\s"']*`), `${1}${2}=***`},
	// A listed key may carry a prefix (mqtt_password, wifi-psk); a bare
	// "key" or "code" only as listed, being common words.
	{regexp.MustCompile(`(?i)\b((?:[\w-]*[_-])?(?:cn_pass|cn_user|calaos_password|calaos_user|password|passwd|pass|pwd|secret|token|api_key|apikey|auth_token|mcp_token|mcp_service_token|smtp_password|influxdb_token|admin_pass|new_pass|admin_session|x-calaos-admin-session|session|psk|private_key|push_key)|[\w-]*[_-]key|code)` + redactValue), `${1}=***`},
	// camelCase keys (adminPassword, apiToken), case-sensitive on purpose.
	{regexp.MustCompile(`\b([a-z][a-zA-Z0-9]*(?:Password|Passwd|Pass|Pwd|Secret|Token|Key|Session|Psk))` + redactValue), `${1}=***`},
	{regexp.MustCompile(`(?i)((?:proxy-)?authorization:\s*(?:bearer|basic|digest))\s+[^\r\n]+`), `${1} ***`},
	{regexp.MustCompile(`(?i)\b((?:set-)?cookie:)\s*[^\r\n]+`), `${1} ***`},
	{regexp.MustCompile(`(?i)Calaos admin code \d{6}`), `Calaos admin code ******`},
}

// Redact masks the credentials a log line may carry.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
