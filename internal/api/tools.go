package api

// Wire types for the tools engine (Settings -> Tools). The engine
// lives in internal/tools; these shapes cross the HTTP/SSE boundary
// and are wiregen-exported to the client (static-src/wire/).

// ToolJob is one tools-engine job: an install/uninstall/update/sync
// run on the single-flight queue. Terminal states are done, failed,
// and cancelled.
type ToolJob struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Names []string `json:"names,omitempty"`
	State string   `json:"state"`
	Error string   `json:"error,omitempty"`
	// Timestamps are Unix milliseconds.
	CreatedAt int64 `json:"created_at"`
	StartedAt int64 `json:"started_at,omitempty"`
	EndedAt   int64 `json:"ended_at,omitempty"`
	// OutputTail carries the job's most recent output lines; populated
	// on the jobs endpoints only (live output streams via the
	// tool_job_output SSE event).
	OutputTail []string `json:"output_tail,omitempty"`
}

// ToolInfo is one tool row in GET /api/tools: the manifest entry
// joined with the engine's install state.
type ToolInfo struct {
	Name             string            `json:"name"`
	Source           string            `json:"source"`
	Version          string            `json:"version"`
	Pin              bool              `json:"pin,omitempty"`
	Requires         []string          `json:"requires,omitempty"`
	Shims            map[string]string `json:"shims,omitempty"`
	Description      string            `json:"description,omitempty"`
	Origin           string            `json:"origin,omitempty"`
	Installed        bool              `json:"installed"`
	InstalledVersion string            `json:"installed_version,omitempty"`
	Latest           string            `json:"latest,omitempty"`
	Installing       bool              `json:"installing"`
	LastError        string            `json:"last_error,omitempty"`
}

// SystemTool is one image-baked binary surfaced read-only in the UI.
type SystemTool struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

// ToolsList is the GET /api/tools response body.
type ToolsList struct {
	Tools  []ToolInfo   `json:"tools"`
	System []SystemTool `json:"system"`
	Job    *ToolJob     `json:"job,omitempty"`
}

// ToolCatalogHit is one catalog search result (GET /api/tools/search).
type ToolCatalogHit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Featured    bool   `json:"featured,omitempty"`
	// Version is the catalog's default pinned version, set only for
	// entries without an upstream version source (manual installs).
	Version string `json:"version,omitempty"`
}

// ToolsSearchResponse is the GET /api/tools/search response body.
type ToolsSearchResponse struct {
	Results []ToolCatalogHit `json:"results"`
}

// ToolJobAccepted is the 202 response body for job-enqueuing tool
// mutations (create, install, update, delete, version patch).
type ToolJobAccepted struct {
	Job *ToolJob `json:"job"`
}

// ToolsJobsResponse is the GET /api/tools/jobs response body.
type ToolsJobsResponse struct {
	Active *ToolJob   `json:"active,omitempty"`
	Recent []*ToolJob `json:"recent,omitempty"`
}
