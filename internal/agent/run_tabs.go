package agent

// The server-side offer of a run's tab. The launching chat is a durable fact
// here (runlease.Lease.ChatID) and the tab set is server-owned, so one answer
// reaches every device at once.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// runTabOpener is the membership coordinator as the run surface uses it: open
// the tab a starting run offers its launching chat. *command.Membership
// satisfies it.
type runTabOpener interface {
	OpenRunTab(ctx context.Context, workflowID string, parentChat vibekit.ChatID, opID string) (command.TabOpened, error)
}

// offerRunTab opens the run's tab under the chat whose agent launched it, once
// per run for the life of that run.
//
// Called from the two frames every launch route passes through — `run_start`
// and each step's `node_start`, which is the retry — so every early return
// below is on the hot path and none of them logs. The offer is spent on the
// run's LEASE rather than in memory, because a reader's close has to stay final
// across a resume and a restart.
func (rs *Runs) offerRunTab(ctx context.Context, chatID vibekit.ChatID, workflowID string) {
	// A parentless run is skipped in both of its spellings: an empty chat id,
	// and the synthetic `run:<workflowId>` a run bridge registers under. The
	// coordinator refuses both anyway; this is what saves taking its lock once
	// per step frame for every scheduled run in the workspace.
	if rs.tabs == nil || workflowID == "" || chatID == "" || isRunChat(chatID) {
		return
	}
	if l, held := rs.lease(workflowID); held && l.TabOffered {
		return
	}
	if _, err := rs.tabs.OpenRunTab(ctx, workflowID, chatID, ""); err != nil {
		// Two normal states are silent, and both would otherwise log on every
		// frame of every run: the launching chat has no tab in the set yet, and
		// this build has no tab set at all. A capacity refusal is NEITHER — a
		// StatusError wrapping errTabsFull — and it starts one slot below
		// tabs.MaxOpenTabs, since the automatic open holds the last one back, so
		// such a workspace logs once per step frame for a run's whole life. That
		// repeat is deliberate and actionable.
		if !errors.Is(err, command.ErrNoParentTab) && !errors.Is(err, command.ErrTabsUnavailable) {
			slog.Warn("run tab not offered to its launching chat",
				"workflow_id", workflowID, "chat_id", chatID, "error", err)
		}
		// The offer stays UNSPENT on every failure, which is what the node_start
		// retry depends on.
		return
	}
	if err := rs.leaseStore().MarkTabOffered(ctx, workflowID); err != nil {
		// The flag is set in memory whenever the lease exists, so only a restart
		// could re-offer — and it would find the tab already open unless the
		// reader had closed it meanwhile.
		slog.Warn("run tab offer not recorded durably; a restart may offer it again",
			"workflow_id", workflowID, "error", err)
	}
}

// offerOnProgress wraps a step frame's handler with the offer's retry.
//
// `node_start` only, of the seven progress kinds: one frame per step catches a
// launching chat that opened after the run started, and the offer's own
// TabOffered check makes the repeat one map read. Composed rather than merged
// into the translator's handler for the reason observePaused and healPaused are
// — this reaches the tab set, the translator reaches the bus.
func (rs *Runs) offerOnProgress(next chatHandler) chatHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		rs.offerRunTab(ctx, chatID, workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}
