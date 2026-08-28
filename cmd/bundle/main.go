// Command bundle builds vibekit's browser client: it bundles the TypeScript
// entrypoints with esbuild (a Go library — no Node, no npm, per the fleet's
// no-Node-in-the-builder doctrine) and assembles the CSS bundle from the
// manifest files. Compression is the server's job (webhttp.StaticHandler
// serves gzip variants); the bundler emits plain artifacts only.
//
// It replaces the previous tsc-emit pipeline (per-module JS served over an
// importmap: ~260 uncached module fetches per page load) with three cacheable
// artifacts: /app.js (+ hashed lazy chunks), /sw.js, /style.css. tsc remains
// the TYPE gate (run with --noEmit in CI and the Docker build); esbuild does
// not typecheck.
//
// Usage: go run ./cmd/bundle   (from the repo root; also run by the
// Dockerfile builder stage). Inputs are static-src/ plus the library sources
// under static-src/node_modules/ (npm install locally; registry tarballs in
// the Docker build). Outputs land in static/, which go:embed ships.
package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	return buildCSS()
}

// cleanOutputs removes previous build artifacts from static/ so stale
// modules from older builds (or the pre-bundler tsc-emit layout) never
// linger into the embed. Committed assets (index.html, manifest.json,
// icons) are untouched: only the patterns the bundle owns are removed.
func cleanOutputs() error {
	for _, dir := range []string{"chunks", "vendor", "handlers", "actions", "fundamentals", "lib", "wire", "__test-helpers__"} {
		if err := os.RemoveAll(filepath.Join(outDir, dir)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".js.map") ||
			strings.HasSuffix(name, ".gz") || name == "style.css" || name == "style.css.map" {
			if err := os.Remove(filepath.Join(outDir, name)); err != nil {
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
