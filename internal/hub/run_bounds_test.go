package hub

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// TestRunCeiling_BoundsAManualRunTheScheduleGateCannotSee is the hole D56a
// closes, written as a test.
//
// Before the ceiling, the ONLY bound was armRunDeadline, whose liveness gate is
// the unattended mark — and that mark is set by the scheduler's launch path
// alone. So the two gates disagree for a manual run, and this pins the
// disagreement: the schedule gate says "not mine", the ceiling says "mine". A
// change that made the ceiling reuse scheduleForRun would fail here, which is
// exactly the regression that would silently unbound every manual, retried,
// resumed and agent-launched run again.
func TestRunCeiling_BoundsAManualRunTheScheduleGateCannotSee(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_manual"

	h.armRunCeiling(id)

	if _, live := h.scheduleForRun(id); live {
		t.Fatal("a manual run reported as schedule-owned; the fixture is not testing what it claims")
	}
	if !h.disarmRunCeiling(id) {
		t.Error("a manual run held no ceiling arm, so nothing would ever stop it")
	}
}

// TestArmRunCeiling_IsIdempotent pins the property `run_start` forces.
//
// That frame re-fires on every resume (probe 6 saw three for one run) and the
// launch verbs arm too, so a run is armed more than once by design. A second arm
// must not start a second timer, or one run accumulates callbacks and the
// termination claim stops being the only thing between them and N cancels.
func TestArmRunCeiling_IsIdempotent(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	h.armRunCeiling("wf_1")
	h.armRunCeiling("wf_1")
	h.armRunCeiling("wf_1")

	if !h.disarmRunCeiling("wf_1") {
		t.Fatal("the run was not armed at all")
	}
	// One arm, so one take. Were three arms recorded, the arm map would still
	// hold entries and a later expiry would cancel a finished run.
	if h.disarmRunCeiling("wf_1") {
		t.Error("a second disarm succeeded, so more than one arm was recorded")
	}
}

// TestDisarmRunCeiling_ReportsWhatItHeld pins the arm's own bookkeeping. It is no
// longer the once-guard — that is the termination claim's job — so what it has to
// get right is only that it reports an arm it actually stopped.
func TestDisarmRunCeiling_ReportsWhatItHeld(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	h.armRunCeiling("wf_1")

	if !h.disarmRunCeiling("wf_1") {
		t.Fatal("the first take failed")
	}
	if h.disarmRunCeiling("wf_1") {
		t.Error("a disarmed run still reported an arm")
	}
	if h.disarmRunCeiling("wf_never_armed") {
		t.Error("an unarmed run reported an arm")
	}
	if h.disarmRunCeiling("") {
		t.Error("the empty workflow id reported an arm")
	}
}

// TestDisarmRunCeiling_StopsTheTimerItForgets is finding 6's first half.
//
// `armed` used to be a SET, so disarm could only forget an arm — the AfterFunc
// stayed live until its deadline. A run paused and resumed a hundred times over a
// week therefore carried a hundred pending callbacks, each of which woke up to ask
// about a run its own arm no longer described. The test reads the timer's own
// answer rather than waiting a ceiling out: Stop reports false for a timer already
// stopped, so a second Stop on the arm disarm handed back proves the first landed.
func TestDisarmRunCeiling_StopsTheTimerItForgets(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	h.armRunCeiling("wf_1")

	h.unattendedMu.Lock()
	arm := h.runBounds.arms["wf_1"]
	h.unattendedMu.Unlock()
	if arm == nil || arm.timer == nil {
		t.Fatal("the arm carries no timer, so nothing can ever stop it")
	}

	h.disarmRunCeiling("wf_1")
	if arm.timer.Stop() {
		t.Error("the timer was still live after its arm was dropped")
	}
}

// TestCeilingArm_StaleGenerationDoesNotClaimAResumedRun is finding 6's second
// half, and the reason an arm carries a generation at all.
//
// `Timer.Stop` does not halt an already-running func, so a callback that fired
// microseconds before a pause is in flight while the pause drops its arm and the
// resume installs a fresh one. Membership alone cannot tell those two arms apart,
// so the stale callback claimed the NEW arm and cancelled the resumed run after
// only the old arm's remainder. Calling the callback directly with the old
// generation is exactly that in-flight state.
func TestCeilingArm_StaleGenerationDoesNotClaimAResumedRun(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"

	h.armRunCeiling(id)
	h.unattendedMu.Lock()
	staleGen := h.runBounds.arms[id].gen
	h.unattendedMu.Unlock()

	h.disarmRunCeiling(id) // the pause
	h.armRunCeiling(id)    // the resume: a fresh ceiling of EXECUTING time

	// The stale arm's callback, arriving now.
	if h.takeCeilingArm(id, staleGen) {
		t.Fatal("a stale arm claimed the resumed run; it would be cancelled after the old arm's remainder")
	}
	if !h.runCeilingArmed(id) {
		t.Error("the resumed run lost its arm to the stale callback, so nothing bounds it")
	}
	// The CURRENT generation is the one that may act.
	h.unattendedMu.Lock()
	liveGen := h.runBounds.arms[id].gen
	h.unattendedMu.Unlock()
	if liveGen == staleGen {
		t.Fatal("the resume reused the stale generation, so the guard cannot distinguish them")
	}
	if !h.takeCeilingArm(id, liveGen) {
		t.Error("the live arm's own callback was refused")
	}
	if h.takeCeilingArm(id, liveGen) {
		t.Error("the arm was taken twice")
	}
}

// TestClaimRunTermination_IsTakenOnce is finding 7's core: one run, one
// termination, whoever asks.
//
// Four callers race for it — the user's Cancel, a schedule's repeat interval, the
// wall clock and a step's turn cap — and before the claim each had a different
// gate, so two could pass at once and the second reason overwrote the first.
func TestClaimRunTermination_IsTakenOnce(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	if !h.claimRunTermination("wf_1") {
		t.Fatal("the first claim failed")
	}
	if h.claimRunTermination("wf_1") {
		t.Error("a second caller also took the claim; both would cancel and record")
	}
	// Independent per run: one run terminating must not stop another's cancel.
	if !h.claimRunTermination("wf_2") {
		t.Error("an unrelated run could not be terminated")
	}
	if h.claimRunTermination("") {
		t.Error("the empty workflow id took a claim")
	}
}

// TestClaimRunTermination_UserCancelBeatsALaterBound is the outcome finding 7
// names: a deliberate stop must not be relabelled a timeout.
//
// A user cancel records NOTHING, which is the only thing that distinguishes it
// from the two bounds on the History row. So a bound that claimed alongside it and
// recorded `overran` did not merely duplicate work — it rewrote what the user did.
func TestClaimRunTermination_UserCancelBeatsALaterBound(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"
	h.armRunCeiling(id)

	// The user's cancel, arriving first and recording nothing.
	if !h.claimRunTermination(id) {
		t.Fatal("the user's cancel could not claim the run")
	}
	h.recordRunEnd(id, "")

	// The ceiling, whose timer fired just before. Its arm is still live, so only
	// the claim can stop it writing.
	h.unattendedMu.Lock()
	gen := h.runBounds.arms[id].gen
	h.unattendedMu.Unlock()
	if !h.takeCeilingArm(id, gen) {
		t.Fatal("the ceiling's own arm was not live; the fixture is not testing the race")
	}
	if h.claimRunTermination(id) {
		t.Fatal("the ceiling claimed a run the user had already cancelled")
	}

	if got := h.runEndReason(id); got != "" {
		t.Errorf("the row reads %q for a user cancel; the absence IS the third value", got)
	}
}

// TestClaimRunTermination_ScheduleDeadlineAndStepCapCannotBothRecord is finding
// 7's second race. `cancelOverrunRun` ignored its disarm's result and recorded
// `overran` even when the step cap had already taken the arm and recorded
// `step_cap`, so the later write overwrote the earlier one and both issued a
// cancel for one run.
func TestClaimRunTermination_ScheduleDeadlineAndStepCapCannotBothRecord(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"

	// The step cap gets there first.
	if !h.claimRunTermination(id) {
		t.Fatal("the step cap could not claim the run")
	}
	h.recordRunEnd(id, runEndStepCap)

	// The schedule deadline, arriving on the same run.
	if h.claimRunTermination(id) {
		t.Fatal("the schedule deadline claimed a run the step cap was already ending")
	}
	if got := h.runEndReason(id); got != runEndStepCap {
		t.Errorf("runEndReason = %q, want the first reason %q", got, runEndStepCap)
	}
}

// TestReleaseRunTermination_ReopensAFailedCancel: the claim means a termination is
// in flight or landed, so a cancel RPC that FAILED must hand it back. Holding it
// would leave the Cancel button silently doing nothing on a run still executing.
func TestReleaseRunTermination_ReopensAFailedCancel(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	if !h.claimRunTermination("wf_1") {
		t.Fatal("the first claim failed")
	}
	h.releaseRunTermination("wf_1")
	if !h.claimRunTermination("wf_1") {
		t.Error("the run stayed claimed after its cancel failed, so nothing can stop it")
	}
}

// TestForgetRunBounds_ClearsTheClaimOnATerminalRun pins what bounds the claim map.
// Membership is the set of runs currently terminating, not a log of every run that
// ever was — and the terminal frame is the moment nothing can act on the run
// again, because every bound's own gate is already false by then.
func TestForgetRunBounds_ClearsTheClaimOnATerminalRun(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"
	h.armRunCeiling(id)
	h.claimRunTermination(id)

	h.forgetRunBounds(id)

	h.unattendedMu.Lock()
	claims := len(h.runBounds.terminating)
	arms := len(h.runBounds.arms)
	h.unattendedMu.Unlock()
	if claims != 0 {
		t.Errorf("the terminal frame left %d claims behind", claims)
	}
	if arms != 0 {
		t.Errorf("the terminal frame left %d arms behind", arms)
	}
}

// TestClearRunEnd_RestoresARetriedRunToUnbounded is finding 9, at the helper.
//
// Retry reuses the workflow id, and two things about the old run outlive it: the
// recorded reason (which history.ts deliberately lets outrank live status, so the
// running retry rendered as aborted) and the termination claim (which would leave
// the retry with no wall clock at all, since no bound can claim a run twice).
func TestClearRunEnd_RestoresARetriedRunToUnbounded(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"

	h.claimRunTermination(id)
	h.recordRunEnd(id, runEndOverran)
	h.recordRunEnd("wf_other", runEndStepCap)

	h.clearRunEnd(id)

	if got := h.runEndReason(id); got != "" {
		t.Errorf("the retried run still reads %q, so its row renders as aborted", got)
	}
	if !h.claimRunTermination(id) {
		t.Error("the retried run kept its termination claim, so no bound can ever stop it")
	}
	// The eviction queue must lose the entry too, or it names a key the map no
	// longer holds and eviction stops bounding the map.
	h.unattendedMu.Lock()
	order := slices.Clone(h.runBounds.order)
	h.unattendedMu.Unlock()
	if slices.Contains(order, id) {
		t.Errorf("the eviction queue still names the cleared run: %v", order)
	}
	// A neighbour is untouched.
	if got := h.runEndReason("wf_other"); got != runEndStepCap {
		t.Errorf("clearing one run's reason changed another's to %q", got)
	}
}

// TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun is the asymmetric half: a user
// cancel takes a claim and records NO reason, so keying the whole clear on a
// recorded reason would leave exactly that run unbounded after a retry.
func TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"

	h.claimRunTermination(id) // the user's cancel
	h.clearRunEnd(id)         // the retry

	if !h.claimRunTermination(id) {
		t.Error("a user-cancelled run stayed claimed through its retry, so nothing bounds it")
	}
}

// TestRearmRetriedRun_GivesAFreshClock: RetryRun's already-hosted branch exists
// for a run aborted WITHOUT a terminal frame, which can still hold the arm it was
// launched with — and armRunCeiling is idempotent, so without the disarm that run
// is retried under the remainder of its previous clock.
func TestRearmRetriedRun_GivesAFreshClock(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"

	h.armRunCeiling(id)
	h.unattendedMu.Lock()
	before := h.runBounds.arms[id].gen
	h.unattendedMu.Unlock()

	h.rearmRetriedRun(id)

	h.unattendedMu.Lock()
	arm := h.runBounds.arms[id]
	h.unattendedMu.Unlock()
	if arm == nil {
		t.Fatal("the retried run holds no arm at all")
	}
	if arm.gen == before {
		t.Error("the retry kept the previous arm, so its ceiling is the old one's remainder")
	}
}

// TestRunEndReason_DistinguishesABoundFromAUserCancel is D56c's whole point.
//
// Both bounds stop a run through the same CancelRun the Cancel button reaches,
// and KAS's status vocabulary has no "cancelled" — a cancel lands on `aborted`
// whoever asked for it. So the row cannot tell a backstop from a person unless
// the side that decided records it, and a user cancel must record NOTHING or the
// absence stops being the third value.
func TestRunEndReason_DistinguishesABoundFromAUserCancel(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	if got := h.runEndReason("wf_user_cancelled"); got != "" {
		t.Errorf("a run nothing recorded reported %q; a user cancel must read as empty", got)
	}
	h.recordRunEnd("wf_overran", runEndOverran)
	h.recordRunEnd("wf_step", runEndStepCap)

	if got := h.runEndReason("wf_overran"); got != runEndOverran {
		t.Errorf("runEndReason(overran) = %q, want %q", got, runEndOverran)
	}
	if got := h.runEndReason("wf_step"); got != runEndStepCap {
		t.Errorf("runEndReason(step cap) = %q, want %q", got, runEndStepCap)
	}
	// Still nothing for the run nobody bounded, now that its neighbours have
	// entries: a shared map must not answer for a key it does not hold.
	if got := h.runEndReason("wf_user_cancelled"); got != "" {
		t.Errorf("the reason leaked to an unrecorded run: %q", got)
	}
}

// TestRecordRunEnd_IsBounded pins the FIFO eviction.
//
// The record has to outlive its run — the History row reads it after the run
// finished — so it cannot be cleared on the terminal frame the way the arm is.
// That makes an unbounded map the default, and a container that runs for months
// would keep every reason forever.
func TestRecordRunEnd_IsBounded(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	for i := range maxRunEndReasons + 10 {
		h.recordRunEnd("wf_"+strconv.Itoa(i), runEndOverran)
	}
	h.unattendedMu.Lock()
	got := len(h.runBounds.reasons)
	order := len(h.runBounds.order)
	h.unattendedMu.Unlock()

	if got > maxRunEndReasons {
		t.Errorf("kept %d reasons, cap is %d", got, maxRunEndReasons)
	}
	if order != got {
		t.Errorf("the eviction queue (%d) and the map (%d) disagree", order, got)
	}
	// Oldest first: the reason for a run nobody is still looking at is the one to
	// lose.
	if h.runEndReason("wf_0") != "" {
		t.Error("the oldest reason survived eviction")
	}
	if h.runEndReason("wf_"+strconv.Itoa(maxRunEndReasons+9)) != runEndOverran {
		t.Error("the newest reason was evicted")
	}
}

// TestRecordRunEnd_RewriteDoesNotDoubleQueue guards the eviction bookkeeping: a
// second record for one run must not enqueue it twice, or the queue drifts from
// the map and eviction starts deleting keys that are already gone.
func TestRecordRunEnd_RewriteDoesNotDoubleQueue(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	h.recordRunEnd("wf_1", runEndOverran)
	h.recordRunEnd("wf_1", runEndStepCap)

	h.unattendedMu.Lock()
	order := len(h.runBounds.order)
	h.unattendedMu.Unlock()
	if order != 1 {
		t.Errorf("the eviction queue holds %d entries for one run, want 1", order)
	}
	if got := h.runEndReason("wf_1"); got != runEndStepCap {
		t.Errorf("runEndReason = %q, want the latest reason %q", got, runEndStepCap)
	}
	// An empty reason is not a reason: recording one would put a run in the queue
	// whose row then reads as unbounded anyway.
	h.recordRunEnd("wf_2", "")
	if got := h.runEndReason("wf_2"); got != "" {
		t.Errorf("an empty reason was recorded as %q", got)
	}
}

// TestStepTurnCapExceeded_CancelsOncePerRun pins the enforcement's arithmetic
// without a bridge behind it.
//
// The cancel itself needs a live utility session, so this asserts the two facts
// that decide whether it is issued at all and what the row says afterwards: the
// arm is taken exactly once, and the reason recorded is the step cap rather than
// the wall clock.
func TestStepTurnCapExceeded_CancelsOncePerRun(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	// Unarmed: a run vibekit is not bounding is not one it may cancel, and a
	// breach reported for it records nothing.
	h.StepTurnCapExceeded("wf_unarmed", "node-1", 200)
	if got := h.runEndReason("wf_unarmed"); got != "" {
		t.Errorf("an unarmed run recorded %q; the arm is the authority to act", got)
	}
}

// TestStepTurnCapExceeded_DoesNotConsumeTheArmItLoses: the step cap's gate reads
// the arm rather than taking it, because a breach that loses the termination claim
// must leave the wall clock to whoever won. Consuming it would strip the ceiling
// from a run that is still executing.
func TestStepTurnCapExceeded_DoesNotConsumeTheArmItLoses(t *testing.T) {
	t.Parallel()
	h := &Hub{}
	const id = "wf_1"
	h.armRunCeiling(id)

	// Something else is already ending the run.
	if !h.claimRunTermination(id) {
		t.Fatal("the fixture could not take the claim it needs to hold")
	}
	h.StepTurnCapExceeded(id, "node-1", 200)

	if !h.runCeilingArmed(id) {
		t.Error("a losing step-cap breach dropped the arm of a run it did not terminate")
	}
	if got := h.runEndReason(id); got != "" {
		t.Errorf("a losing step-cap breach recorded %q over the winner's reason", got)
	}
}

// TestObserveRunComplete_DisarmsOnlyOnATerminalStatus pins the one distinction
// this wrapper exists to make.
//
// KAS reports an `onMaxIterations` policy PAUSE through run_complete too, and
// that run is still this process's to resume — so dropping the arm there would
// leave a resumed run unbounded until its next run_start happened to arrive.
func TestObserveRunComplete_DisarmsOnlyOnATerminalStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status   string
		terminal bool
	}{
		{"completed", true},
		{"failed", true},
		{"aborted", true},
		{"cancelled", true},
		{"paused", false},
		{"", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			if got := terminalRunStatus(tc.status); got != tc.terminal {
				t.Errorf("terminalRunStatus(%q) = %v, want %v", tc.status, got, tc.terminal)
			}
		})
	}
}

// TestDecodeLifecycleFrame reads the two fields the bounds need off a frame, and
// pins the failure direction: an undecodable frame yields no workflow id, so it
// arms nothing rather than arming a run called "".
func TestDecodeLifecycleFrame(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params     string
		wantID     string
		wantStatus string
	}{
		"a run_start frame":        {`{"workflowId":"wf_1","workflowName":"x"}`, "wf_1", ""},
		"a terminal frame":         {`{"workflowId":"wf_1","status":"completed"}`, "wf_1", "completed"},
		"a frame with no id":       {`{"status":"completed"}`, "", "completed"},
		"malformed params":         {`{"workflowId":`, "", ""},
		"params that are not JSON": {`not json`, "", ""},
		"an array":                 {`["wf_1"]`, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := &api.RPCResponse{Params: json.RawMessage(tc.params)}
			got := decodeLifecycleFrame(msg)
			if got.WorkflowID != tc.wantID {
				t.Errorf("WorkflowID = %q, want %q", got.WorkflowID, tc.wantID)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
	// A nil message and an empty body are the shapes a wrapper meets before any
	// decode: both must be "no run", never a run named "".
	if got := workflowIDOfFrame(nil); got != "" {
		t.Errorf("workflowIDOfFrame(nil) = %q, want empty", got)
	}
	if got := workflowIDOfFrame(&api.RPCResponse{}); got != "" {
		t.Errorf("workflowIDOfFrame(empty) = %q, want empty", got)
	}
}

// TestRunCeiling_IsAConstantWithinCrewsClamp pins the number against the source
// it was taken from, and against the rule that it stays a constant.
//
// D57: a backstop the user can raise stops being a backstop, so there is no
// Settings key and no per-run override. Crew's configured value is clamped to
// [60s, 6h]; vibekit's is fixed, and this asserts it sits inside the range that
// clamp defines rather than merely being non-zero.
func TestRunCeiling_IsAConstantWithinCrewsClamp(t *testing.T) {
	t.Parallel()
	const crewMin, crewMax = 60 * time.Second, 6 * time.Hour
	if runCeiling < crewMin || runCeiling > crewMax {
		t.Errorf("runCeiling = %v, outside Crew's [%v, %v]", runCeiling, crewMin, crewMax)
	}
	if runCeiling != time.Hour {
		t.Errorf("runCeiling = %v, want Crew's 3600s default", runCeiling)
	}
}
