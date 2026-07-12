package api

// Spec-workflow board types (v3/KAS _kiro/spec/* surface).
//
// The Specs board is a live, read-only projection of the workspace's
// .kiro/specs/<name> directories: the requirements→design→tasks document
// trio plus the task tree with per-task status. The server enumerates the
// specs directory itself (document existence + mtime) and sources the task
// tree from the KAS _kiro/spec/getTaskStatuses request (served at
// GET /api/specs; see hub/spec.go). Live task-status deltas arrive via the
// _kiro/spec/taskStatusChanged notification, translated to the
// spec_task_changed SSE (translate/spec.go).
//
// getTaskStatuses is stateless (workspacePaths + tasksFilePath), so it runs
// on the utility bridge — the board works with no chat open. The invoke
// verbs (executeTask/runAllTasks/generateDocument/analyzeRequirements/
// createSpec) each drive a fire-and-forget agent turn and are deliberately
// NOT exposed by the board; see hub/spec.go for the rationale.

// Spec is one .kiro/specs/<name> entry surfaced on the board.
type Spec struct {
	// Name is the spec directory name (the KAS featureName).
	Name string `json:"name"`
	// RequirementsPath / DesignPath / TasksPath are workspace-relative
	// paths to each document, present only when the document exists (so
	// the client can open it in the editor via the /file route).
	RequirementsPath string `json:"requirements_path,omitempty"`
	DesignPath       string `json:"design_path,omitempty"`
	TasksPath        string `json:"tasks_path,omitempty"`
	// Error carries a per-spec getTaskStatuses failure so one bad spec
	// doesn't blank the whole board; the doc trio still renders.
	Error string `json:"error,omitempty"`
	// UpdatedAt is the RFC3339 mtime of tasks.md (freshness hint), or ""
	// when there is no tasks.md.
	UpdatedAt string `json:"updated_at,omitempty"`
	// Tasks is the parsed task tree from getTaskStatuses. Empty when the
	// spec has no tasks.md yet (sparse state) or a parse/read failed.
	Tasks []SpecTaskNode `json:"tasks"`
	// HasRequirements / HasDesign / HasTasks report document presence so
	// the client renders the requirements→design→tasks trio with clear
	// "not created yet" states.
	HasRequirements bool `json:"has_requirements"`
	HasDesign       bool `json:"has_design"`
	HasTasks        bool `json:"has_tasks"`
}

// SpecTaskNode is one task in the tree. Mirrors the KAS getTaskStatuses node
// shape (snake_cased for the client). ExecutionStatus / LastSessionID /
// LastExecutionID / PBTResult are absent until an execution has run against
// the task (a hand-authored spec that was never executed carries only the
// markdown-derived fields), so they are all omitempty.
type SpecTaskNode struct {
	// TaskID is the KAS task identifier — the full parsed task line text
	// (e.g. "2.1 Write property test for X"), not just the number.
	TaskID string `json:"task_id"`
	// MarkdownStatus is the checkbox state parsed from tasks.md:
	// not_started | in_progress | completed | queued.
	MarkdownStatus string `json:"markdown_status"`
	// ExecutionStatus is the last execution outcome tracked by KAS:
	// queued | running | succeed | failed | aborted. Empty when the task
	// was never executed through the spec RPC.
	ExecutionStatus string `json:"execution_status,omitempty"`
	// PBTResult is the property-based-test status for the task, when one
	// has been recorded.
	PBTResult *SpecTaskPBT `json:"pbt_result,omitempty"`
	// LastSessionID / LastExecutionID identify the most recent execution
	// (informational; surfaced as provenance, not acted on).
	LastSessionID   string `json:"last_session_id,omitempty"`
	LastExecutionID string `json:"last_execution_id,omitempty"`
	// SubTasks is the nested child list (recursive).
	SubTasks []SpecTaskNode `json:"sub_tasks"`
	// IsLeaf is true when the task has no subtasks.
	IsLeaf bool `json:"is_leaf"`
	// IsOptional flags an optional task (KAS "*(Optional)*" marker).
	IsOptional bool `json:"is_optional,omitempty"`
}

// SpecTaskPBT is the property-based-test status recorded for a task.
type SpecTaskPBT struct {
	// Status is the PBT outcome: passed | failed | not_run | unexpected_pass.
	Status string `json:"status"`
	// FailingExample is the counterexample the PBT library reported on a
	// failed run (empty otherwise).
	FailingExample string `json:"failing_example,omitempty"`
}

// SpecsResponse is the GET /api/specs body.
type SpecsResponse struct {
	Specs []Spec `json:"specs"`
}

// SpecTaskChangedPayload is the payload for type="spec_task_changed",
// translated from the KAS _kiro/spec/taskStatusChanged notification. It
// fires while a spec execution runs (executeTask / runAllTasks), carrying
// the task-level execution-status deltas. Broadcast globally (no chat_id)
// because the board is a workspace-global surface; the client refetches the
// affected spec by FeatureName. FeatureName is derived server-side from the
// notification's tasksFilePath (the basename of its parent directory).
type SpecTaskChangedPayload struct {
	FeatureName   string           `json:"feature_name"`
	TasksFilePath string           `json:"tasks_file_path,omitempty"`
	Changes       []SpecTaskChange `json:"changes"`
}

// SpecTaskChange is one entry in a SpecTaskChangedPayload. A single
// notification can carry several (e.g. an abort flips every in-flight task).
type SpecTaskChange struct {
	TaskID          string `json:"task_id"`
	ExecutionStatus string `json:"execution_status,omitempty"`
	MarkdownStatus  string `json:"markdown_status,omitempty"`
	LastSessionID   string `json:"last_session_id,omitempty"`
	LastExecutionID string `json:"last_execution_id,omitempty"`
}
