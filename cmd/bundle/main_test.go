package main

import (
	"os"
	"path/filepath"
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
