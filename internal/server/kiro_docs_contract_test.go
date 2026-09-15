package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// kiroDocsFixture is the envelope of testdata/kiro_docs.json: one real
// GET /api/workspace/kiro-docs reply over a small `.kiro` tree, byte-pinned here
// and decoded by static-src/docs.node.test.ts through the generated
// decodeKiroDocsResponse.
type kiroDocsFixture struct {
	Comment []string         `json:"_comment"`
	Result  KiroDocsResponse `json:"result"`
}

var kiroDocsFixtureComment = []string{
	"A GET /api/workspace/kiro-docs reply, produced by a real scan over one .kiro tree.",
	"",
	"server.KiroDoc and server.KiroDocsResponse are wiregen-registered; TestKiroDocsWireContract",
	"(Go) asserts the scan marshals to exactly these bytes, and docs.node.test.ts (TypeScript)",
	"decodes it through the generated decodeKiroDocsResponse. Every row kind appears once:",
	"steering (always, fileMatch and a nested doc), a skill with a declared inclusion, an agent",
	"with a model and tools, a spec grouped under its feature, and two hooks from one file.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/server/ -run TestKiroDocsWireContract",
	"then re-run the TS half: npx vitest --run docs.node.test.ts (from static-src/).",
}

// TestKiroDocsWireContract pins the marshaled shape of the docs reply. The tree
// is written to a real directory rather than an fstest.MapFS so the scan runs
// with its path guard, the way the handler runs it.
func TestKiroDocsWireContract(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		".kiro/steering/always.md":           "---\ndescription: Always on\n---\n",
		".kiro/steering/matched.md":          "---\ninclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\ndescription: Go layout\n---\n",
		".kiro/steering/nested/deep.md":      "---\ndescription: Nested\n---\n",
		".kiro/skills/review/SKILL.md":       "---\nname: review\ndescription: Review a change\ninclusion: manual\nsteering_override: true\n---\n",
		".kiro/agents/twin.md":               "---\nname: twin\ndescription: >\n  Even-cycle twin of the\n  other reviewer.\nmodel: claude-opus-5\ntools: [read, write]\n---\n",
		".kiro/specs/search/design.md":       "# Search design\n\nProse.\n",
		".kiro/specs/search/requirements.md": "# Search requirements\n",
		".kiro/hooks/two.json": `{"version":"v1","hooks":[
			{"name":"First","trigger":"PostFileSave","action":{"type":"command","command":"echo one"}},
			{"name":"Second","trigger":"SessionStart","action":{"type":"agent","prompt":"do a thing"}}
		]}`,
	}
	for rel, body := range files {
		writeFile(t, dir, rel, body)
	}
	roots := []kiroRoot{{fsPath: filepath.Join(dir, ".kiro"), prefix: "ws/.kiro"}}

	fx := kiroDocsFixture{Comment: kiroDocsFixtureComment, Result: scanKiroRoots(t.Context(), roots)}
	for _, cat := range []string{catSteering, catSkill, catAgent, catSpec, catHook} {
		if len(docsByCategory(fx.Result.Docs, cat)) == 0 {
			t.Errorf("fixture carries no %q row; the TS side cannot pin a category that never occurs", cat)
		}
	}
	if fx.Result.Truncated {
		t.Fatal("the fixture's scan was cut; a golden over a partial tree pins the wrong shape")
	}

	got, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	const path = "testdata/kiro_docs.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/server/ -run TestKiroDocsWireContract): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/server/ -run TestKiroDocsWireContract, "+
			"then re-run the TS half: npx vitest --run docs.node.test.ts (from static-src/).",
			path, want, got)
	}
}
