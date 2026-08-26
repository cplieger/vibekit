package vibekit_test

// The tab vocabulary is the half of the tab collection that crosses the wire, so
// these tests pin two things a Go compiler cannot: the kind SET (a ninth member
// reaches every client's per-kind factory as a switch with no case for it) and the
// JSON KEYS (the client decodes them by name, and a renamed tag is a field that
// silently arrives undefined).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestTabKind_ValidAndSingletonAgreeOnEveryMember walks the whole vocabulary in
// one table, because Valid and Singleton are two readings of one map and the
// failure mode of them disagreeing is not an error: it is Settings opening twice,
// or a chat tab accepted with no chat behind it.
func TestTabKind_ValidAndSingletonAgreeOnEveryMember(t *testing.T) {
	cases := []struct {
		desc          string
		kind          vibekit.TabKind
		wantValid     bool
		wantSingleton bool
	}{
		{desc: "chat, one tab per conversation", kind: vibekit.TabKindChat, wantValid: true},
		{desc: "editor, one tab per absolute path", kind: vibekit.TabKindEditor, wantValid: true},
		{desc: "run, one tab per workflow run", kind: vibekit.TabKindRun, wantValid: true},
		{desc: "settings", kind: vibekit.TabKindSettings, wantValid: true, wantSingleton: true},
		{desc: "git", kind: vibekit.TabKindGit, wantValid: true, wantSingleton: true},
		{desc: "files, the browser itself rather than one file", kind: vibekit.TabKindFiles, wantValid: true, wantSingleton: true},
		{desc: "history", kind: vibekit.TabKindHistory, wantValid: true, wantSingleton: true},
		{desc: "docs", kind: vibekit.TabKindDocs, wantValid: true, wantSingleton: true},
		{desc: "plan, deleted from the client on 2026-08-25 and deliberately absent here", kind: "plan"},
		{desc: "the empty kind, which is what a payload with no kind field decodes to", kind: ""},
		{desc: "a kind from some other vocabulary", kind: "vibe"},
		{desc: "a kind differing only in case", kind: "Chat"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			if got := tc.kind.Valid(); got != tc.wantValid {
				t.Errorf("TabKind(%q).Valid() = %v, want %v (%s)", tc.kind, got, tc.wantValid, tc.desc)
			}
			if got := tc.kind.Singleton(); got != tc.wantSingleton {
				t.Errorf("TabKind(%q).Singleton() = %v, want %v (%s)", tc.kind, got, tc.wantSingleton, tc.desc)
			}
		})
	}
}

// TestTabKind_TheSetIsExactlyEight is the guard on the set's SIZE, which the table
// above cannot give: a ninth member added to the map and forgotten there would
// leave every existing case passing. Nine kinds is a cross-language change (the
// client's TabKind union, its icon table and its per-kind factory), so it should
// fail here first.
func TestTabKind_TheSetIsExactlyEight(t *testing.T) {
	all := []vibekit.TabKind{
		vibekit.TabKindChat, vibekit.TabKindEditor, vibekit.TabKindRun,
		vibekit.TabKindSettings, vibekit.TabKindGit, vibekit.TabKindFiles,
		vibekit.TabKindHistory, vibekit.TabKindDocs,
	}
	valid := 0
	seen := make([]string, 0, len(all))
	for _, k := range all {
		if k.Valid() {
			valid++
		}
		seen = append(seen, string(k))
	}
	if valid != len(all) {
		t.Errorf("%d of the %d declared kinds are Valid, want all of them", valid, len(all))
	}
	slices.Sort(seen)
	want := []string{"chat", "docs", "editor", "files", "git", "history", "run", "settings"}
	if !slices.Equal(seen, want) {
		t.Errorf("the declared kinds are %v, want %v; a change here is a change to the client's TabKind union", seen, want)
	}
}

// TestTabSubject_WireKeys pins the JSON contract by name. The client decodes a
// subject field by field, so a renamed tag arrives as undefined rather than as an
// error — and Parent and Ref both carry meaning that cannot be re-derived.
//
// Every field is present with no omitempty on purpose: a subject is a complete
// record of one tab, and an absent "parent" would be indistinguishable from a tab
// whose parent the encoder dropped.
func TestTabSubject_WireKeys(t *testing.T) {
	data, err := json.Marshal(vibekit.TabSubject{
		ID:     "a3f1",
		Kind:   vibekit.TabKindRun,
		Ref:    "wf-1",
		Parent: "b7c2",
		Pinned: true,
		Owns:   true,
	})
	if err != nil {
		t.Fatalf("Marshal(TabSubject): %v", err)
	}
	want := `{"id":"a3f1","kind":"run","ref":"wf-1","parent":"b7c2","pinned":true,"owns":true}`
	if got := string(data); got != want {
		t.Errorf("Marshal(TabSubject) = %s, want %s", got, want)
	}

	var back vibekit.TabSubject
	if err := json.Unmarshal([]byte(want), &back); err != nil {
		t.Fatalf("Unmarshal(%s): %v", want, err)
	}
	if back.Kind != vibekit.TabKindRun || back.Ref != "wf-1" || back.Parent != "b7c2" || !back.Owns {
		t.Errorf("Unmarshal(%s) = %+v, want the fields round-tripped", want, back)
	}
}

// TestTabSubject_ZeroValueEncodesEveryField is the same claim from the other side:
// a fresh singleton tab has no ref and no parent, and its record still names them,
// so a client's decoder never has to guess whether a key was dropped or was empty.
func TestTabSubject_ZeroValueEncodesEveryField(t *testing.T) {
	data, err := json.Marshal(vibekit.TabSubject{ID: "x", Kind: vibekit.TabKindSettings})
	if err != nil {
		t.Fatalf("Marshal(TabSubject): %v", err)
	}
	want := `{"id":"x","kind":"settings","ref":"","parent":"","pinned":false,"owns":false}`
	if got := string(data); got != want {
		t.Errorf("Marshal(a singleton subject) = %s, want %s", got, want)
	}
}

// TestOpenTab_WireKeys covers the command payload's half. Here the optional fields
// ARE omitted, because the spec is what a client ASKS for: a chat open naming a
// parent it does not have would otherwise send parent:"" and read as a deliberate
// top-level placement, which is the same value but a different statement.
func TestOpenTab_WireKeys(t *testing.T) {
	cases := []struct {
		desc string
		spec vibekit.OpenTab
		want string
	}{
		{
			desc: "a chat open, the commonest one",
			spec: vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-1"},
			want: `{"kind":"chat","ref":"c-1"}`,
		},
		{
			desc: "a singleton, which carries nothing else",
			spec: vibekit.OpenTab{Kind: vibekit.TabKindDocs},
			want: `{"kind":"docs"}`,
		},
		{
			desc: "a launcher-owned run sub-tab, the case Owns exists for",
			spec: vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-1", Parent: "b7c2", Owns: true},
			want: `{"kind":"run","ref":"wf-1","parent":"b7c2","owns":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			data, err := json.Marshal(tc.spec)
			if err != nil {
				t.Fatalf("Marshal(%+v): %v", tc.spec, err)
			}
			if got := string(data); got != tc.want {
				t.Errorf("Marshal(%s) = %s, want %s", tc.desc, got, tc.want)
			}
			var back vibekit.OpenTab
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("Unmarshal(%s): %v", data, err)
			}
			if back != tc.spec {
				t.Errorf("round trip of %s = %+v, want %+v", tc.desc, back, tc.spec)
			}
		})
	}
}
