package translate

// v3 (KAS) _kiro/spec/taskStatusChanged handler.
//
// KAS emits this while a spec execution runs (executeTask / runAllTasks),
// carrying task-level execution-status deltas. Wire shape (verified against
// the KAS 2.12 acp-server bundle + a live probe):
//
//	{ "sessionId", "tasksFilePath",
//	  "changes": [ { "taskId", "executionStatus", "lastSessionId"?, "lastExecutionId"? } ] }
//
// executionStatus cycles queued → running → succeed | failed | aborted; a
// single notification can carry several changes (a cancel flips every
// in-flight task at once). The Specs board is workspace-global, so this is
// broadcast with an empty chatID (like the knowledge + MCP events); the
// client refetches the affected spec by featureName, which we derive from the
// tasksFilePath (the basename of its parent directory — matches KAS's own
// featureNameFromTasksPath).

import (
	"context"
	"path"

	"github.com/cplieger/vibekit/internal/api"
)

// v3SpecTaskChanged is the wire shape of the taskStatusChanged notification.
type v3SpecTaskChanged struct {
	SessionID     string             `json:"sessionId"`
	TasksFilePath string             `json:"tasksFilePath"`
	Changes       []v3SpecTaskChange `json:"changes"`
}

type v3SpecTaskChange struct {
	TaskID          string `json:"taskId"`
	ExecutionStatus string `json:"executionStatus"`
	MarkdownStatus  string `json:"markdownStatus"`
	LastSessionID   string `json:"lastSessionId"`
	LastExecutionID string `json:"lastExecutionId"`
}

// HandleSpecTaskChanged translates a _kiro/spec/taskStatusChanged notification
// into a spec_task_changed SSE. The feature name is derived from tasksFilePath
// so the client can target the affected spec; the raw path is forwarded too.
func (t *Translator) HandleSpecTaskChanged(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[v3SpecTaskChanged](msg, "spec/taskStatusChanged")
	if !ok {
		return
	}
	if p.TasksFilePath == "" || len(p.Changes) == 0 {
		return
	}
	changes := make([]api.SpecTaskChange, 0, len(p.Changes))
	for _, c := range p.Changes {
		changes = append(changes, api.SpecTaskChange{
			TaskID:          c.TaskID,
			ExecutionStatus: c.ExecutionStatus,
			MarkdownStatus:  c.MarkdownStatus,
			LastSessionID:   c.LastSessionID,
			LastExecutionID: c.LastExecutionID,
		})
	}
	payload := api.SpecTaskChangedPayload{
		FeatureName:   featureNameFromTasksPath(p.TasksFilePath),
		TasksFilePath: p.TasksFilePath,
		Changes:       changes,
	}
	// Broadcast globally (empty chatID): the board is workspace-global, so
	// every client refetches regardless of which chat's bridge emitted it.
	t.deps.Broadcast(ctx, api.NewEvent(api.EventSpecTaskChanged, "", payload))
}

// featureNameFromTasksPath returns the spec's feature name — the basename of
// the tasks file's parent directory (.../.kiro/specs/<feature>/tasks.md).
// Mirrors KAS's featureNameFromTasksPath. Uses path (not filepath) since the
// wire path is always forward-slashed.
func featureNameFromTasksPath(tasksFilePath string) string {
	return path.Base(path.Dir(tasksFilePath))
}
