package settings

import (
	"bytes"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestEffectiveKeys_AreAllKnown(t *testing.T) {
	// Every key the effective view carries must be in KnownKeys, or a GET response
	// round-tripped back as a PATCH would trip the unknown-key warning against
	// keys the server itself just sent.
	for _, k := range effectiveKeys() {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("effectiveKeys includes %q but it is not in KnownKeys", k)
		}
	}
}

// TestEffectiveSettings_EveryFieldIsSettable is the gate that makes adding a field
// safe. A field with no entry in effectiveSetters would serve its default forever
// and silently ignore whatever the user stored, which is the failure the hand-
// written key list this replaced could never catch — it had drifted to 8 of the 15
// keys the client actually reads.
//
// Reflection over the json tags rather than a second list, deliberately: a list
// would be one more thing to forget in exactly the same way.
func TestEffectiveSettings_EveryFieldIsSettable(t *testing.T) {
	settable := make(map[string]struct{}, len(effectiveKeys()))
	for _, k := range effectiveKeys() {
		settable[k] = struct{}{}
	}
	rt := reflect.TypeFor[vibekit.EffectiveSettings]()
	for f := range rt.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			t.Errorf("EffectiveSettings.%s has no json tag; every field is part of the wire", f.Name)
			continue
		}
		if _, ok := settable[tag]; !ok {
			t.Errorf("EffectiveSettings.%s (json %q) has no setter, so a stored value for it is ignored", f.Name, tag)
		}
		// No omitempty anywhere: wiregen emits an OPTIONAL TypeScript field for one,
		// and an optional field is what lets a client reader invent a fallback. The
		// whole point of this type is that it cannot.
		if strings.Contains(f.Tag.Get("json"), "omitempty") {
			t.Errorf("EffectiveSettings.%s carries omitempty; that generates an optional TS field and reopens the client-fallback class", f.Name)
		}
	}
	if got, want := rt.NumField(), len(effectiveKeys()); got != want {
		t.Errorf("EffectiveSettings has %d fields but %d setters; one side gained a key alone", got, want)
	}
}

func TestWarnUnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{
			name: "all known returns nil",
			keys: []string{"debug_logs", "chat_retention_days", "supervised_default"},
			want: nil,
		},
		{
			name: "single unknown surfaces",
			keys: []string{"debug_logs", "typo_key"},
			want: []string{"typo_key"},
		},
		{
			name: "multiple unknowns preserve input order",
			keys: []string{"debug_logs", "zzz_typo", "aaa_typo"},
			want: []string{"zzz_typo", "aaa_typo"},
		},
		{
			name: "all unknown",
			keys: []string{"foo", "bar"},
			want: []string{"foo", "bar"},
		},
		{
			name: "empty input returns nil",
			keys: nil,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WarnUnknownKeys(tc.keys, "test")
			if !slices.Equal(got, tc.want) {
				t.Errorf("WarnUnknownKeys(%v) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}

// TestKnownKeys_CoversTheClientSurface replaces a hand-written list of "the keys
// the frontend declares". That list existed because the client's type was
// hand-written too, so nothing could compare the two; it named 8 keys while the
// client read 15, and the drift was invisible.
//
// The client's type is now GENERATED from vibekit.EffectiveSettings, so the
// comparison is mechanical: KnownKeys must cover every key that type carries, and
// TestEffectiveSettings_EveryFieldIsSettable holds the other direction.
func TestKnownKeys_CoversTheClientSurface(t *testing.T) {
	for _, k := range effectiveKeys() {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("KnownKeys missing %q, which the generated client type reads", k)
		}
	}
	// security_profile is the one KnownKeys member the client does NOT read through
	// this payload: it goes through GET /api/permissions and
	// POST /api/permissions/profile, because selecting a profile REWRITES the policy
	// files rather than setting a preference.
	if _, ok := KnownKeys[KeySecurityProfile]; !ok {
		t.Errorf("KnownKeys missing %q", KeySecurityProfile)
	}
	if slices.Contains(effectiveKeys(), KeySecurityProfile) {
		t.Errorf("%q is in the effective view; it is owned by the permissions endpoints, not this payload", KeySecurityProfile)
	}
	// model_effort is deliberately absent from both sides now: reasoning effort
	// is per-chat, on the chat record (vibekit.Chat.Effort). A key here with no
	// frontend writer and no server reader would only invite one back.
	if _, ok := KnownKeys["model_effort"]; ok {
		t.Error("KnownKeys still declares model_effort; effort moved to the chat record")
	}
}

// TestDefaultAgentIgnoreFiles_SeededAndDiscoverable pins the settled
// "agent read filter ON by default" decision: the seeded ignore-file list is
// non-empty and includes .gitignore + .kiroignore, EffectiveDefaults carries it
// (so a fresh GET answers the real list rather than an empty one), and each
// returned slice is a fresh copy callers can mutate without corrupting the
// shared default.
func TestDefaultAgentIgnoreFiles_SeededAndDiscoverable(t *testing.T) {
	got := DefaultAgentIgnoreFiles()
	if len(got) == 0 {
		t.Fatal("DefaultAgentIgnoreFiles() is empty; a fresh install would not filter agent reads")
	}
	if !slices.Contains(got, ".gitignore") || !slices.Contains(got, ".kiroignore") {
		t.Errorf("DefaultAgentIgnoreFiles() = %v, want it to include .gitignore and .kiroignore", got)
	}

	// The effective view must carry the seeded list, because this is the key whose
	// absent-means-empty reading was a live data-losing bug: the client rendered an
	// empty chip row while the filter was applying both patterns, and the first edit
	// persisted that emptiness over them.
	if list := EffectiveDefaults().AgentIgnoreFiles; !slices.Equal(list, DefaultAgentIgnoreFiles()) {
		t.Errorf("EffectiveDefaults().AgentIgnoreFiles = %v, want %v", list, DefaultAgentIgnoreFiles())
	}

	// Fresh copy: mutating the returned slice must not affect the next call.
	got[0] = "MUTATED"
	if again := DefaultAgentIgnoreFiles(); again[0] == "MUTATED" {
		t.Error("DefaultAgentIgnoreFiles() returned a shared slice; callers can corrupt the default")
	}
}

// captureSlog installs a Debug-level slog handler writing to a buffer and
// restores the previous default logger on cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestWarnUnknownKeys_LogsOnlyWhenUnknownPresent pins the side-effect the
// return-value table test can't see: WarnUnknownKeys emits a single slog.Warn
// iff at least one key is unknown, and stays silent when every key is known.
// The warn is the operator-facing signal for config drift.
func TestWarnUnknownKeys_LogsOnlyWhenUnknownPresent(t *testing.T) {
	const msg = "settings: unknown keys in write"

	t.Run("all_known_no_warn", func(t *testing.T) {
		buf := captureSlog(t)
		got := WarnUnknownKeys([]string{KeySupervisedDefault, KeyDebugLogs}, "test-src")
		if len(got) != 0 {
			t.Errorf("WarnUnknownKeys(all known) = %v, want empty", got)
		}
		if strings.Contains(buf.String(), msg) {
			t.Errorf("warned with zero unknown keys; log=%q", buf.String())
		}
	})

	t.Run("one_unknown_warns", func(t *testing.T) {
		buf := captureSlog(t)
		got := WarnUnknownKeys([]string{"bogus_key"}, "test-src")
		if len(got) != 1 || got[0] != "bogus_key" {
			t.Errorf("WarnUnknownKeys(one unknown) = %v, want [bogus_key]", got)
		}
		if !strings.Contains(buf.String(), msg) {
			t.Errorf("did not warn for an unknown key; log=%q", buf.String())
		}
	})
}
