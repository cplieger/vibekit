// Command toolcatalog compiles vibekit's tool catalog from registry
// data. It joins the mise registry (name -> preferred install backends,
// descriptions, aliases; MIT, github.com/jdx/mise /registry) with the
// aqua registry (per-package binary install definitions; MIT,
// github.com/aquaproj/aqua-registry /pkgs) and a vibekit overlay file
// (curated featured set, shims, manual entries), emitting one
// tool-catalog.json the server loads read-only.
//
// Runs at image build time (Dockerfile downloads both registry
// tarballs at Renovate-pinned refs):
//
//	go run ./cmd/toolcatalog -mise <mise-repo>/registry \
//	    -aqua <aqua-registry-repo>/pkgs \
//	    -overlay catalog-overlays.json \
//	    -refs mise=v2026.7.6,aqua=v4.480.0 \
//	    -out tool-catalog.json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/cplieger/vibekit/internal/tools"
	"gopkg.in/yaml.v3"
)

func main() {
	miseDir := flag.String("mise", "", "path to the mise registry dir (registry/*.toml)")
	aquaDir := flag.String("aqua", "", "path to the aqua-registry pkgs dir")
	overlayPath := flag.String("overlay", "", "path to the vibekit overlay JSON (optional)")
	refsFlag := flag.String("refs", "", "comma-separated name=ref pairs recorded in the catalog")
	outPath := flag.String("out", "tool-catalog.json", "output path")
	flag.Parse()
	if *miseDir == "" || *aquaDir == "" {
		log.Fatal("toolcatalog: -mise and -aqua are required")
	}

	catalog := &tools.Catalog{Refs: parseRefs(*refsFlag), Entries: map[string]tools.CatalogEntry{}}
	stats := compileMiseEntries(catalog, *miseDir, *aquaDir)

	if *overlayPath != "" {
		if err := applyOverlay(catalog, *overlayPath, *aquaDir); err != nil {
			log.Fatalf("toolcatalog: overlay: %v", err)
		}
	}

	checkCatalogInvariants(catalog)
	writeCatalog(catalog, *outPath, stats)
}

// compileStats counts the outcome of a catalog compile run.
type compileStats struct{ tools, aquaBacked, skipped int }

// compileMiseEntries walks the mise registry, compiling each usable
// tool into the catalog and returning the run's counts.
func compileMiseEntries(catalog *tools.Catalog, miseDir, aquaDir string) compileStats {
	var stats compileStats
	entries, err := os.ReadDir(miseDir)
	if err != nil {
		log.Fatalf("toolcatalog: read mise registry: %v", err)
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(de.Name(), ".toml")
		entry, ok, cerr := compileEntry(miseDir, aquaDir, name)
		if cerr != nil {
			log.Fatalf("toolcatalog: %s: %v", name, cerr)
		}
		if !ok {
			stats.skipped++
			continue
		}
		catalog.Entries[name] = entry
		stats.tools++
		if strings.HasPrefix(entry.Source, "aqua:") {
			stats.aquaBacked++
		}
	}
	return stats
}

// checkCatalogInvariants fails the build if the compiled catalog is
// implausibly small or a featured entry lacks a source.
func checkCatalogInvariants(catalog *tools.Catalog) {
	// Build invariants: a Renovate ref bump that guts the catalog must
	// fail loudly, not ship. Floor chosen well under the current 718
	// but far above any plausible healthy shrink.
	const minEntries = 400
	if len(catalog.Entries) < minEntries {
		log.Fatalf("toolcatalog: only %d entries compiled (< %d) — registry format drift?",
			len(catalog.Entries), minEntries)
	}
	for name := range catalog.Entries {
		e := catalog.Entries[name]
		if e.Featured && e.Source == "" {
			log.Fatalf("toolcatalog: featured entry %q has no source", name)
		}
	}
}

// writeCatalog marshals the catalog to outPath and prints a summary.
func writeCatalog(catalog *tools.Catalog, outPath string, stats compileStats) {
	data, err := json.Marshal(catalog)
	if err != nil {
		log.Fatalf("toolcatalog: marshal: %v", err)
	}
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		log.Fatalf("toolcatalog: write: %v", err)
	}
	fmt.Printf("toolcatalog: %d tools (%d aqua-backed, %d skipped) -> %s (%d KB)\n",
		stats.tools, stats.aquaBacked, stats.skipped, outPath, len(data)/1024)
}

func parseRefs(s string) map[string]string {
	refs := map[string]string{}
	for pair := range strings.SplitSeq(s, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(pair), "="); ok {
			refs[k] = v
		}
	}
	return refs
}

// miseTool is the subset of a mise registry/<name>.toml we consume.
// The backends array holds strings or tables ({backend = "...", os =
// [...]}), so it decodes as []any and is coerced below.
type miseTool struct {
	Backends    []any    `toml:"backends"`
	Description string   `toml:"description"`
	Aliases     []string `toml:"aliases"`
	OS          []string `toml:"os"`
}

// compileEntry builds one catalog entry from a mise registry file,
// resolving the first backend vibekit supports. ok=false means the
// tool has no usable backend (or is not for linux) and is skipped.
func compileEntry(miseDir, aquaDir, name string) (tools.CatalogEntry, bool, error) {
	var mt miseTool
	if _, err := toml.DecodeFile(filepath.Join(miseDir, name+".toml"), &mt); err != nil {
		return tools.CatalogEntry{}, false, err
	}
	if len(mt.OS) > 0 && !slices.Contains(mt.OS, "linux") {
		return tools.CatalogEntry{}, false, nil
	}
	entry := tools.CatalogEntry{
		Name:        name,
		Description: strings.TrimSpace(mt.Description),
		Aliases:     mt.Aliases,
	}
	for _, raw := range mt.Backends {
		backend := backendString(raw)
		if backend == "" {
			continue
		}
		source, aq, err := resolveBackend(aquaDir, backend)
		if errors.Is(err, errUnsupported) {
			continue // deliberately unsupported backend kind/type
		}
		if errors.Is(err, fs.ErrNotExist) {
			// The mise entry references an aqua package the pinned
			// aqua-registry ref doesn't have (the two registries move
			// independently). Skip this backend, try the next.
			continue
		}
		if err != nil {
			// Unreadable/unparseable definition = registry format
			// drift. FAIL the build so a Renovate ref bump can't ship
			// a silently shrunken catalog.
			return tools.CatalogEntry{}, false, fmt.Errorf("backend %s: %w", backend, err)
		}
		entry.Source = source
		entry.Aqua = aq
		if entry.Description == "" && aq != nil {
			entry.Description = firstLine(aq.Description)
		}
		return entry, true, nil
	}
	return tools.CatalogEntry{}, false, nil
}

// backendString extracts the backend spec from a string or table form.
// Tables appear both inline ({backend = "..."}) and as [[backends]]
// entries ({full = "...", platforms = [...]}).
func backendString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case map[string]any:
		return backendFromMap(v)
	default:
		return ""
	}
}

// backendFromMap extracts the backend spec from a table-form entry,
// returning "" when the entry restricts itself to non-linux platforms.
func backendFromMap(v map[string]any) string {
	s, _ := v["backend"].(string)
	if s == "" {
		s, _ = v["full"].(string)
	}
	for _, key := range []string{"os", "platforms"} {
		if !platformListAllowsLinux(v[key]) {
			return ""
		}
	}
	return s
}

// platformListAllowsLinux reports whether a table's os/platforms list
// permits linux. An absent or empty list means no restriction.
func platformListAllowsLinux(raw any) bool {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return true
	}
	return slices.Contains(list, any("linux"))
}

// errUnsupported marks a backend/definition vibekit deliberately does
// not compile (unsupported kind or aqua package type). Distinct from
// hard errors: a YAML parse failure or unreadable file is format drift
// and must FAIL the build, not silently shrink the catalog.
var errUnsupported = errors.New("unsupported")

// resolveBackend maps a mise backend spec onto a vibekit source. aqua
// backends must have a parseable, linux-supported definition in the
// aqua registry checkout; ecosystem backends pass through.
func resolveBackend(aquaDir, backend string) (string, *tools.AquaPackage, error) {
	kind, ref, ok := strings.Cut(backend, ":")
	if !ok {
		return "", nil, errUnsupported
	}
	// Strip mise backend options ("ubi:owner/repo[exe=x]").
	if i := strings.IndexByte(ref, '['); i >= 0 {
		ref = ref[:i]
	}
	switch kind {
	case "aqua":
		aq, err := loadAquaDef(aquaDir, ref)
		if err != nil {
			return "", nil, err
		}
		return "aqua:" + ref, aq, nil
	case "npm":
		return "npm:" + ref, nil, nil
	case "pipx":
		return "pip:" + ref, nil, nil
	case "cargo":
		return "cargo:" + ref, nil, nil
	case "go":
		return "go:" + ref, nil, nil
	default:
		// core:*, ubi:*, asdf:*, vfox:*, gem:*, dotnet:*, spm:* are
		// not supported natively; core runtimes arrive via the overlay.
		return "", nil, errUnsupported
	}
}

// loadAquaDef parses pkgs/<ref>/registry.yaml and keeps definitions the
// runtime evaluator supports on linux.
func loadAquaDef(aquaDir, ref string) (*tools.AquaPackage, error) {
	data, err := os.ReadFile(filepath.Join(aquaDir, filepath.FromSlash(ref), "registry.yaml"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Packages []tools.AquaPackage `yaml:"packages"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Packages) == 0 {
		return nil, fmt.Errorf("no packages in %s", ref)
	}
	p := doc.Packages[0]
	switch p.Type {
	case "github_release", "http", "github_content":
	default:
		// A real registry type (go_install, cargo, github_archive, …)
		// vibekit's evaluator doesn't cover — deliberate skip, not drift.
		return nil, fmt.Errorf("%w: aqua type %q", errUnsupported, p.Type)
	}
	// The description travels on the catalog entry, not the def.
	p.Description = ""
	return &p, nil
}

// overlayFile is the catalog-overlays.json document: entries keyed by
// tool name. An entry with a source replaces/creates the whole catalog
// entry; an entry without one patches display fields (featured,
// description, requires, shims) onto the compiled entry.
type overlayFile struct {
	Entries map[string]tools.CatalogEntry `json:"entries"`
}

func applyOverlay(catalog *tools.Catalog, path, aquaDir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var ov overlayFile
	if err := json.Unmarshal(data, &ov); err != nil {
		return err
	}
	for name := range ov.Entries {
		patch := ov.Entries[name]
		if patch.Source == "" {
			cur, ok := catalog.Entries[name]
			if !ok {
				return fmt.Errorf("overlay patches unknown tool %q", name)
			}
			mergeOverlay(&cur, &patch)
			catalog.Entries[name] = cur
			continue
		}
		patch.Name = name
		if ref, ok := strings.CutPrefix(patch.Source, "aqua:"); ok && patch.Aqua == nil {
			aq, err := loadAquaDef(aquaDir, ref)
			if err != nil {
				return fmt.Errorf("overlay %q: %w", name, err)
			}
			patch.Aqua = aq
		}
		catalog.Entries[name] = patch
	}
	return nil
}

func mergeOverlay(cur, patch *tools.CatalogEntry) {
	if patch.Featured {
		cur.Featured = true
	}
	if patch.Description != "" {
		cur.Description = patch.Description
	}
	if patch.Requires != nil {
		cur.Requires = patch.Requires
	}
	if patch.Shims != nil {
		cur.Shims = patch.Shims
	}
	if patch.Probe != "" {
		cur.Probe = patch.Probe
	}
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(first)
}
