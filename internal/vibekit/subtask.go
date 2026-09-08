package vibekit

import "strings"

// StepSubtaskPrefix is what marks a subtask id as a WORKFLOW STEP's rather than a
// subagent's. A subagent's id is a bare uuid, so the prefix is what keeps the two
// id spaces from colliding.
const StepSubtaskPrefix = "wf:"

// StepSubtask is a step subtask id split into the two containers it names.
type StepSubtask struct {
	// WorkflowID is the run the step belongs to.
	WorkflowID string
	// NodePath is the step's address within that run.
	NodePath string
}

// StepSubtaskID mints the id for one step of one run: `wf:<workflowID>:<nodePath>`.
func StepSubtaskID(workflowID, nodePath string) string {
	return StepSubtaskPrefix + workflowID + ":" + nodePath
}

// ParseStepSubtask splits a step subtask id, reporting whether it is one. Absence
// is a NORMAL answer — every subagent uuid and every empty id reaches this — hence
// comma-ok rather than an error. The FIRST colon after the prefix ends the workflow
// id, so a node path may contain colons; static-src/step-subtask.ts is the twin and
// must agree with this on what a malformed id is.
func ParseStepSubtask(s string) (StepSubtask, bool) {
	if !IsStepSubtask(s) {
		return StepSubtask{}, false
	}
	rest := s[len(StepSubtaskPrefix):]
	sep := strings.Index(rest, ":")
	if sep <= 0 || sep == len(rest)-1 {
		return StepSubtask{}, false
	}
	return StepSubtask{WorkflowID: rest[:sep], NodePath: rest[sep+1:]}, true
}

// IsStepSubtask reports whether an id is prefixed as a workflow step's. The PREFIX
// only: a caller asking "is this a step at all" must answer yes for a malformed id
// too, where ParseStepSubtask answers no so the renderer keeps its delegate-box
// fallback. Two questions, so two functions.
func IsStepSubtask(s string) bool {
	return strings.HasPrefix(s, StepSubtaskPrefix)
}
