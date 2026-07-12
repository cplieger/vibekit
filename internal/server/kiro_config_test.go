package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// filterType returns the subset of items with the given Type.
func filterType(items []kiroConfigItem, typ string) []kiroConfigItem {
	var out []kiroConfigItem
	for _, it := range items {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out
}

func indexByName(items []kiroConfigItem) map[string]kiroConfigItem {
	byName := make(map[string]kiroConfigItem, len(items))
	for _, it := range items {
		byName[it.Name] = it
	}
	return byName
}

// TestScanKiroDirFS_AgentsDedupPreferMd verifies a paired foo.json+foo.md
// collapses to ONE agent pointing at the .md, that a .json-only agent is
// still listed (the pre-fix scan omitted it), and that a .md-only agent
// is listed.
func TestScanKiroDirFS_AgentsDedupPreferMd(t *testing.T) {
	fsys := fstest.MapFS{
		"agents/foo.json": {Data: []byte(`{"name":"foo"}`)},
		"agents/foo.md":   {Data: []byte("# foo")},
		"agents/bar.json": {Data: []byte(`{"name":"bar"}`)},
		"agents/baz.md":   {Data: []byte("# baz")},
	}
	agents := filterType(scanKiroDirFS(context.Background(), fsys, "ws/.kiro"), "agent")
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3 (foo paired -> 1): %+v", len(agents), agents)
	}
	byName := indexByName(agents)
	if got := byName["foo"].Path; got != "ws/.kiro/agents/foo.md" {
		t.Errorf("foo path = %q, want ws/.kiro/agents/foo.md (prefer .md over .json)", got)
	}
	if got := byName["bar"].Path; got != "ws/.kiro/agents/bar.json" {
		t.Errorf("bar path = %q, want ws/.kiro/agents/bar.json (.json-only must be listed)", got)
	}
	if got := byName["baz"].Path; got != "ws/.kiro/agents/baz.md" {
		t.Errorf("baz path = %q, want ws/.kiro/agents/baz.md", got)
	}
}

// TestScanKiroDirFS_SkillsAreSubdirs verifies skills are enumerated as
// subdirectories pointing at their SKILL.md, and that a stray flat .md
// file directly under skills/ is NOT treated as a skill.
func TestScanKiroDirFS_SkillsAreSubdirs(t *testing.T) {
	fsys := fstest.MapFS{
		"skills/alpha/SKILL.md": {Data: []byte("# alpha")},
		"skills/beta/SKILL.md":  {Data: []byte("# beta")},
		"skills/loose.md":       {Data: []byte("# not a skill")},
	}
	skills := filterType(scanKiroDirFS(context.Background(), fsys, "ws/.kiro"), "skill")
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2 (subdirs only, flat .md ignored): %+v", len(skills), skills)
	}
	byName := indexByName(skills)
	if got := byName["alpha"].Path; got != "ws/.kiro/skills/alpha/SKILL.md" {
		t.Errorf("alpha path = %q, want ws/.kiro/skills/alpha/SKILL.md", got)
	}
	if _, ok := byName["loose"]; ok {
		t.Error("flat skills/loose.md must not be treated as a skill")
	}
}

// TestScanKiroDirFS_SteeringInclusion verifies steering docs classify by
// their front-matter inclusion mode, including a BOM+CRLF-authored file
// that the old exact-`---\n`-prefix parser would have mis-defaulted to
// "always".
func TestScanKiroDirFS_SteeringInclusion(t *testing.T) {
	fsys := fstest.MapFS{
		"steering/always.md": {Data: []byte("# no frontmatter")},
		"steering/match.md":  {Data: []byte("---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\n---\n")},
		"steering/crlf.md":   {Data: []byte("\ufeff---\r\ninclusion: manual\r\n---\r\n# body")},
	}
	steer := filterType(scanKiroDirFS(context.Background(), fsys, "ws/.kiro"), "steering")
	byName := indexByName(steer)
	if got := byName["always"].Inclusion; got != "always" {
		t.Errorf("always.md inclusion = %q, want always", got)
	}
	if got := byName["match"].Inclusion; got != "fileMatch" {
		t.Errorf("match.md inclusion = %q, want fileMatch", got)
	}
	if got := byName["crlf"].Inclusion; got != "manual" {
		t.Errorf("crlf.md (BOM+CRLF) inclusion = %q, want manual", got)
	}
}

// TestScanKiroDirFS_Caps verifies the per-directory scan caps: 20
// steering docs, 20 skills, 10 agents. Feeding more than each cap and
// asserting the EXACT capped count distinguishes a > cap boundary from a
// broken loop that returns everything or nothing.
func TestScanKiroDirFS_Caps(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := range 25 {
		fsys[fmt.Sprintf("steering/s%02d.md", i)] = &fstest.MapFile{Data: []byte("body")}
		fsys[fmt.Sprintf("skills/k%02d/SKILL.md", i)] = &fstest.MapFile{Data: []byte("body")}
	}
	for i := range 15 {
		fsys[fmt.Sprintf("agents/a%02d.json", i)] = &fstest.MapFile{Data: []byte("{}")}
	}
	items := scanKiroDirFS(context.Background(), fsys, "ws/.kiro")
	if n := len(filterType(items, "steering")); n != maxSteeringPerDir {
		t.Errorf("steering count = %d, want %d (capped)", n, maxSteeringPerDir)
	}
	if n := len(filterType(items, "skill")); n != maxSkillsPerDir {
		t.Errorf("skill count = %d, want %d (capped)", n, maxSkillsPerDir)
	}
	if n := len(filterType(items, "agent")); n != maxAgentsPerDir {
		t.Errorf("agent count = %d, want %d (capped)", n, maxAgentsPerDir)
	}
}

// TestScanSteering_CapsOversizeRead verifies a steering file larger than
// the read cap is still classified from its front-matter head (the read
// is bounded, not skipped) — the OOM guard must not drop the doc.
func TestScanSteering_CapsOversizeRead(t *testing.T) {
	head := "---\ninclusion: manual\n---\n"
	big := make([]byte, steeringReadCap+(1<<20)) // > cap
	for i := range big {
		big[i] = 'x'
	}
	fsys := fstest.MapFS{
		"steering/huge.md": {Data: append([]byte(head), big...)},
	}
	steer := filterType(scanKiroDirFS(context.Background(), fsys, "ws/.kiro"), "steering")
	if len(steer) != 1 {
		t.Fatalf("got %d steering items, want 1", len(steer))
	}
	if steer[0].Inclusion != "manual" {
		t.Errorf("huge.md inclusion = %q, want manual (front-matter head must still parse)", steer[0].Inclusion)
	}
}

// TestHandleKiroConfig_MethodGuard verifies non-GET requests get a 405
// rather than running the scan.
func TestHandleKiroConfig_MethodGuard(t *testing.T) {
	srv := &Server{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/workspace/kiro-config", nil)
		rec := httptest.NewRecorder()
		srv.handleKiroConfig(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s returned %d, want 405", method, rec.Code)
		}
	}
}
