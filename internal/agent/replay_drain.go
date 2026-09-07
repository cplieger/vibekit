package agent

// The completion condition a `session/load` replay closes on, shared by the chat
// twin (load_projection.go) and the step route (step_replay.go). Each route keeps
// its own map, mutex and settle EFFECTS; only the condition is shared.

// THE CONDITION is that the consumer has folded every frame preceding the
// `session/load` RESULT, which `observed >= loadSeq` states exactly. So a replay
// that drained early settles on the reader's own attempt, and a frame arriving
// after the result has a HIGHER position and cannot close the barrier early.

// drainPoint is a position on a bridge's read loop plus the consumer attachment it
// belongs to. A value rather than two uint64 parameters: gen and seq are counters
// of one type, so a transposed pair compiles and settles against the wrong number.
type drainPoint struct {
	// gen is the attachment: a position is only comparable within one.
	gen uint64
	// seq is a frame's own Seq, or for a response the count already delivered.
	seq uint64
}

// replayDrain is one replay's progress against the condition in this file's header.
// Guarded by the OWNER's mutex; the fields are not independently safe.
type replayDrain struct {
	// gen is the attachment both positions below belong to. A NEW one invalidates the
	// load position: its frames are on a channel nobody will drain.
	gen uint64
	// observed is how far the consumer has folded on that attachment.
	observed uint64
	// loadSeq is where the `session/load` response arrived; read only when loaded.
	loadSeq uint64
	// loaded, rather than `loadSeq != 0`, because 0 is a LEGAL position: the sequence
	// pre-increments, so a response ahead of every frame is genuinely 0.
	loaded bool
}

// noteConsumed records that the consumer has folded the frame at `at`.
func (d *replayDrain) noteConsumed(at drainPoint) {
	if at.gen < d.gen {
		return // a straggler from an attachment already replaced
	}
	if at.gen > d.gen {
		d.reattach(at.gen)
	}
	if at.seq > d.observed {
		d.observed = at.seq
	}
}

// markLoadedAt records the position the `session/load` response arrived at.
func (d *replayDrain) markLoadedAt(at drainPoint) {
	if at.gen < d.gen {
		return // a load whose attachment is already gone bounds nothing
	}
	if at.gen > d.gen {
		d.reattach(at.gen)
	}
	d.loadSeq, d.loaded = at.seq, true
}

// reattach adopts a new consumer attachment, dropping everything the previous one
// established: its sequence restarts at zero, so neither position still means anything.
func (d *replayDrain) reattach(gen uint64) {
	d.gen, d.observed, d.loadSeq, d.loaded = gen, 0, 0, false
}

// complete reports whether the replay may be settled from the attachment at gen.
// `sealed` is the bridge-exit call: that channel is closed and drained, so no frame
// can advance the position again. It bypasses the POSITION and never the LOAD — a
// load that never returned has no transcript to adopt, and is DISCARDED instead.
func (d *replayDrain) complete(gen uint64, sealed bool) bool {
	if gen != d.gen {
		return false // this caller is not the attachment the positions describe
	}
	return d.loaded && (sealed || d.observed >= d.loadSeq)
}
