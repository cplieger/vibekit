// The document-oriented `.kiro` inventory behind `GET /api/workspace/kiro-docs`.
//
// # Why this is a second endpoint rather than a widened first one
//
// `/api/workspace/kiro-config` stays exactly as it is. It is ENTITY-oriented —
// one row per skill DIRECTORY, agents de-duplicated across their `.json`/`.md`
// pair — and `role-picker.ts` depends on that shape to seed the mode picker with
// workspace agent names before a session exists. This endpoint is
// DOCUMENT-oriented: one row per file, with the front-matter that makes a row
// legible. The two answer different questions over the same tree, and reusing
// one route for both would have meant a shape flag.
//
// # Scope is per category, deliberately narrower than a glob
//
// Each category names its own root. That is stricter than `.kiro/**/*.md`, which
// would also match `.kiro/README.md` — a file no category claims, which
// therefore gets no row. Markdown anywhere ELSE is ignored: no repo README, no
// CONTRIBUTING, no docs/. "Show me all the project docs" is a plausible future
// request and it is a DIFFERENT page; this one is the agent's own configuration
// surface, and mixing repo documentation in would destroy that meaning.
//
// A per-repo `.kiro/` DOES count, matching the existing scan (workspace root
// plus one level of subdirectory).
//
// # Caps and caching
//
// The old caps (20 steering / 20 skills / 10 agents per tree) cannot serve a page
// that promises the whole inventory — this workspace alone holds ~216 documents.
// So the caps here are per category and set above any real corpus, with a total
// ceiling that still bounds a hostile repo. Reads are capped at the shared
// steering.FrontMatterReadCap, and the whole scan is cached behind a
// directory-mtime signature so front-matter parsing is not repeated per request.

package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/steering"
)

// Document categories, matching the page's sub-tabs. Wire values; the client
// keys its tabs off them.
const (
	catSteering = "steering"
	catSkill    = "skill"
	catAgent    = "agent"
	catSpec     = "spec"
	catHook     = "hook"
)

// Per-category and overall bounds. Set above any plausible real corpus (~216
// documents in this workspace) so the page shows everything, while still
// refusing to enumerate an unbounded tree.
const (
	maxDocsPerCategory = 500
	maxDocsTotal       = 2000
	// maxSpecWalkDepth bounds the specs walk: `.kiro/specs/<feature>/<file>.md`
	// is two levels, so three allows one unexpected nesting level and no more.
	maxSpecWalkDepth = 3
)

// kiroDoc is one row on the configuration browser.
//
// Fields are per-category and mostly omitempty: a steering row carries an
// inclusion and no model, an agent row the reverse, a spec row neither. The
// client shapes each tab's columns; the server does not pretend they are
// uniform.
type kiroDoc struct {
	Category string `json:"category"`
	// Name is the row label: front-matter `name`, else the first H1, else the
	// basename. The universal fallback chain — 11 of 27 skill markdown files
	// carry no front-matter at all (only `*/SKILL.md` is a manifest; the rest
	// is reference material), so this is not a spec special case.
	Name string `json:"name"`
	Path string `json:"path"`
	// Group is the parent label for a nested category: a spec's feature
	// directory. Three files named requirements.md in a flat list identify
	// nothing.
	Group string `json:"group,omitempty"`

	Description string `json:"description,omitempty"`
	Inclusion   string `json:"inclusion,omitempty"`
	FileMatch   string `json:"file_match,omitempty"`
	Model       string `json:"model,omitempty"`

	// Trigger and Action carry a hook row (hooks are JSON, not markdown).
	Trigger string `json:"trigger,omitempty"`
	Action  string `json:"action,omitempty"`

	Tools            []string `json:"tools,omitempty"`
	SteeringOverride bool     `json:"steering_override,omitempty"`
}

// docsCache memoizes one scan behind a cheap directory-mtime signature.
//
// Front-matter parsing is ~200 file opens per scan, and the page refetches on
// every `settings_updated` broadcast, so an uncached scan would repeat that work
// for an unchanged tree. The mutex also serializes concurrent requests into one
// scan rather than N.
type docsCache struct {
	sig  string
	docs []kiroDoc
	mu   sync.Mutex
}

func (s *Server) handleKiroDocs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	docs := s.collectKiroDocs(r.Context())
	if docs == nil {
		docs = []kiroDoc{}
	}
	api.WriteJSON(w, map[string]any{"docs": docs})
}

// collectKiroDocs returns the cached inventory, rescanning when the signature
// changed. Holds the cache mutex across the scan so concurrent requests share
// one pass instead of racing several.
func (s *Server) collectKiroDocs(ctx context.Context) []kiroDoc {
	roots := s.kiroRoots()
	sig := dirSignature(roots)

	if s.kiroDocs == nil {
		// No cache wired (the zero Server in the method-guard tests): scan
		// directly rather than pretending a cache exists.
		return scanKiroRoots(ctx, roots)
	}
	s.kiroDocs.mu.Lock()
	defer s.kiroDocs.mu.Unlock()
	if s.kiroDocs.sig == sig && s.kiroDocs.docs != nil {
		return s.kiroDocs.docs
	}
	docs := scanKiroRoots(ctx, roots)
	// A cancelled scan is partial; caching it would serve a truncated list for
	// as long as the tree is unchanged.
	if ctx.Err() != nil {
		return docs
	}
	s.kiroDocs.sig = sig
	s.kiroDocs.docs = docs
	return docs
}

// kiroRoot is one `.kiro` tree: where it lives and the path prefix its rows
// carry (which is what the client opens in the editor).
type kiroRoot struct {
	fsPath string
	prefix string
}

// kiroRoots enumerates the `.kiro` trees in scope: the workspace root's, plus
// one per non-dot subdirectory. Same shape as collectKiroConfig's walk, so the
// two endpoints agree about what "in scope" means.
func (s *Server) kiroRoots() []kiroRoot {
	workBase := strings.TrimPrefix(s.workDir, "/")
	var roots []kiroRoot
	if info, err := os.Stat(filepath.Join(s.workDir, ".kiro")); err == nil && info.IsDir() {
		roots = append(roots, kiroRoot{
			fsPath: filepath.Join(s.workDir, ".kiro"),
			prefix: workBase + "/.kiro",
		})
	}
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return roots
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		kd := filepath.Join(s.workDir, e.Name(), ".kiro")
		if info, sErr := os.Stat(kd); sErr == nil && info.IsDir() {
			roots = append(roots, kiroRoot{fsPath: kd, prefix: workBase + "/" + e.Name() + "/.kiro"})
		}
	}
	return roots
}

// dirSignature builds a cheap cache key from the mtimes of each root's category
// directories. It detects a file added, removed or renamed (all of which move
// the parent's mtime) and an EDIT to a file only if the editor replaced it. That
// is the accepted limit: the page also refetches on the `settings_updated`
// broadcast, and an in-place body edit changes no field this endpoint reads
// except the description, which is a display string.
func dirSignature(roots []kiroRoot) string {
	var b strings.Builder
	for _, root := range roots {
		for _, sub := range []string{"", "steering", "skills", "agents", "specs", "hooks"} {
			p := root.fsPath
			if sub != "" {
				p = filepath.Join(p, sub)
			}
			info, err := os.Stat(p)
			if err != nil {
				b.WriteString(p + "=-;")
				continue
			}
			b.WriteString(p + "=" + info.ModTime().UTC().Format("20060102150405.000000000") + ";")
		}
	}
	return b.String()
}

// scanKiroRoots scans every root in category order, applying the total cap.
func scanKiroRoots(ctx context.Context, roots []kiroRoot) []kiroDoc {
	var docs []kiroDoc
	for _, root := range roots {
		if ctx.Err() != nil || len(docs) >= maxDocsTotal {
			return docs
		}
		docs = append(docs, scanKiroDocsFS(ctx, os.DirFS(root.fsPath), root.prefix)...)
	}
	if len(docs) > maxDocsTotal {
		docs = docs[:maxDocsTotal]
	}
	return docs
}

// scanKiroDocsFS scans one `.kiro` tree over fs.FS, so it is unit-testable with
// fstest.MapFS. Category order here is the page's fixed tab order.
func scanKiroDocsFS(ctx context.Context, root fs.FS, prefix string) []kiroDoc {
	var docs []kiroDoc
	for _, scan := range []func(context.Context, fs.FS, string) []kiroDoc{
		scanDocsSteering, scanDocsSkills, scanDocsAgents, scanDocsSpecs, scanDocsHooks,
	} {
		if ctx.Err() != nil {
			return docs
		}
		docs = append(docs, scan(ctx, root, prefix)...)
	}
	return docs
}

// scanDocsSteering walks `steering/` recursively for markdown.
func scanDocsSteering(ctx context.Context, root fs.FS, prefix string) []kiroDoc {
	return walkMarkdown(ctx, root, "steering", catSteering, func(rel string, fm steering.FrontMatter, data []byte) kiroDoc {
		return kiroDoc{
			Category:         catSteering,
			Name:             docLabel(&fm, data, rel),
			Path:             prefix + "/steering/" + rel,
			Group:            path.Dir(rel), // "." for the flat common case
			Description:      fm.Description,
			Inclusion:        fm.Inclusion,
			FileMatch:        fm.FileMatch,
			SteeringOverride: fm.SteeringOverride,
		}
	})
}

// scanDocsSkills emits one row per skill MANIFEST (`skills/<name>/SKILL.md`).
// Non-manifest markdown under a skill directory is reference material — the
// regulations, the agent guides — and is deliberately not a row.
func scanDocsSkills(ctx context.Context, root fs.FS, prefix string) []kiroDoc {
	entries, err := fs.ReadDir(root, "skills")
	if err != nil {
		return nil
	}
	docs := make([]kiroDoc, 0, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil || len(docs) >= maxDocsPerCategory {
			return docs
		}
		if !e.IsDir() || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		rel := e.Name() + "/SKILL.md"
		data, rErr := readCappedFS(root, "skills/"+rel)
		if rErr != nil {
			// A directory with no manifest is still a skill (matching the
			// entity scan), just an undescribed one.
			data = nil
		}
		fm := steering.Parse(data)
		name := fm.Name
		if name == "" {
			name = e.Name()
		}
		// A DECLARED mode only, never the default. KAS's SkillFrontMatterSchema
		// declares no `inclusion` key — only SteeringContextFrontMatterSchema
		// does — so steering.Parse's "always" default is the steering default
		// leaking onto a document it was not written for. Forwarding it badged
		// every skill in the browser as always-loaded: a claim about token cost
		// that was never in the file, on the one axis the badge exists to answer.
		//
		// Not simply dropped either, because the schema is `.passthrough()` and
		// `createSteeringCommandSource` reads `config?.inclusion` across skills
		// and steering alike — a skill declaring `manual` or `auto` genuinely
		// becomes a slash command, and that is worth showing. An absent mode
		// renders no badge client-side.
		inclusion := ""
		if fm.HasInclusion {
			inclusion = fm.Inclusion
		}
		docs = append(docs, kiroDoc{
			Category:         catSkill,
			Name:             name,
			Path:             prefix + "/skills/" + rel,
			Description:      fm.Description,
			Inclusion:        inclusion,
			SteeringOverride: fm.SteeringOverride,
		})
	}
	return docs
}

// scanDocsAgents emits one row per agent, de-duplicating the `.json`/`.md` pair
// and preferring the markdown (which is what carries the front-matter).
func scanDocsAgents(ctx context.Context, root fs.FS, prefix string) []kiroDoc {
	entries, err := fs.ReadDir(root, "agents")
	if err != nil {
		return nil
	}
	chosen, order := dedupeAgentFiles(entries)
	docs := make([]kiroDoc, 0, len(order))
	for _, base := range order {
		if ctx.Err() != nil || len(docs) >= maxDocsPerCategory {
			return docs
		}
		file := chosen[base]
		data, rErr := readCappedFS(root, "agents/"+file)
		if rErr != nil {
			slog.Warn("kiro docs: read agent", "name", file, "error", rErr)
			data = nil
		}
		fm := steering.Parse(data)
		name := fm.Name
		if name == "" {
			name = base
		}
		docs = append(docs, kiroDoc{
			Category:    catAgent,
			Name:        name,
			Path:        prefix + "/agents/" + file,
			Description: fm.Description,
			Model:       fm.Model,
			Tools:       fm.Tools,
		})
	}
	return docs
}

// dedupeAgentFiles collapses an agents directory to one file per base name,
// preferring `.md` over `.json`. Returns the choice map and a stable order.
func dedupeAgentFiles(entries []fs.DirEntry) (chosen map[string]string, order []string) {
	chosen = make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		var base string
		switch {
		case strings.HasSuffix(e.Name(), ".md"):
			base = strings.TrimSuffix(e.Name(), ".md")
		case strings.HasSuffix(e.Name(), ".json"):
			base = strings.TrimSuffix(e.Name(), ".json")
		default:
			continue
		}
		if base == "" {
			continue
		}
		existing, seen := chosen[base]
		switch {
		case !seen:
			chosen[base] = e.Name()
			order = append(order, base)
		case strings.HasSuffix(e.Name(), ".md") && strings.HasSuffix(existing, ".json"):
			chosen[base] = e.Name()
		}
	}
	return chosen, order
}

// scanDocsSpecs walks `specs/` and groups each document under its feature
// directory.
//
// Specs carry NO front-matter — a spec doc opens directly on its H1 — so the
// label comes from that heading. There is also no stable requirements/design/
// tasks trio to hardcode: measured across nine feature directories, nine
// requirements.md, nine design.md, one study.md and (in the root tree) zero
// tasks.md. Fixed columns would manufacture an empty Tasks column for every
// feature and hide the study entirely, so a feature is a group with arbitrary
// children, ordered requirements → design → tasks → lexical.
func scanDocsSpecs(ctx context.Context, root fs.FS, prefix string) []kiroDoc {
	docs := walkMarkdown(ctx, root, "specs", catSpec, func(rel string, fm steering.FrontMatter, data []byte) kiroDoc {
		group := path.Dir(rel)
		if group == "." {
			group = "" // a doc loose in specs/ has no feature
		}
		return kiroDoc{
			Category:    catSpec,
			Name:        docLabel(&fm, data, rel),
			Path:        prefix + "/specs/" + rel,
			Group:       group,
			Description: fm.Description,
		}
	})
	sortSpecDocs(docs)
	return docs
}

// specFileRank orders the conventional spec documents ahead of anything else,
// so a feature group reads requirements → design → tasks → whatever it added.
func specFileRank(p string) int {
	switch strings.TrimSuffix(path.Base(p), ".md") {
	case "requirements":
		return 0
	case "design":
		return 1
	case "tasks":
		return 2
	default:
		return 3
	}
}

// sortSpecDocs groups by feature, then applies specFileRank, then lexical.
func sortSpecDocs(docs []kiroDoc) {
	sort.SliceStable(docs, func(i, j int) bool {
		if docs[i].Group != docs[j].Group {
			return docs[i].Group < docs[j].Group
		}
		ri, rj := specFileRank(docs[i].Path), specFileRank(docs[j].Path)
		if ri != rj {
			return ri < rj
		}
		return docs[i].Path < docs[j].Path
	})
}

// scanDocsHooks emits one row per hook, expanding a v1 envelope's several hooks
// into several rows. Reuses steering.ParseHooks so the fields stay sanitized:
// hook files are workspace content, and a raw newline or backtick in a name
// would break out of the span these values render into.
func scanDocsHooks(ctx context.Context, root fs.FS, prefix string) []kiroDoc {
	entries, err := fs.ReadDir(root, "hooks")
	if err != nil {
		return nil
	}
	var docs []kiroDoc
	for _, e := range entries {
		if ctx.Err() != nil || len(docs) >= maxDocsPerCategory {
			return docs
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		data, rErr := readCappedFS(root, "hooks/"+e.Name())
		if rErr != nil {
			slog.Warn("kiro docs: read hook", "name", e.Name(), "error", rErr)
			continue
		}
		docs = append(docs, hookRows(data, prefix, e.Name())...)
	}
	if len(docs) > maxDocsPerCategory {
		docs = docs[:maxDocsPerCategory]
	}
	return docs
}

// hookRows expands one v1 hook envelope into its rows. A file may carry several
// hooks, and each is its own row.
func hookRows(data []byte, prefix, file string) []kiroDoc {
	parsed := steering.ParseHooks(data)
	out := make([]kiroDoc, 0, len(parsed))
	for _, h := range parsed {
		name := h.Name
		if name == "" {
			name = strings.TrimSuffix(file, ".json")
		}
		out = append(out, kiroDoc{
			Category: catHook,
			Name:     name,
			Path:     prefix + "/hooks/" + file,
			Group:    file,
			Trigger:  h.Trigger,
			Action:   h.Command,
		})
	}
	return out
}

// walkMarkdown walks `sub` under root for `.md` files, bounded in depth and
// count, and builds a row per file via mk. Shared by the two recursive
// categories (steering, specs).
func walkMarkdown(
	ctx context.Context,
	root fs.FS,
	sub, category string,
	mk func(rel string, fm steering.FrontMatter, data []byte) kiroDoc,
) []kiroDoc {
	w := &mdWalker{ctx: ctx, root: root, sub: sub, category: category, mk: mk}
	err := fs.WalkDir(root, sub, w.step)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("kiro docs: walk", "category", category, "error", err)
	}
	return w.docs
}

// mdWalker carries the markdown walk's mutable accounting so the visitor is a
// named method rather than a nested closure — the walk's branch set otherwise
// counts against walkMarkdown's own complexity budget.
type mdWalker struct {
	ctx      context.Context
	root     fs.FS
	mk       func(rel string, fm steering.FrontMatter, data []byte) kiroDoc
	sub      string
	category string
	docs     []kiroDoc
}

// step is fs.WalkDir's visitor. A single unreadable directory is skipped rather
// than aborting the category: one bad permission must not empty the page.
func (w *mdWalker) step(p string, d fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return nil //nolint:nilerr // deliberate: skip this entry and keep walking
	}
	if w.ctx.Err() != nil || len(w.docs) >= maxDocsPerCategory {
		return fs.SkipAll
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p, w.sub), "/")
	if d.IsDir() {
		if strings.Count(rel, "/")+1 > maxSpecWalkDepth {
			return fs.SkipDir
		}
		return nil
	}
	if !isMarkdownEntry(d) {
		return nil
	}
	data, err := readCappedFS(w.root, p)
	if err != nil {
		slog.Warn("kiro docs: read", "category", w.category, "path", p, "error", err)
		return nil
	}
	w.docs = append(w.docs, w.mk(rel, steering.Parse(data), data))
	return nil
}

// isMarkdownEntry reports whether a walked entry is a document this scan reads:
// a `.md` file, not a dotfile, with no NUL in its name.
func isMarkdownEntry(d fs.DirEntry) bool {
	name := d.Name()
	return strings.HasSuffix(name, ".md") &&
		!strings.HasPrefix(name, ".") &&
		!strings.ContainsRune(name, 0)
}

// docLabel implements the universal fallback chain: front-matter `name`, else
// the first H1, else the basename without its extension.
//
// Universal rather than per-type because front-matter presence is not per-type:
// specs have none by convention, and 11 of 27 skill markdown files have none
// because only `*/SKILL.md` is a manifest.
func docLabel(fm *steering.FrontMatter, data []byte, rel string) string {
	if fm.Name != "" {
		return fm.Name
	}
	if h := steering.FirstHeading(data); h != "" {
		return h
	}
	return strings.TrimSuffix(path.Base(rel), ".md")
}
