// Environment screening for terminal/create.
//
// Declaring terminal: true makes vibekit responsible for executing the agent's
// shell commands, and os/exec keeps the LAST value for a repeated env key, so an
// agent-supplied variable wins over the process environment. A handful of names
// redirect execution rather than carry data (LD_PRELOAD, PATH, ...), so an
// approved command with one of those set can run different code than the user
// approved. Authorization is per command (vibekit seeds zero Cedar rules), so
// this is what keeps that per-command approval meaningful.

package agent

import (
	"strings"
	"sync"

	"github.com/cplieger/envx/v2"
)

// envAllowVar lets an operator re-permit specific names, comma-separated (a
// preload-based profiler, a vendored NODE_PATH).
const envAllowVar = "VIBEKIT_ALLOW_AGENT_ENV"

// dangerousAgentEnv is upstream kiro-cli's own `dangerous_env_vars` list (read
// verbatim off the 2.18.1 binary) so the same agent behaves the same whether it
// runs through the TUI or through vibekit. Matched EXACTLY, not case-folded: POSIX
// env vars are case-sensitive and the loader reads LD_PRELOAD and nothing else.
//
// Grouped by how each one reaches execution:
var dangerousAgentEnv = map[string]struct{}{
	// Spawn a helper program.
	"PAGER": {}, "EDITOR": {}, "VISUAL": {}, "BROWSER": {}, "MANPAGER": {},
	"GIT_PAGER": {}, "LESS": {}, "LESSOPEN": {}, "LESSCLOSE": {},
	// Inject code into any dynamically linked process.
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {},
	"DYLD_INSERT_LIBRARIES": {}, "DYLD_LIBRARY_PATH": {},
	// Run code when an interpreter starts.
	"PYTHONWARNINGS": {}, "PYTHONSTARTUP": {}, "PYTHONPATH": {}, "PYTHONHOME": {},
	"PERL5OPT": {}, "PERL5LIB": {}, "RUBYOPT": {}, "RUBYLIB": {},
	"NODE_OPTIONS": {}, "NODE_PATH": {},
	// Change what a bare command name resolves to, or what the shell runs first.
	"IFS": {}, "PATH": {}, "HOME": {}, "SHELL": {}, "PROMPT_COMMAND": {},
	"BASH_ENV": {}, "ENV": {},
	// git's own hook and helper hooks, each of which takes a command.
	"GIT_EDITOR": {}, "GIT_SEQUENCE_EDITOR": {}, "GIT_ASKPASS": {},
	"GIT_EXTERNAL_DIFF": {}, "GIT_SSH": {}, "GIT_SSH_COMMAND": {},
	"GIT_PROXY_COMMAND": {}, "GIT_EXEC_PATH": {}, "GIT_TEMPLATE_DIR": {},
}

// safeAgentEnvValues are values that neutralise a dangerous name instead of
// arming it, also taken from upstream (`safe_env_values`). `GIT_PAGER=cat` and
// `PAGER=` are the ordinary way to stop git paging into a terminal nobody is
// watching, so blocking those would train users to disable the whole guard.
// `true` and `cat` are safe because they name a program that reads and exits.
var safeAgentEnvValues = map[string]struct{}{"": {}, "true": {}, "cat": {}}

// parseAllowedEnv turns the operator's comma-separated list into a set. Split out
// from the once-value below so it is directly testable without going through the
// environment (a sync.OnceValue resolves at most once per process).
func parseAllowedEnv(raw string) map[string]struct{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := make(map[string]struct{})
	for name := range strings.SplitSeq(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// operatorAllowedEnv reads envAllowVar once, lazily (nothing else in this
// package takes configuration).
var operatorAllowedEnv = sync.OnceValue(func() map[string]struct{} {
	return parseAllowedEnv(envx.String(envAllowVar))
})

// screenAgentEnv returns the names vibekit refuses to set for the agent, in the
// order the agent asked for them.
//
// Reports rather than filters: dropping a variable silently would leave the
// agent believing it had set one, reading later command output as evidence about
// a world that does not exist. Refusing the whole request makes the agent's next
// move a corrected one.
//
// Takes the allowlist as a PARAMETER so the decision is a pure function of its
// inputs and every case is reachable from a test; the caller supplies
// operatorAllowedEnv().
func screenAgentEnv(vars []termEnvVar, allowed map[string]struct{}) []string {
	var blocked []string
	for _, v := range vars {
		if _, dangerous := dangerousAgentEnv[v.Name]; !dangerous {
			continue
		}
		if _, inert := safeAgentEnvValues[v.Value]; inert {
			continue
		}
		if _, ok := allowed[v.Name]; ok {
			continue
		}
		blocked = append(blocked, v.Name)
	}
	return blocked
}
