package vibekit_test

// NewChatID is the identity half of stage 1b: the id a create RETURNS, replacing
// one the client minted in its own memory. Two things have to hold, and each has a
// consequence that is invisible until production.
//
// The shape must satisfy ids.ValidChatID, because that validator is the gate on
// the chat's own filename and on every command envelope. A mint its own boundary
// rejects would 400 every create.
//
// The bytes must be unguessable. The id addresses a conversation in a URL and in
// the ACP session chain, so a predictable one is an id a stranger can name.
//
// The external test package is deliberate: it lets the test import internal/ids,
// which internal/vibekit itself must not (that package imports no other vibekit
// package, which is what keeps the wire types acyclic). Importing it here is what
// makes the conformance claim a fact rather than a restatement of the regexp.

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestNewChatID_Shape(t *testing.T) {
	id := string(vibekit.NewChatID())

	cases := []struct {
		desc string
		got  bool
		want bool
	}{
		{desc: "carries the c- prefix every chat id has always carried", got: strings.HasPrefix(id, "c-"), want: true},
		{desc: "is 34 characters: c- plus 32 hex digits for 16 bytes", got: len(id) == 34, want: true},
		{desc: "passes the chat-id gate that names the chat file", got: ids.ValidChatID(id), want: true},
		{desc: "holds no path separator", got: strings.ContainsAny(id, "/\\"), want: false},
		{desc: "holds no traversal segment", got: strings.Contains(id, ".."), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s: got %v, want %v (id %q)", tc.desc, tc.got, tc.want, id)
			}
		})
	}
}

// TestNewChatID_HexOnly pins the alphabet rather than trusting the length: a mint
// that fell back to some other encoding could still be 34 characters and still
// pass ValidChatID while changing what the id is made of.
func TestNewChatID_HexOnly(t *testing.T) {
	body := strings.TrimPrefix(string(vibekit.NewChatID()), "c-")
	for i, r := range body {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			t.Errorf("byte %d of the id body is %q, want a lowercase hex digit (body %q)", i, r, body)
		}
	}
}

// TestNewChatID_Unique is the crypto/rand claim reduced to something a test can
// assert: a timestamp-plus-6-random-characters mint (what the client used to do)
// collides at this volume, and a counter would collide immediately.
func TestNewChatID_Unique(t *testing.T) {
	const n = 4096
	seen := make(map[vibekit.ChatID]struct{}, n)
	for range n {
		id := vibekit.NewChatID()
		if _, dup := seen[id]; dup {
			t.Fatalf("minted %q twice in %d draws", id, n)
		}
		seen[id] = struct{}{}
	}
}

// TestNewChatID_LegacyShapeStillValid is the migration claim the design makes:
// "existing chat ids stay valid under ValidChatID, so no chat data moves". If this
// ever fails, every chat file on the volume is unreachable and the answer is a
// migration, not a tweak to the mint.
func TestNewChatID_LegacyShapeStillValid(t *testing.T) {
	cases := []struct {
		desc string
		id   string
	}{
		{desc: "the client-minted c-<ms>-<base36> shape", id: "c-1756150000000-a1b2c3"},
		{desc: "the newly minted shape", id: string(vibekit.NewChatID())},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if !ids.ValidChatID(tc.id) {
				t.Errorf("ValidChatID(%q) = false, want true", tc.id)
			}
		})
	}
}
