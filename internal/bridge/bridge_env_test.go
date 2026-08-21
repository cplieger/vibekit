package bridge

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// envNames pulls the NAMEs out of a composed environment so a case can assert on
// membership without restating every value.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			name = kv
		}
		out = append(out, name)
	}
	return out
}

// The security half: a credential-shaped name in the inherited environment does
// not reach the spawn.
//
// Cases are one per credential FAMILY rather than one per name, because the
// mistake that matters is a family the rules do not reach at all. The
// enumeration of the individual spellings the decision names is the next test,
// where it is an assertion rather than the map compared against itself.
func TestScreenBridgeEnv_DropsCredentialShapedNames(t *testing.T) {
	cases := map[string]struct{ name, value string }{
		"forge token":            {"GITHUB_TOKEN", "ghp_live"},
		"cloud key id":           {"AWS_ACCESS_KEY_ID", "AKIAEXAMPLE"},
		"cloud secret":           {"AWS_SECRET_ACCESS_KEY", "wJalr"},
		"package registry":       {"NPM_TOKEN", "npm_live"},
		"oauth client secret":    {"OIDC_CLIENT_SECRET", "s3cr3t"},
		"an unforeseen spelling": {"SOME_VENDOR_API_TOKEN", "tok"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env, dropped := screenBridgeEnv([]string{c.name + "=" + c.value}, nil, nil)
			if len(env) != 0 {
				t.Errorf("env = %v, want the credential dropped", env)
			}
			if !slices.Equal(dropped, []string{c.name}) {
				t.Errorf("dropped = %v, want [%s]", dropped, c.name)
			}
			// The value is what must not travel. Assert on it directly rather
			// than only on the name, so a future "mask the value" shortcut
			// that still passes the variable through fails here.
			for _, kv := range env {
				if strings.Contains(kv, c.value) {
					t.Errorf("value survived in %q", kv)
				}
			}
		})
	}
}

// The enumeration D96 names, asserted one spelling at a time. The suffix rules
// are what actually catch most of them, so this is the test that would fail if a
// rule were narrowed to an exact list and a name were forgotten.
func TestScreenBridgeEnv_DropsEveryNameTheDecisionNames(t *testing.T) {
	named := []string{
		"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "GITEA_SERVER_TOKEN", "NPM_TOKEN",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN",
	}
	for _, name := range named {
		t.Run(name, func(t *testing.T) {
			env, dropped := screenBridgeEnv([]string{name + "=x"}, nil, nil)
			if len(env) != 0 || len(dropped) != 1 {
				t.Errorf("%s: env = %v, dropped = %v, want dropped", name, env, dropped)
			}
		})
	}
}

// The usability half, and it is the half that decides whether the guard survives
// contact with the work: the agent's children are compilers, package managers,
// linkers and git, so a screen that took an ordinary build variable would be
// switched off rather than corrected.
func TestScreenBridgeEnv_KeepsTheAmbientBuildEnvironment(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin", "HOME=/config/home", "TERM=xterm", "LANG=C.UTF-8", "TZ=UTC",
		"CGO_ENABLED=1", "GOFLAGS=-mod=mod", "GOMODCACHE=/config/go/pkg/mod",
		"CARGO_HOME=/config/cargo", "npm_config_registry=https://registry.npmjs.org",
		"AWS_REGION=eu-west-1", "AWS_PROFILE=default",
		// Names that merely CONTAIN a credential word. The rules are suffix and
		// exact-match, so none of these is a credential and none may be dropped.
		"TOKEN_BUCKET_SIZE=64", "SECRET_DIR=/run/secrets", "TOKENIZER=bpe",
		"AWS_DEFAULT_REGION=eu-west-1", "SSH_AUTH_SOCK=/run/ssh-agent",
	}
	env, dropped := screenBridgeEnv(inherited, nil, nil)
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want nothing", dropped)
	}
	if !slices.Equal(env, inherited) {
		t.Errorf("env = %v, want the inherited environment unchanged", env)
	}
}

// A name that is nothing but the suffix reaches no consumer, so firing on it
// would be the one case where the rule drops a variable and protects nothing.
func TestScreenBridgeEnv_BareSuffixIsNotACredential(t *testing.T) {
	for _, name := range []string{"_TOKEN", "_SECRET"} {
		env, dropped := screenBridgeEnv([]string{name + "=x"}, nil, nil)
		if len(dropped) != 0 || len(env) != 1 {
			t.Errorf("%s: env = %v, dropped = %v, want kept", name, env, dropped)
		}
	}
}

// The overlay is vibekit's own, built in this process rather than inherited, and
// os/exec keeps the LAST value for a repeated key. Filtering it could silently
// drop an entry this server deliberately set and leave PATH resolving out of the
// wrong install, so the screen must not touch it — including when it carries a
// name the inherited half would lose.
func TestScreenBridgeEnv_OverlayIsExemptAndStaysLast(t *testing.T) {
	env, dropped := screenBridgeEnv(
		[]string{"PATH=/usr/bin", "GITHUB_TOKEN=leak"},
		[]string{"PATH=/config/tools/kiro-cli-versions/2.18.1:/usr/bin", "GH_TOKEN=deliberate"},
		nil,
	)
	if !slices.Equal(dropped, []string{"GITHUB_TOKEN"}) {
		t.Errorf("dropped = %v, want only the inherited credential", dropped)
	}
	want := []string{
		"PATH=/usr/bin",
		"PATH=/config/tools/kiro-cli-versions/2.18.1:/usr/bin",
		"GH_TOKEN=deliberate",
	}
	if !slices.Equal(env, want) {
		t.Errorf("env = %v, want %v (overlay unfiltered and last)", env, want)
	}
}

// The overlay is appended whether or not anything was inherited. A server whose
// inherited environment is empty still has to receive vibekit's own overlay,
// which is what puts the active install's directory at the front of PATH.
func TestScreenBridgeEnv_OverlayLandsWithNothingInherited(t *testing.T) {
	overlay := []string{"PATH=/config/tools/kiro-cli-versions/2.18.1", "VIBEKIT_HOME=/config"}
	env, dropped := screenBridgeEnv(nil, overlay, nil)
	if !slices.Equal(env, overlay) {
		t.Errorf("screenBridgeEnv(nil, %v, nil) env = %v, want %v", overlay, env, overlay)
	}
	if len(dropped) != 0 {
		t.Errorf("screenBridgeEnv(nil, %v, nil) dropped = %v, want nothing", overlay, dropped)
	}
}

// The override, which is what keeps a false positive from being a reason to
// disable the whole screen.
func TestScreenBridgeEnv_OperatorOverridePassesTheNameThrough(t *testing.T) {
	inherited := []string{"BUILDKITE_AGENT_TOKEN=needed", "GITHUB_TOKEN=leak"}
	env, dropped := screenBridgeEnv(inherited, nil, ParseEnvAllowlist("BUILDKITE_AGENT_TOKEN"))
	if !slices.Equal(envNames(env), []string{"BUILDKITE_AGENT_TOKEN"}) {
		t.Errorf("env = %v, want only the allowed name", env)
	}
	if !slices.Equal(dropped, []string{"GITHUB_TOKEN"}) {
		t.Errorf("dropped = %v, want the unallowed credential", dropped)
	}
}

func TestParseEnvAllowlist(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"empty":              {"", nil},
		"blank":              {"   ", nil},
		"all separators":     {" , , ", nil},
		"single":             {"GH_TOKEN", []string{"GH_TOKEN"}},
		"several with space": {"GH_TOKEN, NPM_TOKEN ,X_SECRET", []string{"GH_TOKEN", "NPM_TOKEN", "X_SECRET"}},
		"trailing separator": {"GH_TOKEN,", []string{"GH_TOKEN"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := ParseEnvAllowlist(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("ParseEnvAllowlist(%q) has %d entries, want %d", c.raw, len(got), len(c.want))
			}
			for _, w := range c.want {
				if _, ok := got[w]; !ok {
					t.Errorf("ParseEnvAllowlist(%q) missing %q", c.raw, w)
				}
			}
		})
	}
}

// A variable with no `=` cannot be an assignment, but os.Environ has been
// observed to carry one on exotic platforms and the screen must not index past
// the end of the string classifying it.
func TestScreenBridgeEnv_MalformedEntryIsKeptWhole(t *testing.T) {
	env, dropped := screenBridgeEnv([]string{"NOTANASSIGNMENT", "ALSO_A_TOKEN"}, nil, nil)
	if !slices.Equal(env, []string{"NOTANASSIGNMENT"}) {
		t.Errorf("env = %v, want the non-assignment kept", env)
	}
	if !slices.Equal(dropped, []string{"ALSO_A_TOKEN"}) {
		t.Errorf("dropped = %v, want the credential-shaped bare name", dropped)
	}
}

// FuzzScreenBridgeEnv pins the invariant the screen exists for, on arbitrary
// input: nothing whose NAME is credential-shaped survives into the composed
// environment, and nothing else is lost.
func FuzzScreenBridgeEnv(f *testing.F) {
	f.Add("PATH=/usr/bin\nGITHUB_TOKEN=ghp\nHOME=/config/home")
	f.Add("AWS_SECRET_ACCESS_KEY=x\nAWS_REGION=eu-west-1")
	f.Add("=leading\n_TOKEN=bare\nA_SECRET=y")
	f.Add("no-equals\nGOFLAGS=-mod=mod")
	f.Fuzz(func(t *testing.T, raw string) {
		inherited := strings.Split(raw, "\n")
		env, dropped := screenBridgeEnv(inherited, nil, nil)
		if len(env)+len(dropped) != len(inherited) {
			t.Fatalf("accounting: %d kept + %d dropped != %d inherited", len(env), len(dropped), len(inherited))
		}
		for _, kv := range env {
			name, _, ok := strings.Cut(kv, "=")
			if !ok {
				name = kv
			}
			if isCredentialEnv(name, nil) {
				t.Errorf("credential-shaped name %q survived", name)
			}
		}
		for _, name := range dropped {
			if !isCredentialEnv(name, nil) {
				t.Errorf("dropped %q, which is not credential-shaped", name)
			}
		}
	})
}

// envDumpFake writes a fake kiro-cli that records the environment it was spawned
// with before answering the handshake, so a test can assert on what the child
// actually received rather than on what the composer returned.
func envDumpFake(t *testing.T, dir, dumpPath string) string {
	t.Helper()
	script := `#!/bin/sh
env > "` + dumpPath + `"
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-env"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "env-dump-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return scriptPath
}

// The screen holds through a real spawn, and the operator is told which names it
// took away. Both halves matter: the unit cases above prove the composer's
// answer, and this proves the spawn uses it — a screen applied to a value the
// spawn then ignored would pass every one of them. The log is the only notice an
// operator gets that a variable they set never reached the agent, so a silent
// drop is a support call about a tool that "doesn't authenticate".
//
// Not parallel: it sets an environment variable and swaps the slog default.
func TestStart_ScreensCredentialsOutOfTheSpawnAndNamesThem(t *testing.T) {
	const probe = "VIBEKIT_SPAWN_PROBE_TOKEN"
	t.Setenv(probe, "shh")

	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "child.env")
	scriptPath := envDumpFake(t, dir, dumpPath)

	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &vibekit.StartOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read the child environment dump: %v", err)
	}
	if strings.Contains(string(dump), probe) {
		t.Errorf("%s reached the kiro-cli environment; the spawn does not apply the credential screen", probe)
	}
	if !strings.Contains(logs.String(), probe) {
		t.Errorf("the spawn dropped %s without naming it in the log:\n%s", probe, logs.String())
	}
}
