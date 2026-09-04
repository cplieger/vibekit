// Command bundle builds vibekit's browser client: it bundles the TypeScript
// entrypoints with esbuild (a Go library — no Node, no npm, per the fleet's
// no-Node-in-the-builder doctrine) and assembles the CSS bundle from the
// manifest files. Compression is the server's job (webhttp.StaticHandler
// serves gzip variants); the bundler emits plain artifacts only.
//
// It replaces the previous tsc-emit pipeline (per-module JS served over an
// importmap: ~260 uncached module fetches per page load) with three cacheable
// artifacts: /app.js (+ hashed lazy chunks), /sw.js, /style.css, plus the
// /precache.json list the service worker reads to cache them. tsc remains the
// TYPE gate (--noEmit in CI and the Docker build); esbuild does not typecheck.
//
// Usage: go run ./cmd/bundle   (from the repo root; also run by the Dockerfile
// builder stage). Inputs are static-src/ plus the library sources under
// static-src/node_modules/. Outputs land in static/, which go:embed ships.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const (
	srcDir = "static-src"
	outDir = "static"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bundle:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(filepath.Join(srcDir, "app.ts")); err != nil {
		return fmt.Errorf("run from the repo root: %w", err)
	}
	if err := cleanOutputs(); err != nil {
		return err
	}
	if err := bundleApp(); err != nil {
		return err
	}
	if err := bundleServiceWorker(); err != nil {
		return err
	}
	if err := buildCSS(); err != nil {
		return err
	}
	return writePrecacheManifest()
}

// precacheManifest is what static/precache.json holds: the cacheable asset list
// and a stamp over their names. Field names are the wire contract with
// static-src/sw.ts.
//
// FETCHED rather than generated into the worker, because a worker's own bytes are
// its update signal: an inlined list would reach a client only through a worker
// update, and this worker never calls skipWaiting, so the previous one stays in
// control until its clients are gone.
type precacheManifest struct {
	Stamp  string   `json:"stamp"`
	Assets []string `json:"assets"`
}

// precacheName is the manifest's own path, relative to outDir. Named because three
// things have to agree on it: this writer, cleanOutputs (via bundleOwns), and the
// worker's fetch.
const precacheName = "precache.json"

// writePrecacheManifest enumerates the shell's cacheable assets and stamps them.
//
// THE LIST IS BUILT, not written by hand: the lazy chunks are content-hashed, so
// the worker has no way to guess their names.
//
// THE STAMP IS OVER THE NAMES, which is honest only because of what the list
// holds: every entry carries esbuild's content hash in its own name, so bytes that
// move move a name. Sourcemaps are excluded: a precache is for running.
func writePrecacheManifest() error {
	assets, err := precacheAssets()
	if err != nil {
		return err
	}
	sum := sha256.New()
	for _, name := range assets {
		// Separated, or two adjacent names could be re-cut into the same stream.
		sum.Write([]byte(name + "\n"))
	}
	doc, err := json.Marshal(precacheManifest{
		Stamp:  hex.EncodeToString(sum.Sum(nil))[:16],
		Assets: assets,
	})
	if err != nil {
		return fmt.Errorf("precache marshal: %w", err)
	}
	return os.WriteFile(filepath.Join(outDir, precacheName), doc, 0o600)
}

// precacheAssets lists the content-hashed lazy chunks, sorted, as URL paths
// relative to the site root.
//
// THE HASHED NAMES ARE THE WHOLE LIST, because a name is cacheable without
// revalidation exactly when its bytes cannot change under it — the rule
// static-src/precache.ts states for the worker's fetch arm, and why assetCachePolicy
// marks app.js and style.css `no-cache`. sw.js is out for its own reason (a worker
// that caches itself makes a broken worker permanent), index.html for its `no-store`.
func precacheAssets() ([]string, error) {
	// Non-nil even when empty: a nil slice marshals to JSON `null`, and
	// parseManifest reads that as an unusable document rather than as no assets.
	assets := []string{}
	entries, err := os.ReadDir(filepath.Join(outDir, "chunks"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A build that produced no lazy chunk at all. Not reachable from this
			// entrypoint today, and not an error: an empty list is a valid document,
			// and the worker treats it as one.
			return assets, nil
		}
		return nil, fmt.Errorf("precache chunks: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".js" {
			continue
		}
		assets = append(assets, "chunks/"+e.Name())
	}
	slices.Sort(assets)
	return assets, nil
}

// cleanOutputs removes previous build artifacts from static/ so stale modules
// from older builds (or the pre-bundler tsc-emit layout) never linger into the
// embed. Committed assets (index.html, manifest.json, icons) are untouched.
//
// The bundle's ownership is stated by EXTENSION AT ANY DEPTH, matching
// .gitignore's `static/**/*.js` — an enumerated directory list is what let
// `static/exec-view/` survive every rebuild and reach the embedded tree, and
// each module tree added next was another silent gap. `chunks` and `vendor`
// keep a whole-directory removal because they may hold entries that are not
// bundle-owned by extension (a fetched package's metadata).
func cleanOutputs() error {
	for _, dir := range []string{"chunks", "vendor"} {
		if err := os.RemoveAll(filepath.Join(outDir, dir)); err != nil {
			return err
		}
	}
	if err := removeBundleFiles(outDir); err != nil {
		return err
	}
	// A directory that held only bundle output is now an empty shell: it would
	// still be embedded, and it is what a reader mistakes for a live module
	// tree.
	return pruneEmptyDirs(outDir)
}

// bundleOwns reports whether the bundler (or a generator writing into the
// bundle's tree) owns this file name, so removing it can never take a
// hand-authored asset. Kept in step with .gitignore's static/ block.
func bundleOwns(name string) bool {
	switch filepath.Ext(name) {
	case ".js", ".map", ".gz":
		return true
	}
	// Named rather than matched by ".json": static/manifest.json is hand-authored
	// and sits in the same directory.
	return name == "style.css" || name == precacheName
}

// removeBundleFiles deletes every bundle-owned file under dir, at any depth.
func removeBundleFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := removeBundleFiles(path); err != nil {
				return err
			}
			continue
		}
		if !bundleOwns(e.Name()) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// pruneEmptyDirs removes every directory under dir left empty, deepest first.
// dir itself is kept: it is the embed root and holds the committed assets.
func pruneEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if err := pruneEmptyDirs(sub); err != nil {
			return err
		}
		rest, err := os.ReadDir(sub)
		if err != nil {
			return err
		}
		if len(rest) == 0 {
			if err := os.Remove(sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// bundleApp bundles the main client entry as ESM with code splitting:
// the dynamic import() sites (history, conflicts, editor panes) become
// hashed lazy chunks under /chunks/. The entry keeps its stable /app.js
// name — the HTML never needs rewriting; cache correctness comes from the
// server's ETag revalidation (assets are no-cache, HTML is no-store).
func bundleApp() error {
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{filepath.Join(srcDir, "app.ts")},
		Outdir:            outDir,
		Bundle:            true,
		Format:            api.FormatESModule,
		Splitting:         true,
		EntryNames:        "[name]",
		ChunkNames:        "chunks/[name]-[hash]",
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Sourcemap:         api.SourceMapLinked,
		Charset:           api.CharsetUTF8,
		LogLevel:          api.LogLevelWarning,
		Write:             true,
	})
	return buildErr("app", &result)
}

// bundleServiceWorker bundles sw.ts as a single classic script (IIFE):
// app.ts registers it without {type:"module"}, and a service worker must
// stay a single file at a stable URL (byte-diff at /sw.js is the browser's
// update signal).
func bundleServiceWorker() error {
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{filepath.Join(srcDir, "sw.ts")},
		Outdir:            outDir,
		Bundle:            true,
		Format:            api.FormatIIFE,
		EntryNames:        "[name]",
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Sourcemap:         api.SourceMapLinked,
		Charset:           api.CharsetUTF8,
		LogLevel:          api.LogLevelWarning,
		Write:             true,
	})
	return buildErr("sw", &result)
}

func buildErr(what string, result *api.BuildResult) error {
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage, Color: false})
		return fmt.Errorf("%s bundle failed:\n%s", what, strings.Join(msgs, "\n"))
	}
	return nil
}

// cssManifest is one ordered concat source: a manifest file listing CSS
// paths (relative to baseDir; blank lines and #-comments skipped).
type cssManifest struct {
	manifestPath string
	baseDir      string
}

// buildCSS assembles static/style.css exactly as the former Dockerfile
// shell loop did: the @cplieger/web-terminal-ui component bundle FIRST
// (root-scoped, zero-specificity :where(.wt-root) selectors), then
// vibekit's own splits — library-before-consumer source order is the
// override mechanism (see the manifest headers).
func buildCSS() error {
	wtui := filepath.Join(srcDir, "node_modules", "@cplieger", "web-terminal-ui", "css")
	appCSS := filepath.Join(srcDir, "css")
	sources := []cssManifest{
		{manifestPath: filepath.Join(wtui, "MANIFEST.touch"), baseDir: wtui},
		{manifestPath: filepath.Join(appCSS, "MANIFEST"), baseDir: appCSS},
	}
	var out strings.Builder
	parts := 0
	for _, src := range sources {
		data, err := os.ReadFile(src.manifestPath)
		if err != nil {
			return fmt.Errorf("css manifest: %w", err)
		}
		for line := range strings.Lines(string(data)) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			part, err := os.ReadFile(filepath.Join(src.baseDir, line))
			if err != nil {
				return fmt.Errorf("css part: %w", err)
			}
			out.Write(part)
			parts++
		}
	}
	// A manifest that EXISTS and lists nothing — every line commented out, or a
	// body lost to a bad merge — is the one shape neither the missing-manifest nor
	// the missing-part error covers, and nothing downstream catches it: tsc does
	// not see CSS, the image smoke test's healthcheck reads none, and the CSS tests
	// read the manifest SOURCES because static/style.css is gitignored. So a
	// zero-byte stylesheet would ship as a build success and surface as a
	// completely unstyled app. The floor is "not zero"; the real counts are 44
	// parts and ~709 KB, and a size threshold would be a guess.
	if out.Len() == 0 {
		return fmt.Errorf("css: the manifests listed no parts (%d manifests read)", len(sources))
	}
	if err := os.WriteFile(filepath.Join(outDir, "style.css"), []byte(out.String()), 0o600); err != nil {
		return err
	}
	// Say what was measured, so a shrink is visible in the build log rather than
	// only in the browser.
	fmt.Printf("bundle: css %d parts, %d bytes\n", parts, out.Len())
	return nil
}
