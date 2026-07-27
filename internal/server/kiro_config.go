package server

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/steering"
)

// Per-.kiro-directory scan caps, consistent with the environment.md
// generator (internal/steering/discovery.go). They bound the JSON
// response and memory when a workspace holds many repos, each with a
// large .kiro tree.
const (
	maxSteeringPerDir = 20
	maxSkillsPerDir   = 20
	maxAgentsPerDir   = 10

	// steeringReadCap bounds how much of each steering .md is read for
	// front-matter parsing. Only the head matters, so a crafted repo
	// can't OOM the container with a giant file. Mirrors the 64 KiB cap
	// in internal/steering.
	steeringReadCap = 64 << 10
)

// handleKiroConfig scans .kiro/ for steering docs, skills, and agents.
type kiroConfigItem struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	Inclusion string `json:"inclusion,omitempty"`
}

func (s *Server) handleKiroConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	items := s.collectKiroConfig(r.Context())
	if items == nil {
		items = []kiroConfigItem{}
	}
	api.WriteJSON(w, map[string]any{"items": items})
}

func (s *Server) collectKiroConfig(ctx context.Context) []kiroConfigItem {
	workBase := strings.TrimPrefix(s.workDir, "/")
	var items []kiroConfigItem

	// Scan .kiro at workspace root
	if info, err := os.Stat(filepath.Join(s.workDir, ".kiro")); err == nil && info.IsDir() {
		items = append(items, scanKiroDir(ctx, filepath.Join(s.workDir, ".kiro"), workBase+"/.kiro")...)
		if ctx.Err() != nil {
			return items
		}
	}

	// Scan .kiro inside each subdirectory (git repos)
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return items
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return items
		}
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		kd := filepath.Join(s.workDir, e.Name(), ".kiro")
		if info, err := os.Stat(kd); err == nil && info.IsDir() {
			items = append(items, scanKiroDir(ctx, kd, workBase+"/"+e.Name()+"/.kiro")...)
		}
	}
	return items
}

// scanKiroDir scans a .kiro directory on the real filesystem.
// It delegates to scanKiroDirFS with os.DirFS for testability.
func scanKiroDir(ctx context.Context, fsPath, prefix string) []kiroConfigItem {
	return scanKiroDirFS(ctx, os.DirFS(fsPath), prefix)
}

// scanKiroDirFS scans a .kiro directory via the fs.FS interface, classifying
// entries into steering docs, skills, and agents. It is unit-testable with
// fstest.MapFS without touching the real filesystem.
func scanKiroDirFS(ctx context.Context, root fs.FS, prefix string) []kiroConfigItem {
	var items []kiroConfigItem
	items = append(items, scanSteering(ctx, root, prefix)...)
	if ctx.Err() != nil {
		return items
	}
	items = append(items, scanSkills(ctx, root, prefix)...)
	if ctx.Err() != nil {
		return items
	}
	items = append(items, scanAgents(ctx, root, prefix)...)
	return items
}

// scanSteering returns kiroConfigItems for markdown files under steering/.
func scanSteering(ctx context.Context, root fs.FS, prefix string) []kiroConfigItem {
	var items []kiroConfigItem
	entries, err := fs.ReadDir(root, "steering")
	if err != nil {
		return items
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return items
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		// Read only the head of each file: front-matter is at the top,
		// so an untrusted workspace repo can't OOM the container with a
		// multi-GiB steering .md.
		data, err := readCappedFS(root, "steering/"+e.Name(), steeringReadCap)
		if err != nil {
			slog.Warn("kiro config: read steering file",
				"name", e.Name(), "error", err)
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if name == "" {
			continue
		}
		items = append(items, kiroConfigItem{
			Name:      name,
			Path:      prefix + "/steering/" + e.Name(),
			Type:      "steering",
			Inclusion: parseSteeringInclusion(data),
		})
		if len(items) >= maxSteeringPerDir {
			break
		}
	}
	return items
}

// readCappedFS reads at most limit bytes of name from root. Used for
// untrusted workspace steering files so a crafted large file can't OOM
// the container — only the front-matter head is needed. Mirrors
// readCappedFile in internal/steering, but over the fs.FS interface so
// scanKiroDirFS stays testable with fstest.MapFS.
func readCappedFS(root fs.FS, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// scanSkills returns kiroConfigItems for subdirectories under skills/.
func scanSkills(_ context.Context, root fs.FS, prefix string) []kiroConfigItem {
	var items []kiroConfigItem
	entries, err := fs.ReadDir(root, "skills")
	if err != nil {
		return items
	}
	for _, e := range entries {
		if !e.IsDir() || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		items = append(items, kiroConfigItem{
			Name: e.Name(),
			Path: prefix + "/skills/" + e.Name() + "/SKILL.md",
			Type: "skill",
		})
		if len(items) >= maxSkillsPerDir {
			break
		}
	}
	return items
}

// scanAgents returns kiroConfigItems for agent configs under agents/. An
// agent may ship as a `.md` doc, a `.json` ACP config, or both; paired
// files share a base name and produce ONE item, preferring the `.md`
// (a JSON-only agent is otherwise omitted). Capped at maxAgentsPerDir.
func scanAgents(_ context.Context, root fs.FS, prefix string) []kiroConfigItem {
	entries, err := fs.ReadDir(root, "agents")
	if err != nil {
		return nil
	}
	// De-dupe by base name; prefer the .md file when both .md and .json
	// exist for the same agent.
	chosen := make(map[string]string) // base name -> chosen filename
	var order []string
	for _, e := range entries {
		if e.IsDir() || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		var name string
		switch {
		case strings.HasSuffix(e.Name(), ".md"):
			name = strings.TrimSuffix(e.Name(), ".md")
		case strings.HasSuffix(e.Name(), ".json"):
			name = strings.TrimSuffix(e.Name(), ".json")
		default:
			continue
		}
		if name == "" {
			continue
		}
		existing, seen := chosen[name]
		switch {
		case !seen:
			chosen[name] = e.Name()
			order = append(order, name)
		case strings.HasSuffix(e.Name(), ".md") && strings.HasSuffix(existing, ".json"):
			chosen[name] = e.Name()
		}
	}
	items := make([]kiroConfigItem, 0, min(len(order), maxAgentsPerDir))
	for _, name := range order {
		if len(items) >= maxAgentsPerDir {
			break
		}
		items = append(items, kiroConfigItem{
			Name: name,
			Path: prefix + "/agents/" + chosen[name],
			Type: "agent",
		})
	}
	return items
}

// parseSteeringInclusion returns the validated inclusion mode for a
// steering doc. It delegates to steering.ParseInclusion so the REST scan
// and the environment.md generator share one CRLF/BOM-tolerant,
// validated front-matter parser rather than a divergent copy that
// returned the raw value and broke on a CRLF- or BOM-authored file.
func parseSteeringInclusion(data []byte) string {
	return steering.ParseInclusion(data)
}
