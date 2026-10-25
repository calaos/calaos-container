package models

import (
	"regexp"
	"strings"
)

// need is a literal pre-check on the message and its lower-case form: a
// rule whose trigger is absent is skipped, as the regexes are the cost of a
// query. Replacements never add a trigger for a later rule.
type redaction struct {
	need func(s, lower string) bool
	re   *regexp.Regexp
	repl string
}

func has(lower string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

func hasKV(s string) bool { return strings.ContainsAny(s, "=:") }

func needUserinfo(s, _ string) bool { return strings.Contains(s, "://") && strings.Contains(s, "@") }

func needQuery(s, _ string) bool { return strings.Contains(s, "=") && strings.ContainsAny(s, "?&") }

func needKey(s, lower string) bool {
	return hasKV(s) && has(lower, "pass", "pwd", "secret", "token", "key", "session", "psk", "code", "cn_user", "calaos_user")
}

func needCamel(s, _ string) bool {
	return hasKV(s) && has(s, "Pass", "Pwd", "Secret", "Token", "Key", "Session", "Psk")
}

func needAuth(_, lower string) bool   { return strings.Contains(lower, "authorization:") }
func needCookie(_, lower string) bool { return strings.Contains(lower, "cookie:") }
func needCode(_, lower string) bool   { return strings.Contains(lower, "calaos admin code ") }

// The value of a key/value pair: quoted, or up to the next blank.
const redactValue = `["']?\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`

// redactions run in order on every journal message before it leaves the
// daemon (docs/API.md, Logs). Best effort: they catch credentials printed in
// a URL, a header or a key/value pair, not every secret.
var redactions = []redaction{
	// The password runs to the last "@" of the token, so a "/" or an "@"
	// in it is masked too.
	{needUserinfo, regexp.MustCompile(`(?i)://[^/\s]+:\S+@`), `://***@`},
	{needQuery, regexp.MustCompile(`(?i)([?&])(\w*(?:pass|pwd|token|key|secret|session|auth|user)\w*)=[^&\s"']*`), `${1}${2}=***`},
	// A listed key may carry a prefix (mqtt_password, wifi-psk); a bare
	// "key" or "code" only as listed, being common words.
	{needKey, regexp.MustCompile(`(?i)\b((?:[\w-]*[_-])?(?:cn_pass|cn_user|calaos_password|calaos_user|password|passwd|pass|pwd|secret|token|api_key|apikey|auth_token|mcp_token|mcp_service_token|smtp_password|influxdb_token|admin_pass|new_pass|admin_session|x-calaos-admin-session|session|psk|private_key|push_key)|[\w-]*[_-]key|code)` + redactValue), `${1}=***`},
	// camelCase keys (adminPassword, apiToken), case-sensitive on purpose.
	{needCamel, regexp.MustCompile(`\b([a-z][a-zA-Z0-9]*(?:Password|Passwd|Pass|Pwd|Secret|Token|Key|Session|Psk))` + redactValue), `${1}=***`},
	{needAuth, regexp.MustCompile(`(?i)((?:proxy-)?authorization:\s*(?:bearer|basic|digest))\s+[^\r\n]+`), `${1} ***`},
	{needCookie, regexp.MustCompile(`(?i)\b((?:set-)?cookie:)\s*[^\r\n]+`), `${1} ***`},
	{needCode, regexp.MustCompile(`(?i)Calaos admin code \d{6}`), `Calaos admin code ******`},
}

// Redact masks the credentials a log line may carry.
func Redact(s string) string {
	lower := strings.ToLower(s)
	for _, r := range redactions {
		if r.need(s, lower) {
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return s
}
