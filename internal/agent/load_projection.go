package agent

// Replay-projection lifecycle. Replay frames are PUSHED to the bridge's buffered
// notifCh before session/load returns, so settling at Start's return would race a
// partial transcript; replay_drain.go owns the completion condition instead, and the
// step route reads the same type. bridge.replayBudget bounds the RPC above, so the
// condition needs no timeout of its own. A load that never returned leaves the drain
// unloaded and the projection is DISCARDED.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// replay owns the session/load transcript projection: the in-flight rebuild
// of a chat's history from KAS's own replay, and the swap that makes it the
// record.
type replay struct {
	// chats is where a settled projection lands; needs Mutate, not just Get.
	chats translate.ChatRecords
	// lifetime supplies the context the swap runs under.
	lifetime *lifetime
	// projections are the rebuilds in flight, keyed by chat.
	projections map[vibekit.ChatID]*loadProjection
	// settling holds a projection whose swap is IN FLIGHT: it has left projections, so
	// no new frame lands in it, but its barrier stays reachable here until Mutate
	// returns, which is what makes the barrier span the store write.
	settling map[vibekit.ChatID]*loadProjection
	// onProjection receives a settled transcript, called WITHOUT projMu held: the swap
	// writes the chat store, and holding the lock across that would let a store
	// mutation and a replay frame deadlock against each other.
	onProjection func(chatID vibekit.ChatID, msgs []vibekit.Message, watermark string)
	// broadcast publishes the replacement announcement.
	broadcast func(context.Context, vibekit.ServerEvent)
	// workDir is the workspace root a projected diff path is made relative to. A VALUE
	// rather than a read through lifetime, because the barrier tests build a bare replay
	// carrying no lifetime and still open a projection. An empty one yields absolute
	// paths that key ChangedFiles differently from a live row's, and requireCollaborators
	// cannot see a string, so the single production caller is the whole guard.
	workDir string
	projMu  sync.Mutex
}

// loadProjection is one in-flight session/load's accumulating transcript.
// Guarded by Runtime.projMu; the fields are not independently safe.
type loadProjection struct {
	proj *translate.Projection
	// settled is closed exactly once, when this projection has been ADOPTED into the
	// record or abandoned — the barrier ReplaySettled hands out.
	settled chan struct{}
	// frames counts replay frames ingested: many frames projecting zero messages is a
	// decoding bug.
	frames int
	// drain is the completion condition, shared with the step route. Last so every
	// pointer field stays ahead of it (govet fieldalignment).
	drain replayDrain
}

// The three triggers a settle can run from, for the settle log: the reader's own
// post-load attempt, the position a consumed frame reached, and the bridge-exit seal.
const (
	settleOnLoad  = "load"
	settleOnFrame = "frame"
	settleOnExit  = "exit"
)

// closedBarrier is ReplaySettled's answer for a chat with no projection open, so a
// caller never waits for a resume that is not happening. One value for the whole
// process: a closed channel is stateless and always ready.
var closedBarrier = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// OpenReplayProjection starts a projection for a chat about to session/load. A
// projection already open for that chat is discarded: the only way to reach this twice
// is a re-load (model-switch fallback), whose replay supersedes.
func (rp *replay) OpenReplayProjection(chatID vibekit.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if rp.projections == nil {
		rp.projections = make(map[vibekit.ChatID]*loadProjection)
	}
	if prev, dup := rp.projections[chatID]; dup {
		slog.Debug("replay projection: superseding an open one", "chat_id", chatID)
		// The superseded settle can never run, so release its barrier or a waiter
		// waits for the life of the process; the replacement closes a different one.
		close(prev.settled)
	}
	rp.projections[chatID] = &loadProjection{
		proj:    translate.NewProjection(newMessageID, rp.workDir),
		settled: make(chan struct{}),
	}
}

// ReplaySettled returns a channel closed once the chat's in-flight replay has been
// adopted into the record — or discarded, or superseded. No projection open answers
// already-closed, so a caller never blocks on a resume that is not happening.
func (rp *replay) ReplaySettled(chatID vibekit.ChatID) <-chan struct{} {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if lp := rp.projections[chatID]; lp != nil {
		return lp.settled
	}
	if lp := rp.settling[chatID]; lp != nil {
		return lp.settled
	}
	return closedBarrier
}

// MarkReplayLoadedAt records the read-loop position the session/load response arrived
// at, and ATTEMPTS one settle. Called from the spawn goroutine.
//
// The attempt is the point: a replay Forward has already drained is complete HERE and
// no later frame is coming to notice it.
func (rp *replay) MarkReplayLoadedAt(chatID vibekit.ChatID, at drainPoint) {
	lp := rp.claimSettled(chatID, at.gen, false, func(d *replayDrain) { d.markLoadedAt(at) })
	rp.adopt(chatID, lp, settleOnLoad)
}

// DiscardReplayProjection drops a chat's projection unsettled, when the load failed,
// so a half-built transcript cannot be adopted later.
func (rp *replay) DiscardReplayProjection(chatID vibekit.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if lp, open := rp.projections[chatID]; open {
		delete(rp.projections, chatID)
		// A discarded projection adopts nothing, so there is no swap left to race.
		close(lp.settled)
		slog.Debug("replay projection: discarded", "chat_id", chatID)
	}
}

// ingestReplayFrame folds one replay-tagged frame into the chat's open projection.
// Reports whether a projection consumed it, so the caller can drop a replay that
// arrived with no load in flight.
func (rp *replay) ingestReplayFrame(chatID vibekit.ChatID, kind vibekit.ACPUpdateKind, raw json.RawMessage) bool {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return false
	}
	lp.proj.Ingest(kind, raw)
	lp.frames++
	return true
}

// SettleReplayProjection folds one drain observation into the chat's projection and
// completes it once the consumer has folded everything that preceded the load result.
// `at` is the frame's own position and the attachment that consumed it; `force` is the
// bridge-exit seal, which bypasses the position because no frame can advance it again.
//
// No-op when no projection is open, so callers may call it per frame.
func (rp *replay) SettleReplayProjection(chatID vibekit.ChatID, at drainPoint, force bool) {
	lp := rp.claimSettled(chatID, at.gen, force, func(d *replayDrain) { d.noteConsumed(at) })
	trigger := settleOnFrame
	if force {
		trigger = settleOnExit
	}
	rp.adopt(chatID, lp, trigger)
}

// claimSettled applies one observation to the chat's drain and, when that leaves the
// replay complete, MOVES the projection into `settling` — returning it to exactly one
// caller and nil to every other. The claim is what makes the settle run once: three
// triggers reach it, on two goroutines. The move rather than a delete is what keeps the
// barrier reachable across the swap.
func (rp *replay) claimSettled(chatID vibekit.ChatID, gen uint64, force bool, note func(*replayDrain)) *loadProjection {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return nil
	}
	note(&lp.drain)
	if !lp.drain.complete(gen, force) {
		return nil
	}
	delete(rp.projections, chatID)
	if rp.settling == nil {
		rp.settling = make(map[vibekit.ChatID]*loadProjection)
	}
	rp.settling[chatID] = lp
	return lp
}

// adopt swaps a claimed projection into the record and releases its barrier ON RETURN.
// Nil is the ordinary answer — a settle attempt that claimed nothing.
func (rp *replay) adopt(chatID vibekit.ChatID, lp *loadProjection, trigger string) {
	if lp == nil {
		return
	}
	// Released on RETURN, not at claimSettled's map move: onProjection is the swap, and
	// a waiter woken before its Mutate has landed still races it.
	defer func() {
		rp.projMu.Lock()
		// Only when the entry is still OURS: a supersede can register a second
		// projection for this chat mid-swap, and deleting that would hide its barrier.
		if rp.settling[chatID] == lp {
			delete(rp.settling, chatID)
		}
		close(lp.settled)
		rp.projMu.Unlock()
	}()

	msgs := lp.proj.Messages()
	slog.Info("replay projection settled",
		"chat_id", chatID,
		"frames", lp.frames,
		"messages", len(msgs),
		"watermark", lp.proj.Watermark,
		"trigger", trigger)

	if rp.onProjection != nil {
		rp.onProjection(chatID, msgs, lp.proj.Watermark)
	}
}

// swapProjectedTranscript makes a settled replay the chat's transcript, merged with what
// a replay cannot speak for (see mergeProjection). Runs on the Forward goroutine OR on the
// spawn goroutine, so it must not hold projMu — the store mutation below can block and a
// replay frame arriving meanwhile needs that lock.
//
// It ANNOUNCES the replacement, because it is the only site that can tell one from an
// append and the merge is where that becomes knowable. The announcement is a
// subject_changed for the chat: Mutate's own chat_updated carries the header and the new
// `chat` stamp, and a client applying that frame records the version and calls its window
// fresh while the message set underneath it was swapped — a count cannot tell a fill from
// a swap. subject_changed is the fetch instruction the client already honours: it refetches
// the transcript and records the stamp only on commit, so the digest names this chat again
// if the refetch fails. Emitted only when the message set changed: a watermark-only move
// replaces no transcript.
func (rp *replay) swapProjectedTranscript(chatID vibekit.ChatID, msgs []vibekit.Message, watermark string) {
	var before, after int
	var changed bool
	var stats mergeStats
	version, err := rp.chats.Mutate(durable.Context(rp.lifetime.shutdownCtx), chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		before = len(c.Messages)
		merged, ch, st := mergeProjection(c.Messages, msgs)
		after, changed, stats = len(merged), ch, st
		sameWatermark := watermark == "" || watermark == c.CompactionWatermark
		// The merge's own answer, not a comparison performed after it: a FIELD-only
		// difference is invisible to sameMessageIDs and is still a change to the transcript
		// a client is rendering.
		if !changed && sameWatermark {
			return false
		}
		c.Messages = merged
		if watermark != "" {
			c.CompactionWatermark = watermark
		}
		return true
	})
	if err != nil {
		slog.Error("replay projection: swap failed", "chat_id", chatID, "error", err)
		return
	}
	// Both counts because a SHRINK is the signal: the replay covered turns the record
	// no longer holds.
	// paired is the whole diagnostic for the pairing key: 0 on a chat with live assistant
	// rows means the key did not arrive.
	slog.Info("replay projection: transcript swapped",
		"chat_id", chatID, "was", before, "now", after, "projected", len(msgs),
		"paired", stats.Paired, "added", stats.Added,
		"dropped", stats.Dropped, "replaced", stats.Replaced)
	// Mutate's own chat_updated is broadcast inside Mutate, so the header necessarily
	// precedes this frame: the count first, the fetch instruction second, stamped with the
	// version that mutation minted so the refetch commits at exactly that version.
	if changed {
		frame := vibekit.NewEvent(vibekit.EventSubjectChanged, chatID, vibekit.SubjectChangedPayload{})
		frame.Subject = vibekit.NewSubjectStamp(string(subject.KindChat), string(chatID), version)
		rp.broadcast(durable.Context(rp.lifetime.shutdownCtx), frame)
	}
}

// sameMessageIDs answers whether the merge left the rows and their order alone, which is
// the STRUCTURAL half of `changed`; a count cannot answer it, since a merge can return as
// many rows as it was given and none of the same ones.
func sameMessageIDs(existing, merged []vibekit.Message) bool {
	return slices.EqualFunc(existing, merged, func(a, b vibekit.Message) bool {
		return a.ID == b.ID
	})
}

// mergeStats is what the swap LOGS about one merge.
//
// Paired counts PAIRINGS, not consumed record rows. Replaced counts a projected row that
// took over a record row's ID without pairing with it, which is neither an addition (the
// transcript already held that id) nor a drop (the id is still there, carrying the
// projection's copy). A record row the merge PRESERVED is in none of them, deliberately:
// it is the emitted length minus the projected count.
type mergeStats struct{ Paired, Added, Dropped, Replaced int }

// mergeProjection decides the transcript to persist after a replay: each record row is
// PAIRED with its replayed twin on AgentSideID and the two are unioned, and a record row
// the replay does not cover is preserved by preserveExisting — KAS's log is not fsynced, so
// a turn vibekit durably holds can legitimately be absent from a replay.
func mergeProjection(existing, projected []vibekit.Message) (merged []vibekit.Message, changed bool, stats mergeStats) {
	if len(projected) == 0 {
		return existing, false, mergeStats{}
	}

	newest := int64(0)
	projectedIDs := make(map[string]struct{}, len(projected))
	projectedCompaction := false
	for i := range projected {
		projectedIDs[projected[i].ID] = struct{}{}
		if projected[i].Ts > newest {
			newest = projected[i].Ts
		}
		if projected[i].EventKind == vibekit.EventCompacted {
			projectedCompaction = true
		}
	}

	// firstByKey and byID answer different questions — which record row supplies the stamps,
	// and which record row a projected id is taking over — so the ID-dup arm's comparison
	// names the row it is really replacing. keyToIndices holds ALL of a key's rows so a
	// pairing marks them consumed without an O(n) rescan. FIRST wins in both: nothing here
	// produces two record rows under one key, so a duplicate is a defect elsewhere and the
	// merge must answer the same way whichever one produced it.
	firstByKey := make(map[string]int, len(existing))
	keyToIndices := make(map[string][]int, len(existing))
	byID := make(map[string]int, len(existing))
	for i := range existing {
		if key := existing[i].AgentSideID(); key != "" {
			if _, seen := firstByKey[key]; !seen {
				firstByKey[key] = i
			}
			keyToIndices[key] = append(keyToIndices[key], i)
		}
		if _, seen := byID[existing[i].ID]; !seen {
			byID[existing[i].ID] = i
		}
	}

	out := make([]vibekit.Message, 0, len(projected)+len(existing))
	out = append(out, projected...)
	claimed := make(map[string]bool, len(projected))
	consumed := make(map[int]bool, len(existing))
	for j := range out {
		key := out[j].ID
		i, byKey := firstByKey[key]
		d, byDupID := byID[key]
		// A SWITCH rather than an if/else-if chain: a second projected row under one key
		// also satisfies the ID-dup test, so a chain counted it nowhere while every other
		// account of this merge called it an addition.
		switch {
		case byKey && !claimed[key] && existing[i].Role == out[j].Role:
			claimed[key] = true
			out[j] = union(&existing[i], &projected[j])
			for _, k := range keyToIndices[key] {
				consumed[k] = true
			}
			stats.Paired++
			changed = changed || !reflect.DeepEqual(existing[i], out[j])
		case byKey && claimed[key]:
			// The projected copy verbatim and NO stamps: the record holds that key once and
			// the output now holds it twice, which sameMessageIDs reports structurally.
			stats.Added++
		case byDupID:
			changed = changed || !reflect.DeepEqual(existing[d], out[j])
			stats.Replaced++
		default:
			stats.Added++
		}
	}

	// Indexed, not ranged by value: vibekit.Message is 216 bytes (gocritic rangeValCopy).
	for i := range existing {
		// A CONSUMED row goes regardless of preserveExisting: pairing is positive evidence
		// the replay covers it, strictly stronger than the Ts heuristic — and a paired live
		// row is ROUTINELY newer than its twin, its Ts being time.Now() at turn end against
		// the twin's first frame, so preserving it here would double the turn.
		if consumed[i] {
			continue
		}
		if _, dup := projectedIDs[existing[i].ID]; dup {
			continue
		}
		if preserveExisting(&existing[i], newest, projectedCompaction) {
			out = append(out, existing[i])
			continue
		}
		stats.Dropped++
	}

	// Stable, so at the same instant a projected row stays ADJACENT-ahead of a preserved
	// one. It says nothing about which copy is more complete: that is only true of a PAIRED
	// row, whose two accounts the union has already merged.
	slices.SortStableFunc(out, func(a, b vibekit.Message) int {
		return cmp.Compare(a.Ts, b.Ts)
	})
	return out, changed || !sameMessageIDs(existing, out), stats
}

// union merges one record row with the replayed twin the pair key matched.
//
// The record's row is the BASE, and the direction is the whole design: record-owned is the
// structural default, so a field added to the message type later is preserved with no edit
// here, where building from the projected row would make a new stamped field vanish on
// every paired row. Exactly the fields the AGENT states are overwritten, each only when the
// projection states one (an empty projected value is an absent statement, not a denial).
func union(rec, proj *vibekit.Message) vibekit.Message {
	out := *rec
	out.ID = proj.ID
	out.Role = proj.Role
	out.EventKind = cmp.Or(proj.EventKind, rec.EventKind)
	out.UserKind = cmp.Or(proj.UserKind, rec.UserKind)
	// SteerState is RECORD-owned when the record states one: the record's state is observed
	// (persistSteer wrote it from the live turn) where the projection's is inferred from
	// resend evidence, so a delivered steer must never read as dropped. An ABSENT state is
	// not a state, though — a row projected before the resend rule existed carries none, and
	// renders under the label a delivered steer gets. cmp.Or keeps the precedence and fills
	// only the absence.
	out.SteerState = cmp.Or(rec.SteerState, proj.SteerState)
	out.KASMessageID = cmp.Or(proj.KASMessageID, rec.KASMessageID)
	out.Ts = cmp.Or(proj.Ts, rec.Ts)
	out.TurnCredits = cmp.Or(proj.TurnCredits, rec.TurnCredits)
	out.TurnElapsedMs = cmp.Or(proj.TurnElapsedMs, rec.TurnElapsedMs)
	if proj.Refusal != nil {
		out.Refusal = proj.Refusal
	}
	if len(proj.ChangedFiles) > 0 {
		out.ChangedFiles = proj.ChangedFiles
	}
	// A USER row keeps the RECORD's content: BuildPromptBlocks augments the sent form with a
	// path reference per attachment it could not inline, so the replay's text can hold
	// machine-appended references the reader never typed.
	if out.Role != vibekit.RoleUser && proj.Content != "" {
		out.Content = proj.Content
	}
	if proj.Reasoning != "" {
		out.Reasoning = proj.Reasoning
	}
	if len(proj.Blocks) > 0 {
		out.Blocks = proj.Blocks
	}
	if len(proj.ToolCalls) > 0 {
		out.ToolCalls = unionToolCalls(rec.ToolCalls, proj.ToolCalls)
	}
	unionConclusion(&out, rec, proj)
	return out
}

// unionConclusion applies the CONCLUSION UNIT: {TurnOutcome, TurnStopReasonRaw,
// TurnTruncated, TurnFailureReason} travel together, because all four come from ONE
// ConcludeStopReason call on each side and a per-field union yields a `completed` turn
// carrying a "cancelled…" sentence — which both turn projections render as a green outcome
// beside a failure line. An empty projected outcome keeps all four: the process may have
// died with a local conclusion KAS logged no turn_end for.
func unionConclusion(out, rec, proj *vibekit.Message) {
	if proj.TurnOutcome == "" {
		return
	}
	out.TurnOutcome = proj.TurnOutcome
	out.TurnStopReasonRaw = proj.TurnStopReasonRaw
	out.TurnTruncated = proj.TurnTruncated
	// The record's reason is the ONLY reason that exists (turn_end.stopDetails is 0 of
	// 1,472 measured), so it is kept wherever both sides agree the turn ended badly. A
	// clean outcome can never carry a failure sentence; unknown counts as non-clean,
	// because an unrecognised stop reason is not evidence the turn was fine.
	out.TurnFailureReason = proj.TurnFailureReason
	if out.TurnFailureReason == "" && out.TurnOutcome != vibekit.TurnOutcomeCompleted {
		out.TurnFailureReason = rec.TurnFailureReason
	}
}

// unionToolCalls walks the PROJECTED calls in order, unioning each with the record call of
// the same ToolCall.ID. `var calls` plus append rather than make-with-capacity: a tool-free
// turn must yield NIL, because DeepEqual distinguishes nil from empty-non-nil and the store
// omits tool_calls under omitempty, so the empty form is a write plus a subject_changed
// on every load, forever.
//
// claimedCall is the sub-pairing's own claim: two creates for one toolCallId yield two
// projected calls with equal IDs, and without it both would copy one record call's stamps.
func unionToolCalls(rec, proj []vibekit.ToolCall) []vibekit.ToolCall {
	byID := make(map[string]int, len(rec))
	for i := range rec {
		if _, seen := byID[rec[i].ID]; !seen {
			byID[rec[i].ID] = i
		}
	}
	claimedCall := make(map[string]bool, len(proj))
	var calls []vibekit.ToolCall
	for j := range proj {
		i, ok := byID[proj[j].ID]
		if !ok || claimedCall[proj[j].ID] {
			calls = append(calls, proj[j])
			continue
		}
		claimedCall[proj[j].ID] = true
		calls = append(calls, unionToolCall(&rec[i], &proj[j]))
	}
	return calls
}

// unionToolCall merges one record call with the projected call of the same ToolCall.ID,
// record's as the base for union's own reason. The OUTCOME UNIT and Input are deliberately
// absent from the overwrite set — each is one statement with one owner per row, decided by
// which side has an outcome and by whether the store already cut the record's copy.
func unionToolCall(rec, proj *vibekit.ToolCall) vibekit.ToolCall {
	out := *rec
	out.ID = proj.ID
	out.Title = cmp.Or(proj.Title, rec.Title)
	out.Kind = cmp.Or(proj.Kind, rec.Kind)
	out.AgentSubtaskID = cmp.Or(proj.AgentSubtaskID, rec.AgentSubtaskID)
	out.WorkflowID = cmp.Or(proj.WorkflowID, rec.WorkflowID)
	out.TerminalID = cmp.Or(proj.TerminalID, rec.TerminalID)
	out.Ts = cmp.Or(proj.Ts, rec.Ts)
	if len(proj.Locations) > 0 {
		out.Locations = proj.Locations
	}
	if proj.Checkpoint != nil {
		out.Checkpoint = proj.Checkpoint
	}
	if proj.Disclosed != nil {
		out.Disclosed = proj.Disclosed
	}
	if proj.Denial != nil {
		out.Denial = proj.Denial
	}
	// The store re-cuts Input on every write, so a record whose input it ALREADY cut can
	// never hold the replay's whole copy: overwriting reports a difference, writes, gets
	// re-cut and repeats on every later load. Truncated.InputBytes is the store's own
	// statement that this call's input is deliberately short. Cost: such a call keeps its
	// short input forever, which is what the store produces for that input anyway. It reads
	// the RECORD's marker, so it cannot see an over-budget input the record never held —
	// there the overwrite runs once, the store mints the marker, and the next load declines.
	cutByStore := rec.Truncated != nil && rec.Truncated.InputBytes != 0
	if !cutByStore && statesInput(proj.Input) && !sameRawJSON(rec.Input, proj.Input) {
		out.Input = proj.Input
	}
	// The OUTCOME UNIT, taken whole from the side that HAS an outcome. No default arm: an
	// absent status, a non-terminal one and a spelling neither predicate knows are all
	// statements the merge cannot act on, so the record's stand by base-copy.
	switch {
	case proj.Status.IsOutcome():
		// Declined when the record has an outcome of its own: both sides read ONE
		// tool_result, so a disagreement there is the two paths differing about one frame.
		if !rec.Status.IsOutcome() {
			out.Status = proj.Status
			out.Output = proj.Output
			// Belt-and-braces: OutputSpans' one writer is reached only from the
			// completed/failed arm, so an outcome-less record call carries none anyway.
			out.OutputSpans = nil
			if len(proj.Diffs) > 0 {
				out.Diffs = proj.Diffs
			}
			out.Truncated = keptCuts(rec.Truncated, len(proj.Diffs) == 0)
			// The live writer runs AFTER its own outcome guard, so a call reaching this arm
			// carries 0; deriveDuration is reached only where a tool_result produced an
			// update, which is this arm's own condition. cmp.Or, so a record value the
			// projection lacks is not destroyed.
			out.DurationMs = cmp.Or(rec.DurationMs, proj.DurationMs)
		}
	case proj.Status.Terminal():
		// `aborted`: the projection could not settle this call either, and it brings nothing
		// else — a tool_result is what would have settled it, and 0 of 66,749 persisted
		// tool_call records carry a content member, so handing it the unit would replace the
		// record's own fragment with nothing.
		if !rec.Status.Terminal() {
			out.Status = proj.Status
		}
	}
	return out
}

// statesInput reports whether a raw input says anything at all. A create frame carrying no
// rawInput yields nil and one carrying an empty object yields two bytes that mean the same
// thing; either way it is an absent statement, which the record answers for. Wider than a
// length test for a measured reason: over 65,980 persisted calls none carries a null or a
// missing input, while 85 carry an empty `args` object — 55 of them fs_write, a tool whose
// input cannot honestly be empty.
func statesInput(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "{}", "[]":
		return false
	}
	return true
}

// sameRawJSON reports whether two raw JSON values encode the same document as the ENCODER
// would write them. Required rather than defensive: the store persists with MarshalIndent, so
// a value read back off disk carries that indentation and `<` as `\u003c` where the replay's
// copy is the compact wire bytes, and a byte comparison is then unequal on every load for
// every call with an object input. json.Marshal on a RawMessage compacts AND HTML-escapes in
// one idempotent call, which is what internal/chat's own inputWireBytes relies on; a
// marshal error falls back to a byte comparison, which errs toward WRITING.
func sameRawJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	na, err := json.Marshal(a)
	if err != nil {
		return bytes.Equal(a, b)
	}
	nb, err := json.Marshal(b)
	if err != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(na, nb)
}

// keptCuts keeps the store's cut record for the values the outcome unit kept, and drops it
// for the one it replaced.
//
// A marker for bytes that are gone misreports the cut, and it is the only producer of the
// card's `truncated, N bytes`. One deleted for bytes still on the row renders a truncated
// value as complete AND makes the Input gate read false next load, so the merge re-widens an
// input the store re-cuts, a write per load. Nil rather than a zero-valued pointer when
// nothing survives: DeepEqual distinguishes them and omitempty hands back nil.
func keptCuts(t *vibekit.ToolTruncation, keptDiffs bool) *vibekit.ToolTruncation {
	if t == nil {
		return nil
	}
	out := vibekit.ToolTruncation{InputBytes: t.InputBytes}
	if keptDiffs {
		out.DiffBytes, out.DiffCount = t.DiffBytes, t.DiffCount
	}
	if out == (vibekit.ToolTruncation{}) {
		return nil
	}
	return &out
}

// preserveExisting reports whether a record row the replay did not re-project survives.
//
// A COMPACTION is the one event row a replay CAN speak for, so it leaves the unconditional
// event preserve. The duplication has two halves: load N against N+1 is closed by the
// projection's derived id, which makes the record's copy a duplicate here, and the LIVE
// event against its projected twin only by this exclusion.
func preserveExisting(m *vibekit.Message, newest int64, projectedCompaction bool) bool {
	if m.Ts > newest || isPlanRow(m) {
		return true
	}
	if m.Role != vibekit.RoleEvent {
		return false
	}
	// The narrow exclusion: only a compaction, and only when the replay produced one of
	// its own. A compaction NEWER than the replay already survived above, which is the
	// case the un-fsynced KAS log makes real.
	if projectedCompaction && m.EventKind == vibekit.EventCompacted {
		return false
	}
	return true
}

// isPlanRow reports whether m is a turn's plan row: an assistant message whose ONLY
// payload is Plan. Every other assistant field must be empty, or a real reply carrying a
// plan would survive a replay that already re-projected it and render twice.
func isPlanRow(m *vibekit.Message) bool {
	return m.Role == vibekit.RoleAssistant &&
		len(m.Plan) > 0 &&
		m.Content == "" &&
		m.Reasoning == "" &&
		len(m.ToolCalls) == 0 &&
		len(m.Blocks) == 0
}
