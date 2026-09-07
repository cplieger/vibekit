package agent

// The server-side run-tab offer: who gets one, once, and what a refusal leaves
// behind.
//
// The property under all of it is that the offer is SPENT durably and only on a
// success. `run_start` re-fires on every resume and each step frame retries, so a
// mark that does not stick re-offers a tab the reader closed; a mark that sticks on
// a refusal denies the run its tab for good, because TabSubject.Parent is immutable
// and the retry is the only door left.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// fakeRunTabOpener records every automatic open and answers with whatever the
// case is about. *command.Membership is the production implementation; what this
// double stands in for is its verdict, which is the only thing the offer branches
// on.
type fakeRunTabOpener struct {
	calls []struct {
		workflowID string
		parentChat vibekit.ChatID
	}
	err error
}

func (f *fakeRunTabOpener) OpenRunTab(_ context.Context, workflowID string, parentChat vibekit.ChatID, _ string) (command.TabOpened, error) {
	f.calls = append(f.calls, struct {
		workflowID string
		parentChat vibekit.ChatID
	}{workflowID, parentChat})
	if f.err != nil {
		return command.TabOpened{}, f.err
	}
	return command.TabOpened{Created: true}, nil
}

// offerFixture is a run surface with an in-memory lease store and a fake opener.
func offerFixture(t *testing.T, err error) (*Runs, *fakeRunTabOpener) {
	t.Helper()
	opener := &fakeRunTabOpener{err: err}
	return &Runs{tabs: opener, leases: runlease.NewMemory()}, opener
}

// TestOfferRunTab_OpensOnceForAChatParentedRun is symptom A's fix at its own
// seam: the run's launching chat is named from the frame, and the durable mark is
// what makes the offer survive a resume and a restart.
func TestOfferRunTab_OpensOnceForAChatParentedRun(t *testing.T) {
	t.Parallel()
	rs, opener := offerFixture(t, nil)
	const id = "wf_1"
	rs.grantLease(t.Context(), id, "publish", launchFromChat("c-1"))

	rs.offerRunTab(t.Context(), "c-1", id)

	if len(opener.calls) != 1 {
		t.Fatalf("opener saw %d calls, want 1", len(opener.calls))
	}
	if got := opener.calls[0]; got.workflowID != id || got.parentChat != "c-1" {
		t.Errorf("opened %+v, want the run under its launching chat", got)
	}
	if l, _ := rs.lease(id); !l.TabOffered {
		t.Error("a successful offer left the lease unmarked, so every resume re-offers the tab")
	}

	// `run_start` re-fires on every resume, and each step frame retries.
	rs.offerRunTab(t.Context(), "c-1", id)
	rs.offerRunTab(t.Context(), "c-1", id)
	if len(opener.calls) != 1 {
		t.Errorf("opener saw %d calls after two repeats, want 1: a close must stay final",
			len(opener.calls))
	}
}

// TestOfferRunTab_LeavesTheOfferUnspentWhenTheChatHasNoTab is the retry's
// precondition. Parent is immutable, so the alternative to refusing is a
// permanently top-level tab.
func TestOfferRunTab_LeavesTheOfferUnspentWhenTheChatHasNoTab(t *testing.T) {
	// No t.Parallel: captureLogs swaps slog's process-global default.
	logs := captureLogs(t)
	rs, opener := offerFixture(t, command.ErrNoParentTab)
	const id = "wf_1"
	rs.grantLease(t.Context(), id, "publish", launchFromChat("c-1"))

	rs.offerRunTab(t.Context(), "c-1", id)

	if l, _ := rs.lease(id); l.TabOffered {
		t.Fatal("a refused offer was marked spent, so the run can never be offered a tab again")
	}
	// The reader opens the launching chat; the next step frame retries.
	opener.err = nil
	rs.offerRunTab(t.Context(), "c-1", id)
	if len(opener.calls) != 2 {
		t.Errorf("opener saw %d calls, want 2: the retry is what covers a chat opened late",
			len(opener.calls))
	}
	if l, _ := rs.lease(id); !l.TabOffered {
		t.Error("the retry's success did not mark the offer spent")
	}
	if out := logs.String(); strings.Contains(out, "run tab not offered") {
		t.Errorf("a chat with no tab logged a warning; it is a normal state and this frame "+
			"arrives per step. Got: %s", out)
	}
}

// TestOfferRunTab_ReportsARealRefusalAndStillDoesNotSpendTheOffer: the capacity
// refusal is a fault worth one line, unlike the two normal states, and it must not
// spend the offer either — closing a tab frees a slot and the next step retries.
func TestOfferRunTab_ReportsARealRefusalAndStillDoesNotSpendTheOffer(t *testing.T) {
	// No t.Parallel: captureLogs swaps slog's process-global default.
	logs := captureLogs(t)
	rs, _ := offerFixture(t, errors.New("too many tabs are open; close a tab first"))
	const id = "wf_1"
	rs.grantLease(t.Context(), id, "publish", launchFromChat("c-1"))

	rs.offerRunTab(t.Context(), "c-1", id)

	if l, _ := rs.lease(id); l.TabOffered {
		t.Error("a capacity refusal spent the offer, so freeing a slot would not help")
	}
	const wantLine = "run tab not offered to its launching chat"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("a real refusal said nothing; want a line reading %q. Got: %s", wantLine, out)
	}
}

// TestOfferRunTab_SkipsWhatHasNoLaunchingChat pins the parentless case the fix
// must not break, in all three of its spellings, plus the two guards that keep the
// hot path from reaching the coordinator at all.
func TestOfferRunTab_SkipsWhatHasNoLaunchingChat(t *testing.T) {
	t.Parallel()
	for name, frame := range map[string]struct {
		chatID     vibekit.ChatID
		workflowID string
	}{
		// A manual or scheduled launch: the lifecycle frames are workspace-global.
		"a parentless run's empty chat id": {chatID: "", workflowID: "wf_1"},
		// A run bridge's synthetic id, which is not a chat and has no tab.
		"a run bridge's synthetic chat id": {chatID: runChatID("wf_1"), workflowID: "wf_1"},
		// A frame that carried no workflow id at all.
		"no workflow id": {chatID: "c-1", workflowID: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rs, opener := offerFixture(t, nil)
			rs.offerRunTab(t.Context(), frame.chatID, frame.workflowID)
			if len(opener.calls) != 0 {
				t.Errorf("opener saw %+v, want nothing opened", opener.calls)
			}
		})
	}
}

// TestOfferRunTab_WithNoOpenerWiredDoesNothing is what keeps a bare &Runs{}
// usable in a test: the nil check is for that shape, not for production, where the
// field is required and a wiring miss panics at boot.
func TestOfferRunTab_WithNoOpenerWiredDoesNothing(t *testing.T) {
	t.Parallel()
	rs := &Runs{leases: runlease.NewMemory()}
	rs.grantLease(t.Context(), "wf_1", "publish", launchFromChat("c-1"))
	rs.offerRunTab(t.Context(), "c-1", "wf_1")
	if l, _ := rs.lease("wf_1"); l.TabOffered {
		t.Error("the offer was marked spent without anything opening a tab")
	}
}

// TestOfferRunTab_OffersARunItHoldsNoLeaseFor is the shape a restart leaves: the
// lease store starts empty, so the flag reads false and the run earns one offer.
// The mark then fails with ErrNotFound, which must not stop the tab from opening.
func TestOfferRunTab_OffersARunItHoldsNoLeaseFor(t *testing.T) {
	// No t.Parallel: captureLogs swaps slog's process-global default.
	logs := captureLogs(t)
	rs, opener := offerFixture(t, nil)

	rs.offerRunTab(t.Context(), "c-1", "wf_unleased")

	if len(opener.calls) != 1 {
		t.Errorf("opener saw %d calls, want 1: an unleased run still deserves its tab", len(opener.calls))
	}
	if out := logs.String(); !strings.Contains(out, "run tab offer not recorded durably") {
		t.Errorf("a mark that could not be recorded said nothing. Got: %s", out)
	}
}

// TestObserveRunStart_OffersTheRunsTabUnderItsLaunchingChat is the wiring: the
// frame every launch route passes through reaches the offer, and it does so with
// the chat id the frame carried.
func TestObserveRunStart_OffersTheRunsTabUnderItsLaunchingChat(t *testing.T) {
	t.Parallel()
	opener := &fakeRunTabOpener{}
	rs := &Runs{tabs: opener, leases: runlease.NewMemory(), translate: &noopRunTranslator{}}

	rs.observeStart(t.Context(), "c-1", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish",
	}))

	if len(opener.calls) != 1 {
		t.Fatalf("opener saw %d calls, want 1", len(opener.calls))
	}
	if got := opener.calls[0]; got.parentChat != "c-1" {
		t.Errorf("opened under %q, want the frame's own chat", got.parentChat)
	}

	// A parentless run's frames are workspace-global, and its tab stays the
	// launcher's own.
	rs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_manual", "workflowName": "nightly",
	}))
	if len(opener.calls) != 1 {
		t.Errorf("opener saw %d calls, want 1: a parentless run is offered nothing", len(opener.calls))
	}
}

// TestOfferOnProgress_RetriesTheOfferAndStillTranslates is the retry's wiring
// over `node_start`. Both halves matter: the retry has to fire, and the frame has
// to reach the handler it wrapped.
func TestOfferOnProgress_RetriesTheOfferAndStillTranslates(t *testing.T) {
	t.Parallel()
	opener := &fakeRunTabOpener{}
	rs := &Runs{tabs: opener, leases: runlease.NewMemory()}
	// The lease `observeStart` granted before the first step frame arrived. Without
	// one the flag has nowhere to live, so the offer repeats per step — idempotent
	// at the coordinator, but a call rather than a map read.
	rs.grantLease(t.Context(), "wf_1", "publish", launchFromChat("c-1"))
	var translated int
	wrapped := rs.offerOnProgress(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
		translated++
	})

	wrapped(t.Context(), "c-1", runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_1", "nodeId": "coder",
	}))

	if len(opener.calls) != 1 {
		t.Errorf("opener saw %d calls, want 1: node_start is the offer's retry", len(opener.calls))
	}
	if translated != 1 {
		t.Errorf("the wrapped handler ran %d times, want 1", translated)
	}

	// The second step frame costs one flag read, not a second open.
	wrapped(t.Context(), "c-1", runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_1", "nodeId": "reviewer",
	}))
	if len(opener.calls) != 1 {
		t.Errorf("opener saw %d calls, want 1: the offer is spent", len(opener.calls))
	}
	if translated != 2 {
		t.Errorf("the wrapped handler ran %d times, want 2", translated)
	}
}

// TestTranslateACPEvent_ARunsFramesReachTheTabOffer is the REGISTRATION, driven
// through the chat bridge's own door.
//
// The two handler tests above prove the wrappers work; this one proves they are
// wired, which is a separate failure — a run_start handler registered bare, or a
// node_start wrapper dropped, leaves both of them green and every run tabless.
func TestTranslateACPEvent_ARunsFramesReachTheTabOffer(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	if h.runs.tabs == nil {
		t.Fatal("New did not wire the run surface's tab opener")
	}
	opener := &fakeRunTabOpener{}
	h.runs.tabs = opener

	h.translateACPEvent("c-1", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish",
	}))
	if len(opener.calls) != 1 {
		t.Fatalf("run_start produced %d opens, want 1: observeStart is not registered", len(opener.calls))
	}

	// The retry. The lease `run_start` granted carries the mark, so a step frame
	// costs a flag read; what this asserts is that the frame REACHES the offer at
	// all, which a dropped wrapper would silently stop.
	h.runs.releaseLease(t.Context(), "wf_1")
	h.translateACPEvent("c-1", runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_1", "nodeId": "coder",
	}))
	if len(opener.calls) != 2 {
		t.Errorf("node_start produced %d opens in total, want 2: the retry is not registered",
			len(opener.calls))
	}
}

// launchFromChat is the origin an agent-launched run's lease carries: the
// launching chat, which is the fact the offer reads.
func launchFromChat(chatID string) launchOrigin {
	return launchOrigin{origin: runlease.OriginAgent, chatID: chatID}
}

// noopRunTranslator satisfies the one translate role observeStart reaches.
type noopRunTranslator struct{}

func (noopRunTranslator) HandleRunStart(context.Context, vibekit.ChatID, *vibekit.RPCResponse)    {}
func (noopRunTranslator) HandleRunComplete(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {}
func (noopRunTranslator) RecordRunSteps(json.RawMessage)                                          {}
func (noopRunTranslator) ForgetRunSteps(string)                                                   {}
func (noopRunTranslator) SessionNotifyAsk(*vibekit.RPCResponse) (vibekit.RunInputNeededPayload, bool) {
	return vibekit.RunInputNeededPayload{}, false
}
