package translate

import (
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// at is a fixed frame-arrival instant, so the stamped timings are assertable.
var at = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

const atRFC = "2026-03-04T05:06:07Z"

// TestRunProgress_NodeFramesCarryTheNodesState is the change that removes the
// refetch: the client applies these rather than answering each one with a
// `GET /api/runs/{id}` — a JSON-RPC round trip to KAS for the whole state tree,
// up to five concurrently per burst of node events.
func TestRunProgress_NodeFramesCarryTheNodesState(t *testing.T) {
	cases := []struct {
		name  string
		kind  vibekit.RunProgressKind
		frame kasRunNode
		want  vibekit.RunProgressPayload
	}{
		{
			name: "node_start asserts running and stamps the start",
			kind: vibekit.RunProgressNodeStart,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "coder", NodePath: []string{"seq", "coder"},
			},
			want: vibekit.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "coder", NodePath: "seq/coder",
				Status: "running", StartedAt: atRFC, Kind: vibekit.RunProgressNodeStart,
			},
		},
		{
			name: "node_complete forwards KAS's own terminal word and stamps the end",
			kind: vibekit.RunProgressNodeComplete,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "coder", NodePath: []string{"seq", "coder"},
				Status: "completed",
			},
			want: vibekit.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "coder", NodePath: "seq/coder",
				Status: "completed", EndedAt: atRFC, Kind: vibekit.RunProgressNodeComplete,
			},
		},
		{
			name: "a failed node carries KAS's reason, so the row says why without a refetch",
			kind: vibekit.RunProgressNodeComplete,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "coder", NodePath: []string{"coder"},
				Status: "failed", Reason: "the build did not link",
			},
			want: vibekit.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "coder", NodePath: "coder",
				Status: "failed", EndedAt: atRFC, FailureReason: "the build did not link",
				Kind: vibekit.RunProgressNodeComplete,
			},
		},
		{
			name: "node_paused asserts paused and stamps neither end",
			kind: vibekit.RunProgressNodePaused,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "ask", NodePath: []string{"ask"},
				Reason: "Step requested user input via send_message.",
			},
			want: vibekit.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "ask", NodePath: "ask",
				Status: "paused", Kind: vibekit.RunProgressNodePaused,
			},
		},
		{
			name: "watch_poll names its node and asserts no status, because a poll changes none",
			kind: vibekit.RunProgressWatchPoll,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "watch", NodePath: []string{"watch"},
			},
			want: vibekit.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "watch", NodePath: "watch",
				Kind: vibekit.RunProgressWatchPoll,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := c.frame.NodeID
			if node == "" {
				node = c.frame.LoopID
			}
			got := runProgress(c.kind, node, &c.frame, at)
			if got != c.want {
				t.Errorf("runProgress(%q, …) = %+v, want %+v", c.kind, got, c.want)
			}
		})
	}
}

// TestRunProgress_ShapeChangingKindsCarryNoNodePath is the signal that keeps the
// invalidation contract exactly where it is still needed: an empty node path is
// what tells the client to refetch, and these three cannot be expressed as a
// per-node patch.
func TestRunProgress_ShapeChangingKindsCarryNoNodePath(t *testing.T) {
	cases := []struct {
		kind  vibekit.RunProgressKind
		frame kasRunNode
		why   string
	}{
		{
			kind:  vibekit.RunProgressLoopIteration,
			frame: kasRunNode{WorkflowID: "wf1", LoopID: "loop"},
			why:   "a new iteration container appears in the tree",
		},
		{
			kind:  vibekit.RunProgressStepsQueued,
			frame: kasRunNode{WorkflowID: "wf1"},
			why:   "steps are appended to the tree",
		},
		{
			kind:  vibekit.RunProgressPaused,
			frame: kasRunNode{WorkflowID: "wf1"},
			why:   "it is run-level and its pauseReason is on inspect alone",
		},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			node := c.frame.NodeID
			if node == "" {
				node = c.frame.LoopID
			}
			got := runProgress(c.kind, node, &c.frame, at)
			if got.NodePath != "" {
				t.Errorf("node_path = %q, want empty (%s)", got.NodePath, c.why)
			}
			if got.Status != "" || got.StartedAt != "" || got.EndedAt != "" {
				t.Errorf("carries node state %+v, want none", got)
			}
			if got.Kind != c.kind || got.WorkflowID != "wf1" {
				t.Errorf("address = (%q, %q), want (wf1, %q)", got.WorkflowID, got.Kind, c.kind)
			}
		})
	}
}

// TestRunProgress_FallsBackToTheNodeIDWithNoPath: an empty path means "refetch",
// so a node frame that arrived without one must not silently join the run-level
// kinds — a row in the wrong place beats content that vanishes.
func TestRunProgress_FallsBackToTheNodeIDWithNoPath(t *testing.T) {
	f := kasRunNode{WorkflowID: "wf1", NodeID: "coder"}
	got := runProgress(vibekit.RunProgressNodeStart, "coder", &f, at)
	if got.NodePath != "coder" {
		t.Errorf("node_path = %q, want %q", got.NodePath, "coder")
	}
}
