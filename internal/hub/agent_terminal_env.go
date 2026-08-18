// Environment screening for terminal/create.
//
// When vibekit declares `terminal: true`, KAS stops executing the agent's shell
// commands itself and sends them here instead (ACPTerminalManager rather than
// DefaultTerminalManager, chosen off that capability). Claiming a capability is
// therefore a promise to service the request AND to apply the checks the layer we
// displaced was applying — the same rule that makes the fs trio confine rather
// than grant, and that puts an http/https test on _kiro/openExternalUrl.
//
// This file is the terminal half of that rule, and it was the one missing piece:
// termEnv layers the agent's requested variables on TOP of os.Environ(), and
// os/exec keeps the LAST value for a repeated key, so an agent-supplied entry
// wins over the process environment. A handful of names do not carry data, they
// redirect execution — so an approved `tar -xf backup.tar` with LD_PRELOAD set
// runs the agent's code instead. The user answered a prompt about tar and got
// something else, which defeats the one thing a per-command prompt is for.
//
// It matters BECAUSE authorization is per command. vibekit seeds zero Cedar rules
// so every sensitive call prompts, and the narrow grant a user gives from a
// permission card (or an unattended run's one-shot approval) is exactly what this
// bypasses.

package hub

import (
	"strings"
	"sync"

	"github.com/cplieger/envx/v2"
)

// envAllowVar lets an operator re-permit specific names, comma-separated. A dev
// box has legitimate uses (a preload-based profiler, a vendored NODE_PATH), and
// refusing them with no way back would make the guard something people work
// around by turning the agent loose elsewhere.
const envAllowVar = "VIBEKIT_ALLOW_AGENT_ENV"

// dangerousAgentEnv is upstream kiro-cli's own `dangerous_env_vars` list, read
// verbatim off the 2.18.1 binary rather than re-derived.
//
// Copied deliberately, for two reasons. It is the reference implementation, so
// the same agent behaves the same whether it runs through the TUI or through
// vibekit — a divergent list would be a second policy to keep in step. And the
// membership is not obvious: PYTHONWARNINGS and GIT_TEMPLATE_DIR both reach code
// execution by routes most readers would not guess, so a hand-written list would
// be shorter and wrong.
//
// Matched EXACTLY, not case-folded. POSIX environments are case-sensitive and the
// loader reads LD_PRELOAD and nothing else, so a case variant is inert rather than
// a bypass, and folding would only refuse harmless variables.
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
// arming it, also taken from upstream (`safe_env_values`).
//
// This is what keeps the guard usable rather than merely safe, and a name-only
// denylist would have been wrong here: `GIT_PAGER=cat` and `PAGER=` are the
// ordinary way to stop git paging into a terminal nobody is watching, so an agent
// running git hits them constantly. Blocking those would train a user to switch
// the whole guard off.
//
// `true` and `cat` are safe because they name a program that reads and exits;
// neither can be pointed at anything else, unlike `PAGER=sh -c …`.
var safeAgentEnvValues = map[string]struct{}{"": {}, "true": {}, "cat": {}}

// parseAllowedEnv turns the operator's comma-separated list into a set. Split out
// from the once-value below so the parsing is directly testable: a sync.OnceValue
// resolves at most once per process, so a test that drove it through the
// environment would pass or skip depending on which sibling ran first.
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

// operatorAllowedEnv reads envAllowVar once. Lazily rather than at construction
// because nothing else in this package takes configuration, and once because this
// keeps the per-request path a map lookup instead of a parse.
var operatorAllowedEnv = sync.OnceValue(func() map[string]struct{} {
	return parseAllowedEnv(envx.String(envAllowVar))
})

// screenAgentEnv returns the names vibekit refuses to set for the agent, in the
// order the agent asked for them.
//
// Reports rather than filters, deliberately. Dropping a variable silently would
// leave the agent believing it had set one, so it would read the command's
// output as evidence about a world that does not exist — and the operator would
// have no idea why a build behaved oddly. Refusing the whole request makes the
// agent's next move a corrected one.
//
// Takes the allowlist as a PARAMETER rather than reading the once-value itself, so
// the decision is a pure function of its inputs and every case is reachable from a
// test. The caller supplies operatorAllowedEnv().
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
