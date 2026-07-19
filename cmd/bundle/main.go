// Command bundle builds vibekit's browser client: it bundles the TypeScript
// entrypoints with esbuild (a Go library — no Node, no npm, per the fleet's
// no-Node-in-the-builder doctrine), assembles the CSS bundle from the
// manifest files, and writes precompressed .gz siblings for every emitted
// text asset.
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
	"compress/gzip"
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
	if err := buildCSS(); err != nil {
		return err
	}
	return gzipOutputs()
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
		}
	}
	return os.WriteFile(filepath.Join(outDir, "style.css"), []byte(out.String()), 0o600)
}

// gzipOutputs writes a maximum-compression .gz sibling for every emitted
// .js/.css/.map file (recursively, so chunks/ is covered). The server
// serves the sibling to Accept-Encoding: gzip clients; a bundle this size
// compresses ~3-4x, and precompressing at build beats per-request
// compression on every axis (CPU, latency, simplicity).
func gzipOutputs() error {
	return filepath.WalkDir(outDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".css") && !strings.HasSuffix(p, ".map") {
			return nil
		}
		return gzipFile(p)
	})
}

// gzipFile writes a BestCompression .gz sibling next to p.
func gzipFile(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	f, err := os.Create(p + ".gz")
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := zw.Write(data); err != nil {
		zw.Close()
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
