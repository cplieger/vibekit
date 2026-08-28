package agent

// What the five terminal responders owe a request they decline to process.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestTerminalResponders_AnswerAnUndecodableRequest is the red check for the
// drop class: a frame carrying an id that vibekit declines to process gets a
// well-formed fail-closed ANSWER, never a bare return.
//
// The id is already verified non-nil by the router, KAS awaits these with no
// timeout, and vibekit's own Call has no deadline either — so a dropped request
// does not fail the tool call, it strands the promise and wedges the batch until
// the process dies. respondCreate has answered through respondErr all along;
// its four siblings returned in silence, with nothing logged either.
func TestTerminalResponders_AnswerAnUndecodableRequest(t *testing.T) {
	for _, method := range []string{
		methodTermOutput,
		methodTermRelease,
		methodTermWaitForExit,
		methodTermKill,
		// The counter-example that makes the other four an oversight rather than a
		// decision: identical shape, and it has always answered.
		methodTermCreate,
	} {
		t.Run(method, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			id := int64(4711)

			// Valid JSON with the wrong TYPE on the field each verb reads, so the
			// frame reaches the responder and fails at its decode rather than
			// earlier. This is the trigger the whole class needs — the fields are
			// type-stable strings today, so what makes it reachable is an upstream
			// shape change, not a malformed sender.
			h.translateACPEvent("c1", &vibekit.RPCResponse{
				Method: method,
				ID:     &id,
				Params: json.RawMessage(`{"terminalId":42,"command":42}`),
			})

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s with undecodable params got no response: an unanswered request "+
					"wedges the tool batch until process teardown", method)
			}

			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()

			if got.id != id {
				t.Errorf("%s answered id %d, want %d", method, got.id, id)
			}
			if got.err == nil {
				t.Errorf("%s answered with a success result (%v), want an error: the params did "+
					"not decode, so there is nothing to succeed at", method, got.result)
			}
		})
	}
}

// TestTerminalResponders_UseTheRequestsOwnChatID pins the half that decides
// whether the refusal above reaches anyone.
//
// respondErr resolves the reply bridge from the manager BY CHAT ID, so a refusal
// composed with an empty one misses the lookup and is dropped — which reproduces
// the very wedge it was added to prevent, silently. respondOutput carries that
// warning in a comment for its not-found path; it binds the decode path too.
func TestTerminalResponders_UseTheRequestsOwnChatID(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	id := int64(4712)

	h.translateACPEvent("", &vibekit.RPCResponse{
		Method: methodTermOutput,
		ID:     &id,
		Params: json.RawMessage(`{"terminalId":42}`),
	})

	select {
	case <-br.done:
		t.Fatal("a chat with no bridge got a response, so this test cannot tell a delivered " +
			"refusal from a dropped one")
	case <-time.After(200 * time.Millisecond):
	}

	// The same frame on the chat that owns the bridge lands.
	h.translateACPEvent("c1", &vibekit.RPCResponse{
		Method: methodTermOutput,
		ID:     &id,
		Params: json.RawMessage(`{"terminalId":42}`),
	})
	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the refusal did not reach the request's own bridge")
	}
}
