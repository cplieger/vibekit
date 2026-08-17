package hub

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// TestTranslateACPEvent_RefusesUnknownRequests is the red check for the wedge
// class, and it is the test whose absence let the defect ship.
//
// KAS calls its ext-methods with `await connection.extMethod(...)` and no
// timeout; the only rejection is the connection closing. vibekit's Bridge.Call
// has no client-side deadline either, deliberately, because a turn can
// legitimately run for hours. Those two facts compose into a failure that is
// worse than an error: an unanswered A→C request means the session/prompt Call
// never returns, bridgePrompting is never released, and every later prompt on
// that chat 409s into a client queue whose only drain is a turn_ended that will
// never fire. The chat is dead with a spinner and no diagnosis.
//
// The utility bridge and the run bridge both already had this fallback, each
// with the rationale in a comment. The chat dispatcher was the one of the three
// without it, which is the shape worth remembering: a guard present in two of
// three sibling paths reads as a convention until someone checks.
//
// _kiro/workspace/currently_open_files is the reachable case rather than a
// hypothetical: KAS registers that resolver with NO capability gate and reaches
// it from processPromptWithContext on any `#[[...]]` reference in a
// workspace-authored agent prompt, and vibekit deliberately does not implement
// the pull direction.
func TestTranslateACPEvent_RefusesUnknownRequests(t *testing.T) {
	cases := map[string]string{
		// The live case: an ungated KAS resolver vibekit chose not to implement.
		"ungated workspace pull": "_kiro/workspace/currently_open_files",
		// A _kiro/* method with no handler and no noop entry.
		"unknown kiro extension": "_kiro/some/future/verb",
		// A terminal verb the prefix router accepts but the switch does not
		// implement; it must not fall off the end of the switch.
		"unimplemented terminal verb": "terminal/resize",
		// A core ACP method vibekit does not implement.
		"unknown core method": "session/somethingNew",
		// A method on the NOOP table, arriving with an id. The table is keyed by
		// method only and is consulted BEFORE the refusal, so without the id test
		// on that lookup a request-shaped noop returns early and is never
		// answered — the same wedge, reached through the one door that logs
		// nothing on its way past. Every member is a notification today; this
		// pins the guard so adding a request-shaped one cannot reopen it.
		"a noop method arriving as a request": methodV3ToolsDidChange,
	}

	for name, method := range cases {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			id := int64(4242)

			h.translateACPEvent("c1", &api.RPCResponse{
				Method: method,
				ID:     &id,
			})

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatalf("no response to %s: an unanswered request wedges the turn", method)
			}

			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()

			if got.id != id {
				t.Errorf("responded to id %d, want %d", got.id, id)
			}
			if got.err == nil {
				t.Fatalf("responded to %s with a success result (%v), want an error: "+
					"vibekit does not implement it and must say so", method, got.result)
			}
			// The CODE matters, not just that it errored. -32601 is what JSON-RPC
			// 2.0 assigns to method-not-found; -32603 would label a deliberate
			// refusal an internal fault and make these logs blame the wrong side.
			var rpcErr *api.RPCError
			if !errors.As(got.err, &rpcErr) {
				t.Fatalf("refusal for %s is not an *api.RPCError (%T); the code is not on the wire",
					method, got.err)
			}
			if rpcErr.Code != api.RPCCodeMethodNotFound {
				t.Errorf("refusal for %s used code %d, want %d (method not found)",
					method, rpcErr.Code, api.RPCCodeMethodNotFound)
			}
			if !strings.Contains(rpcErr.Message, method) {
				t.Errorf("refusal message %q does not name the method; a log line would not say what was refused",
					rpcErr.Message)
			}
		})
	}
}

// TestTranslateACPEvent_IgnoresUnknownNotifications is the other half of the
// contract, and it is what keeps the refusal branch from becoming a bug of its
// own. A notification carries no id and owes nothing on the wire, so answering
// one would be a protocol error. The `msg.ID != nil` guard is the whole
// distinction, so it gets its own assertion rather than riding on the case
// above.
func TestTranslateACPEvent_IgnoresUnknownNotifications(t *testing.T) {
	// Both an unrecognised method and a NOOP-table member, because the noop
	// lookup now carries the same id test and must stay silent on the
	// notification side of it.
	for name, method := range map[string]string{
		"unknown extension": "_kiro/some/future/notification",
		"noop table member": methodV3ToolsDidChange,
	} {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())

			h.translateACPEvent("c1", &api.RPCResponse{
				Method: method,
				ID:     nil,
			})

			select {
			case <-br.done:
				br.respMu.Lock()
				got := br.response
				br.respMu.Unlock()
				t.Fatalf("responded to a notification (id=%d, err=%v): notifications owe no reply",
					got.id, got.err)
			case <-time.After(200 * time.Millisecond):
				// Correct: nothing sent.
			}
		})
	}
}

// hubContextIsLive guards the assumption both tests above rest on: the
// dispatcher derives its context from the hub's shutdown context, so a hub that
// is already shutting down would refuse to send and the tests would pass for
// the wrong reason.
func TestHubContextIsLiveOnAFreshHub(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx, cancel := h.hubContext()
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("a fresh hub's context is already done (%v); the refusal tests would be vacuous", err)
	}
}
