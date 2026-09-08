package agent

import (
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayPendingSteers emits one steer_queued per steer still in KAS's steering
// buffer, so a reconnecting client's dock comes back holding the messages the
// model has not read yet.
//
// This is the half `handlers/system.ts`'s gap door has always needed and never
// had. That door forgets every chat's dock without promoting anything — correct,
// because a gap means the frames that resolved those steers may be among the lost
// ones, so promoting would assert "the agent never read this" on no evidence — and
// its own comment recorded the cost: "streamInitialState does not replay the
// steering buffer, so a gap mid-turn loses the rest of that turn's rows". The
// steer was still queued in KAS and would still post; the reader simply had no way
// to know that, so they re-sent it.
//
// It is a separate replay beside the permissions and the run asks rather than
// folded into either, for the reason run_ask.go already records about those two:
// the registries answer for different wire objects with different lifetimes, and
// no removal path of one settles another's entry.
//
// WHAT IT CANNOT RECOVER, stated because it is the other half of the reported
// symptom: a steer the turn ENDED on. KAS clears its buffer at every boundary, so
// there is nothing left to replay and the message genuinely never posted. That
// loss is made visible instead — `dropSteers` promotes each unread row as a
// `dropped: true` transcript mark, and the client resolves that mark's anchor
// against the window it actually holds (store.ts `steerMarks`) so the
// "not delivered" note renders rather than silently landing nowhere.
//
// `chatFilter` is honoured exactly as the permission replay honours it: a client
// subscribed to one chat is served that chat's rows only.
func (rt *Runtime) replayPendingSteers(
	writeFn func(vibekit.ServerEvent) (int, error),
	chatFilter vibekit.ChatID,
) error {
	events := rt.bus.steers.List(chatFilter)
	for _, evt := range events {
		if _, err := writeFn(evt); err != nil {
			return err
		}
	}
	if len(events) > 0 {
		// The counterpart to the client's own gap line: together they say whether a
		// dock that emptied was force-emptied by a gap and refilled here, or emptied
		// because the turn ended and the steer was never read.
		slog.Debug("SSE connect steer replay", "steers", len(events), "chat_filter", chatFilter)
	}
	return nil
}
