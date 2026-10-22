package netroute

import "regexp"

var secretJSON = regexp.MustCompile(`(?i)("(?:access_?token|refresh_?token|id_?token|token|auth_?code|authCodeInfo|authorization|cookie|set-cookie|nonce|verifier|code_?verifier|device_?private_?key)"\s*:\s*)"(?:[^"\\]|\\.)*"`)
var secretKV = regexp.MustCompile(`(?i)((?:access_?token|refresh_?token|id_?token|token|auth_?code|nonce|verifier|code_?verifier)\s*[=:]\s*)([^&\s,;"'<>]+)`)
var secretJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]{4,}){1,2}\b`)
var secretAuthorization = regexp.MustCompile(`(?i)(?:Bearer|Cloud-IDE-JWT)\s+[A-Za-z0-9._+/=-]+`)

// Redact is for public errors/logs, not business-code classification input.
func Redact(s string) string {
	s = secretJSON.ReplaceAllString(s, `${1}"***"`)
	s = secretKV.ReplaceAllString(s, `${1}***`)
	s = secretAuthorization.ReplaceAllString(s, "Authorization ***")
	return secretJWT.ReplaceAllString(s, "***jwt***")
}
