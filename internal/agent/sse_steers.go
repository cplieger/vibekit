package agent

import (
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayPendingSteers emits one steer_queued per steer still in KAS's steering
// buffer, so a reconnecting client's dock comes back holding the messages the model
// has not read yet. chatFilter is honoured as the permission replay honours it.
//
// It cannot recover a steer the turn ENDED on: KAS clears its buffer at every
// boundary, so nothing is left to replay. dropSteers makes that loss visible
// instead, as a `dropped: true` transcript mark.
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
		// The counterpart to the client's own gap line: together they say whether an
		// emptied dock was force-emptied by a gap and refilled here, or emptied
		// because the turn ended with the steer unread.
		slog.Debug("SSE connect steer replay", "steers", len(events), "chat_filter", chatFilter)
	}
	return nil
}
