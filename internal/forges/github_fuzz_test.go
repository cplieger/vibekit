package forges

import "testing"

// FuzzClassifyGHCheck verifies that folding one rollup entry always lands
// in the canonical check vocabulary. Both shapes GitHub returns in that
// one array (a CheckRun's status+conclusion and a StatusContext's state)
// are fuzzed together, because the decoder reads all three keys off every
// entry and cannot rely on __typename being the one it expects.
//
// Bug class: a new upstream state word leaking through as a chip value
// the client has no branch for, or an unrecognised word classifying as a
// PASS, which would paint a green chip over an unknown state.
func FuzzClassifyGHCheck(f *testing.F) {
	f.Add("COMPLETED", "SUCCESS", "")
	f.Add("COMPLETED", "FAILURE", "")
	f.Add("COMPLETED", "SKIPPED", "")
	f.Add("IN_PROGRESS", "", "")
	f.Add("QUEUED", "", "")
	f.Add("", "", "SUCCESS")
	f.Add("", "", "PENDING")
	f.Add("", "", "FAILURE")
	f.Add("", "", "")
	f.Add("completed", "success", "success")

	f.Fuzz(func(t *testing.T, status, conclusion, state string) {
		got := classifyGHCheck(status, conclusion, state)
		switch got {
		case checkPassing, checkPending, checkFailing, "":
			// ok
		default:
			t.Fatalf("classifyGHCheck(%q,%q,%q) = %q; outside the canonical set",
				status, conclusion, state, got)
		}
		// A pass must be claimed by an explicit success word, never
		// inferred from an unrecognised one.
		if got == checkPassing && !equalFoldAny(conclusion, "SUCCESS") && !equalFoldAny(state, "SUCCESS") {
			t.Fatalf("classifyGHCheck(%q,%q,%q) reported a pass with no SUCCESS word",
				status, conclusion, state)
		}
	})
}

// FuzzMapGHMergeState verifies the merge-block cause stays inside the
// vocabulary the client renders prose for.
//
// Bug class: an unmapped mergeStateStatus reaching the row as a raw
// upstream enum, which would disable Merge with an empty tooltip.
func FuzzMapGHMergeState(f *testing.F) {
	f.Add("CLEAN", "")
	f.Add("BLOCKED", checkFailing)
	f.Add("BLOCKED", checkPending)
	f.Add("BLOCKED", "")
	f.Add("DIRTY", "")
	f.Add("DRAFT", "")
	f.Add("BEHIND", "")
	f.Add("UNSTABLE", checkFailing)
	f.Add("UNKNOWN", "")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, mergeState, checkStatus string) {
		got := mapGHMergeState(mergeState, checkStatus)
		switch got {
		case "", blockDraft, blockConflicts, blockChecksFailing,
			blockChecksRunning, blockBehind, blockProtected, blockUnknown:
			// ok
		default:
			t.Fatalf("mapGHMergeState(%q,%q) = %q; outside the canonical set",
				mergeState, checkStatus, got)
		}
	})
}

// FuzzIsHexSHA verifies the head-commit boundary check cannot be talked
// into accepting something that is not an object id. The pin travels into
// a subprocess argv and a JSON body, so a false accept is the input that
// matters.
//
// Bug class: a flag-shaped, whitespace-bearing or over-long value passing
// the gate.
func FuzzIsHexSHA(f *testing.F) {
	f.Add("abc1234")
	f.Add("5f2c1e4a9b8d7c6e5f4a3b2c1d0e9f8a7b6c5d4e")
	f.Add("--match-head-commit")
	f.Add("abc1234 --admin")
	f.Add("abc1234;rm -rf /")
	f.Add("../../etc/passwd")
	f.Add("")
	f.Add("ABCDEF1")

	f.Fuzz(func(t *testing.T, s string) {
		if !isHexSHA(s) {
			return
		}
		// Everything accepted must be a bounded, purely hexadecimal
		// string: no separators, no flag prefix, nothing a shell or a
		// flag parser could reinterpret.
		if len(s) < 7 || len(s) > 64 {
			t.Fatalf("isHexSHA(%q) accepted a %d-byte value", s, len(s))
		}
		for i := range len(s) {
			switch c := s[i]; {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'f':
			case c >= 'A' && c <= 'F':
			default:
				t.Fatalf("isHexSHA(%q) accepted a non-hex byte %q at %d", s, c, i)
			}
		}
	})
}

// equalFoldAny reports a case-insensitive match, kept local to the fuzz
// file so the production path carries no test-only helper.
func equalFoldAny(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range len(got) {
		a, b := got[i], want[i]
		if a >= 'a' && a <= 'z' {
			a -= 'a' - 'A'
		}
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
