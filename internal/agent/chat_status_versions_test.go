package agent

import (
	"testing"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func statusVersion(t *testing.T, v *subject.Versions) string {
	t.Helper()
	cur, _ := v.Current(subject.KindStatus, "")
	return cur
}

func newVersionedStatusCache() (*chatStatusCache, *subject.Versions) {
	v := &subject.Versions{}
	c := newChatStatusCache()
	c.versions = v
	return c, v
}

func TestMergeStamped_BumpsAndReturnsTheMintedStamp(t *testing.T) {
	c, v := newVersionedStatusCache()
	payload, stamp := c.MergeStamped("c1", vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading"})
	if payload.Status != "in_progress" {
		t.Fatalf("MergeStamped payload = %+v, want the merged declaration", payload)
	}
	want := vibekit.SubjectStamp{Kind: "status", Version: statusVersion(t, v)}
	if stamp == nil || *stamp != want {
		t.Fatalf("MergeStamped stamp = %+v, want %+v", stamp, want)
	}
	if want.Version == subject.Unminted {
		t.Fatal("MergeStamped did not move the status counter")
	}
	_, second := c.MergeStamped("c1", vibekit.ChatStatusPayload{Description: "writing"})
	if second.Version == stamp.Version {
		t.Errorf("second MergeStamped returned %q, same as the first; every merge mints", second.Version)
	}
}

func TestMergeStamped_ChatlessDeclarationMintsNothing(t *testing.T) {
	c, v := newVersionedStatusCache()
	c.MergeStamped("c1", vibekit.ChatStatusPayload{Status: "in_progress"})
	before := statusVersion(t, v)
	_, stamp := c.MergeStamped("", vibekit.ChatStatusPayload{Status: "idle"})
	if after := statusVersion(t, v); after != before {
		t.Errorf("a chat-less merge moved the status counter %q -> %q", before, after)
	}
	if stamp == nil || stamp.Version != before {
		t.Errorf("chat-less stamp = %+v, want the current version %q", stamp, before)
	}
}

func TestStatusCache_OnlyWaitingRemovalsMint(t *testing.T) {
	c, v := newVersionedStatusCache()
	c.Merge("waiting", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser})
	c.Merge("busy", vibekit.ChatStatusPayload{Status: "in_progress"})
	c.Merge("busy2", vibekit.ChatStatusPayload{Status: "in_progress"})

	before := statusVersion(t, v)
	c.ClearAtTurnEnd("waiting")
	if after := statusVersion(t, v); after != before {
		t.Errorf("ClearAtTurnEnd on a waiting row moved the counter %q -> %q; it removes nothing", before, after)
	}
	c.ClearAtTurnEnd("busy")
	if after := statusVersion(t, v); after != before {
		t.Errorf("ClearAtTurnEnd on a non-waiting row moved the counter %q -> %q; that row is not in the certified set", before, after)
	}

	if c.ClearWaiting("busy2") {
		t.Fatal("ClearWaiting on a non-waiting row reported a removal")
	}
	if after := statusVersion(t, v); after != before {
		t.Errorf("ClearWaiting on a non-waiting row moved the counter %q -> %q", before, after)
	}
	c.Clear("busy2")
	if after := statusVersion(t, v); after != before {
		t.Errorf("Clear on a non-waiting row moved the counter %q -> %q", before, after)
	}

	if !c.ClearWaiting("waiting") {
		t.Fatal("ClearWaiting on the waiting row reported nothing removed")
	}
	afterWaiting := statusVersion(t, v)
	if afterWaiting == before {
		t.Errorf("ClearWaiting on a waiting row left the counter at %q; the certified set shrank", before)
	}

	c.Merge("waiting", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser})
	beforeClear := statusVersion(t, v)
	c.Clear("waiting")
	if after := statusVersion(t, v); after == beforeClear {
		t.Errorf("Clear on a waiting row left the counter at %q; the certified set shrank", beforeClear)
	}
}

func TestStatusSnapshotStamped_CarriesTheWaitingSetMinusBusyChats(t *testing.T) {
	c, v := newVersionedStatusCache()
	c.Merge("w1", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser, Description: "one"})
	c.Merge("w2", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser, Description: "two"})
	c.Merge("busy", vibekit.ChatStatusPayload{Status: "in_progress"})
	c.Merge("w3", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser})
	open := map[vibekit.ChatID]openTurnFacts{"w3": {}}

	payload, stamp := c.SnapshotStamped(open)
	if len(payload.Rows) != 2 || payload.Rows[0].ChatID != "w1" || payload.Rows[1].ChatID != "w2" {
		t.Fatalf("SnapshotStamped rows = %+v, want w1 and w2 in chat order", payload.Rows)
	}
	if payload.Rows[0].Description != "one" || payload.Rows[0].Status != vibekit.ChatStatusWaitingOnUser {
		t.Errorf("row w1 = %+v, want the retained status and description", payload.Rows[0])
	}
	want := vibekit.SubjectStamp{Kind: "status", Version: statusVersion(t, v)}
	if stamp == nil || *stamp != want {
		t.Errorf("SnapshotStamped stamp = %+v, want %+v", stamp, want)
	}
	empty, emptyStamp := newChatStatusCache().SnapshotStamped(nil)
	if empty.Rows == nil || len(empty.Rows) != 0 {
		t.Errorf("empty SnapshotStamped rows = %#v, want an empty non-nil slice (the wire needs `rows: []`)", empty.Rows)
	}
	if emptyStamp == nil || emptyStamp.Version != subject.Unminted {
		t.Errorf("empty SnapshotStamped stamp = %+v, want version %q", emptyStamp, subject.Unminted)
	}
}
