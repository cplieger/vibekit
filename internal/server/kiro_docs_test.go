package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// writeFile creates dir/rel with its parents, for the real-filesystem cases
// (the cache signature is mtime-based, so fstest.MapFS cannot exercise it).
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func docsByCategory(docs []kiroDoc, cat string) []kiroDoc {
	var out []kiroDoc
	for _, d := range docs {
		if d.Category == cat {
			out = append(out, d)
		}
	}
	return out
}

func findDoc(docs []kiroDoc, name string) (kiroDoc, bool) {
	for _, d := range docs {
		if d.Name == name {
			return d, true
		}
	}
	return kiroDoc{}, false
}

// TestScanKiroDocs_FoldedDescriptionSurvives is the endpoint half of the parser
// regression: the row a user reads is the one that used to say ">".
func TestScanKiroDocs_FoldedDescriptionSurvives(t *testing.T) {
	fsys := fstest.MapFS{
		"agents/twin.md": {Data: []byte("---\nname: twin\ndescription: >\n  Even-cycle twin of the\n  other reviewer.\nmodel: claude-opus-5\ntools: [read, write]\n---\n")},
	}
	docs := scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil)
	d, ok := findDoc(docs, "twin")
	if !ok {
		t.Fatalf("agent row missing: %+v", docs)
	}
	if d.Description != "Even-cycle twin of the other reviewer." {
		t.Errorf("Description = %q, want the folded value joined", d.Description)
	}
	if d.Model != "claude-opus-5" {
		t.Errorf("Model = %q", d.Model)
	}
	if !slices.Equal(d.Tools, []string{"read", "write"}) {
		t.Errorf("Tools = %v", d.Tools)
	}
	if d.Path != "ws/.kiro/agents/twin.md" {
		t.Errorf("Path = %q", d.Path)
	}
}

func TestScanKiroDocs_Steering(t *testing.T) {
	fsys := fstest.MapFS{
		"steering/always.md": {Data: []byte("---\ndescription: Always on\n---\n")},
		"steering/matched.md": {Data: []byte(
			"---\ninclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\ndescription: Go layout\n---\n",
		)},
		"steering/manual.md": {Data: []byte("---\ninclusion: manual\n---\n")},
		// Recursive: a nested steering doc must still be found.
		"steering/nested/deep.md": {Data: []byte("---\ndescription: Nested\n---\n")},
	}
	steer := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catSteering)
	if len(steer) != 4 {
		t.Fatalf("got %d steering rows, want 4: %+v", len(steer), steer)
	}
	m, ok := findDoc(steer, "matched")
	if !ok {
		t.Fatal("matched.md missing")
	}
	if m.Inclusion != "fileMatch" || m.FileMatch != "internal/**/*.go" {
		t.Errorf("inclusion/fileMatch = %q/%q", m.Inclusion, m.FileMatch)
	}
	if man, mOK := findDoc(steer, "manual"); !mOK || man.Inclusion != "manual" {
		t.Errorf("manual.md inclusion = %+v", man)
	}
	// A doc with no front-matter name falls back to the basename, and the
	// nested one carries its subdirectory as the group.
	if n, nOK := findDoc(steer, "deep"); !nOK || n.Group != "nested" {
		t.Errorf("nested row = %+v, want group %q", n, "nested")
	}
}

// A skill that declares NO inclusion must report none. steering.Parse defaults
// the field to "always" because that is right for a steering document, and
// forwarding it here badged every skill in the browser as always-loaded — a
// claim about token cost that was never in the file. KAS's
// SkillFrontMatterSchema declares no inclusion key at all.
func TestScanKiroDocs_SkillWithoutInclusionReportsNone(t *testing.T) {
	fsys := fstest.MapFS{
		"skills/plain/SKILL.md": {Data: []byte("---\nname: plain\ndescription: No mode declared\n---\n")},
	}
	skills := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catSkill)
	p, ok := findDoc(skills, "plain")
	if !ok {
		t.Fatal("plain row missing")
	}
	if p.Inclusion != "" {
		t.Errorf("Inclusion = %q, want empty — the mode was never declared", p.Inclusion)
	}
}

// The other half, and why the fix is not simply dropping the field: the schema is
// `.passthrough()` and createSteeringCommandSource reads `config?.inclusion`
// across skills and steering alike, so a skill declaring manual really does
// become a slash command and that is worth showing.
// (TestScanKiroDocs_SkillsAreManifestsOnly below covers the declared case.)

// TestScanKiroDocs_SkillsAreManifestsOnly pins the design's scoping rule:
// reference material under a skill directory is not a row.
func TestScanKiroDocs_SkillsAreManifestsOnly(t *testing.T) {
	fsys := fstest.MapFS{
		"skills/judgement/SKILL.md":                 {Data: []byte("---\nname: judgement\ndescription: Adversarial\ninclusion: manual\nsteering_override: true\n---\n")},
		"skills/judgement/references/format.md":     {Data: []byte("# Report format\n")},
		"skills/judgement/regulations/rewrite.md":   {Data: []byte("# Rewrite\n")},
		"skills/judgement/judgement-agent-guide.md": {Data: []byte("# Guide\n")},
		"skills/nomanifest/notes.md":                {Data: []byte("# Notes\n")},
	}
	skills := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catSkill)
	if len(skills) != 2 {
		t.Fatalf("got %d skill rows, want 2 (one per directory, manifests only): %+v", len(skills), skills)
	}
	j, ok := findDoc(skills, "judgement")
	if !ok {
		t.Fatal("judgement row missing")
	}
	if j.Inclusion != "manual" {
		t.Errorf("Inclusion = %q, want manual", j.Inclusion)
	}
	if !j.SteeringOverride {
		t.Error("SteeringOverride = false, want true")
	}
	if j.Path != "ws/.kiro/skills/judgement/SKILL.md" {
		t.Errorf("Path = %q", j.Path)
	}
	// A directory without a manifest is still a skill, named by its directory.
	if _, nOK := findDoc(skills, "nomanifest"); !nOK {
		t.Error("a skill directory without SKILL.md must still get a row")
	}
	for _, s := range skills {
		if strings.Contains(s.Path, "/references/") || strings.Contains(s.Path, "/regulations/") {
			t.Errorf("reference material became a row: %s", s.Path)
		}
	}
}

func TestScanKiroDocs_AgentsDedupePreferMd(t *testing.T) {
	fsys := fstest.MapFS{
		"agents/pair.md":   {Data: []byte("---\nname: pair\ndescription: From markdown\n---\n")},
		"agents/pair.json": {Data: []byte(`{"name":"pair"}`)},
		"agents/only.json": {Data: []byte(`{"name":"only"}`)},
	}
	agents := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catAgent)
	if len(agents) != 2 {
		t.Fatalf("got %d agent rows, want 2 (the pair collapses): %+v", len(agents), agents)
	}
	p, ok := findDoc(agents, "pair")
	if !ok {
		t.Fatal("pair row missing")
	}
	if !strings.HasSuffix(p.Path, ".md") {
		t.Errorf("Path = %q, want the .md of the pair (it carries the front-matter)", p.Path)
	}
	// A JSON-only agent still gets a row, named from its basename.
	if _, oOK := findDoc(agents, "only"); !oOK {
		t.Error("a .json-only agent must still get a row")
	}
}

// TestScanKiroDocs_SpecsGroupAndOrder pins both halves of the specs decision:
// the label comes from the H1 (specs carry no front-matter), and a feature is a
// group with arbitrary children rather than a hardcoded trio.
func TestScanKiroDocs_SpecsGroupAndOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"specs/alpha/design.md":       {Data: []byte("# Design — Alpha\n")},
		"specs/alpha/requirements.md": {Data: []byte("# Requirements — Alpha\n")},
		"specs/alpha/study.md":        {Data: []byte("# Study — Alpha\n")},
		"specs/beta/requirements.md":  {Data: []byte("# Requirements — Beta\n")},
		"specs/beta/tasks.md":         {Data: []byte("# Tasks — Beta\n")},
	}
	specs := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catSpec)
	if len(specs) != 5 {
		t.Fatalf("got %d spec rows, want 5: %+v", len(specs), specs)
	}
	if specs[0].Name != "Requirements — Alpha" {
		t.Errorf("first row = %q, want the H1 (specs carry no front-matter)", specs[0].Name)
	}
	// requirements → design → tasks → lexical, grouped by feature.
	var order []string
	for _, d := range specs {
		order = append(order, d.Group+"/"+strings.TrimSuffix(pathBase(d.Path), ".md"))
	}
	want := []string{
		"alpha/requirements", "alpha/design", "alpha/study",
		"beta/requirements", "beta/tasks",
	}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func TestScanKiroDocs_HooksExpandEnvelope(t *testing.T) {
	fsys := fstest.MapFS{
		"hooks/two.json": {Data: []byte(`{"version":"v1","hooks":[
			{"name":"First","trigger":"PostFileSave","action":{"type":"command","command":"echo one"}},
			{"name":"Second","trigger":"SessionStart","action":{"type":"agent","prompt":"do a thing"}}
		]}`)},
	}
	hooks := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catHook)
	if len(hooks) != 2 {
		t.Fatalf("got %d hook rows, want 2 (one envelope, two hooks): %+v", len(hooks), hooks)
	}
	f, ok := findDoc(hooks, "First")
	if !ok {
		t.Fatal("First hook missing")
	}
	if f.Trigger != "PostFileSave" || f.Action != "echo one" {
		t.Errorf("trigger/action = %q/%q", f.Trigger, f.Action)
	}
	// An agent hook's prompt is the action preview.
	if sec, sOK := findDoc(hooks, "Second"); !sOK || sec.Action != "do a thing" {
		t.Errorf("Second = %+v, want the prompt as the action", sec)
	}
}

// TestScanKiroDocs_HookFieldsAreSanitized pins that the scan goes through
// steering.ParseHooks rather than decoding JSON itself. Hook files are workspace
// content, and these values render inside a span.
func TestScanKiroDocs_HookFieldsAreSanitized(t *testing.T) {
	fsys := fstest.MapFS{
		"hooks/evil.json": {Data: []byte(`{"version":"v1","hooks":[
			{"name":"Bad\nName","trigger":"PostFileSave","action":{"type":"command","command":"a` + "`" + `b"}}
		]}`)},
	}
	hooks := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catHook)
	if len(hooks) != 1 {
		t.Fatalf("got %d hook rows, want 1", len(hooks))
	}
	if strings.ContainsAny(hooks[0].Name, "\n\r") {
		t.Errorf("Name = %q still contains a newline", hooks[0].Name)
	}
	if strings.Contains(hooks[0].Action, "`") {
		t.Errorf("Action = %q still contains a backtick", hooks[0].Action)
	}
}

// TestScanKiroDocs_NamelessHookFallsBackToTheFileName pins hookRows' name
// fallback, which was measurably unasserted: replacing
// cmp.Or(h.Name, strings.TrimSuffix(file, ".json")) with a bare h.Name left the
// whole internal/server suite green, while the two sibling fallbacks (a skill
// directory without a manifest, a .json-only agent) each had a test. A row whose
// Name is empty renders as a blank entry in the browser's Hooks tab, so the
// derived name is what makes an unnamed hook openable at all.
func TestScanKiroDocs_NamelessHookFallsBackToTheFileName(t *testing.T) {
	fsys := fstest.MapFS{
		"hooks/lint-on-save.json": {Data: []byte(`{"version":"v1","hooks":[
			{"trigger":"PostFileSave","action":{"type":"command","command":"echo lint"}}
		]}`)},
	}
	hooks := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catHook)
	if len(hooks) != 1 {
		t.Fatalf("got %d hook rows, want 1: %+v", len(hooks), hooks)
	}
	if hooks[0].Name != "lint-on-save" {
		t.Errorf("Name = %q, want the file's base name %q", hooks[0].Name, "lint-on-save")
	}
}

// TestScanKiroDocs_UnclaimedMarkdownGetsNoRow pins the scope boundary: a file no
// category claims is simply absent, which is what makes the "is the total 216 or
// 217" question stop mattering.
func TestScanKiroDocs_UnclaimedMarkdownGetsNoRow(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md":           {Data: []byte("# Kiro readme\n")},
		"scripts/notes.md":    {Data: []byte("# Notes\n")},
		"steering/claimed.md": {Data: []byte("---\ndescription: Claimed\n---\n")},
	}
	docs := scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil)
	if len(docs) != 1 {
		t.Fatalf("got %d rows, want 1 (only the steering doc is claimed): %+v", len(docs), docs)
	}
	if docs[0].Name != "claimed" {
		t.Errorf("row = %q, want claimed", docs[0].Name)
	}
}

func TestScanKiroDocs_PerCategoryCap(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := range maxDocsPerCategory + 25 {
		fsys[fmt.Sprintf("steering/s%04d.md", i)] = &fstest.MapFile{Data: []byte("---\ndescription: x\n---\n")}
	}
	steer := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catSteering)
	if len(steer) != maxDocsPerCategory {
		t.Errorf("steering count = %d, want %d (capped)", len(steer), maxDocsPerCategory)
	}
}

// TestScanKiroDocs_CancelledContextStops pins that a cancelled request does not
// keep walking the tree.
func TestScanKiroDocs_CancelledContextStops(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := range 50 {
		fsys[fmt.Sprintf("steering/s%02d.md", i)] = &fstest.MapFile{Data: []byte("---\ndescription: x\n---\n")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if docs := scanKiroDocsFS(ctx, fsys, "ws/.kiro", nil); len(docs) != 0 {
		t.Errorf("got %d rows from a cancelled scan, want 0", len(docs))
	}
}

// TestScanKiroDocs_OversizeReadStillClassifies mirrors the config endpoint's
// guard: the read is bounded, not skipped, so a huge file keeps its row.
func TestScanKiroDocs_OversizeReadStillClassifies(t *testing.T) {
	head := "---\ninclusion: manual\ndescription: Head still parses\n---\n"
	big := strings.Repeat("x", int(steeringReadCap)+(1<<20))
	fsys := fstest.MapFS{"steering/huge.md": {Data: []byte(head + big)}}
	steer := docsByCategory(scanKiroDocsFS(t.Context(), fsys, "ws/.kiro", nil), catSteering)
	if len(steer) != 1 {
		t.Fatalf("got %d rows, want 1", len(steer))
	}
	if steer[0].Inclusion != "manual" || steer[0].Description != "Head still parses" {
		t.Errorf("row = %+v, want the front-matter head parsed", steer[0])
	}
}

func TestHandleKiroDocs_MethodGuard(t *testing.T) {
	srv := &Server{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/workspace/kiro-docs", nil)
		rec := httptest.NewRecorder()
		srv.handleKiroDocs(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s returned %d, want 405", method, rec.Code)
		}
	}
}

// TestHandleKiroDocs_EmptyIsArrayNotNull pins the wire form: the client iterates
// the value, so a null would throw.
func TestHandleKiroDocs_EmptyIsArrayNotNull(t *testing.T) {
	srv := &Server{workDir: t.TempDir(), kiroDocs: &docsCache{}}
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/kiro-docs", nil)
	rec := httptest.NewRecorder()
	srv.handleKiroDocs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"docs":[]`) {
		t.Errorf("body = %s, want an empty array", body)
	}
}

// TestCollectKiroDocs_CachesOnSignature pins the memoization: an unchanged tree
// must not be rescanned, because front-matter parsing is ~200 file opens and the
// page refetches on every settings_updated broadcast.
func TestCollectKiroDocs_CachesOnSignature(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".kiro/steering/a.md", "---\ndescription: A\n---\n")
	srv := &Server{workDir: dir, kiroDocs: &docsCache{}}

	first := srv.collectKiroDocs(t.Context())
	if len(first) != 1 {
		t.Fatalf("first scan returned %d rows, want 1", len(first))
	}
	sigAfterFirst := srv.kiroDocs.sig
	if sigAfterFirst == "" {
		t.Fatal("signature not recorded after the first scan")
	}

	// Same tree: the cached slice comes back and the signature is unchanged.
	second := srv.collectKiroDocs(t.Context())
	if len(second) != 1 || srv.kiroDocs.sig != sigAfterFirst {
		t.Errorf("second scan changed the cache: rows=%d sig-changed=%v", len(second), srv.kiroDocs.sig != sigAfterFirst)
	}

	// A new file moves the directory mtime, so the signature must change and
	// the row must appear.
	writeFile(t, dir, ".kiro/steering/b.md", "---\ndescription: B\n---\n")
	third := srv.collectKiroDocs(t.Context())
	if len(third) != 2 {
		t.Errorf("after adding a doc got %d rows, want 2 (the signature must invalidate)", len(third))
	}
}

// TestCollectKiroDocs_NilCacheStillScans covers the zero Server the method-guard
// tests use: no cache wired must mean "scan directly", not a nil dereference.
func TestCollectKiroDocs_NilCacheStillScans(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".kiro/steering/a.md", "---\ndescription: A\n---\n")
	srv := &Server{workDir: dir}
	if docs := srv.collectKiroDocs(t.Context()); len(docs) != 1 {
		t.Errorf("got %d rows with no cache wired, want 1", len(docs))
	}
}

// TestCollectKiroDocs_PerRepoTreeCounts pins that a repo's own .kiro contributes
// rows, with its own path prefix.
func TestCollectKiroDocs_PerRepoTreeCounts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".kiro/steering/root.md", "---\ndescription: Root\n---\n")
	writeFile(t, dir, "myrepo/.kiro/steering/repo.md", "---\ndescription: Repo\n---\n")
	srv := &Server{workDir: dir, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context())
	if len(docs) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(docs), docs)
	}
	r, ok := findDoc(docs, "repo")
	if !ok {
		t.Fatal("the per-repo row is missing")
	}
	if !strings.Contains(r.Path, "/myrepo/.kiro/steering/repo.md") {
		t.Errorf("per-repo Path = %q, want the repo in the prefix", r.Path)
	}
}
