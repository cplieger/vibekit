package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// configOptionFake writes a fake kiro-cli that logs every request line to
// logPath and answers the three methods Start needs. session/new reports a
// model the caller did not ask for, so a test can tell the requested model
// apart from the session default.
func configOptionFake(t *testing.T, logPath string) string {
	t.Helper()
	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + logPath + `"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-cfg","configOptions":[{"id":"model","currentValue":"engine-default"}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	dir := filepath.Dir(logPath)
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return scriptPath
}

// The requested model and effort reach a new session as config options, because
// they cannot reach it as launch flags: kiro-cli refuses --model and --effort
// with --agent-engine=v3 and exits before answering initialize (measured on
// 2.17.0 and 2.18.0). Before this, a chat's model request was written into argv,
// so the process died on spawn and the model was never applied at all — every
// session ran on the engine default and every model switch 500'd.
//
// It asserts on the RAW request bytes rather than on a Go struct because the
// defect class is a value that never leaves the process.
func TestNewSession_AppliesRequestedModelAndEffort(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &api.StartOpts{Model: "claude-opus-5", Effort: "high"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	got := string(raw)

	wants := []string{
		`"method":"session/set_config_option"`,
		`"configId":"model"`,
		`"value":"claude-opus-5"`,
		`"configId":"effortLevel"`,
		`"value":"high"`,
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("request log does not contain %s\nlog:\n%s", want, got)
		}
	}

	// The bridge must report the model it actually selected, not the default it
	// was handed: the chat header reads this.
	if id := b.ModelID(); id != "claude-opus-5" {
		t.Errorf("ModelID() = %q, want claude-opus-5", id)
	}
}

// A model equal to the session default costs no round trip, and `auto` is not a
// model id at all — sending it would make KAS reject a legal chat.
func TestNewSession_SkipsRedundantModelConfigOption(t *testing.T) {
	cases := []struct {
		name  string
		model string
	}{
		{"auto is not a model id", api.ModelAuto},
		{"already the session default", "engine-default"},
		{"unset", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "requests.log")
			scriptPath := configOptionFake(t, logPath)

			b := New(scriptPath, dir)
			t.Cleanup(b.Stop)
			if err := b.Start(context.Background(), &api.StartOpts{Model: tc.model}); err != nil {
				t.Fatalf("Start: %v", err)
			}

			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read request log: %v", err)
			}
			if strings.Contains(string(raw), `"configId":"model"`) {
				t.Errorf("model %q sent a config option it did not need\nlog:\n%s", tc.model, raw)
			}
		})
	}
}

// An effort level outside the accepted set is dropped rather than sent, so a bad
// persisted setting cannot turn into a failed call on the session-creation path.
func TestNewSession_DropsInvalidEffort(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &api.StartOpts{Effort: "ultra"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(raw), `"configId":"effortLevel"`) {
		t.Errorf("invalid effort reached the wire\nlog:\n%s", raw)
	}
}

// An invalid model identifier is refused before the process is spawned. It no
// longer flows into argv, but it does flow onto the wire, and Start is the one
// place that sees it.
func TestStart_RefusesInvalidModelIdentifier(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	err := b.Start(context.Background(), &api.StartOpts{Model: "bad model; rm -rf /"})
	if err == nil {
		t.Fatal("Start accepted an invalid model identifier, want an error")
	}
	if !strings.Contains(err.Error(), "invalid model identifier") {
		t.Errorf("error = %v, want it to name the invalid model identifier", err)
	}
}

// The context that bounds the startup handshake must NOT own the subprocess.
//
// This is the bug that made vibekit's bridges die after the first message.
// CmdPrompt runs a turn under a per-turn context and cancels it on handler
// return; Start assigned that context to the subprocess, so exec's Cancel hook
// closed the process's stdin and signalled its head the moment the FIRST prompt
// finished. Nothing detected it — kiro-cli passes its stdio down to
// kiro-cli-chat and node, so all three hold the write end of the stdout pipe
// and the head's death never reaches the readLoop as EOF. The bridge stayed
// registered and healthy-looking while every write returned "file already
// closed", which is what made every model switch fall back to a restart, and
// each abandoned child tree leaked ~250 MB.
//
// The assertion is a live round trip rather than a liveness probe on the pid:
// "the bridge is still usable" is the property that broke, and it holds whether
// or not a reaper has collected anything.
func TestStart_HandshakeCtxDoesNotOwnTheSubprocess(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	handshakeCtx, cancelHandshake := context.WithCancel(context.Background())
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	t.Cleanup(cancelLifetime)

	if err := b.Start(handshakeCtx, &api.StartOpts{Lifetime: lifetime}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Exactly what CmdPrompt's `defer cancel()` does when a turn's handler returns.
	cancelHandshake()

	if _, err := b.Call(context.Background(), "_probe/ping", map[string]any{}); err != nil {
		t.Fatalf("Call after the handshake ctx was cancelled: %v; the subprocess took its lifetime from the handshake context", err)
	}
}

// The lifetime context DOES own the subprocess, so hub shutdown still reaps a
// bridge even if Stop races or panics. Without this the belt-and-braces kill
// that the handshake context used to provide would simply be gone.
func TestStart_LifetimeCtxOwnsTheSubprocess(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	if err := b.Start(context.Background(), &api.StartOpts{Lifetime: lifetime}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancelLifetime()

	// Cancel closes the process's stdin, so the fake's `read` loop ends and its
	// stdout EOFs; readLoop then drains the waiters and closes notifCh.
	select {
	case <-b.NotifCh():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling StartOpts.Lifetime did not tear the bridge down")
	}
}

// A caller that names no lifetime keeps a subprocess owned solely by Stop, and
// must not inherit the handshake context's cancellation by accident — that is the
// defect above wearing a default.
func TestStart_NilLifetimeDoesNotInheritHandshakeCancellation(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	handshakeCtx, cancelHandshake := context.WithCancel(context.Background())
	if err := b.Start(handshakeCtx, &api.StartOpts{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancelHandshake()

	if _, err := b.Call(context.Background(), "_probe/ping", map[string]any{}); err != nil {
		t.Fatalf("Call after the handshake ctx was cancelled: %v", err)
	}
}
