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
