package herdrapp

import "strings"

// ScopeVariable names the token that herdr-core puts in the environment of
// each terminal and of each command that hc exec runs. When that terminal or
// command ends, herdr-core ends every process that has the token in its
// environment, and the processes that they started. This includes a process
// that left the session.
const ScopeVariable = "HC_SCOPE"

// WithoutScope returns environment without ScopeVariable. A process that must
// continue after the terminal or the command that started it ends needs this
// environment, for example the daemon, a background job or a browser.
func WithoutScope(environment []string) []string {
	out := make([]string, 0, len(environment))
	for _, value := range environment {
		if key, _, _ := strings.Cut(value, "="); key != ScopeVariable {
			out = append(out, value)
		}
	}
	return out
}
