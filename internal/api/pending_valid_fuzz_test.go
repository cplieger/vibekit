package api

import (
	"testing"
)

// FuzzPendingChangeKindValid targets the PendingChangeKind validation.
// Bug class: case-sensitivity bypass or unicode confusable that passes
// Valid() but isn't a real enum value — could lead to unrecognised ops
// reaching the filesystem handler without being caught at the boundary.
func FuzzPendingChangeKindValid(f *testing.F) {
	f.Add("create")
	f.Add("edit")
	f.Add("delete")
	f.Add("CREATE")
	f.Add("")
	f.Add("cre\x00ate")
	f.Add("edit ")
	f.Add("delete\n")
	f.Add("creat\u0435") // Cyrillic е

	f.Fuzz(func(t *testing.T, s string) {
		kind := PendingChangeKind(s)
		valid := kind.Valid()

		// Invariant: Valid() must return true ONLY for the exact canonical values.
		canonical := s == "create" || s == "edit" || s == "delete"
		if valid != canonical {
			t.Fatalf("PendingChangeKind(%q).Valid() = %v, want %v", s, valid, canonical)
		}
	})
}

// FuzzPendingActionValid targets the PendingAction validation.
// Bug class: same as above — accept/reject bypass could auto-accept
// changes the user intended to reject, or vice versa.
func FuzzPendingActionValid(f *testing.F) {
	f.Add("accept")
	f.Add("reject")
	f.Add("ACCEPT")
	f.Add("")
	f.Add("accept ")
	f.Add("rej\x00ect")

	f.Fuzz(func(t *testing.T, s string) {
		action := PendingAction(s)
		valid := action.Valid()

		canonical := s == "accept" || s == "reject"
		if valid != canonical {
			t.Fatalf("PendingAction(%q).Valid() = %v, want %v", s, valid, canonical)
		}
	})
}

// FuzzPushKindValid targets the PushKind validation.
// Bug class: accepting unknown push kinds that could be injected into
// notification dispatch, triggering unsupported code paths or bypassing
// rate limits scoped per-kind.
func FuzzPushKindValid(f *testing.F) {
	f.Add("agent_finished")
	f.Add("permission")
	f.Add("")
	f.Add("agent_finished\x00")
	f.Add("PERMISSION")

	f.Fuzz(func(t *testing.T, s string) {
		kind := PushKind(s)
		valid := kind.Valid()

		canonical := s == "agent_finished" || s == "permission"
		if valid != canonical {
			t.Fatalf("PushKind(%q).Valid() = %v, want %v", s, valid, canonical)
		}
	})
}
