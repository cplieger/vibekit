// Command bundle builds vibekit's browser client: it bundles the TypeScript
// entrypoints with esbuild (a Go library — no Node, no npm) and assembles the CSS
// bundle from the manifest files. tsc remains the TYPE gate; esbuild does not
// typecheck. Compression is the server's job; the bundler emits plain artifacts.
//
// Usage: go run ./cmd/bundle (from the repo root; also run by the Dockerfile builder
// stage). Inputs are static-src/ plus static-src/node_modules/; outputs land in
// static/, which go:embed ships.
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

// precacheManifest is what static/precache.json holds: the cacheable asset list and a
// stamp over their names. Field names are the wire contract with static-src/sw.ts.
// FETCHED rather than inlined into the worker: an inlined list would reach a client
// only through a worker update, and this worker never calls skipWaiting.
type precacheManifest struct {
	Stamp  string   `json:"stamp"`
	Assets []string `json:"assets"`
}

// precacheName is the manifest's own path, relative to outDir. Three things must agree
// on it: this writer, cleanOutputs (via bundleOwns), and the worker's fetch.
const precacheName = "precache.json"

// writePrecacheManifest enumerates the shell's cacheable assets and stamps them. The
// stamp is over the NAMES, which is honest because every entry carries esbuild's
// content hash, so bytes that move move a name. Sourcemaps are excluded.
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

// precacheAssets lists the content-hashed chunks, sorted, as URL paths relative to the
// site root. Eligibility is by NAME: cacheable without revalidation exactly when the
// bytes cannot change under the name, which excludes app.js and style.css. sw.js is
// out because a worker that caches itself makes a broken worker permanent.
func precacheAssets() ([]string, error) {
	// Non-nil even when empty: a nil slice marshals to JSON `null`, and
	// parseManifest reads that as an unusable document rather than as no assets.
	assets := []string{}
	entries, err := os.ReadDir(filepath.Join(outDir, "chunks"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No chunks at all is a valid document, and the worker treats it as one.
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

// cleanOutputs removes previous build artifacts from static/ so stale modules never
// linger into the embed; committed assets (index.html, manifest.json, icons) are
// untouched. Ownership is by EXTENSION AT ANY DEPTH, matching .gitignore's
// `static/**/*.js`: an enumerated directory list let `static/exec-view/` survive every
// rebuild and reach the embedded tree. `chunks` and `vendor` are removed whole,
// because they may hold entries no extension rule owns.
func cleanOutputs() error {
	for _, dir := range []string{"chunks", "vendor"} {
		if err := os.RemoveAll(filepath.Join(outDir, dir)); err != nil {
			return err
		}
	}
	if err := removeBundleFiles(outDir); err != nil {
		return err
	}
	// A directory left holding only bundle output is an empty shell that still embeds.
	return pruneEmptyDirs(outDir)
}

// bundleOwns reports whether the bundler owns this file name, so removing it can never
// take a hand-authored asset. Kept in step with .gitignore's static/ block.
func bundleOwns(name string) bool {
	switch filepath.Ext(name) {
	case ".js", ".map", ".gz":
		return true
	}
	// Named rather than matched by ".json": static/manifest.json is hand-authored.
	return name == "style.css" || name == precacheName
}

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

// bundleApp bundles the main client entry as ESM with code splitting: the dynamic
// import() sites and the code they share with the entry become hashed chunks under
// /chunks/. The entry keeps its stable /app.js name, so the HTML never needs rewriting
// and cache correctness comes from the server's ETag revalidation.
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

// bundleServiceWorker bundles sw.ts as a single classic script (IIFE): app.ts registers
// it without {type:"module"}, and a worker must stay a single file at a stable URL,
// because a byte-diff at /sw.js is the browser's update signal.
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

// cssManifest is one ordered concat source: a manifest file listing CSS paths relative
// to baseDir; blank lines and #-comments are skipped.
type cssManifest struct {
	manifestPath string
	baseDir      string
}

// buildCSS assembles static/style.css: the @cplieger/web-terminal-ui component bundle
// FIRST (root-scoped, zero-specificity :where(.wt-root) selectors), then vibekit's own
// splits — library-before-consumer source order is the override mechanism.
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
	// A manifest that EXISTS and lists nothing is the shape neither the
	// missing-manifest nor the missing-part error covers, and nothing downstream
	// catches it: a zero-byte stylesheet would ship as a build success.
	if out.Len() == 0 {
		return fmt.Errorf("css: the manifests listed no parts (%d manifests read)", len(sources))
	}
	if err := os.WriteFile(filepath.Join(outDir, "style.css"), []byte(out.String()), 0o600); err != nil {
		return err
	}
	// So a shrink is visible in the build log rather than only in the browser.
	fmt.Printf("bundle: css %d parts, %d bytes\n", parts, out.Len())
	return nil
}
