// Package permissions evaluates vibekit's own permission controls from
// the user's config.json: the shell-command safety policy (auto-approve
// / prompt / deny for execute_bash) and the Supervised-mode default.
// Reads fresh on every call so UI toggles take effect on the next
// evaluation without a server restart.
//
// Note: tool-call authorization on v3 (KAS) is owned by kiro-cli's
// native Cedar policy engine (surfaced/edited via GET|POST
// /api/permissions). The old imperative "trust-all / trust-list /
// prompt" mode (the --trust-all-tools / --trust-tools kiro-cli spawn
// flags) was removed in 2026-07: those flags are inert on v3 (the
// engine ignores them; the permission prompt fires regardless), and
// they fed nothing else. Cedar is the real approval surface now.
//
// Settings schema (all optional):
//
//	{
//	  "shell_policy":       "no_commands" | "safe_commands" | "all_commands",
//	  "supervised_default": true | false
//	}
//
// Fail-mode philosophy (both readers fail CLOSED):
//
//   - readShellPolicy: fail CLOSED to safe_commands on any error.
//     safe_commands is the default-and-safest policy, so a corrupt
//     config.json defaults to prompting for destructive commands.
//   - SupervisedDefault: fail CLOSED to false. Supervised mode is
//     opt-in; a user who never touches Settings must not suddenly
//     get every write gated on approval because of a parse error.
//
// Do not relax either fallback without revisiting its safety
// semantics.
package permissions
