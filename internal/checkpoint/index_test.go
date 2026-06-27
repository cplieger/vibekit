package checkpoint

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

func TestCrossChatIndex(t *testing.T) {
	type step struct {
		action func(idx *crossChatIndex)
		assert func(t *testing.T, idx *crossChatIndex)
		name   string
	}

	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "ApplySkipsNonSnapshot",
			steps: []step{
				{
					name:   "turnStart is no-op",
					action: func(idx *crossChatIndex) { idx.apply("c", &event{Kind: kindTurnStart, Turn: 1}) },
					assert: func(t *testing.T, idx *crossChatIndex) {
						if len(idx.entries) != 0 {
							t.Errorf("apply(turnStart) populated entries: %+v", idx.entries)
						}
					},
				},
				{
					name:   "snapshot with empty path is no-op",
					action: func(idx *crossChatIndex) { idx.apply("c", &event{Kind: kindSnapshot, Path: "", AfterSHA: "abc"}) },
					assert: func(t *testing.T, idx *crossChatIndex) {
						if len(idx.entries) != 0 {
							t.Errorf("apply(snapshot without path) populated entries: %+v", idx.entries)
						}
					},
				},
			},
		},
		{
			name: "ApplySkipsEmptyAfterSHA",
			steps: []step{{
				name: "empty AfterSHA ignored",
				action: func(idx *crossChatIndex) {
					idx.apply("c", &event{Kind: kindSnapshot, Path: "f.go", BeforeSHA: "before", AfterSHA: "", TS: 100})
				},
				assert: func(t *testing.T, idx *crossChatIndex) {
					if _, ok := idx.entries["f.go"]; ok {
						t.Error("apply recorded observation with empty AfterSHA")
					}
				},
			}},
		},
		{
			name: "CheckUnknownPath",
			steps: []step{{
				name:   "unknown path returns false",
				action: func(_ *crossChatIndex) {},
				assert: func(t *testing.T, idx *crossChatIndex) {
					if _, ok := idx.check("c", "never-observed.go", "any-sha"); ok {
						t.Error("check(unknown path) = true, want false")
					}
				},
			}},
		},
		{
			name: "CheckSameChatNeverConflicts",
			steps: []step{{
				name: "same chat drift is not a conflict",
				action: func(idx *crossChatIndex) {
					idx.apply("c", &event{Kind: kindSnapshot, Path: "f.go", AfterSHA: "expected", TS: 100})
				},
				assert: func(t *testing.T, idx *crossChatIndex) {
					if _, ok := idx.check("c", "f.go", "different"); ok {
						t.Error("check(same chat, drifted) = true, want false")
					}
				},
			}},
		},
		{
			name: "ForgetChatNoOp",
			steps: []step{{
				name: "forget unknown chat leaves others intact",
				action: func(idx *crossChatIndex) {
					idx.apply("keeper", &event{Kind: kindSnapshot, Path: "f.go", AfterSHA: "sha", TS: 1})
					idx.forgetChat("never-existed")
				},
				assert: func(t *testing.T, idx *crossChatIndex) {
					if _, ok := idx.entries["f.go"]; !ok {
						t.Error("forgetChat(unknown) removed unrelated entry")
					}
				},
			}},
		},
		{
			name: "ForgetChatUsesReverseIndex",
			steps: []step{{
				name: "forget removes owned paths only",
				action: func(idx *crossChatIndex) {
					idx.apply("a", &event{Kind: kindSnapshot, Path: "p1.go", AfterSHA: "s1", TS: 1})
					idx.apply("a", &event{Kind: kindSnapshot, Path: "p2.go", AfterSHA: "s2", TS: 2})
					idx.apply("b", &event{Kind: kindSnapshot, Path: "p3.go", AfterSHA: "s3", TS: 3})
					idx.forgetChat("a")
				},
				assert: func(t *testing.T, idx *crossChatIndex) {
					if _, ok := idx.entries["p1.go"]; ok {
						t.Error("forgetChat(a) did not remove p1.go")
					}
					if _, ok := idx.entries["p2.go"]; ok {
						t.Error("forgetChat(a) did not remove p2.go")
					}
					if _, ok := idx.entries["p3.go"]; !ok {
						t.Error("forgetChat(a) removed unrelated entry p3.go")
					}
					if _, ok := idx.byChat["a"]; ok {
						t.Error("forgetChat(a) did not clean byChat map")
					}
				},
			}},
		},
		{
			name: "ByChatOwnershipTransfer",
			steps: []step{{
				name: "later chat takes ownership",
				action: func(idx *crossChatIndex) {
					idx.apply("a", &event{Kind: kindSnapshot, Path: "f.go", AfterSHA: "s1", TS: 1})
					idx.apply("b", &event{Kind: kindSnapshot, Path: "f.go", AfterSHA: "s2", TS: 2})
					idx.forgetChat("a")
				},
				assert: func(t *testing.T, idx *crossChatIndex) {
					if _, ok := idx.entries["f.go"]; !ok {
						t.Error("forgetChat(a) removed f.go owned by b")
					}
				},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := newCrossChatIndex()
			for _, s := range tc.steps {
				s.action(idx)
				s.assert(t, idx)
			}
		})
	}
}

// TestCrossChatIndex_PropertyBased exercises state-machine invariants under
// random operation sequences. Invariants checked:
//  1. check(chatID, path, sha) never returns true when chatID owns the entry
//  2. after forgetChat(c), no entry with chatID==c remains
//  3. apply with later TS overwrites earlier entry from different chat
//  4. apply with AfterSHA=="" is a no-op
func TestCrossChatIndex_PropertyBased(t *testing.T) {
	const (
		numOps   = 2000
		numChats = 5
		numPaths = 10
	)

	chats := make([]string, numChats)
	for i := range chats {
		chats[i] = fmt.Sprintf("chat_%d", i)
	}
	paths := make([]string, numPaths)
	for i := range paths {
		paths[i] = fmt.Sprintf("dir/file_%d.go", i)
	}

	rng := rand.New(rand.NewPCG(42, 99))
	idx := newCrossChatIndex()

	for i := range numOps {
		chat := chats[rng.IntN(numChats)]
		path := paths[rng.IntN(numPaths)]
		ts := int64(i + 1)

		switch rng.IntN(4) {
		case 0: // apply with valid AfterSHA
			sha := fmt.Sprintf("sha_%d", rng.IntN(100))
			idx.apply(chat, &event{Kind: kindSnapshot, Path: path, AfterSHA: sha, TS: ts})

			// Invariant 1: same-chat check never conflicts
			if _, conflict := idx.check(chat, path, "anything"); conflict {
				t.Fatalf("op %d: same-chat check returned conflict for chat=%s path=%s", i, chat, path)
			}

		case 1: // apply with empty AfterSHA (invariant 4: must be no-op)
			before := idx.entries[path]
			idx.apply(chat, &event{Kind: kindSnapshot, Path: path, AfterSHA: "", TS: ts})
			after := idx.entries[path]
			if before != after {
				t.Fatalf("op %d: apply with empty AfterSHA mutated entry for path=%s", i, path)
			}

		case 2: // forgetChat
			idx.forgetChat(chat)
			// Invariant 2: no entry owned by this chat remains
			for p, obs := range idx.entries {
				if obs.chatID == chat {
					t.Fatalf("op %d: forgetChat(%s) left entry for path=%s", i, chat, p)
				}
			}
			if s := idx.byChat[chat]; len(s) > 0 {
				t.Fatalf("op %d: forgetChat(%s) left byChat set with %d entries", i, chat, len(s))
			}

		case 3: // check from different chat
			otherChat := chats[(rng.IntN(numChats-1)+1)%numChats]
			if otherChat == chat {
				otherChat = chats[(rng.IntN(numChats-1)+2)%numChats]
			}
			idx.check(otherChat, path, "probe_sha")
			// No crash = success; conflict detection correctness is covered by invariant 1
		}
	}

	// Final invariant 3 check: apply with later TS overwrites
	idx2 := newCrossChatIndex()
	idx2.apply("old", &event{Kind: kindSnapshot, Path: "x.go", AfterSHA: "old_sha", TS: 1})
	idx2.apply("new", &event{Kind: kindSnapshot, Path: "x.go", AfterSHA: "new_sha", TS: 2})
	if obs := idx2.entries["x.go"]; obs.chatID != "new" || obs.expectedSHA != "new_sha" {
		t.Fatalf("later TS did not overwrite: got %+v", obs)
	}
}

func BenchmarkCrossChatIndex_Apply(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("%d_paths", n), func(b *testing.B) {
			idx := newCrossChatIndex()
			events := make([]*event, n)
			for i := range n {
				events[i] = &event{
					Kind:     kindSnapshot,
					Path:     fmt.Sprintf("dir/file_%d.go", i),
					AfterSHA: fmt.Sprintf("sha_%d", i),
					TS:       int64(i + 1),
				}
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				for i, ev := range events {
					idx.apply(fmt.Sprintf("chat_%d", i%10), ev)
				}
			}
		})
	}
}

func BenchmarkCrossChatIndex_Check(b *testing.B) {
	idx := newCrossChatIndex()
	for i := range 100 {
		idx.apply(fmt.Sprintf("chat_%d", i%10), &event{
			Kind:     kindSnapshot,
			Path:     fmt.Sprintf("dir/file_%d.go", i),
			AfterSHA: fmt.Sprintf("sha_%d", i),
			TS:       int64(i + 1),
		})
	}

	b.Run("parallel_readers", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				idx.check(fmt.Sprintf("chat_%d", (i+5)%10), fmt.Sprintf("dir/file_%d.go", i%100), "other_sha")
				i++
			}
		})
	})
}

func BenchmarkCrossChatIndex_CheckScaling(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("%d_paths", n), func(b *testing.B) {
			idx := newCrossChatIndex()
			for i := range n {
				idx.apply(fmt.Sprintf("chat_%d", i%10), &event{
					Kind:     kindSnapshot,
					Path:     fmt.Sprintf("dir/file_%d.go", i),
					AfterSHA: fmt.Sprintf("sha_%d", i),
					TS:       int64(i + 1),
				})
			}
			checkChat := "chat_other"
			checkPath := fmt.Sprintf("dir/file_%d.go", n/2)
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				idx.check(checkChat, checkPath, "different_sha")
			}
		})
	}
}

func BenchmarkCrossChatIndex_ApplyParallel(b *testing.B) {
	idx := newCrossChatIndex()
	for i := range 100 {
		idx.apply(fmt.Sprintf("chat_%d", i%10), &event{
			Kind:     kindSnapshot,
			Path:     fmt.Sprintf("dir/file_%d.go", i),
			AfterSHA: fmt.Sprintf("sha_%d", i),
			TS:       int64(i + 1),
		})
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			idx.apply(fmt.Sprintf("chat_%d", i%10), &event{
				Kind:     kindSnapshot,
				Path:     fmt.Sprintf("dir/file_%d.go", i%100),
				AfterSHA: fmt.Sprintf("sha_new_%d", i),
				TS:       int64(1000 + i),
			})
			i++
		}
	})
}

func BenchmarkCrossChatIndex_ForgetChat(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("%d_paths", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				idx := newCrossChatIndex()
				for i := range n {
					idx.apply("target", &event{
						Kind:     kindSnapshot,
						Path:     fmt.Sprintf("dir/file_%d.go", i),
						AfterSHA: fmt.Sprintf("sha_%d", i),
						TS:       int64(i + 1),
					})
				}
				// Add some entries from other chats to ensure selectivity.
				for i := range n / 10 {
					idx.apply("other", &event{
						Kind:     kindSnapshot,
						Path:     fmt.Sprintf("other/file_%d.go", i),
						AfterSHA: fmt.Sprintf("other_sha_%d", i),
						TS:       int64(n + i + 1),
					})
				}
				b.StartTimer()
				idx.forgetChat("target")
			}
		})
	}
}

// TestCrossChatIndex_TimestampTieKeepsIncumbent pins that on a
// timestamp tie between two different chats the incumbent keeps the
// slot (the newcomer must be strictly newer to take over).
func TestCrossChatIndex_TimestampTieKeepsIncumbent(t *testing.T) {
	idx := newCrossChatIndex()
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "aaa", TS: 100})
	idx.apply("B", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "bbb", TS: 100}) // same ts, other chat
	if got := idx.entries["P"].chatID; got != "A" {
		t.Errorf("entries[P].chatID = %q, want %q: on a ts tie the incumbent chat keeps the slot", got, "A")
	}
}

// TestCrossChatIndex_SameChatOverwritesRegardlessOfTimestamp pins that
// a same-chat update always overwrites the incumbent, even when its
// timestamp is older than the existing one.
func TestCrossChatIndex_SameChatOverwritesRegardlessOfTimestamp(t *testing.T) {
	idx := newCrossChatIndex()
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "first", TS: 100})
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "second", TS: 50}) // same chat, OLDER ts
	if got := idx.entries["P"].expectedSHA; got != "second" {
		t.Errorf("entries[P].expectedSHA = %q, want %q: same-chat updates overwrite regardless of ts", got, "second")
	}
}

// TestCrossChatIndex_OwnershipTransferRemovesPriorOwner pins that when
// a path's ownership transfers from chat A to chat B, A's byChat set
// loses the path (and, being empty, is dropped) so the path isn't
// owned by both chats.
func TestCrossChatIndex_OwnershipTransferRemovesPriorOwner(t *testing.T) {
	idx := newCrossChatIndex()
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "aaa", TS: 100})
	idx.apply("B", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "bbb", TS: 200}) // newer ts, other chat
	if got := idx.entries["P"].chatID; got != "B" {
		t.Fatalf("entries[P].chatID = %q, want B (transfer precondition)", got)
	}
	if n := len(idx.byChat["A"]); n != 0 {
		t.Errorf("byChat[A] has %d paths after transfer to B, want 0: the prior owner's set must lose the path", n)
	}
}
