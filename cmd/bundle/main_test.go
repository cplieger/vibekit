package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stageCSS lays out the two manifest sources buildCSS reads and makes their
// parent the process cwd, which is how buildCSS resolves srcDir and outDir. A
// part is keyed "wtui/<file>" or "app/<file>" to say which manifest owns it.
// t.Chdir is process-wide, so no caller may be parallel.
func stageCSS(t *testing.T, wtuiManifest, appManifest string, parts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	wtuiDir := filepath.Join(dir, srcDir, "node_modules", "@cplieger", "web-terminal-ui", "css")
	appDir := filepath.Join(dir, srcDir, "css")
	for _, d := range []string{wtuiDir, appDir, filepath.Join(dir, outDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(wtuiDir, "MANIFEST.touch"), wtuiManifest)
	write(filepath.Join(appDir, "MANIFEST"), appManifest)
	for key, body := range parts {
		owner, file, ok := strings.Cut(key, "/")
		if !ok {
			t.Fatalf("part key %q must be wtui/<file> or app/<file>", key)
		}
		root := appDir
		if owner == "wtui" {
			root = wtuiDir
		}
		write(filepath.Join(root, file), body)
	}
	t.Chdir(dir)
	return dir
}

// TestBuildCSS_RefusesAnEmptyBundle: a manifest that EXISTS and lists nothing —
// every line commented out, or a body lost to a bad merge — used to write a
// zero-byte static/style.css and return nil. Nothing downstream catches that:
// tsc does not see CSS, the image smoke test's healthcheck reads none, and the
// CSS tests read the manifest SOURCES because the built bundle is gitignored. So
// the one artifact whose emptiness is instantly visible to a user was the one no
// gate measured.
func TestBuildCSS_RefusesAnEmptyBundle(t *testing.T) {
	dir := stageCSS(t, "# the library manifest, all comments\n", "\n#  and the app one\n\n", nil)
	if err := buildCSS(); err == nil {
		t.Fatal("buildCSS() = nil, want a refusal when the manifests listed no parts")
	}
	if _, err := os.Stat(filepath.Join(dir, outDir, "style.css")); err == nil {
		t.Error("a zero-byte style.css was written, want nothing")
	}
}

// TestBuildCSS_ConcatenatesInManifestOrder is the green half, and it pins the
// ordering the override mechanism depends on: the library's parts precede the
// app's, because source order is what makes a later rule win.
func TestBuildCSS_ConcatenatesInManifestOrder(t *testing.T) {
	dir := stageCSS(t, "base.css\n", "# a comment, then a part\napp.css\n", map[string]string{
		"wtui/base.css": "/*base*/\n",
		"app/app.css":   "/*app*/\n",
	})
	if err := buildCSS(); err != nil {
		t.Fatalf("buildCSS() = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, outDir, "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "/*base*/\n/*app*/\n" {
		t.Errorf("style.css = %q, want the library part before the app part", got)
	}
}

// TestBuildCSS_ReportsAMissingPart: a manifest naming a stylesheet that is not
// there stays a hard failure, so the new empty-bundle refusal cannot be reached
// by a build that lost its parts one at a time.
func TestBuildCSS_ReportsAMissingPart(t *testing.T) {
	stageCSS(t, "base.css\n", "gone.css\n", map[string]string{"wtui/base.css": "/*base*/\n"})
	err := buildCSS()
	if err == nil || !strings.Contains(err.Error(), "css part") {
		t.Errorf("buildCSS() = %v, want a css part error", err)
	}
}

// stageOut lays out a static/ tree and makes its parent the process cwd, which
// is how cleanOutputs resolves outDir. A key is a slash-separated path under
// static/. t.Chdir is process-wide, so no caller may be parallel.
func stageOut(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, outDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

// TestCleanOutputs_SweepsANestedModuleTree is the regression this file exists
// for: the sweep enumerated the directories the bundler emitted at the time it
// was written, so `static/exec-view/` — a module tree added later — survived
// every rebuild, stayed gitignored and therefore invisible, and was embedded
// into the binary and served. The rule is now the extension at any depth, so a
// tree added next needs no maintenance here.
func TestCleanOutputs_SweepsANestedModuleTree(t *testing.T) {
	dir := stageOut(t, map[string]string{
		"exec-view/status.js":   "stale\n",
		"exec-view/model.js":    "stale\n",
		"deeper/nested/tree.js": "stale\n",
		"app.js":                "bundle\n",
		"index.html":            "<!doctype html>\n",
		"favicon.svg":           "<svg/>\n",
	})
	if err := cleanOutputs(); err != nil {
		t.Fatalf("cleanOutputs() = %v", err)
	}
	for _, gone := range []string{"exec-view/status.js", "exec-view/model.js", "deeper/nested/tree.js", "app.js"} {
		if _, err := os.Stat(filepath.Join(dir, outDir, filepath.FromSlash(gone))); !os.IsNotExist(err) {
			t.Errorf("static/%s still present, want it swept", gone)
		}
	}
	// An emptied module directory is embedded too, so the shell goes with its
	// contents.
	for _, gone := range []string{"exec-view", "deeper"} {
		if _, err := os.Stat(filepath.Join(dir, outDir, gone)); !os.IsNotExist(err) {
			t.Errorf("static/%s/ still present, want the empty shell pruned", gone)
		}
	}
	for _, kept := range []string{"index.html", "favicon.svg"} {
		if _, err := os.Stat(filepath.Join(dir, outDir, kept)); err != nil {
			t.Errorf("static/%s was removed, want the committed asset untouched: %v", kept, err)
		}
	}
}

// TestCleanOutputs_KeepsADirectoryHoldingACommittedAsset: the prune may only
// take a shell the sweep emptied. A directory holding a hand-authored file
// stays, with that file, however much bundle output sat beside it.
func TestCleanOutputs_KeepsADirectoryHoldingACommittedAsset(t *testing.T) {
	dir := stageOut(t, map[string]string{
		"icons/logo.svg": "<svg/>\n",
		"icons/logo.js":  "stale\n",
	})
	if err := cleanOutputs(); err != nil {
		t.Fatalf("cleanOutputs() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, outDir, "icons", "logo.svg")); err != nil {
		t.Errorf("static/icons/logo.svg = %v, want it kept", err)
	}
	if _, err := os.Stat(filepath.Join(dir, outDir, "icons", "logo.js")); !os.IsNotExist(err) {
		t.Error("static/icons/logo.js still present, want it swept")
	}
}

// stampOf stages an output tree, writes the manifest over it and returns the
// decoded document. t.Chdir is process-wide, so no caller may be parallel.
func stampOf(t *testing.T, files map[string]string) precacheManifest {
	t.Helper()
	dir := stageOut(t, files)
	if err := writePrecacheManifest(); err != nil {
		t.Fatalf("writePrecacheManifest() = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, outDir, precacheName))
	if err != nil {
		t.Fatalf("read %s: %v", precacheName, err)
	}
	var got precacheManifest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", precacheName, err)
	}
	return got
}

// TestWritePrecacheManifest_ListsOnlyTheHashedChunks: the worker cannot guess the
// hashed chunk names, so the list has to be built from what landed — and it holds
// NOTHING ELSE. app.js and style.css are served `no-cache` because a release
// replaces their bytes under those names, so a cache that answered them would pair
// a fresh index.html with the previous build's bundle; sw.js is excluded for its
// own reason (a worker that caches itself makes a broken worker permanent), and
// index.html stays out because it is `no-store`.
func TestWritePrecacheManifest_ListsOnlyTheHashedChunks(t *testing.T) {
	got := stampOf(t, map[string]string{
		"app.js":                        "entry\n",
		"app.js.map":                    "map\n",
		"style.css":                     "css\n",
		"sw.js":                         "worker\n",
		"chunks/editor-AAAA1111.js":     "chunk\n",
		"chunks/editor-AAAA1111.js.map": "map\n",
		"chunks/history-BBBB2222.js":    "chunk\n",
		"index.html":                    "<!doctype html>\n",
		"manifest.json":                 "{}\n",
	})
	want := []string{"chunks/editor-AAAA1111.js", "chunks/history-BBBB2222.js"}
	if !slices.Equal(got.Assets, want) {
		t.Errorf("assets = %v, want %v", got.Assets, want)
	}
	if got.Stamp == "" {
		t.Error("stamp is empty, want a stamp over the names")
	}
}

// TestWritePrecacheManifest_StampTracksTheChunkSet: a deploy frequently leaves
// sw.js byte-identical, in which case the browser runs no worker update at all and
// the manifest's stamp is the ONLY thing that can tell the cache the build moved.
// Any change to what the list holds has to move it, and a chunk carries its
// content hash in its own name, so a rename IS a content change.
func TestWritePrecacheManifest_StampTracksTheChunkSet(t *testing.T) {
	before := stampOf(t, map[string]string{
		"app.js":                    "entry\n",
		"chunks/editor-AAAA1111.js": "chunk\n",
	})
	renamed := stampOf(t, map[string]string{
		"app.js":                    "entry\n",
		"chunks/editor-CCCC3333.js": "chunk\n",
	})
	if before.Stamp == renamed.Stamp {
		t.Errorf("stamp %q survived a chunk rename", renamed.Stamp)
	}
	added := stampOf(t, map[string]string{
		"app.js":                     "entry\n",
		"chunks/editor-AAAA1111.js":  "chunk\n",
		"chunks/history-BBBB2222.js": "chunk\n",
	})
	if before.Stamp == added.Stamp {
		t.Errorf("stamp %q survived a second chunk arriving", added.Stamp)
	}
}

// TestWritePrecacheManifest_StampIgnoresAStableName: the other half of the same
// contract. Nothing the worker caches changed when only app.js did, so the stamp
// must not move and make the worker re-fetch and re-prune a cache that is already
// correct.
func TestWritePrecacheManifest_StampIgnoresAStableName(t *testing.T) {
	before := stampOf(t, map[string]string{
		"app.js":                    "entry v1\n",
		"chunks/editor-AAAA1111.js": "chunk\n",
	})
	after := stampOf(t, map[string]string{
		"app.js":                    "entry v2\n",
		"chunks/editor-AAAA1111.js": "chunk\n",
	})
	if before.Stamp != after.Stamp {
		t.Errorf("stamp moved from %q to %q for a name the cache never holds", before.Stamp, after.Stamp)
	}
}

// TestWritePrecacheManifest_NoChunksDirectory: a build that split nothing is not
// an error, and the manifest is still valid — an EMPTY list rather than a JSON
// null, which is what parseManifest reads as an unusable document.
func TestWritePrecacheManifest_NoChunksDirectory(t *testing.T) {
	dir := stageOut(t, map[string]string{
		"app.js":    "entry\n",
		"style.css": "css\n",
	})
	if err := writePrecacheManifest(); err != nil {
		t.Fatalf("writePrecacheManifest() = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, outDir, precacheName))
	if err != nil {
		t.Fatalf("read %s: %v", precacheName, err)
	}
	if !strings.Contains(string(body), `"assets":[]`) {
		t.Errorf("manifest = %s, want an empty assets list", body)
	}
}

// TestCleanOutputs_SweepsThePrecacheManifest: it is build output, so a rebuild
// must not leave the previous build's list beside the new assets. manifest.json
// is hand-authored and sits in the same directory, which is why bundleOwns names
// this file rather than matching ".json".
func TestCleanOutputs_SweepsThePrecacheManifest(t *testing.T) {
	dir := stageOut(t, map[string]string{
		precacheName:    `{"stamp":"stale","assets":[]}`,
		"manifest.json": `{"name":"vibekit"}`,
	})
	if err := cleanOutputs(); err != nil {
		t.Fatalf("cleanOutputs() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, outDir, precacheName)); !os.IsNotExist(err) {
		t.Errorf("static/%s still present, want it swept", precacheName)
	}
	if _, err := os.Stat(filepath.Join(dir, outDir, "manifest.json")); err != nil {
		t.Errorf("static/manifest.json was removed, want the committed asset untouched: %v", err)
	}
}

// stageScripts lays out a static-src/ holding the two script entries bundleScripts
// builds, with the worker body given, and makes the parent the process cwd. The page
// entry reads __SSE_WORKER_URL__ so the injected literal reaches app.js. t.Chdir is
// process-wide, so no caller may be parallel.
func stageScripts(t *testing.T, workerBody string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, srcDir)
	for _, d := range []string{src, filepath.Join(dir, outDir)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	app := "declare const __SSE_WORKER_URL__: string;\nconsole.log(__SSE_WORKER_URL__);\n"
	if err := os.WriteFile(filepath.Join(src, "app.ts"), []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorker(t, dir, workerBody)
	t.Chdir(dir)
	return dir
}

func writeWorker(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, srcDir, "sse-worker.ts"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// workerChunks lists the sse-worker scripts under static/chunks/, sourcemaps excluded.
func workerChunks(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, outDir, "chunks"))
	if err != nil {
		t.Fatalf("read chunks: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sse-worker-") && filepath.Ext(e.Name()) == ".js" {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestBundleScripts_InjectsTheHashedWorkerURL: the worker lands under /chunks/ at a
// content-hashed name, exactly one of them, and app.js names it — the page constructs
// the worker from that literal, so a mismatch is a worker that never spawns. A change
// to the worker's source moves the name and the rebuild removes the old one, which is
// what makes the URL the worker's identity across a deploy.
func TestBundleScripts_InjectsTheHashedWorkerURL(t *testing.T) {
	dir := stageScripts(t, "self.onconnect = () => { console.log('one'); };\n")
	build := func() {
		if err := cleanOutputs(); err != nil {
			t.Fatalf("cleanOutputs() = %v", err)
		}
		if err := bundleScripts(); err != nil {
			t.Fatalf("bundleScripts() = %v", err)
		}
	}
	build()
	first := workerChunks(t, dir)
	if len(first) != 1 {
		t.Fatalf("worker chunks after build = %q, want exactly one", first)
	}
	if !strings.HasPrefix(first[0], "sse-worker-") || first[0] == "sse-worker-.js" {
		t.Errorf("worker chunk = %q, want sse-worker-<hash>.js", first[0])
	}
	app, err := os.ReadFile(filepath.Join(dir, outDir, "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	want := `"/chunks/` + first[0] + `"`
	if !strings.Contains(string(app), want) {
		t.Errorf("app.js does not carry %s; body = %s", want, app)
	}

	writeWorker(t, dir, "self.onconnect = () => { console.log('two'); };\n")
	build()
	second := workerChunks(t, dir)
	if len(second) != 1 {
		t.Fatalf("worker chunks after rebuild = %q, want exactly one", second)
	}
	if second[0] == first[0] {
		t.Errorf("worker chunk after a source change = %q, want a different hash than %q", second[0], first[0])
	}
	app, err = os.ReadFile(filepath.Join(dir, outDir, "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if !strings.Contains(string(app), `"/chunks/`+second[0]+`"`) {
		t.Errorf("app.js after rebuild does not carry the new worker name %q", second[0])
	}
}

// TestEmittedEntry_RefusesAnAmbiguousMetafile: the page needs ONE worker URL, so a
// metafile naming two entry scripts (or none) is refused rather than picked from.
func TestEmittedEntry_RefusesAnAmbiguousMetafile(t *testing.T) {
	cases := map[string]string{
		"none": `{"outputs":{"static/chunks/x.js.map":{}}}`,
		"two": `{"outputs":{"static/chunks/a-1.js":{"entryPoint":"static-src/a.ts"},` +
			`"static/chunks/b-2.js":{"entryPoint":"static-src/b.ts"}}}`,
	}
	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := emittedEntry(meta); err == nil {
				t.Errorf("emittedEntry(%s) = %q, nil; want a refusal", name, got)
			}
		})
	}
	got, err := emittedEntry(`{"outputs":{"static/chunks/sse-worker-AB.js":{"entryPoint":"static-src/sse-worker.ts"},` +
		`"static/chunks/sse-worker-AB.js.map":{}}}`)
	if err != nil || got != "/chunks/sse-worker-AB.js" {
		t.Errorf("emittedEntry(one) = %q, %v; want /chunks/sse-worker-AB.js, nil", got, err)
	}
}
