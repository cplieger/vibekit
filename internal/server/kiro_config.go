package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"vibekit/internal/api"
)

// handleKiroConfig scans .kiro/ for steering docs, skills, and agents.
type kiroConfigItem struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	Inclusion string `json:"inclusion,omitempty"`
}

func (s *Server) handleKiroConfig(w http.ResponseWriter, r *http.Request) {
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
		data, err := fs.ReadFile(root, "steering/"+e.Name())
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
	}
	return items
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
	}
	return items
}

// scanAgents returns kiroConfigItems for markdown files under agents/.
func scanAgents(_ context.Context, root fs.FS, prefix string) []kiroConfigItem {
	var items []kiroConfigItem
	entries, err := fs.ReadDir(root, "agents")
	if err != nil {
		return items
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if name == "" {
			continue
		}
		items = append(items, kiroConfigItem{
			Name: name,
			Path: prefix + "/agents/" + e.Name(),
			Type: "agent",
		})
	}
	return items
}

// parseSteeringInclusion extracts the "inclusion:" value from a steering
// markdown file's YAML front-matter. It is a pure function operating on
// file content for testability.
func parseSteeringInclusion(data []byte) string {
	const defaultInclusion = "always"
	if len(data) == 0 {
		return defaultInclusion
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return defaultInclusion
	}
	end := strings.Index(content[4:], "\n---")
	if end <= 0 {
		return defaultInclusion
	}
	fm := content[4 : 4+end]
	for line := range strings.SplitSeq(fm, "\n") {
		if after, ok := strings.CutPrefix(line, "inclusion:"); ok {
			return strings.TrimSpace(after)
		}
	}
	return defaultInclusion
}
