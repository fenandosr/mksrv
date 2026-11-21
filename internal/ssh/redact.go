// SPDX-License-Identifier: Apache-2.0

package ssh

import "regexp"

// RedactCommand masks secrets in a command line before it is quoted in an error
// message. mksrv builds commands that carry credentials in their text (an
// OpenBao token in `-e BAO_TOKEN=…`, `kv put … password=…`, a connection URL), and
// an error that quotes the command would print them to the terminal and into any
// log. It is deliberately broad: a false positive costs a readable word, a miss
// leaks a credential.
func RedactCommand(command string) string {
	for _, p := range secretPatterns {
		command = p.re.ReplaceAllString(command, p.repl)
	}
	return command
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Environment assignments that carry a credential: keep the name, drop the value.
	{regexp.MustCompile(`(?i)((?:BAO_TOKEN|VAULT_TOKEN|PGPASSWORD|AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN)=)('[^']*'|"[^"]*"|\S+)`), "${1}<redacted>"},
	// key=value arguments whose key names a secret (`password=…`, `secret_id=…`).
	{regexp.MustCompile(`(?i)((?:password|passwd|secret_id|secret_access_key|client_secret|api_key|authkey|token)=)('[^']*'|"[^"]*"|[^\s']+)`), "${1}<redacted>"},
	// The password of a connection URL: scheme://user:PASSWORD@host.
	{regexp.MustCompile(`(://[^/\s:@']+:)[^@\s']+(@)`), "${1}<redacted>${2}"},
}
