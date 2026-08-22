package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// findPermissionNeeded returns the first permission_needed payload broadcast.
func findPermissionNeeded(t *testing.T, events *[]vibekit.ServerEvent) (vibekit.PermissionNeededPayload, bool) {
	t.Helper()
	for _, e := range *events {
		if e.Type != vibekit.EventPermissionNeeded {
			continue
		}
		p, ok := e.Payload.(vibekit.PermissionNeededPayload)
		if !ok {
			t.Fatalf("permission_needed payload type = %T, want vibekit.PermissionNeededPayload", e.Payload)
		}
		return p, true
	}
	return vibekit.PermissionNeededPayload{}, false
}

// TestHandlePermissionRequest_DecodesFlatParamsAndEnvelopeID pins the v3 decode
// contract: session/request_permission params are FLAT ({sessionId, toolCall,
// options}) and the correlation id is on the JSON-RPC envelope (msg.ID), not in
// params. The prior code wrapped the fields under a `params` object and read the
// id from params, so unmarshalParams (which decodes msg.Params directly) yielded
// all-zero — an empty dialog with request_id=0. This test fails against that bug
// and passes with the flat decode.
func TestHandlePermissionRequest_DecodesFlatParamsAndEnvelopeID(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(4242)
	msg := &vibekit.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-9",
				"title":      "Write config.tf",
				"kind":       "edit",
			},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
			},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.RequestID != id {
		t.Errorf("RequestID = %d, want %d (must come from the envelope, not params)", got.RequestID, id)
	}
	if got.ToolCallID != "tc-9" {
		t.Errorf("ToolCallID = %q, want tc-9", got.ToolCallID)
	}
	if got.Title != "Write config.tf" {
		t.Errorf("Title = %q, want 'Write config.tf'", got.Title)
	}
	if len(got.Options) != 2 || got.Options[0].OptionID != "allow" || got.Options[1].OptionID != "deny" {
		t.Errorf("Options = %+v, want 2 options [allow, deny]", got.Options)
	}
}

// TestHandlePermissionRequest_MissingIDDropped pins that a request with no
// envelope id is dropped (its outcome could never be routed back to the agent)
// rather than surfaced as an unanswerable dialog.
func TestHandlePermissionRequest_MissingIDDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	msg := &vibekit.RPCResponse{ // no ID
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc", "title": "x", "kind": "edit"},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	if _, ok := findPermissionNeeded(t, events); ok {
		t.Fatal("permission_needed broadcast for a request with no id (should be dropped)")
	}
}

// --- Turn approval ---------------------------------------------------------
//
// A turn approval is not a separate method: KAS raises it as an ordinary
// session/request_permission and puts the file list in `_meta.kiro`. These
// tests pin the three things the client depends on — the discriminator, the
// path normalisation, and the action id — because every one of them is
// silently recoverable-looking when wrong. A missed discriminator renders a
// bare "Allow / Reject" with no file list, and the user approves a turn they
// were never shown.

// turnApprovalParams builds a session/request_permission whose _meta marks it a
// turn approval carrying `files`.
func turnApprovalParams(t *testing.T, files []map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionId": "sess_x",
		"toolCall": map[string]any{
			"toolCallId": "tc-turn",
			"title":      "Review changes",
			"kind":       "edit",
		},
		"options": []map[string]any{
			{"optionId": "accept", "name": "Accept", "kind": "allow_once"},
			{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
		},
		"_meta": map[string]any{
			"kiro": map[string]any{
				"type":        "turn_approval",
				"executionId": "exec-1",
				"files":       files,
			},
		},
	})
}

// TestHandlePermissionRequest_TurnApprovalCarriesFiles pins the decode of
// `_meta.kiro.files`: KAS sends ABSOLUTE paths and names the action id
// `toolCallId`, and vibekit puts workspace-relative paths on the wire under
// `action_id`. A client that received the absolute path would render the
// operator's whole home directory in a file row, and one that lost the action
// id could not answer at all — the decision map is keyed by it.
func TestHandlePermissionRequest_TurnApprovalCarriesFiles(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &workDirDeps{baseDeps: base, workDir: "/work"}
	tr := New(rolesOf(deps))

	id := int64(77)
	msg := &vibekit.RPCResponse{
		ID: &id,
		Params: turnApprovalParams(t, []map[string]any{
			{"path": "/work/src/a.ts", "snapshotUri": "kiro-snapshot-v2://s:abc/", "toolCallId": "act-1"},
			{"path": "/work/src/b.ts", "toolCallId": "act-2"},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if len(got.Files) != 2 {
		t.Fatalf("Files length = %d, want 2: %+v", len(got.Files), got.Files)
	}
	want := []vibekit.ApprovalFile{
		{Path: "src/a.ts", SnapshotURI: "kiro-snapshot-v2://s:abc/", ActionID: "act-1"},
		{Path: "src/b.ts", ActionID: "act-2"},
	}
	for i, w := range want {
		if got.Files[i] != w {
			t.Errorf("Files[%d] = %+v, want %+v", i, got.Files[i], w)
		}
	}
}

// TestHandlePermissionRequest_SharedActionIDPreserved pins that a multi-file
// semantic rename keeps BOTH entries under their one shared action id rather
// than being deduped. The client groups on that id to render one undividable
// row; collapsing the pair here would hide the second path from review, and
// re-keying them apart would offer a choice the wire cannot express (the
// decision map is per action).
func TestHandlePermissionRequest_SharedActionIDPreserved(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &workDirDeps{baseDeps: base, workDir: "/work"}
	tr := New(rolesOf(deps))

	id := int64(78)
	msg := &vibekit.RPCResponse{
		ID: &id,
		Params: turnApprovalParams(t, []map[string]any{
			{"path": "/work/old.py", "toolCallId": "ren-1"},
			{"path": "/work/new.py", "toolCallId": "ren-1"},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, _ := findPermissionNeeded(t, events)
	if len(got.Files) != 2 {
		t.Fatalf("Files length = %d, want 2 (both halves of the rename): %+v", len(got.Files), got.Files)
	}
	if got.Files[0].ActionID != "ren-1" || got.Files[1].ActionID != "ren-1" {
		t.Errorf("ActionIDs = %q/%q, want both ren-1", got.Files[0].ActionID, got.Files[1].ActionID)
	}
}

// TestHandlePermissionRequest_OrdinaryPermissionHasNoFiles is the inverse, and
// it is the half that keeps the discriminator honest: without the
// `_meta.kiro.type == "turn_approval"` check, any permission request could
// arrive carrying a files list and the client would render a turn-approval card
// for a bash command.
func TestHandlePermissionRequest_OrdinaryPermissionHasNoFiles(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(79)
	// _meta present but a DIFFERENT type, with files attached: the type is what
	// decides, not the presence of the array.
	msg := &vibekit.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "ls -la", "kind": "execute"},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
			"_meta": map[string]any{
				"kiro": map[string]any{
					"type":  "something_else",
					"files": []map[string]any{{"path": "/work/a.ts", "toolCallId": "act-1"}},
				},
			},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.Files != nil {
		t.Errorf("Files = %+v, want nil for a non-turn_approval request", got.Files)
	}
}

// --- Always-allow persistability (kiro-cli 2.19.1 _meta.kiro.consent) -------
//
// The polarity is ABSENT-MEANS-YES, so the three cases below are the tri-state
// a *bool has and a plain bool does not. Case (a) is the one that matters most:
// it is the 2.19.0 wire, and a plain bool would decode it as "not persistable"
// and suppress the Always-allow row on every command of every request.

// consentParams builds a shell permission request whose `_meta.kiro` carries
// whatever `consent` object the case wants — or none at all when nil.
func consentParams(t *testing.T, consent map[string]any) []byte {
	t.Helper()
	kiro := map[string]any{}
	if consent != nil {
		kiro["consent"] = consent
	}
	return mustJSON(t, map[string]any{
		"sessionId": "sess_x",
		"toolCall": map[string]any{
			"toolCallId": "tc-sh",
			"title":      "git status",
			"kind":       "execute",
		},
		"options": []map[string]any{
			{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
		},
		"_meta": map[string]any{"kiro": kiro},
	})
}

// TestHandlePermissionRequest_AbsentConsentIsNotBlocked is the REGRESSION TEST
// for the whole feature. Every kiro-cli through 2.19.0 sends no `consent` object
// at all, and KAS on 2.19.1 omits it whenever a rule WOULD match — so absent is
// both the old wire and the common new case, and it must mean "the offer
// stands".
//
// Red-check it by making ACPConsentMeta.PersistableConsent a plain bool: the
// zero value is false, the derivation reads that as not-persistable, and this
// case fails while (b) still passes.
func TestHandlePermissionRequest_AbsentConsentIsNotBlocked(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(3001)
	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID:     &id,
		Params: consentParams(t, nil),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.AlwaysAllowBlocked != "" {
		t.Errorf("AlwaysAllowBlocked = %q, want empty: an absent consent object means PERSISTABLE, "+
			"and reading it as blocked suppresses the Always-allow row on every 2.19.0 request",
			got.AlwaysAllowBlocked)
	}
}

// TestHandlePermissionRequest_PersistableFalseBlocksAlwaysAllow is the case the
// field exists for: KAS probed its three candidate patterns and none would
// match, so the card must not offer to write a rule that could never fire.
func TestHandlePermissionRequest_PersistableFalseBlocksAlwaysAllow(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(3002)
	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID: &id,
		Params: consentParams(t, map[string]any{
			"persistableConsent":       false,
			"persistableConsentReason": upstreamConsentReason,
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.AlwaysAllowBlocked != vibekit.AlwaysAllowBlockUnparseable {
		t.Errorf("AlwaysAllowBlocked = %q, want %q",
			got.AlwaysAllowBlocked, vibekit.AlwaysAllowBlockUnparseable)
	}
}

// TestHandlePermissionRequest_PersistableTrueIsNotBlocked is the tri-state's
// third leg. KAS is not documented to send an explicit true, but the wire type
// can carry one, and present-and-true must read the same as absent — otherwise
// the day upstream starts sending it, every row disappears.
func TestHandlePermissionRequest_PersistableTrueIsNotBlocked(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(3003)
	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID: &id,
		Params: consentParams(t, map[string]any{
			"persistableConsent": true,
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.AlwaysAllowBlocked != "" {
		t.Errorf("AlwaysAllowBlocked = %q, want empty for an explicit persistableConsent:true",
			got.AlwaysAllowBlocked)
	}
}
