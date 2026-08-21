package agent

import (
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// bridgeSpawnKey's fields are both validator-restricted today (ids.ValidChatID
// and ids.ValidIdent), so a colon-free join is already unambiguous and the
// encoded key must stay byte-identical to the plain concatenation. That
// identity is the whole reason this adoption is free: it is a pure encoding
// change with no length, log-readability or comparison surprise.
func TestBridgeSpawnKey_ByteIdenticalForValidatedFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		chatID        vibekit.ChatID
		modelOverride string
		want          string
	}{
		{"c1", "", "c1:"},
		{"c1", "auto", "c1:auto"},
		{"my_chat-01", "claude-sonnet-4.5", "my_chat-01:claude-sonnet-4.5"},
	}
	for _, tc := range cases {
		if !ids.ValidChatID(string(tc.chatID)) {
			t.Fatalf("test case chat id %q does not pass ids.ValidChatID", tc.chatID)
		}
		if !ids.ValidIdent(tc.modelOverride) {
			t.Fatalf("test case model %q does not pass ids.ValidIdent", tc.modelOverride)
		}
		if got := bridgeSpawnKey(tc.chatID, tc.modelOverride); got != tc.want {
			t.Errorf("bridgeSpawnKey(%q, %q) = %q, want %q",
				tc.chatID, tc.modelOverride, got, tc.want)
		}
	}
}

// The key must stay injective even for field values the current validators
// reject, because that is the whole point of encoding rather than trusting an
// alphabet: widening ids.ValidIdent (a model id taken verbatim from an upstream
// catalog) is an edit in another package that would not look like it touched
// key encoding. A collapse here makes singleflight hand one caller another
// caller's bridge — a chat talking to a session started for a different model.
func TestBridgeSpawnKey_DistinctPairsNeverCollapse(t *testing.T) {
	t.Parallel()

	pairs := []struct {
		name           string
		aChat          vibekit.ChatID
		aModel         string
		bChat          vibekit.ChatID
		bModel         string
		naiveSeparator string
	}{
		// The pair a plain ':' join would collapse.
		{"boundary moves across the colon", "c", "1:m", "c:1", "m", ":"},
		// The pair the pre-keyenc 0x00 join would collapse, were 0x00 ever
		// reachable through either field.
		{"boundary moves across the NUL", "c", "1\x00m", "c\x001", "m", "\x00"},
		// A literal backslash next to a separator: escaping must not
		// reintroduce the ambiguity it exists to remove.
		{"escape adjacent to a separator", `a\`, "b:c", `a\:b`, "c", ":"},
		// The empty model override is a real value (bare restart / auto), so it
		// must not alias a chat id that ends in the separator.
		{"empty override distinct from separator-suffixed chat", "c1:", "", "c1", ":", ":"},
	}

	for _, tc := range pairs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			naiveA := string(tc.aChat) + tc.naiveSeparator + tc.aModel
			naiveB := string(tc.bChat) + tc.naiveSeparator + tc.bModel
			if naiveA != naiveB {
				t.Fatalf("test setup: the naive join does not collapse this pair (%q vs %q)", naiveA, naiveB)
			}
			a := bridgeSpawnKey(tc.aChat, tc.aModel)
			b := bridgeSpawnKey(tc.bChat, tc.bModel)
			if a == b {
				t.Errorf("bridgeSpawnKey collapsed (%q,%q) and (%q,%q) onto %q",
					tc.aChat, tc.aModel, tc.bChat, tc.bModel, a)
			}
		})
	}
}
