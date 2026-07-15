package tools

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cplieger/ssrf/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// backendDeps maps a source kind to the tool that must be installed
// first for the backend to function at all.
var backendDeps = map[string]string{
	SourceNpm:   "node",
	SourcePip:   "uv",
	SourceCargo: "rust",
	SourceGo:    "go",
}

// systemBinaries are the image-baked binaries surfaced read-only in
// the UI's System group.
var systemBinaries = []string{"git", "jq", "curl", "unzip", "xz", "ssh", "tar", "bash"}

// Config wires an Engine.
type Config struct {
	// ConfigDir holds tools.json + tools-state.json (the persistent
	// config volume root).
	ConfigDir string
	// ToolsDir is the install tree root (bin/, opt/, npm/, python/).
	ToolsDir string
	// CatalogPath is the compiled catalog baked into the image
	// (optional; missing = degraded search).
	CatalogPath string
	// Broadcaster publishes tool_job_* SSE events (optional).
	Broadcaster api.Broadcaster
}

// Engine is the tools subsystem: the single owner of the manifest and
// install tree, the job queue, and the catalog.
type Engine struct {
	store    *store
	catalog  *Catalog
	queue    *jobQueue
	inst     *installer
	versions *versionResolver
	toolsDir string
}

// New constructs and starts an Engine (initializes the manifest files
// and launches the job worker).
func New(cfg Config) (*Engine, error) {
	st := newStore(cfg.ConfigDir)
	if err := st.initFiles(); err != nil {
		return nil, fmt.Errorf("tools: init manifest: %w", err)
	}
	// Downloads and version checks go to registry-defined public URLs;
	// the SSRF-safe transport blocks redirect tricks into internal
	// networks and non-HTTPS schemes.
	client := &http.Client{
		Transport: ssrf.SafeTransport(ssrf.WithAllowedPorts(443)),
		Timeout:   15 * time.Minute,
	}
	e := &Engine{
		store:    st,
		catalog:  loadCatalog(cfg.CatalogPath),
		versions: newVersionResolver(client),
		toolsDir: cfg.ToolsDir,
	}
	e.queue = newJobQueue(cfg.Broadcaster, e.executeJob)
	e.inst = &installer{toolsDir: cfg.ToolsDir, client: client, output: func(string) {}}
	if err := os.MkdirAll(filepath.Join(cfg.ToolsDir, "bin"), 0o755); err != nil {
		return nil, err
	}
	return e, nil
}

// Close stops the job worker (cancelling any running job).
func (e *Engine) Close() { e.queue.Close() }

// --- read side ---

// List assembles the GET /api/tools response.
func (e *Engine) List() (*api.ToolsList, error) {
	m, err := e.store.Manifest()
	if err != nil {
		return nil, err
	}
	st := e.store.State()
	installing := e.queue.InstallingSet()

	res := &api.ToolsList{Tools: []api.ToolInfo{}, System: e.systemTools(), Job: e.queue.Active()}
	names := make([]string, 0, len(m.Tools))
	for n := range m.Tools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t := m.Tools[n]
		s := st.Tools[n]
		v := api.ToolInfo{
			Name:             n,
			Source:           t.Source,
			Version:          t.Version,
			Pin:              t.Pin,
			Requires:         t.Requires,
			Shims:            t.Shims,
			Description:      t.Description,
			Origin:           t.Origin,
			Installed:        e.probeInstalled(n, t, s),
			InstalledVersion: s.InstalledVersion,
			Installing:       installing[n],
			LastError:        s.LastError,
		}
		if latest := e.versions.Cached(t.Source); latest != "" && latest != t.Version {
			v.Latest = latest
		}
		res.Tools = append(res.Tools, v)
	}
	return res, nil
}

// probeInstalled checks the tool's bin presence: every recorded bin
// (or the derived probe name before first status write) resolves in
// the bin dir.
func (e *Engine) probeInstalled(name string, t Tool, s ToolStatus) bool {
	bins := append(append([]string{}, s.Bins...), s.PMBins...)
	if len(bins) == 0 {
		// No recorded bins (never installed by this engine): fall back
		// to the derived probe name so pre-seeded volumes still read
		// as installed when the binary exists.
		probe := t.Probe
		if probe == "" {
			probe = pkgBinName(strings.TrimPrefix(name, "@"))
		}
		bins = []string{probe}
	}
	for _, b := range bins {
		if _, err := os.Stat(filepath.Join(e.toolsDir, "bin", b)); err != nil {
			return false
		}
	}
	return true
}

func (e *Engine) systemTools() []api.SystemTool {
	out := make([]api.SystemTool, 0, len(systemBinaries))
	for _, b := range systemBinaries {
		_, err := exec.LookPath(b)
		out = append(out, api.SystemTool{Name: b, Installed: err == nil})
	}
	return out
}

// Search queries the catalog (empty query = featured set), hiding
// entries already in the manifest.
func (e *Engine) Search(query string) []CatalogEntry {
	hits := e.catalog.Search(query)
	m, err := e.store.Manifest()
	if err != nil {
		return hits
	}
	out := hits[:0]
	for _, h := range hits {
		if _, exists := m.Tools[h.Name]; !exists {
			out = append(out, h)
		}
	}
	return out
}

// Jobs returns the active job (with output tail) and recent history.
func (e *Engine) Jobs() (active *api.ToolJob, recent []*api.ToolJob) { return e.queue.Snapshot() }

// CancelJob aborts a queued or running job.
func (e *Engine) CancelJob(id string) bool { return e.queue.Cancel(id) }

// --- write side ---

// CreateRequest is the POST /api/tools body.
type CreateRequest struct {
	Name        string            `json:"name"`
	Source      string            `json:"source,omitempty"`  // optional when the catalog knows the name
	Version     string            `json:"version,omitempty"` // optional: resolve latest
	Pin         bool              `json:"pin,omitempty"`
	Requires    []string          `json:"requires,omitempty"`
	Shims       map[string]string `json:"shims,omitempty"`
	Description string            `json:"description,omitempty"`
	Origin      string            `json:"origin,omitempty"`
	Install     string            `json:"install,omitempty"`
	Uninstall   string            `json:"uninstall,omitempty"`
	Probe       string            `json:"probe,omitempty"`
}

// Create adds a tool to the manifest and enqueues its install.
func (e *Engine) Create(ctx context.Context, req CreateRequest) (*api.ToolJob, error) {
	name := strings.TrimSpace(req.Name)
	if !validToolName(name) {
		return nil, fmt.Errorf("invalid tool name")
	}
	t, err := e.resolveNewTool(ctx, name, req)
	if err != nil {
		return nil, err
	}
	err = e.store.MutateManifest(func(m *Manifest) error {
		if _, exists := m.Tools[name]; exists {
			return fmt.Errorf("tool %q already exists", name)
		}
		m.Tools[name] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	jv, err := e.queue.Enqueue(JobKindInstall, []string{name})
	if err != nil {
		// Queue full: undo the manifest row so a rejected create
		// doesn't leave phantom intent with no install job.
		if rollback := e.store.MutateManifest(func(m *Manifest) error {
			delete(m.Tools, name)
			return nil
		}); rollback != nil {
			slog.Error("tools: create rollback failed", "error", rollback)
		}
		return nil, err
	}
	return jv, nil
}

// resolveNewTool merges the request with catalog knowledge and resolves
// a concrete version.
func (e *Engine) resolveNewTool(ctx context.Context, name string, req CreateRequest) (Tool, error) {
	t := Tool{
		Source:      strings.TrimSpace(req.Source),
		Version:     strings.TrimSpace(req.Version),
		Pin:         req.Pin,
		Requires:    req.Requires,
		Shims:       req.Shims,
		Description: strings.TrimSpace(req.Description),
		Origin:      req.Origin,
		Install:     strings.TrimSpace(req.Install),
		Uninstall:   strings.TrimSpace(req.Uninstall),
		Probe:       strings.TrimSpace(req.Probe),
	}
	if cat, ok := e.catalog.Lookup(name); ok {
		if t.Source == "" {
			t.Source = cat.Source
		}
		if t.Source == cat.Source {
			if t.Description == "" {
				t.Description = cat.Description
			}
			if t.Requires == nil {
				t.Requires = cat.Requires
			}
			if t.Shims == nil {
				t.Shims = cat.Shims
			}
			if t.Install == "" {
				t.Install = cat.Install
			}
			if t.Uninstall == "" {
				t.Uninstall = cat.Uninstall
			}
			if t.Probe == "" {
				t.Probe = cat.Probe
			}
			if t.Version == "" {
				t.Version = cat.Version
			}
		}
	}
	if t.Source == "" {
		return t, fmt.Errorf("unknown tool %q: pick a source (npm:/pip:/cargo:/go:/aqua:/manual)", name)
	}
	if err := validateSource(t.Source, t.Install); err != nil {
		return t, err
	}
	if t.Version == "" {
		latest, err := e.versions.Latest(ctx, t.Source, e.aquaDef(t.Source))
		if err != nil {
			return t, fmt.Errorf("resolve latest version: %w", err)
		}
		t.Version = latest
	}
	if !validVersionString(t.Version) {
		return t, fmt.Errorf("invalid version string")
	}
	return t, nil
}

// PatchRequest is the PATCH /api/tools/{name} body. Pointer fields
// distinguish "absent" from zero values.
type PatchRequest struct {
	Version     *string            `json:"version,omitempty"`
	Pin         *bool              `json:"pin,omitempty"`
	Description *string            `json:"description,omitempty"`
	Requires    *[]string          `json:"requires,omitempty"`
	Shims       *map[string]string `json:"shims,omitempty"`
	Install     *string            `json:"install,omitempty"`
	Uninstall   *string            `json:"uninstall,omitempty"`
}

// Patch merges fields into an existing tool. A version change enqueues
// a reinstall job (returned non-nil).
func (e *Engine) Patch(name string, req PatchRequest) (*api.ToolJob, error) {
	if req.Version != nil && !validVersionString(*req.Version) {
		return nil, fmt.Errorf("invalid version string")
	}
	versionChanged := false
	prevVersion := ""
	err := e.store.MutateManifest(func(m *Manifest) error {
		t, ok := m.Tools[name]
		if !ok {
			return errNotFound
		}
		if req.Version != nil && *req.Version != t.Version {
			prevVersion = t.Version
			t.Version = *req.Version
			versionChanged = true
		}
		if req.Pin != nil {
			t.Pin = *req.Pin
		}
		if req.Description != nil {
			t.Description = *req.Description
		}
		if req.Requires != nil {
			t.Requires = *req.Requires
		}
		if req.Shims != nil {
			t.Shims = *req.Shims
		}
		if req.Install != nil {
			t.Install = *req.Install
		}
		if req.Uninstall != nil {
			t.Uninstall = *req.Uninstall
		}
		m.Tools[name] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	if versionChanged {
		jv, err := e.queue.Enqueue(JobKindInstall, []string{name})
		if err != nil {
			// Queue full: restore the previous version so the manifest
			// doesn't claim a version no job will ever install.
			if rollback := e.store.MutateManifest(func(m *Manifest) error {
				if t, ok := m.Tools[name]; ok {
					t.Version = prevVersion
					m.Tools[name] = t
				}
				return nil
			}); rollback != nil {
				slog.Error("tools: patch rollback failed", "error", rollback)
			}
			return nil, err
		}
		return jv, nil
	}
	return nil, nil
}

// errNotFound distinguishes a missing tool from other mutate errors.
var errNotFound = fmt.Errorf("tool not found")

// IsNotFound reports whether err is the engine's not-found sentinel.
func IsNotFound(err error) bool { return err == errNotFound }

// Delete removes a tool. Without force, a tool that others Require is
// refused and the dependents are returned. The removed manifest
// entries travel on the uninstall job so source-specific cleanup
// (npm/pip uninstalls, manual uninstall commands) still knows the
// sources after the manifest rows are gone.
func (e *Engine) Delete(name string, force bool) (*api.ToolJob, []string, error) {
	var dependents []string
	removed := map[string]Tool{}
	err := e.store.MutateManifest(func(m *Manifest) error {
		t, ok := m.Tools[name]
		if !ok {
			return errNotFound
		}
		for other, ot := range m.Tools {
			if other != name && slices.Contains(ot.Requires, name) {
				dependents = append(dependents, other)
			}
		}
		if len(dependents) > 0 && !force {
			return errHasDependents
		}
		removed[name] = t
		delete(m.Tools, name)
		if force {
			for _, d := range dependents {
				removed[d] = m.Tools[d]
				delete(m.Tools, d)
			}
		}
		return nil
	})
	if err != nil {
		return nil, dependents, err
	}
	names := make([]string, 0, len(removed))
	for n := range removed {
		names = append(names, n)
	}
	sort.Strings(names)
	jv, err := e.queue.EnqueueRemoval(names, removed)
	if err != nil {
		// Queue full: roll the manifest rows back so intent and reality
		// don't diverge (the tool is still installed on disk).
		rollback := e.store.MutateManifest(func(m *Manifest) error {
			for n, t := range removed {
				if _, exists := m.Tools[n]; !exists {
					m.Tools[n] = t
				}
			}
			return nil
		})
		if rollback != nil {
			slog.Error("tools: delete rollback failed", "error", rollback)
		}
		return nil, dependents, err
	}
	return jv, dependents, nil
}

// errHasDependents marks a refused delete (dependents present, no force).
var errHasDependents = fmt.Errorf("tool has dependents")

// IsHasDependents reports whether err is the dependents-conflict sentinel.
func IsHasDependents(err error) bool { return err == errHasDependents }

// InstallOne re-enqueues an install for an existing tool (retry /
// install-missing).
func (e *Engine) InstallOne(name string) (*api.ToolJob, error) {
	m, err := e.store.Manifest()
	if err != nil {
		return nil, err
	}
	if _, ok := m.Tools[name]; !ok {
		return nil, errNotFound
	}
	return e.queue.Enqueue(JobKindInstall, []string{name})
}

// UpdateAll enqueues an update job over every unpinned tool (or the
// given names).
func (e *Engine) UpdateAll(names []string) (*api.ToolJob, error) {
	return e.queue.Enqueue(JobKindUpdate, names)
}

// StartBoot enqueues the boot sync job unless the manifest is empty.
func (e *Engine) StartBoot() {
	m, err := e.store.Manifest()
	if err != nil || len(m.Tools) == 0 {
		return
	}
	if _, err := e.queue.Enqueue(JobKindSync, nil); err == nil {
		return
	}
}

// EnsureInstalled synchronously guarantees a tool: present in the
// manifest (created from the catalog when missing), installed, and on
// PATH. Used by flows that need a binary before proceeding (forge
// login installing gh/glab/tea, MCP flows installing node).
func (e *Engine) EnsureInstalled(ctx context.Context, name string) error {
	m, err := e.store.Manifest()
	if err != nil {
		return err
	}
	t, inManifest := m.Tools[name]
	if inManifest && e.probeInstalled(name, t, e.store.State().Tools[name]) {
		return nil
	}
	var jv *api.ToolJob
	if inManifest {
		jv, err = e.InstallOne(name)
	} else {
		jv, err = e.Create(ctx, CreateRequest{Name: name})
	}
	if err != nil {
		return err
	}
	final, err := e.queue.Wait(ctx, jv.ID)
	if err != nil {
		return err
	}
	if final.State != JobDone {
		return fmt.Errorf("install %s: %s", name, orDefault(final.Error, final.State))
	}
	return nil
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// --- job execution ---

// executeJob runs one dequeued job on the worker goroutine.
func (e *Engine) executeJob(ctx context.Context, j *job, output func(string)) error {
	e.inst.output = output
	defer func() { e.inst.output = func(string) {} }()
	switch j.kind {
	case JobKindInstall:
		return e.runInstall(ctx, j.names, output)
	case JobKindUninstall:
		return e.runUninstall(ctx, j)
	case JobKindUpdate:
		return e.runUpdate(ctx, j.names, output)
	case JobKindSync:
		return e.runSync(ctx, output)
	default:
		return fmt.Errorf("unknown job kind %q", j.kind)
	}
}

// runInstall installs the named tools plus any missing dependencies,
// dependencies first.
func (e *Engine) runInstall(ctx context.Context, names []string, output func(string)) error {
	m, err := e.store.Manifest()
	if err != nil {
		return err
	}
	ordered, err := e.installOrder(ctx, m, names)
	if err != nil {
		return err
	}
	var failed []string
	var firstErr error
	for _, n := range ordered {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.installTool(ctx, n, output); err != nil {
			failed = append(failed, n)
			if firstErr == nil {
				firstErr = err
			}
			output(fmt.Sprintf("ERROR %s: %v", n, err))
			// A failed dependency dooms its dependents; carry on so
			// unrelated names in the same job still install.
		}
	}
	switch len(failed) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%s: %w", failed[0], firstErr)
	default:
		return fmt.Errorf("failed: %s", strings.Join(failed, ", "))
	}
}

// installOrder expands names with backend deps + Requires (creating
// manifest entries from the catalog for missing deps) and returns them
// dependency-first.
func (e *Engine) installOrder(ctx context.Context, m *Manifest, names []string) ([]string, error) {
	var ordered []string
	seen := map[string]bool{}
	var visit func(n string, stack []string) error
	visit = func(n string, stack []string) error {
		if seen[n] {
			return nil
		}
		if slices.Contains(stack, n) {
			return fmt.Errorf("requires cycle through %q", n)
		}
		t, ok := m.Tools[n]
		if !ok {
			// A dependency not yet in the manifest: adopt it from the
			// catalog at its latest version.
			req := CreateRequest{Name: n}
			nt, err := e.resolveNewTool(ctx, n, req)
			if err != nil {
				return fmt.Errorf("dependency %q: %w", n, err)
			}
			if err := e.store.MutateManifest(func(mm *Manifest) error {
				if _, exists := mm.Tools[n]; !exists {
					mm.Tools[n] = nt
				}
				return nil
			}); err != nil {
				return err
			}
			m.Tools[n] = nt
			t = nt
		}
		stack = append(stack, n)
		for _, dep := range e.depsOf(n, t) {
			if err := visit(dep, stack); err != nil {
				return err
			}
		}
		seen[n] = true
		ordered = append(ordered, n)
		return nil
	}
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// depsOf merges backend-implied deps with the entry's Requires.
func (e *Engine) depsOf(name string, t Tool) []string {
	var deps []string
	kind, _, _ := strings.Cut(t.Source, ":")
	if d, ok := backendDeps[kind]; ok && d != name {
		deps = append(deps, d)
	}
	for _, r := range t.Requires {
		if r != name && !slices.Contains(deps, r) {
			deps = append(deps, r)
		}
	}
	return deps
}

// installTool installs one tool when not already at its manifest
// version, recording status either way.
func (e *Engine) installTool(ctx context.Context, name string, output func(string)) error {
	m, err := e.store.Manifest()
	if err != nil {
		return err
	}
	t, ok := m.Tools[name]
	if !ok {
		return errNotFound
	}
	st := e.store.State().Tools[name]
	if st.InstalledVersion == t.Version && e.probeInstalled(name, t, st) {
		output(fmt.Sprintf("%s %s already installed", name, t.Version))
		return nil
	}
	output(fmt.Sprintf("installing %s %s (%s)", name, t.Version, t.Source))
	bins, pmBins, err := e.inst.install(ctx, name, t, e.aquaDef(t.Source), st.PMBins)
	if err != nil {
		e.store.setToolStatus(name, func(s *ToolStatus) { s.LastError = err.Error() })
		return err
	}
	e.store.setToolStatus(name, func(s *ToolStatus) {
		s.InstalledVersion = t.Version
		s.Bins = bins
		s.PMBins = pmBins
		s.LastError = ""
	})
	return nil
}

// runUninstall removes the named tools' installs. The job carries the
// removed manifest entries (Delete deletes them before enqueueing), so
// source-specific cleanup — npm/pip package removal, manual uninstall
// commands — runs with the real Tool definition.
func (e *Engine) runUninstall(ctx context.Context, j *job) error {
	st := e.store.State()
	for _, n := range j.names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.inst.output(fmt.Sprintf("uninstalling %s", n))
		t, ok := j.removed[n]
		if !ok {
			// No definition available (shouldn't happen): bin/opt
			// cleanup still covers the user-visible footprint.
			t = Tool{Source: SourceManual}
		}
		if err := e.inst.uninstall(ctx, n, t, st.Tools[n]); err != nil {
			return err
		}
		e.store.dropToolStatus(n)
	}
	return nil
}

// runUpdate refreshes latest-version data and reinstalls outdated,
// unpinned tools (or the explicit names).
func (e *Engine) runUpdate(ctx context.Context, names []string, output func(string)) error {
	m, err := e.store.Manifest()
	if err != nil {
		return err
	}
	targets := names
	if len(targets) == 0 {
		for n := range m.Tools {
			targets = append(targets, n)
		}
		sort.Strings(targets)
	}
	var bumped []string
	for _, n := range targets {
		t, ok := m.Tools[n]
		if !ok {
			continue
		}
		if t.Pin && len(names) == 0 {
			output(fmt.Sprintf("%s pinned at %s, skipping", n, t.Version))
			continue
		}
		if t.Source == SourceManual {
			continue
		}
		latest, err := e.versions.Latest(ctx, t.Source, e.aquaDef(t.Source))
		if err != nil {
			output(fmt.Sprintf("%s: version check failed: %v", n, err))
			continue
		}
		if latest == t.Version {
			continue
		}
		output(fmt.Sprintf("%s: %s -> %s", n, t.Version, latest))
		if err := e.store.MutateManifest(func(mm *Manifest) error {
			cur, ok := mm.Tools[n]
			if !ok {
				return nil
			}
			cur.Version = latest
			mm.Tools[n] = cur
			return nil
		}); err != nil {
			return err
		}
		bumped = append(bumped, n)
	}
	if len(bumped) == 0 {
		output("everything up to date")
		return nil
	}
	return e.runInstall(ctx, bumped, output)
}

// runSync is the boot job: install everything missing, then update
// unpinned tools.
func (e *Engine) runSync(ctx context.Context, output func(string)) error {
	m, err := e.store.Manifest()
	if err != nil {
		return err
	}
	st := e.store.State()
	var missing []string
	for n, t := range m.Tools {
		if !e.probeInstalled(n, t, st.Tools[n]) {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		output(fmt.Sprintf("installing missing tools: %s", strings.Join(missing, ", ")))
		if err := e.runInstall(ctx, missing, output); err != nil {
			return err
		}
	}
	return e.runUpdate(ctx, nil, output)
}

// aquaDef returns the catalog's aqua definition for an aqua: source.
func (e *Engine) aquaDef(source string) *AquaPackage {
	kind, ref, _ := strings.Cut(source, ":")
	if kind != SourceAqua {
		return nil
	}
	for _, entry := range e.catalog.Entries {
		if entry.Source == source && entry.Aqua != nil {
			return entry.Aqua
		}
	}
	// Fallback: synthesize a plain github_release definition so an
	// aqua ref outside the catalog still resolves the common shape.
	owner, repo, ok := strings.Cut(ref, "/")
	if !ok {
		return nil
	}
	return &AquaPackage{Type: "github_release", RepoOwner: owner, RepoName: repo}
}

// validToolName mirrors the v1 rule plus @scope/name support: the name
// is a display/manifest key, so keep it boring. A slash is legal only
// in exactly the npm scoped form `@scope/name` with non-empty halves
// (rejects `@/x`, `@x/`, `x/y`, `@a/b/c`).
func validToolName(name string) bool {
	if name == "" || len(name) > 80 {
		return false
	}
	if i := strings.IndexByte(name, '/'); i >= 0 {
		if !strings.HasPrefix(name, "@") || i < 2 || i == len(name)-1 ||
			strings.Count(name, "/") > 1 {
			return false
		}
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+' || r == '@' || r == '/':
		default:
			return false
		}
	}
	return true
}

// validateSource sanity-checks a source string at create time.
func validateSource(source, install string) error {
	if source == SourceManual {
		if strings.TrimSpace(install) == "" {
			return fmt.Errorf("manual tools need an install command")
		}
		return nil
	}
	kind, ref, ok := strings.Cut(source, ":")
	if !ok || ref == "" {
		return fmt.Errorf("source must be <kind>:<ref> or manual")
	}
	switch kind {
	case SourceAqua:
		if !strings.Contains(ref, "/") {
			return fmt.Errorf("aqua source must be aqua:owner/repo")
		}
	case SourceNpm, SourcePip, SourceCargo, SourceGo:
	default:
		return fmt.Errorf("unknown source kind %q", kind)
	}
	return nil
}
