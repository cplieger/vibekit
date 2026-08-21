package vibekit

// Whether this account can actually use a model id.
//
// kiro-cli accepts a model id it cannot serve. The `--model` launch flag and
// `session/set_config_option` both succeed locally, and only the SERVICE rejects
// it, mid-prompt, on every later turn. So the id has to be checked against what
// the session advertised before it goes on the wire, and the check has to be ONE
// predicate: upstream (KiroCrew #1596 / #1549 / #1550) found that a picker, a
// validator and a wire path with three separate opinions is how a client ends up
// sending something it just refused to display.
//
// vibekit's exposure is a persisted value rather than a user's live pick.
// `chat.Model` is written from the client's `last_model`, a cross-device setting
// restored at startup with no list check, so an id that was valid under a
// previous entitlement rides the launch flag at every spawn of every new chat.

import "slices"

// ModelServed reports whether id is one the session can actually run.
//
// The two fail-open cases are deliberate and neither is laziness:
//
//   - An EMPTY served set means the session advertised no catalog, so entitlement
//     is unknowable and the send must proceed. A backend that omits the list must
//     behave exactly as it did before this check existed.
//   - An EMPTY id means "inherit the backend default" (vibekit never sends `auto`
//     or a blank on the wire; both mean send no model at all), so there is
//     nothing to validate.
//
// served must be the UNFILTERED advertised set (ACPBridge.ServedModels, not
// Models). Passing the display list would refuse a deprecated model the account
// can still use, which converts a working session into a client-side refusal:
// worse than the defect this prevents.
//
// Namespace mismatch, which upstream had to carve a whole backend out for, is not
// guarded here because it cannot arise: every id vibekit sends originated in KAS's
// own catalog, either through the picker or through `last_model`, which the picker
// wrote. If a future catalog changes id format, this check starts refusing
// everything at once and loudly rather than silently, which is the failure
// direction to prefer.
func ModelServed(id string, served []string) bool {
	if id == "" || id == ModelAuto || len(served) == 0 {
		return true
	}
	return slices.Contains(served, id)
}
