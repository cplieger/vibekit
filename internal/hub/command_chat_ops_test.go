package hub

// Tests for the archive teardown path (OnChatArchiving / OnChatArchived) and
// the delete teardown (CleanupChatState): archiving a chat with a live bridge
// tears it down (bridge closed, pending perms + supervised trust cleared,
// assistant buffer dropped) but PRESERVES checkpoints (archive is reversible), while
// a delete reaps them.
