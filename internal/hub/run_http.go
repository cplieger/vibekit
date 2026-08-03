package hub

// The run read surface: GET /api/runs/{id}.
//
// ONE route, and the shape of what it returns is a deliberate non-decision: KAS's
// `state` and `nodePlan` pass through VERBATIM. The node tree is KAS's structure,
// it already carries every execution fact a reader wants (status, timings,
// agentName, modelId, sessionId, completionSignal, capturedOutput on each step),
// and a projected copy here would be a second representation of a thing vibekit
// does not own — one more contract to keep in sync for no reader benefit.
//
// TWO routes an earlier design specified are absent, each for a measured reason:
//
//   - `GET /api/runs` (the cross-chat list) existed for a run BOARD that is
//     deleted. The history surface already lists runs, from the same
//     `_kiro/workflow/list` call, beside previous chats — so a second endpoint
//     would return the same rows to no additional consumer.
//   - `GET /api/runs/{id}/steps/{nodeId}` (a step's own transcript) cannot be
//     served without new bridge machinery AND is not needed. A step session can
//     only be read with `session/load`, which on a chat's bridge would mount a
//     second session whose replay frames feed that CHAT's projection — splicing a
//     step's transcript into the conversation — and on the utility bridge would
//     displace the session the six generators share. Meanwhile a LIVE step's
//     content now reaches the launching chat's transcript correctly attributed
//     (see translate.ACPWorkflowMeta.SubtaskID), and a FINISHED step's product is
//     its `capturedOutput`, which `inspect` returns and the run view renders. The
//     transcript would be a third copy of content already in two places.
//
// There are no mutations here at all. Runs have no controls (user decision): no
// Retry, no Continue, no Pause, no Resume, no Delete. The agent orchestrates and
// decides; the one cancel vibekit performs is on tab close and is server-internal.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/workflow"
)

// handleRun: GET /api/runs/{workflowId} → one run's full state.
//
// Two things happen besides the passthrough, and both are about telling the user
// something true:
//
//  1. The step sessions in the returned tree are recorded, which is the ONLY
//     recovery path for step-frame attribution. `node_start` announces a step's
//     session id live, but a container restart empties that registry while the
//     run carries on — so a resumed run's frames would arrive on the chat's
//     connection with session ids nothing in this process ever announced, and be
//     classified as a subagent's. Reading a run is exactly when the durable copy
//     of that mapping is in hand.
//  2. A missing VERB is distinguished from a missing RUN. KAS answers an
//     unregistered `_kiro/workflow/*` name with a -32603 whose `error.data`
//     carries its persistence classifier's text, so "this build has no workflow
//     engine" is detectable — and reporting it as 404 told the user their run had
//     been deleted, which is a different and alarming thing.
func (h *Hub) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		api.BadRequest(w, "missing workflow id")
		return
	}
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(r.Context(), sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow inspect call", methodKiroWorkflowInspect,
		callerParams(map[string]any{"workflowId": id}))
	if err != nil {
		if workflow.IsUnknownMethod(err) {
			slog.Warn("workflow inspect: engine not available on this kiro-cli",
				"workflow_id", id, "detail", workflow.Details(err))
			api.WriteJSONStatus(w, http.StatusServiceUnavailable,
				map[string]string{"error": "the workflow engine is not available on this kiro-cli build"})
			return
		}
		slog.Warn("workflow inspect failed", "workflow_id", id,
			"error", err, "detail", workflow.Details(err))
		api.NotFound(w, "workflow run not found")
		return
	}
	h.recordRunStepSessions(raw)
	api.WriteRawJSON(w, raw)
}

// recordRunStepSessions seeds the step-session registry from an inspect result.
//
// Best-effort on purpose: a decode failure here must not fail the read, because
// the response is passed through raw and is useful whether or not the side
// effect landed. The cost of missing it is one run's step frames classified as a
// subagent's until the next read.
func (h *Hub) recordRunStepSessions(raw json.RawMessage) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil {
		return
	}
	for _, s := range workflow.StepSessions(res.State) {
		h.translator.RecordStepSession(s.SessionID, res.State.WorkflowID, s.NodeID)
	}
}
