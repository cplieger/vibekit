package agent

// The tab set is server-owned, so one offer of a run's tab reaches every device.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// runTabOpener opens the tab a starting run offers its launching chat.
// *command.Membership satisfies it.
type runTabOpener interface {
	OpenRunTab(ctx context.Context, workflowID string, parentChat vibekit.ChatID, opID string) (command.TabOpened, error)
}

// offerRunTab opens the run's tab under the chat whose agent launched it, once per
// run for the life of that run. Called from `run_start` and each step's
// `node_start` (the retry), so every early return below is on the hot path. The
// offer is spent on the run's LEASE, so a reader's close survives a restart.
func (rs *Runs) offerRunTab(ctx context.Context, chatID vibekit.ChatID, workflowID string) {
	// A parentless run is skipped in both spellings — an empty chat id, and the
	// synthetic `run:<workflowId>` — to save the coordinator's lock per step frame.
	if rs.tabs == nil || workflowID == "" || chatID == "" || isRunChat(chatID) {
		return
	}
	if l, held := rs.lease(workflowID); held && l.TabOffered {
		return
	}
	if _, err := rs.tabs.OpenRunTab(ctx, workflowID, chatID, ""); err != nil {
		// Two normal states would otherwise log on every frame of every run: the
		// launching chat has no tab yet, and this build has no tab set. A capacity
		// refusal is neither, and its repeat is deliberate and actionable.
		if !errors.Is(err, command.ErrNoParentTab) && !errors.Is(err, command.ErrTabsUnavailable) {
			slog.Warn("run tab not offered to its launching chat",
				"workflow_id", workflowID, "chat_id", chatID, "error", err)
		}
		// The offer stays UNSPENT on failure; the node_start retry depends on it.
		return
	}
	if err := rs.leaseStore().MarkTabOffered(ctx, workflowID); err != nil {
		// The flag is set in memory while the lease exists, so only a restart could
		// re-offer — and it would find the tab open unless the reader had closed it.
		slog.Warn("run tab offer not recorded durably; a restart may offer it again",
			"workflow_id", workflowID, "error", err)
	}
}

// offerOnProgress wraps a step frame's handler with the offer's retry. `node_start`
// only, of the seven progress kinds: one frame per step catches a launching chat
// that opened after the run started, and the TabOffered check makes it a map read.
func (rs *Runs) offerOnProgress(next chatHandler) chatHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		rs.offerRunTab(ctx, chatID, workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}
