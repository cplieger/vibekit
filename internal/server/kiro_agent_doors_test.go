package server

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/cplieger/vibekit/internal/steering"
)

// agentDoorFixture is the listing shared with internal/steering's
// agentfiles_test.go, which holds the table stating what it MEANS. Both scans
// here are checked against steering.DedupeAgentFiles' answer for it rather than
// against restated expectations — three doors that each spell out the rule is the
// arrangement this replaced, and it would rot the same way.
//
// The document scan is ENTRY-oriented and the entity scan is one row per agent
// directory, but the file each picks is the same question, so that is what these
// assert.
var agentDoorFixture = map[string]*fstest.MapFile{
	"agents/.hidden.md":     {Data: []byte("# hidden\n")},
	"agents/.md":            {Data: []byte("")},
	"agents/README.txt":     {Data: []byte("not an agent\n")},
	"agents/deploy.json":    {Data: []byte(`{"name":"deploy"}`)},
	"agents/notes.md":       {Data: []byte("# notes\n")},
	"agents/reviewer.json":  {Data: []byte(`{"name":"reviewer"}`)},
	"agents/reviewer.md":    {Data: []byte("---\nname: reviewer\n---\n# reviewer\n")},
	"agents/nested/keep.md": {Data: []byte("# inside a subdirectory\n")},
}

// ruleFor reads the fixture the way production does and returns the rule's answer,
// so a door's expected file list is DERIVED rather than written down twice.
func ruleFor(t *testing.T, root fs.FS) []steering.AgentFile {
	t.Helper()
	entries, err := fs.ReadDir(root, "agents")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return steering.DedupeAgentFiles(entries)
}

// TestAgentScanDoorsAgreeWithTheSharedRule pins the consolidation itself: both
// REST scans resolve one listing to the same files, and to the files the rule
// names. A copy reintroduced in either door fails here.
func TestAgentScanDoorsAgreeWithTheSharedRule(t *testing.T) {
	root := fstest.MapFS(agentDoorFixture)
	want := ruleFor(t, root)
	if len(want) == 0 {
		t.Fatal("the fixture resolves to no agents; the assertions below would be vacuous")
	}

	t.Run("entity scan (kiro_config)", func(t *testing.T) {
		items := scanAgents(t.Context(), root, "ws/.kiro")
		if len(items) != len(want) {
			t.Fatalf("scanAgents = %+v (len %d), want the rule's %+v (len %d)", items, len(items), want, len(want))
		}
		for i, a := range want {
			if items[i].Name != a.Base {
				t.Errorf("scanAgents[%d].Name = %q, the rule says %q", i, items[i].Name, a.Base)
			}
			if wantPath := "ws/.kiro/agents/" + a.File; items[i].Path != wantPath {
				t.Errorf("scanAgents[%d].Path = %q, want %q", i, items[i].Path, wantPath)
			}
			if items[i].Type != "agent" {
				t.Errorf("scanAgents[%d].Type = %q, want %q", i, items[i].Type, "agent")
			}
		}
	})

	t.Run("document scan (kiro_docs)", func(t *testing.T) {
		// A nil guard admits everything, which is what the MapFS tests want: this
		// asserts the dedupe, not the provenance rules.
		docs := scanDocsAgents(t.Context(), root, "ws/.kiro", nil)
		if len(docs) != len(want) {
			t.Fatalf("scanDocsAgents = %+v (len %d), want the rule's %+v (len %d)", docs, len(docs), want, len(want))
		}
		for i, a := range want {
			if wantPath := "ws/.kiro/agents/" + a.File; docs[i].Path != wantPath {
				t.Errorf("scanDocsAgents[%d].Path = %q, want %q", i, docs[i].Path, wantPath)
			}
			if docs[i].Category != catAgent {
				t.Errorf("scanDocsAgents[%d].Category = %q, want %q", i, docs[i].Category, catAgent)
			}
		}
	})
}

// TestAgentScanDoorsSkipTheSameNonAgents states the negative half in the terms a
// reader of the REST reply cares about: the entries the rule refuses appear in
// NEITHER door's output, so a hidden file or a subdirectory cannot surface as a
// row on one page and not the other.
func TestAgentScanDoorsSkipTheSameNonAgents(t *testing.T) {
	root := fstest.MapFS(agentDoorFixture)
	refused := []string{".hidden", "", "README", "nested", "reviewer.json"}

	items := scanAgents(t.Context(), root, "ws/.kiro")
	docs := scanDocsAgents(t.Context(), root, "ws/.kiro", nil)

	for _, name := range refused {
		for i, it := range items {
			if it.Name == name {
				t.Errorf("scanAgents[%d] listed %q, which the rule refuses", i, name)
			}
		}
		for i, d := range docs {
			// reviewer.json is refused as a FILE (its pair's .md is chosen), so the
			// path is what the assertion has to read for that case.
			if d.Path == "ws/.kiro/agents/"+name {
				t.Errorf("scanDocsAgents[%d] listed %q, which the rule refuses", i, name)
			}
		}
	}
}
