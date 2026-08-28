package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
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

			h.translateACPEvent("c1", &vibekit.RPCResponse{
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
			var rpcErr *vibekit.RPCError
			if !errors.As(got.err, &rpcErr) {
				t.Fatalf("refusal for %s is not an *vibekit.RPCError (%T); the code is not on the wire",
					method, got.err)
			}
			if rpcErr.Code != vibekit.RPCCodeMethodNotFound {
				t.Errorf("refusal for %s used code %d, want %d (method not found)",
					method, rpcErr.Code, vibekit.RPCCodeMethodNotFound)
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

			h.translateACPEvent("c1", &vibekit.RPCResponse{
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
// dispatcher derives its context from the runtime's shutdown context, so a runtime that
// is already shutting down would refuse to send and the tests would pass for
// the wrong reason.
func TestHubContextIsLiveOnAFreshHub(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx, cancel := h.lifecycle.derivedContext()
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("a fresh runtime's context is already done (%v); the refusal tests would be vacuous", err)
	}
}

// TestTranslateACPEvent_ReportsARefusalItCouldNotDeliver is the failure mode the
// refusal was added to prevent, arriving anyway.
//
// The refusal exists because an unanswered A→C request wedges the turn forever —
// so a refusal that could not be WRITTEN leaves exactly that wedge, with the one
// difference that vibekit knows about it. The line is the only diagnosis available
// for a chat stuck on a spinner, which also means a guard flipped here prints it
// after every successful refusal and makes the log useless for finding the real one.
func TestTranslateACPEvent_ReportsARefusalItCouldNotDeliver(t *testing.T) {
	const wantLine = "chat bridge: refusal could not be delivered; the turn may be wedged"

	t.Run("a refusal that went nowhere is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h, br := hubForFSTest(t, t.TempDir())
		br.respMu.Lock()
		br.respondErr = errors.New("bridge gone")
		br.respMu.Unlock()
		id := int64(4242)

		h.translateACPEvent("c1", &vibekit.RPCResponse{Method: "_kiro/some/future/verb", ID: &id})

		select {
		case <-br.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the refusal was never attempted")
		}
		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a refusal that could not be written said nothing, so a wedged turn has no "+
				"diagnosis; want a line reading %q. Got: %s", wantLine, out)
		}
	})

	t.Run("a delivered refusal is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h, br := hubForFSTest(t, t.TempDir())
		id := int64(4242)

		h.translateACPEvent("c1", &vibekit.RPCResponse{Method: "_kiro/some/future/verb", ID: &id})

		select {
		case <-br.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the refusal was never attempted")
		}
		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a refusal that landed was reported as undelivered: %s", out)
		}
	})
}

// TestTranslateACPEvent_HandlerTableIsIDAware pins the gate on the handler-map
// LOOKUP, which is the guard the noop line one branch below has always had.
//
// The table holds two dozen methods and only three are request-shaped. Ungated,
// a method the backend later promotes from a notification to a request reaches a
// notification handler that reads Params and returns — the same wedge the -32601
// fence exists to prevent, arriving through the one door that answers nothing and
// logs nothing. The three that ARE request-shaped are dispatched from
// routeInboundRequest instead, so the two halves are disjoint by construction.
func TestTranslateACPEvent_HandlerTableIsIDAware(t *testing.T) {
	const method = "_kiro/mcp/status" // a notification handler in the table
	params := mustJSON(t, map[string]any{
		"servers": []map[string]any{{"name": "github", "status": "connected"}},
	})

	t.Run("a notification still reaches its handler", func(t *testing.T) {
		h, _ := hubForFSTest(t, t.TempDir())

		h.translateACPEvent("c1", &vibekit.RPCResponse{Method: method, Params: params})

		if snap := h.mcpRegistry.Snapshot(); len(snap) != 1 {
			t.Fatalf("registry snapshot = %+v, want the one server the notification carried: "+
				"gating the lookup must not stop notification handling", snap)
		}
	})

	t.Run("the same method arriving as a request is refused", func(t *testing.T) {
		h, br := hubForFSTest(t, t.TempDir())
		id := int64(31337)

		h.translateACPEvent("c1", &vibekit.RPCResponse{Method: method, ID: &id, Params: params})

		select {
		case <-br.done:
		case <-time.After(2 * time.Second):
			t.Fatal("a request-shaped table member got no response; a notification handler " +
				"swallowed it and the fence was never reached")
		}
		br.respMu.Lock()
		got := br.response
		br.respMu.Unlock()

		var rpcErr *vibekit.RPCError
		if !errors.As(got.err, &rpcErr) {
			t.Fatalf("refusal is not an *vibekit.RPCError (%T, result %v)", got.err, got.result)
		}
		if rpcErr.Code != vibekit.RPCCodeMethodNotFound {
			t.Errorf("refusal code = %d, want %d", rpcErr.Code, vibekit.RPCCodeMethodNotFound)
		}
		// The handler must not have run: a frame that was both handled AND refused
		// is the double-dispatch hazard, one id with two answers.
		if snap := h.mcpRegistry.Snapshot(); len(snap) != 0 {
			t.Errorf("the notification handler also ran (snapshot %+v); the frame was handled "+
				"and refused, which is two answers on one id", snap)
		}
	})
}

// TestTranslateACPEvent_AskMethodsDispatchOnce is the other half of the gate: the
// three request-shaped members of the table are dispatched from the REQUEST side,
// exactly once.
//
// One permission_needed broadcast is what "once" looks like from here, because
// the permission handler's answer is the card rather than an RPC reply. The
// failure this catches is ZERO — the whitelist missing while the table lookup is
// gated, which sends the user's approval to the -32601 fence instead of to them.
// Measured: two is unreachable rather than merely absent, because the caller
// returns the moment routeInboundRequest claims a frame, so the table can never
// see one of these three. Do not read this test as the guard against that.
func TestTranslateACPEvent_AskMethodsDispatchOnce(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	_, before := h.bus.fanout.Bounds()
	id := int64(31338)

	h.translateACPEvent("c1", &vibekit.RPCResponse{
		Method: vibekit.MethodRequestPermission,
		ID:     &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_1",
			"toolCall":  map[string]any{"toolCallId": "tc1", "title": "write file", "kind": "edit"},
			"options":   []map[string]any{{"optionId": "accept", "name": "Allow", "kind": "allow_once"}},
		}),
	})

	asks := 0
	for _, e := range bufferedSince(h, before) {
		var msg vibekit.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == vibekit.EventPermissionNeeded {
			asks++
		}
	}
	if asks != 1 {
		t.Errorf("permission_needed fired %d times, want 1: 0 means the ask never reached its "+
			"handler, 2 means both halves dispatched it", asks)
	}

	select {
	case <-br.done:
		br.respMu.Lock()
		got := br.response
		br.respMu.Unlock()
		t.Errorf("the ask was answered on the wire (id=%d, err=%v) instead of raised to the user; "+
			"it fell through to the refusal", got.id, got.err)
	case <-time.After(200 * time.Millisecond):
	}
}
