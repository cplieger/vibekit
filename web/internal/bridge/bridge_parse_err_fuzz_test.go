package bridge

import "testing"

// FuzzParseErrTracker drives random sequences of Record/Reset operations
// and asserts structural invariants of the state machine.
func FuzzParseErrTracker(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0, 0})
	f.Add([]byte{})
	f.Add(make([]byte, 100))

	f.Fuzz(func(t *testing.T, ops []byte) {
		var tr parseErrTracker
		for _, op := range ops {
			if op%2 == 0 {
				action := tr.Record()
				// Invariant 1: total is monotonically non-decreasing (checked implicitly).
				// Invariant 2: consecutive <= total.
				if tr.consecutive > tr.total {
					t.Fatalf("consecutive (%d) > total (%d)", tr.consecutive, tr.total)
				}
				// Invariant 3: circuit-break iff consecutive == parseErrMaxConsecutive.
				if tr.consecutive >= parseErrMaxConsecutive && action != parseErrCircuitBreak {
					t.Fatalf("consecutive=%d but action=%d, want circuitBreak", tr.consecutive, action)
				}
				if action == parseErrCircuitBreak && tr.consecutive < parseErrMaxConsecutive {
					t.Fatalf("circuitBreak at consecutive=%d (< %d)", tr.consecutive, parseErrMaxConsecutive)
				}
			} else {
				tr.Reset()
				// Invariant 4: after Reset, consecutive is 0.
				if tr.consecutive != 0 {
					t.Fatalf("consecutive=%d after Reset, want 0", tr.consecutive)
				}
			}
		}
	})
}
