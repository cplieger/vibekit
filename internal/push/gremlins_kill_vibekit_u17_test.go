package push

// Tests added by mutant-killing unit vibekit-u17. Target: the surviving
// gremlins mutant service.go:165:61 (CONDITIONALS_NEGATION on the
// `u.Host != ""` operand inside Subscribe).
//
// All identifiers defined here are prefixed gk_vibekit_u17_ to avoid
// collisions with sibling units that share this package (notably u16,
// which owns the same dir).

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

const gk_vibekit_u17_subject = "mailto:test@example.com"

// gk_vibekit_u17_logRec is one captured slog record (message + flattened attrs).
type gk_vibekit_u17_logRec struct {
	msg   string
	attrs map[string]any
}

// gk_vibekit_u17_logCapture is a slog.Handler that records every log line so a
// test can assert on the "host" attribute Subscribe emits. The Subscribe
// branch under test writes only a local `host` variable that is passed
// straight to slog — there is no return value or exported state — so the
// logged host is the only observable distinguishing the original from the
// mutant.
type gk_vibekit_u17_logCapture struct {
	mu   sync.Mutex
	recs []gk_vibekit_u17_logRec
}

func (c *gk_vibekit_u17_logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *gk_vibekit_u17_logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	c.recs = append(c.recs, gk_vibekit_u17_logRec{msg: r.Message, attrs: attrs})
	c.mu.Unlock()
	return nil
}

func (c *gk_vibekit_u17_logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *gk_vibekit_u17_logCapture) WithGroup(string) slog.Handler      { return c }

// find returns the most recent captured record with the given message.
func (c *gk_vibekit_u17_logCapture) find(msg string) (gk_vibekit_u17_logRec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.recs) - 1; i >= 0; i-- {
		if c.recs[i].msg == msg {
			return c.recs[i], true
		}
	}
	return gk_vibekit_u17_logRec{}, false
}

// gk_vibekit_u17_installCapture swaps the global slog default for a capturing
// handler (all levels) and restores it via t.Cleanup. Callers must NOT use
// t.Parallel — the slog default is process-global.
func gk_vibekit_u17_installCapture(t *testing.T) *gk_vibekit_u17_logCapture {
	t.Helper()
	c := &gk_vibekit_u17_logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// Test_gk_vibekit_u17_SubscribeHostNonEmptyGate kills service.go:165:61
// CONDITIONALS_NEGATION on the second operand of
// `err == nil && u.Host != ""` inside Subscribe.
//
// Original: a parseable endpoint with a non-empty Host logs that host; a
// parseable endpoint with an empty Host (scheme/opaque URL) leaves
// host="unknown".
//
// Mutant (`u.Host == ""`): the two outcomes invert — a non-empty host is
// suppressed (host stays "unknown"), and an empty host is logged verbatim as
// "" (the branch body assigns host = u.Host).
//
// The empty-host case additionally ISOLATES this operand (col 61) from the
// sibling 165:44 mutant (`err == nil` -> `err != nil`): under that mutant the
// empty-host endpoint also yields host="unknown", identical to the original,
// so this assertion depends specifically on the `!=`/`==` at col 61.
func Test_gk_vibekit_u17_SubscribeHostNonEmptyGate(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantHost string
	}{
		{
			// true branch of `u.Host != ""`: parseable, non-empty host.
			name:     "non_empty_host_logged_verbatim",
			endpoint: "https://fcm.googleapis.com/fcm/send/u17-sub",
			wantHost: "fcm.googleapis.com",
		},
		{
			// false branch of `u.Host != ""`: parseable, empty host.
			name:     "empty_host_keeps_unknown",
			endpoint: "mailto:someone@example.com",
			wantHost: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(context.Background(), t.TempDir(), gk_vibekit_u17_subject)
			defer s.Close() // drain writeLoop before TempDir cleanup

			// Install capture AFTER New so its "push: ready" line is excluded.
			capLog := gk_vibekit_u17_installCapture(t)
			s.Subscribe(api.PushSubscription{Endpoint: tt.endpoint})

			rec, ok := capLog.find("push: subscribed")
			if !ok {
				t.Fatalf("Subscribe(%q) did not emit a %q log line",
					tt.endpoint, "push: subscribed")
			}
			if got := rec.attrs["host"]; got != tt.wantHost {
				t.Errorf("Subscribe(%q) logged host = %v, want %q",
					tt.endpoint, got, tt.wantHost)
			}
		})
	}
}
