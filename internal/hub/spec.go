package hub

// Spec-workflow board: GET /api/specs (live, read-only) over the v3 (KAS)
// _kiro/spec/getTaskStatuses request. Mirrors the REST-backed-by-bridge.Call
// shape of knowledge.go / account_usage.go.
//
// Bridge-targeting model (the board is workspace-GLOBAL, like knowledge and
// account usage): the server enumerates <workDir>/.kiro/specs/<name> itself
// (document existence + tasks.md mtime — a pure filesystem read, no bridge)
// and sources each spec's task tree from getTaskStatuses issued on the
// long-lived UTILITY bridge. getTaskStatuses is stateless — it takes
// workspacePaths + tasksFilePath in params and needs no session (verified
// live) — so it works with no chat open, and one bad spec sets that spec's
// Error field rather than blanking the whole board.
//
// Why the invoke verbs are NOT wired (deliberate, evidence-backed):
// executeTask / runAllTasks / generateDocument / analyzeRequirements /
// createSpec are all functional over acp, but each drives a FIRE-AND-FORGET
// agent turn: the invoke request returns {sessionId, executionId} immediately
// and the turn then streams session/update tagged with the invoking session
// but emits NO turn-end signal over the acp bridge (vibekit's turn lifecycle
// is finalized by the session/prompt RESPONSE's stopReason, which a spec
// invoke never produces). Hosting a spec invoke as a server-canonical,
// persisted vibekit chat turn (with proper turn_ended finalization) would
// require re-architecting the turn model — out of scope for the board's core
// read-only value. executeTask / runAllTasks additionally execute code
// (high blast radius). Spec work is still driven through the existing
// spec-mode chat (the mode picker); the board surfaces its progress live via
// the taskStatusChanged → spec_task_changed SSE (translate/spec.go).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// specCallTimeout bounds one getTaskStatuses round-trip. The only slow path is
// the first call, which lazily spins up the utility bridge (session/new + auth
// handshake); the read itself is fast. bridge.Call has no timeout of its own.
const specCallTimeout = 45 * time.Second

// maxSpecs caps how many spec directories the board reads in one request, so a
// pathological .kiro/specs tree can't fan out into unbounded bridge calls.
const maxSpecs = 200

// specDocNames are the three workflow documents, in board order.
const (
	specDocRequirements = "requirements.md"
	specDocDesign       = "design.md"
	specDocTasks        = "tasks.md"
)

// kasSpecTasksResult is the _kiro/spec/getTaskStatuses reply: a task tree (or
// an empty list when tasks.md is missing/unparseable — verified live).
type kasSpecTasksResult struct {
	Tasks []kasSpecTaskNode `json:"tasks"`
}

// kasSpecTaskNode is one node of the raw getTaskStatuses tree. executionStatus
// / lastSessionId / lastExecutionId / pbtResult are omitted by KAS until an
// execution has run against the task (a hand-authored spec carries only the
// markdown-derived fields).
type kasSpecTaskNode struct {
	TaskID          string            `json:"taskId"`
	MarkdownStatus  string            `json:"markdownStatus"`
	ExecutionStatus string            `json:"executionStatus"`
	LastSessionID   string            `json:"lastSessionId"`
	LastExecutionID string            `json:"lastExecutionId"`
	PBTResult       *kasSpecPBTResult `json:"pbtResult"`
	SubTasks        []kasSpecTaskNode `json:"subTasks"`
	IsLeaf          bool              `json:"isLeaf"`
	IsOptional      bool              `json:"isOptional"`
}

type kasSpecPBTResult struct {
	Status         string `json:"status"`
	FailingExample string `json:"failingExample"`
}

// specsDir is the absolute path to the workspace's .kiro/specs directory.
func (h *Hub) specsDir() string {
	return filepath.Join(h.lifecycle.workDir, ".kiro", "specs")
}

// listSpecs enumerates <workDir>/.kiro/specs/<name>, resolving each spec's
// document trio + task tree. Returns specs sorted by name. A getTaskStatuses
// failure for one spec is recorded in that spec's Error field, not fatal.
func (h *Hub) listSpecs(ctx context.Context) ([]api.Spec, error) {
	root := h.specsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			// No specs directory yet — an empty board, not an error.
			return []api.Spec{}, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > maxSpecs {
		names = names[:maxSpecs]
	}

	specs := make([]api.Spec, 0, len(names))
	for _, name := range names {
		specs = append(specs, h.buildSpec(ctx, name))
	}
	return specs, nil
}

// buildSpec resolves one spec directory: document presence + workspace-relative
// paths, tasks.md mtime, and (when tasks.md exists) the getTaskStatuses tree.
func (h *Hub) buildSpec(ctx context.Context, name string) api.Spec {
	dir := filepath.Join(h.specsDir(), name)
	spec := api.Spec{Name: name, Tasks: []api.SpecTaskNode{}}

	if statSpecDoc(dir, specDocRequirements) {
		spec.HasRequirements = true
		spec.RequirementsPath = specRelPath(name, specDocRequirements)
	}
	if statSpecDoc(dir, specDocDesign) {
		spec.HasDesign = true
		spec.DesignPath = specRelPath(name, specDocDesign)
	}
	tasksAbs := filepath.Join(dir, specDocTasks)
	if info, err := os.Stat(tasksAbs); err == nil && !info.IsDir() {
		spec.HasTasks = true
		spec.TasksPath = specRelPath(name, specDocTasks)
		spec.UpdatedAt = info.ModTime().UTC().Format(time.RFC3339)
		tasks, terr := h.specTaskStatuses(ctx, tasksAbs, name)
		if terr != nil {
			slog.Warn("spec getTaskStatuses failed", "feature", name, "error", terr)
			spec.Error = "task status unavailable"
		} else {
			spec.Tasks = tasks
		}
	}
	return spec
}

// statSpecDoc reports whether <dir>/<doc> exists as a regular file.
func statSpecDoc(dir, doc string) bool {
	info, err := os.Stat(filepath.Join(dir, doc))
	return err == nil && !info.IsDir()
}

// specRelPath builds the workspace-relative (forward-slash) path to a spec
// document, for the client's /file editor route.
func specRelPath(name, doc string) string {
	return ".kiro/specs/" + name + "/" + doc
}

// specTaskStatuses issues getTaskStatuses on the utility bridge for one spec
// and converts the raw KAS tree into the domain tree. tasksFilePath is passed
// absolute (unambiguous); workspacePaths is the workspace root.
func (h *Hub) specTaskStatuses(ctx context.Context, tasksFilePath, featureName string) ([]api.SpecTaskNode, error) {
	ub := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, specCallTimeout)
	defer cancel()
	raw, err := ub.specTaskStatusesRaw(cctx, map[string]any{
		"workspacePaths": []string{h.lifecycle.workDir},
		"tasksFilePath":  tasksFilePath,
		"featureName":    featureName,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []api.SpecTaskNode{}, nil
	}
	var res kasSpecTasksResult
	if uErr := json.Unmarshal(raw, &res); uErr != nil {
		return nil, uErr
	}
	return convertSpecTaskNodes(res.Tasks), nil
}

// convertSpecTaskNodes maps the raw KAS task tree onto the domain tree,
// recursively. A nil PBTResult stays nil; an empty subtask slice becomes a
// non-nil empty slice so the client always sees an array.
func convertSpecTaskNodes(in []kasSpecTaskNode) []api.SpecTaskNode {
	out := make([]api.SpecTaskNode, 0, len(in))
	for i := range in {
		n := &in[i]
		node := api.SpecTaskNode{
			TaskID:          n.TaskID,
			MarkdownStatus:  n.MarkdownStatus,
			ExecutionStatus: n.ExecutionStatus,
			LastSessionID:   n.LastSessionID,
			LastExecutionID: n.LastExecutionID,
			IsLeaf:          n.IsLeaf,
			IsOptional:      n.IsOptional,
			SubTasks:        convertSpecTaskNodes(n.SubTasks),
		}
		if n.PBTResult != nil {
			node.PBTResult = &api.SpecTaskPBT{
				Status:         n.PBTResult.Status,
				FailingExample: n.PBTResult.FailingExample,
			}
		}
		out = append(out, node)
	}
	return out
}

// --- HTTP handler (registered by registerSpecRoutes) ---

// handleSpecsList: GET /api/specs → the workspace's specs with document trio
// presence + task trees. The client polls this while the board is open and
// refetches on the spec_task_changed SSE.
func (h *Hub) handleSpecsList(w http.ResponseWriter, r *http.Request) {
	specs, err := h.listSpecs(r.Context())
	if err != nil {
		slog.Warn("list specs failed", "error", err)
		api.WriteJSONStatus(w, http.StatusBadGateway, api.ErrorJSON("specs unavailable"))
		return
	}
	api.WriteJSON(w, api.SpecsResponse{Specs: specs})
}

// registerSpecRoutes wires the spec-board endpoint.
func (h *Hub) registerSpecRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/specs", h.handleSpecsList)
}
