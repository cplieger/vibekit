// Tools manifest CRUD beyond the existing /api/tools/install endpoint.
//
// Endpoints:
//
//	POST   /api/tools/{section}/{name}/enable    flip enabled:true,
//	                                              auto-enable transitive
//	                                              requires, run setup-tools.sh
//	DELETE /api/tools/{section}/{name}            cancel any in-flight install
//	                                              for this entry, run cleanup
//	                                              (clear_tool via shell), flip
//	                                              enabled:false
//	PATCH  /api/tools/{section}/{name}            update auto_update flag.
//	                                              Body: {"auto_update": bool}.
//	GET    /api/tools/status                      map of binary -> bool for
//	                                              the well-known tools the UI
//	                                              gates feature panels on
//	                                              (node, npm, npx, uv, gh,
//	                                              glab, tea, ...).
//
// All mutations write through the same atomic-rename pattern as
// forges/install.go's addToolsManifestEntry: read, mutate the in-memory
// map, marshal, write to <path>.tmp, rename. No half-written manifests.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

const (
	// Generous: a cold Rust toolchain (rustup + std), clangd (~160 MB),
	// or a JRE on a slow connection can each take several minutes. Too
	// short a timeout cancels mid-download and leaves a partial install.
	toolsInstallTimeout = 15 * time.Minute
	maxToolsInstallBody = 4 << 10
)

// inflightInstalls tracks per-entry install contexts so a DELETE can
// cancel a running enable. Keyed by "section.name". The mutex protects
// the map; each cancel func is called with the lock held briefly.
var (
	inflightMu       sync.Mutex
	inflightInstalls = map[string]context.CancelFunc{}
)

// manifestMu serializes read-modify-write cycles on tools.json so two
// concurrent mutations (e.g. an Enable and a forge-login auto-enable)
// can't clobber each other (the file is read, mutated in memory, then
// rewritten — a classic TOCTOU without this guard). Held across the
// read+write pair, not during the install subprocess.
var manifestMu sync.Mutex

// statusBinaries is the set of binaries the UI may probe via
// /api/tools/status. Each key matches a name kiro-cli or vibekit
// expects to find on PATH; presence determines whether the
// feature panel shows an "install" prompt.
var statusBinaries = []string{
	"node", "npm", "npx",
	"go", "gofmt",
	"java",
	"ruby", "gem",
	"cargo", "rustc",
	"uv", "uvx",
	"gh", "glab", "tea",
	"typescript-language-server", "tsgo",
	"pyright", "pyrefly",
	"gopls", "rust-analyzer", "clangd",
	"jdtls", "kotlin-language-server", "solargraph",
}

// allowedSections is the closed set of top-level sections the API will
// accept in {section} path params. Anything outside this is a 404 to
// keep arbitrary jq paths from leaking through the URL.
var allowedSections = map[string]bool{
	"runtimes": true,
	"binary":   true,
	"go":       true,
	"npm":      true,
	"pip":      true,
	"custom":   true,
	"cargo":    true,
	"lsp":      true,
	"apt":      true,
}

// toolNamePattern accepts the names we ship in the default and any
// the user is likely to add via the UI. Conservative on purpose:
// alphanumeric, dot, dash, underscore, plus and at-sign for npm-style
// scoped packages. No slashes, no spaces, no shell metas.
func validToolName(name string) bool {
	if name == "" || len(name) > 80 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+' || r == '@':
		default:
			return false
		}
	}
	return true
}

// readManifest returns the parsed tools.json and the path to it.
// Missing file is treated as empty manifest (lets fresh installs
// receive their first PATCH/POST without a prior write).
func (s *Server) readManifest() (manifest map[string]any, path string, err error) {
	path = filepath.Join(s.configDir, "tools.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, path, nil
		}
		return nil, path, fmt.Errorf("read manifest: %w", err)
	}
	var m map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, path, fmt.Errorf("parse manifest: %w", err)
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, path, nil
}

func writeManifest(path string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	_, err = atomicfile.WriteFile(context.Background(), path, append(data, '\n'),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755))
	return err
}

// entryAt returns a typed view of the entry at section.name within
// the parsed manifest. ok reports whether the entry exists.
func entryAt(m map[string]any, section, name string) (entry map[string]any, ok bool) {
	sec, hasSec := m[section].(map[string]any)
	if !hasSec {
		return nil, false
	}
	e, hasEntry := sec[name].(map[string]any)
	if !hasEntry {
		return nil, false
	}
	return e, true
}

// requiresOf returns the section.name strings declared in the entry's
// .requires array, or nil if absent.
func requiresOf(entry map[string]any) []string {
	raw, ok := entry["requires"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resolveDeps walks the .requires graph starting at section.name and
// returns every entry that needs to be enabled, in dependency order
// (deps first, then the requested entry last). Cycles abort with an
// error; the manifest's requires graph is small and human-curated so
// this should never trigger in practice.
func resolveDeps(m map[string]any, section, name string) ([]string, error) {
	visited := map[string]bool{}
	visiting := map[string]bool{}
	var order []string

	var visit func(sec, n string) error
	visit = func(sec, n string) error {
		key := sec + "." + n
		if visited[key] {
			return nil
		}
		if visiting[key] {
			return fmt.Errorf("requires cycle through %s", key)
		}
		visiting[key] = true
		entry, ok := entryAt(m, sec, n)
		if !ok {
			return fmt.Errorf("requires unknown entry %s", key)
		}
		for _, req := range requiresOf(entry) {
			parts := strings.SplitN(req, ".", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid requires %q on %s", req, key)
			}
			if !allowedSections[parts[0]] {
				return fmt.Errorf("invalid section %q in requires %q on %s", parts[0], req, key)
			}
			if err := visit(parts[0], parts[1]); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		order = append(order, key)
		return nil
	}
	if err := visit(section, name); err != nil {
		return nil, err
	}
	return order, nil
}

// dependentsOf returns every enabled entry that lists section.name in
// its .requires. Used by DELETE to surface a cascade warning to the UI.
func dependentsOf(m map[string]any, section, name string) []string {
	target := section + "." + name
	var out []string
	for sec, secMapAny := range m {
		// Only scan real tool sections — skips _comment and any other
		// non-section top-level keys.
		if !allowedSections[sec] {
			continue
		}
		secMap, ok := secMapAny.(map[string]any)
		if !ok {
			continue
		}
		for n, eAny := range secMap {
			e, ok := eAny.(map[string]any)
			if !ok {
				continue
			}
			// enabled defaults to true when absent, matching
			// setup-tools.sh's entry_enabled. A missing flag means the
			// entry IS active, so it must be counted as a dependent —
			// otherwise cascade-delete would silently orphan it.
			if !boolField(e, "enabled", true) {
				continue
			}
			for _, req := range requiresOf(e) {
				if req == target {
					out = append(out, sec+"."+n)
				}
			}
		}
	}
	return out
}

func boolField(m map[string]any, key string, fallback bool) bool {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}

// runSetupTools runs setup-tools.sh under the provided context and
// returns combined output. Used by both /enable and /install handlers.
func runSetupTools(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "/opt/vibekit/setup-tools.sh")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// parseToolPath extracts {section} and {name} from URL paths like
// /api/tools/{section}/{name}/... — using net/http's path values.
// Returns ok=false when either is missing or invalid.
func parseToolPath(r *http.Request) (section, name string, ok bool) {
	section = r.PathValue("section")
	name = r.PathValue("name")
	if !allowedSections[section] || !validToolName(name) {
		return "", "", false
	}
	return section, name, true
}

// handleToolEnable: POST /api/tools/{section}/{name}/enable
//
// Flips enabled=true on the entry and every transitive dep, then runs
// setup-tools.sh. Returns the combined script output. Errors during
// setup are surfaced but the manifest changes stay (so a partial
// install can be retried by clicking Enable again, idempotent).
func (s *Server) handleToolEnable(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	section, name, ok := parseToolPath(r)
	if !ok {
		api.NotFound(w, "unknown tool")
		return
	}

	// Read-modify-write of the manifest is serialized so a concurrent
	// enable / forge-login can't clobber this flag flip. Released before
	// the long-running install subprocess below.
	manifestMu.Lock()
	manifest, path, err := s.readManifest()
	if err != nil {
		manifestMu.Unlock()
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	if _, exists := entryAt(manifest, section, name); !exists {
		manifestMu.Unlock()
		api.NotFound(w, fmt.Sprintf("%s.%s not in tools.json", section, name))
		return
	}

	chain, err := resolveDeps(manifest, section, name)
	if err != nil {
		manifestMu.Unlock()
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	for _, key := range chain {
		parts := strings.SplitN(key, ".", 2)
		entry, _ := entryAt(manifest, parts[0], parts[1])
		entry["enabled"] = true
	}
	if err := writeManifest(path, manifest); err != nil {
		manifestMu.Unlock()
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	manifestMu.Unlock()

	// Track this install so a DELETE can cancel it mid-run.
	key := section + "." + name
	ctx, cancel := context.WithTimeout(r.Context(), toolsInstallTimeout)
	defer cancel()
	inflightMu.Lock()
	if prior, exists := inflightInstalls[key]; exists {
		prior() // cancel any older run for the same entry
	}
	inflightInstalls[key] = cancel
	inflightMu.Unlock()
	defer func() {
		inflightMu.Lock()
		delete(inflightInstalls, key)
		inflightMu.Unlock()
	}()

	out, runErr := runSetupTools(ctx)
	resp := map[string]any{
		"output":        out,
		"enabled_chain": chain,
		"section":       section,
		"name":          name,
	}
	if runErr != nil {
		resp["error"] = runErr.Error()
		// Roll back the target entry's enabled flag so the UI shows the
		// Enable button again (otherwise the row renders as enabled+
		// healthy despite no working binary). Deps stay enabled — they
		// may have installed cleanly; the user can retry just this
		// entry. Best-effort: a write failure here is logged into the
		// response output; the original error is the user-visible one.
		manifestMu.Lock()
		if m, mp, mErr := s.readManifest(); mErr == nil {
			if e, ok := entryAt(m, section, name); ok {
				e["enabled"] = false
				if wErr := writeManifest(mp, m); wErr != nil {
					resp["output"] = out + "\n[rollback failed: " + wErr.Error() + "]"
				}
			}
		}
		manifestMu.Unlock()
	}
	api.WriteJSON(w, resp)
}

// handleToolDelete: DELETE /api/tools/{section}/{name}
//
// Cancels any in-flight install for this entry, flips enabled=false,
// and shells out to setup-tools.sh's clear_tool() to remove installed
// files + shims. Cascade detection: if the body has
// "force": true, dependents that require this entry also get
// disabled+cleaned; otherwise the call returns 409 Conflict listing
// blockers so the UI can confirm.
func (s *Server) handleToolDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		api.MethodNotAllowed(w)
		return
	}
	section, name, ok := parseToolPath(r)
	if !ok {
		api.NotFound(w, "unknown tool")
		return
	}

	// Cancel any in-flight install first.
	key := section + "." + name
	inflightMu.Lock()
	if cancel, exists := inflightInstalls[key]; exists {
		cancel()
		delete(inflightInstalls, key)
	}
	inflightMu.Unlock()

	manifestMu.Lock()
	manifest, path, err := s.readManifest()
	if err != nil {
		manifestMu.Unlock()
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	entry, exists := entryAt(manifest, section, name)
	if !exists {
		manifestMu.Unlock()
		api.NotFound(w, fmt.Sprintf("%s.%s not in tools.json", section, name))
		return
	}

	// Body parse: optional "force" boolean.
	var body struct {
		Force bool `json:"force"`
	}
	if r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxToolsInstallBody))
		_ = dec.Decode(&body) // soft-fail; missing body == force:false
	}

	dependents := dependentsOf(manifest, section, name)
	if len(dependents) > 0 && !body.Force {
		manifestMu.Unlock()
		api.WriteJSONStatus(w, http.StatusConflict, map[string]any{
			"error":      "tool has enabled dependents",
			"code":       "has_dependents",
			"dependents": dependents,
			"hint":       "send {\"force\": true} to disable them too",
		})
		return
	}

	// Cascade: disable the target plus any dependents.
	toClear := []string{section + "." + name}
	if body.Force {
		toClear = append(toClear, dependents...)
	}
	for _, key := range toClear {
		parts := strings.SplitN(key, ".", 2)
		e, ok := entryAt(manifest, parts[0], parts[1])
		if !ok {
			continue
		}
		e["enabled"] = false
	}
	_ = entry // entry is implicitly mutated via manifest above; keep var for read-clarity
	if err := writeManifest(path, manifest); err != nil {
		manifestMu.Unlock()
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	manifestMu.Unlock()

	// Shell out to setup-tools.sh's cleanup helper for each disabled entry.
	// We re-invoke setup-tools.sh in a "clear-only" mode by sourcing it
	// and calling clear_tool directly. Simpler: spawn one bash command
	// per entry that sources the script and runs clear_tool.
	ctx, cancel := context.WithTimeout(r.Context(), toolsInstallTimeout)
	defer cancel()
	var out strings.Builder
	for _, key := range toClear {
		parts := strings.SplitN(key, ".", 2)
		// Source the script in a no-op-ish mode: setup-tools.sh runs its
		// install loop unconditionally on source. Calling clear_tool
		// directly via a one-liner shell is cleaner.
		clearCmd := fmt.Sprintf(
			"set -uo pipefail; source /opt/vibekit/setup-tools.sh >/dev/null 2>&1 || true; clear_tool %q %q",
			parts[0], parts[1],
		)
		cmd := exec.CommandContext(ctx, "bash", "-c", clearCmd) //nolint:gosec // parts come from validated allowlist + name pattern
		cmd.Env = os.Environ()
		o, _ := cmd.CombinedOutput()
		fmt.Fprintf(&out, "cleared %s\n%s", key, o)
	}

	api.WriteJSON(w, map[string]any{
		"output":   out.String(),
		"disabled": toClear,
	})
}

// handleToolPatch: PATCH /api/tools/{section}/{name}
// Body: {"auto_update": bool}
func (s *Server) handleToolPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		api.MethodNotAllowed(w)
		return
	}
	section, name, ok := parseToolPath(r)
	if !ok {
		api.NotFound(w, "unknown tool")
		return
	}
	var body struct {
		AutoUpdate *bool `json:"auto_update"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxToolsInstallBody))
	if err := dec.Decode(&body); err != nil {
		api.BadRequest(w, "invalid body")
		return
	}
	if body.AutoUpdate == nil {
		api.BadRequest(w, "missing auto_update field")
		return
	}

	manifestMu.Lock()
	defer manifestMu.Unlock()
	manifest, path, err := s.readManifest()
	if err != nil {
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	entry, exists := entryAt(manifest, section, name)
	if !exists {
		api.NotFound(w, fmt.Sprintf("%s.%s not in tools.json", section, name))
		return
	}
	entry["auto_update"] = *body.AutoUpdate
	if err := writeManifest(path, manifest); err != nil {
		api.WriteJSON(w, api.ErrorJSON(err.Error()))
		return
	}
	api.WriteJSON(w, map[string]any{
		"section":     section,
		"name":        name,
		"auto_update": *body.AutoUpdate,
	})
}

// handleToolStatus: GET /api/tools/status
//
// Returns a map of well-known binaries to a presence boolean. The UI
// uses this to gate feature surfaces — e.g. show "Setting up Node..."
// when the user opens the npm panel but Node hasn't been enabled yet.
func (s *Server) handleToolStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	out := make(map[string]bool, len(statusBinaries))
	for _, b := range statusBinaries {
		_, err := exec.LookPath(b)
		out[b] = err == nil
	}
	api.WriteJSON(w, out)
}
