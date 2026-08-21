// Environment screening for the kiro-cli process this package spawns.
//
// DIRECTION MATTERS, and this file guards the opposite one from
// agent/agent_terminal_env.go. That file screens what the AGENT may INJECT into a
// terminal it asks vibekit to run: names that redirect execution (LD_PRELOAD,
// PATH, GIT_SSH_COMMAND), refused wholesale so a per-command approval cannot be
// turned into approval of something else. This file screens what the bridge
// INHERITS from the server's own environment on its way DOWN to kiro-cli and
// everything kiro-cli runs: names that carry a credential. Neither list belongs
// in the other, and a name moved between them would guard nothing.
//
// It is DEFENCE IN DEPTH, not a boundary. Nothing in the shipped image puts a
// credential in this environment (forge tokens live in each CLI's own store,
// KAS's OAuth blobs live in secretstore, SSO lives under $HOME/.aws), and the
// agent is trusted to run compilers and package managers. What this closes is
// the operator's compose file growing a `GITHUB_TOKEN:` line for some unrelated
// reason and thereby handing every agent turn a credential it was never given.
// So a name it misses is not a failure of the design: the design is that a
// credential should not be here in the first place, and this makes the common
// spellings inert if one is.
//
// DENYLIST rather than allowlist, deliberately. The agent's children are
// compilers, package managers, linkers and git, and they read a broad and
// unenumerable ambient environment (GOFLAGS, GOMODCACHE, CARGO_HOME, npm's
// several dozen npm_config_*, CC, CFLAGS, LANG, TERM, TZ). An allowlist here
// would break the work the app exists for on its first unlisted name, and the
// person who hit it would reach for the override rather than the list.
//
// A DROP, not a refusal, and that is the other half of the asymmetry with the
// agent. There the request has a requester, so refusing it makes the agent's next
// move a corrected one. Here the input is the operator's own container
// environment and there is nobody to refuse: refusing would mean refusing to
// start a chat. So the variable is dropped and the drop is LOGGED by name, which
// is the only honest form the notice can take when the values are the secret.

package bridge

import "strings"

// EnvAllowVar is the operator override: a comma-separated list of names to pass
// through anyway. Exported because composition reads it (this package reads no
// environment of its own, following ParseACPArgs on the same seam).
//
// An override exists for the same reason the runtime's does. A denylist keyed on name
// shape will eventually catch a name that is not a credential — a build flag
// spelled `*_SECRET`, a service discovery variable ending `_TOKEN` — and a guard
// with no way past it becomes a guard people disable.
const EnvAllowVar = "VIBEKIT_ALLOW_BRIDGE_ENV"

// credentialEnvSuffixes catch the two shapes that are a credential by
// convention across every ecosystem: `*_TOKEN` and `*_SECRET`.
//
// The suffix rules are what make this list short enough to be correct. Every
// forge and registry spelling worth naming ends in one of them —  GH_TOKEN,
// GITHUB_TOKEN, GH_ENTERPRISE_TOKEN, GITLAB_TOKEN, CI_JOB_TOKEN,
// GITEA_SERVER_TOKEN, NPM_TOKEN, NODE_AUTH_TOKEN, AWS_SESSION_TOKEN,
// AWS_SECURITY_TOKEN, and any `*_CLIENT_SECRET` — so enumerating them as exact
// entries would be a list to maintain that asserts the same thing twice.
// TestScreenBridgeEnv_DropsEveryNameTheDecisionNames pins each of those names
// individually, which is where the enumeration belongs.
//
// Matched EXACTLY on case, not folded, for the same reason the runtime's list is:
// POSIX environments are case-sensitive, so a case variant is a different
// variable that no consumer reads rather than a bypass, and folding would only
// drop harmless names.
var credentialEnvSuffixes = []string{"_TOKEN", "_SECRET"}

// credentialEnvNames are the credential-bearing names the suffix rules cannot
// reach. Both are AWS long-term credentials, whose spellings end in `_ID` and
// `_KEY`; neither suffix can be a rule of its own without dropping ordinary
// variables (a build id, a cache key, a public key path).
//
// AWS_REGION, AWS_PROFILE and AWS_DEFAULT_REGION are deliberately absent. They
// are configuration rather than credentials, and an `AWS_` prefix rule would
// take them along with these two, breaking `aws` for the agent while protecting
// nothing.
var credentialEnvNames = map[string]struct{}{
	"AWS_ACCESS_KEY_ID":     {},
	"AWS_SECRET_ACCESS_KEY": {},
}

// ParseEnvAllowlist turns the operator's comma-separated EnvAllowVar value into
// a set. Nil for a blank or all-blank input.
//
// Exported and separate from any reading of the environment so composition owns
// the read (the ParseACPArgs shape) and the parsing is directly testable.
func ParseEnvAllowlist(raw string) map[string]struct{} {
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

// isCredentialEnv reports whether name carries a credential by its shape.
func isCredentialEnv(name string, allowed map[string]struct{}) bool {
	if _, ok := allowed[name]; ok {
		return false
	}
	if _, ok := credentialEnvNames[name]; ok {
		return true
	}
	for _, suffix := range credentialEnvSuffixes {
		// The suffix must not be the whole name: a variable literally called
		// `_TOKEN` is nothing anyone reads, and matching it would be the one
		// case where the rule fires on no name at all.
		if len(name) > len(suffix) && strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// screenBridgeEnv composes the environment for the kiro-cli spawn: inherited
// minus credential-shaped names, then extra appended unfiltered. It also
// returns the dropped NAMES, in inherited order, for the caller to log.
//
// extra is exempt on purpose. It is vibekit's own overlay (the install
// manager's active version directory leading PATH), constructed in this process
// rather than inherited, and os/exec keeps the LAST value for a repeated key —
// so filtering the concatenation could silently drop an overlay entry this
// server deliberately set and leave PATH resolving out of the wrong install.
// The threat this guards is what came IN.
//
// A pure function of its inputs, taking the allowlist as a parameter, so every
// branch is reachable from a test without touching the process environment.
func screenBridgeEnv(inherited, extra []string, allowed map[string]struct{}) (env, dropped []string) {
	env = make([]string, 0, len(inherited)+len(extra))
	for _, kv := range inherited {
		// A `KEY=` with an empty value is still an assignment, and an entry with
		// no `=` at all is not one — os.Environ has been observed to carry such
		// a thing on exotic platforms, and it is classified by its whole text
		// rather than skipped.
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			name = kv
		}
		if isCredentialEnv(name, allowed) {
			dropped = append(dropped, name)
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...), dropped
}
