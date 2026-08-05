package hub

import (
	"encoding/json"
	"testing"
)

// TestTriggerResultPrefersTheCallbackOutput pins the ordering that decides
// whether a failed hook's output reaches the user.
//
// KAS sets `failed: exitCode !== 0` and then reports `success: false`, so a hook
// whose command exits non-zero arrives with success false AND a populated
// executeHook callback result. Checking the success flag first threw the callback
// result away and returned a bare error, on precisely the run the user clicked
// "Run now" to diagnose. A genuine trigger failure never issues the callback, so
// the presence of a callback result is the distinguisher.
//
// This asserts on the decision rather than on the wire: parseHookResult and the
// callback store are separately covered, and what regressed was which of the two
// is consulted first.
func TestTriggerResultPrefersTheCallbackOutput(t *testing.T) {
	// KAS's reply for a command that ran and exited non-zero.
	raw := json.RawMessage(`{"success":false,"code":"HOOK_FAILED","error":"hook failed"}`)
	res := parseHookResult(raw)
	if res.Success {
		t.Fatal("fixture is wrong: KAS reports success:false for a non-zero exit")
	}

	// With a callback result present, the run outcome must win over the flag.
	run := &hookRunResult{Output: "make: *** [build] Error 1", ExitCode: 2, Ran: true}
	chosen, err := chooseHookOutcome(res, run)
	if err != nil {
		t.Fatalf("a run that produced output returned an error (%v); its output is discarded", err)
	}
	if chosen.Output != run.Output || chosen.ExitCode != 2 {
		t.Errorf("outcome = %+v, want the callback result %+v", chosen, *run)
	}

	// With no callback result, the flag is all there is and must surface.
	if _, err := chooseHookOutcome(res, nil); err == nil {
		t.Error("a trigger that never ran the command returned no error; " +
			"a session-gone or refused trigger must not look like a clean run")
	}
}
