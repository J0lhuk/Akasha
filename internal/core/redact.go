package core

import "regexp"

var privateKey = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]*PRIVATE KEY)-----.*?-----END (?:[A-Z ]*PRIVATE KEY)-----`)
var knownToken = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[A-Z0-9]{16})\b`)
var assignment = regexp.MustCompile(`(?im)((?:[a-z0-9_]*(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token))["']?\s*[:=]\s*)(?:"[^"\r\n]+"|'[^'\r\n]+'|[^\s,;\r\n]+)`)
var bearer = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]{16,}={0,2}`)
var urlPassword = regexp.MustCompile(`(https?://[^\s/:]+:)[^\s/@]+@`)

// Redact is a best-effort ingestion filter, not a substitute for the encrypted vault.
// Raw files and arbitrary prose may contain credentials that no pattern can identify.
func Redact(s string) string {
	s = privateKey.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = knownToken.ReplaceAllString(s, "[REDACTED TOKEN]")
	s = assignment.ReplaceAllString(s, "${1}[REDACTED]")
	s = bearer.ReplaceAllString(s, "${1}[REDACTED]")
	return urlPassword.ReplaceAllString(s, "${1}[REDACTED]@")
}
