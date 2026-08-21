package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestInitialize_SecretStorageTracksTheStore pins that
// `_meta.kiro.secretStorage` is declared from StartOpts.SecretStorage rather
// than as a literal true.
//
// The asymmetry is the whole point, and it is why the false case matters more
// than the true one. Declaring the capability is a commitment: KAS rethrows a
// client-side store failure into its MCP connect path, so a bridge that offers
// credential storage with no store behind it turns every MCP OAuth connect into
// a failure (runtime's secretStoreResult answers -32603). NOT offering it costs one
// `POST /register` per spawn and nothing else. So a regression to a hardcoded
// true is strictly worse than the state before the capability existed, and the
// false subtest is what catches it — it fails against the literal this replaced.
func TestInitialize_SecretStorageTracksTheStore(t *testing.T) {
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '%s\n' "$line" >> "$INIT_CAPTURE"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess_secrettest","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())

	run := func(t *testing.T, secretStorage bool) string {
		t.Helper()
		capture := filepath.Join(t.TempDir(), "init.jsonl")
		t.Setenv("INIT_CAPTURE", capture)
		b := New(scriptPath, dir)
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "m", SecretStorage: secretStorage}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("read init capture: %v", err)
		}
		return string(data)
	}

	t.Run("a store present declares the capability", func(t *testing.T) {
		got := run(t, true)
		if !strings.Contains(got, `"secretStorage":true`) {
			t.Errorf("initialize omitted the secret-storage opt-in with a store present; got: %s", got)
		}
	})

	t.Run("no store declares it false", func(t *testing.T) {
		got := run(t, false)
		if strings.Contains(got, `"secretStorage":true`) {
			t.Errorf("initialize declared secret storage with NO store behind it; KAS will rethrow the -32603 from every store into its MCP connect path. got: %s", got)
		}
		if !strings.Contains(got, `"secretStorage":false`) {
			t.Errorf("initialize should carry an explicit secretStorage:false; got: %s", got)
		}
		// The rest of the handshake must be unaffected: this gates one
		// capability, not the whole _meta.kiro block.
		if !strings.Contains(got, `"openExternalUrl":true`) || !strings.Contains(got, `"knowledge":true`) {
			t.Errorf("initialize lost unrelated base capabilities; got: %s", got)
		}
	})
}
