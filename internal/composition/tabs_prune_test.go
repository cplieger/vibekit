package composition

// The load-time prune, and what its resolver decides per kind.
//
// It runs ONCE, at load, before the listener serves anything. The live integrity
// mechanism is the membership coordinator; this exists for the crash that landed
// between a chat write and its tab write. Calling it periodically would be
// treating recovery as a substitute for ordering, and it would also race the
// coordinator, which is the only other writer of this store.

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestPruneTabs_DropsAChatTabWhoseChatIsGoneAndKeepsTheRest is the whole resolver
// as one table, because every arm is a DECISION rather than a default:
//
//   - a chat tab resolves against the chat store, since a chat that is gone cannot
//     be opened and its tab is a row that can only fail;
//   - an editor tab is left alone, because a missing file is not a reason to close
//     a tab the reader opened — a branch switch that removes a file for a minute
//     would otherwise cost them their place;
//   - a run tab is left alone for the same shape of reason: a finished run is still
//     reviewable from History;
//   - a singleton always resolves.
func TestPruneTabs_DropsAChatTabWhoseChatIsGoneAndKeepsTheRest(t *testing.T) {
	chatStore, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	if err := chatStore.Mutate(t.Context(), "c-live", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "still here"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	tabStore, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("tab store: %v", err)
	}
	opened := map[string]string{}
	for _, spec := range []vibekit.OpenTab{
		{Kind: vibekit.TabKindChat, Ref: "c-live"},
		{Kind: vibekit.TabKindChat, Ref: "c-gone"},
		{Kind: vibekit.TabKindEditor, Ref: "/workspace/deleted-by-a-branch-switch.go"},
		{Kind: vibekit.TabKindRun, Ref: "wf-finished"},
		{Kind: vibekit.TabKindSettings},
	} {
		sub, _, _, openErr := tabStore.Open(t.Context(), spec)
		if openErr != nil {
			t.Fatalf("open %+v: %v", spec, openErr)
		}
		opened[string(spec.Kind)+":"+spec.Ref] = sub.ID
	}

	pruneTabs(t.Context(), tabStore, chatStore)

	open, _ := tabStore.List()
	kept := map[string]bool{}
	for _, tab := range open {
		kept[string(tab.Kind)+":"+tab.Ref] = true
	}
	cases := []struct {
		desc string
		key  string
		want bool
	}{
		{desc: "a chat tab whose chat is still there survives", key: "chat:c-live", want: true},
		{desc: "a chat tab whose chat is gone is dropped", key: "chat:c-gone", want: false},
		{desc: "an editor tab for a missing file is left alone", key: "editor:/workspace/deleted-by-a-branch-switch.go", want: true},
		{desc: "a run tab is left alone", key: "run:wf-finished", want: true},
		{desc: "a singleton always resolves", key: "settings:", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if kept[tc.key] != tc.want {
				t.Errorf("%s kept = %v, want %v (set: %+v)", tc.key, kept[tc.key], tc.want, open)
			}
		})
	}
}

// TestPruneTabs_ToleratesAnUnwiredStore. A build with no config dir has no tab
// store, and load-time recovery must not be the thing that stops it booting.
func TestPruneTabs_ToleratesAnUnwiredStore(t *testing.T) {
	chatStore, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}

	pruneTabs(t.Context(), nil, chatStore)
}
