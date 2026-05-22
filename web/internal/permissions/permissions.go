// Package permissions resolves kiro-cli permission args from the user's
// settings.json. Reads fresh on every call so UI toggles take effect on
// the next bridge spawn without a server restart.
//
// Settings schema (new fields, all optional):
//
//	{
//	  "permission_mode": "prompt" | "trust-list" | "trust-all",
//	  "trust_tools":     ["fsWrite", "executePwsh", ...]
//	}
//
// Fail-mode philosophy (intentional asymmetry between readers):
//
//   - Args / read: fail OPEN to "--trust-all-tools" on any
//     read/parse error. Matches the previous hard-coded behaviour
//     and avoids silently flooding the user with prompts on a fresh
//     install. A typo in permission_mode is therefore a permissive
//     failure, not a restrictive one; surfaced via slog.Warn.
//   - readShellPolicy: fail CLOSED to safe_commands on any error.
//     safe_commands is the default-and-safest policy, so a corrupt
//     settings.json defaults to prompting for destructive commands.
//   - SupervisedDefault: fail CLOSED to false. Supervised mode is
//     opt-in; a user who never touches Settings must not suddenly
//     get every write gated on approval because of a parse error.
//
// Do not "fix" the asymmetry without revisiting each reader's
// semantics. Args' permissive fallback is a UX decision (no surprises
// on fresh install); the others' restrictive fallbacks are safety
// decisions.
package permissions
