package composition

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	"github.com/cplieger/vibekit/internal/kirocli"
)

// kiroRuntime is the running kiro-cli subsystem the rest of the wiring consumes.
// Every field is a FUNCTION because the answers change while the server runs:
// the install completes after the listener binds, so a path or an environment
// captured at construction would freeze the first (empty) answer forever.
type kiroRuntime struct {
	// cliPath resolves the absolute path of the active kiro-cli, or "" when no
	// version is active. Every consumer calls it per use: the bridge factory
	// once per chat bridge, the auth shell-outs and the CLI runner per request.
	cliPath func() string
	// env is the environment overlay for a spawned kiro-cli, or nil when there
	// is nothing to add.
	env func() []string
	// ready is the /api/health verdict plus its reason, or nil when this app
	// does not own the install (a bare `go run` with no pins) and readiness
	// stays pure-listener.
	ready func() (bool, string)
	// rescan re-derives the active version from disk without downloading, or nil
	// when there is no manager. It backs the loopback repair hook.
	rescan func(context.Context) (bool, error)
	// stop cancels the background install.
	stop func()
}

// unmanagedKiroRuntime is the runtime for a process with no pins in its
// environment: a bare `go run` outside the container. kiro-cli is resolved by
// bare name through the developer's own PATH, there is no environment overlay,
// and no readiness gate (nil ready), so /api/health reflects only that the
// listener is up. In the image entrypoint.sh always exports the pins, so this
// shape is unreachable there.
func unmanagedKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return "kiro-cli" },
		// Non-nil on purpose: cliPath, env and stop are called unconditionally
		// (the bridge factory calls env() on every spawn), so only ready and
		// rescan may be nil, and their nil-ness is what MEANS "no manager".
		env:  func() []string { return nil },
		stop: func() {},
	}
}

// unavailableKiroRuntime is the runtime for a container that CANNOT install
// kiro-cli: the pins it was handed are unusable, so no version can ever be
// activated. It reports unready rather than pretending, which surfaces the fault
// on /api/health instead of letting every chat fail one at a time. Degraded,
// never fatal: the UI, the diagnostics and the `docker exec` repair path stay
// alive, per invariant 6.
func unavailableKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return "" },
		env:     func() []string { return nil },
		ready:   func() (bool, string) { return false, kirocli.ReasonUnavailable },
		stop:    func() {},
	}
}

// startKiroCLI builds the kiro-cli install manager and starts the install in the
// background, bind-first: the listener comes up immediately and only READINESS
// waits, the same shape the toolbelt boot reconcile uses. A first-boot download
// therefore answers /api/health with a reason instead of refusing connections,
// and the UI, the diagnostics page and the loopback repair hook stay reachable
// throughout.
//
// Three shapes come out of it: no pins at all (a bare `go run` outside the
// container), pins the manager cannot use (unready, so the fault is reported
// rather than hidden), and the managed install. There is no operator input that
// selects among them and no way to stand the manager down: inside the container
// the pins are always exported, so a managed install is the only kiro-cli vibekit
// ever runs, and the manager is the only source of its path.
func startKiroCLI(ctx context.Context, cfg *Config) kiroRuntime {
	if cfg.KiroCLIVersion == "" || cfg.ToolsDir == "" {
		slog.Warn("no kiro-cli pins in the environment: resolving kiro-cli by bare name and installing nothing",
			"hint", "expected outside the container (bare `go run`); in the image entrypoint.sh exports KIRO_CLI_VERSION and both digests")
		return unmanagedKiroRuntime()
	}
	mgr, err := kirocli.New(&kirocli.Config{
		Version:     cfg.KiroCLIVersion,
		SHA256:      cfg.KiroCLISHA256,
		SHA256ARM64: cfg.KiroCLISHA256ARM64,
		ToolsDir:    cfg.ToolsDir,
		// vibekit's required set is the main dispatcher alone (kirocli's default):
		// `kiro-cli acp` is served by it and no Go path here invokes `chat`, so
		// both sidecars are installed when present and only warn when absent.
		Optional: []string{"kiro-cli-chat", "kiro-cli-term"},
		Settings: kiroSettings(),
	})
	if err != nil {
		slog.Error("kiro-cli install manager could not be built from the exported pins; no version can be installed, so chats stay unavailable",
			"error", err,
			"hint", "this is an image defect: check the KIRO_CLI_VERSION / KIRO_CLI_SHA256 / KIRO_CLI_SHA256_ARM64 literals in entrypoint.sh")
		return unavailableKiroRuntime()
	}
	ensureCtx, cancel := context.WithCancel(ctx)
	go func() {
		// The error is already logged by EnsureWithRetry, with the attempt count
		// and the in-container repair hint, and there is nothing here that could
		// act on it: the server stays up either way.
		_ = mgr.EnsureWithRetry(ensureCtx)
	}()
	return kiroRuntime{
		cliPath: mgr.CLIPath,
		env:     func() []string { return kiroPathEnv(mgr.PathEntry()) },
		// Ready, not Phase, is the authority: Phase only explains a "no".
		ready:  mgr.Ready,
		rescan: mgr.Rescan,
		stop:   cancel,
	}
}

// kiroPathEnv returns the environment overlay that puts the active kiro-cli
// version directory FIRST on PATH, or nil when no version is active.
//
// Leading is the point, not a detail. That directory holds only kiro-cli's own
// dispatchers, so it shadows nothing else, while $TOOLS/bin is co-owned by the
// toolbelt engine and $TOOLS/go/bin is GOPATH/bin, where a `go install` can land
// anything — including a stale kiro-cli from a restored backup volume. With the
// version directory first, a kiro-cli that looks for a sibling of its own
// executable and one that looks for a bare name on PATH both land inside the
// same verified install.
func kiroPathEnv(entry string) []string {
	if entry == "" {
		return nil
	}
	return []string{"PATH=" + entry + string(os.PathListSeparator) + os.Getenv("PATH")}
}

// kiroSettings is vibekit's kiro-cli settings set, applied against the active
// binary on every boot. These are the ten calls entrypoint.sh used to make after
// the install; they moved here with the installer, because the shell block was
// gated on the binary already being present and that gate is false on every
// first boot once the install runs after exec.
//
// app.disableAutoupdates is deliberately NOT in this list: the manager adds it
// itself as REQUIRED, so no caller can configure the integrity gate away. Every
// setting here is best-effort — a failure warns and readiness is unaffected.
// Rationale for each value is in the Settings docs (Settings -> General surfaces
// the same keys through /api/kiro-settings).
func kiroSettings() []kirocli.Setting {
	return []kirocli.Setting{
		// Features vibekit renders natively.
		boolSetting("chat.enableTodoList", true),
		boolSetting("chat.enableKnowledge", true),
		boolSetting("chat.enableSubagent", true),
		boolSetting("chat.enablePromptHints", true),
		boolSetting("hooks.showStatus", true),
		// Off: duplicates of vibekit's own systems, telemetry, and the two
		// toggles seeded only so the Settings UI reflects reality instead of an
		// unset-means-on fallback.
		boolSetting("chat.enableCheckpoint", false),
		boolSetting("telemetry.enabled", false),
		boolSetting("toolSearch.enabled", false),
		boolSetting("chat.disableInheritingDefaultResources", false),
		// vibekit owns chat retention end to end (its own chat_retention_days),
		// so kiro-cli's competing purge is pinned off: 0 = never.
		{Key: "cleanup.periodDays", Value: "0"},
	}
}

// boolSetting is one boolean kiro-cli setting. The settings CLI takes its value
// as a string, so keeping the app's intent as a Go bool lets the compiler catch
// a typo the CLI would silently accept as "not true".
func boolSetting(key string, on bool) kirocli.Setting {
	return kirocli.Setting{Key: key, Value: strconv.FormatBool(on)}
}
