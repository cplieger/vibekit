package composition

import (
	"context"
	"log/slog"
	"os"

	"github.com/cplieger/pinstall"
	"github.com/cplieger/pinstall/kirocli"
)

// The layout facts vibekit brings to the install: where the convenience symlink
// goes, and what its own SHELL-era installer left on the volume.
const (
	// kiroLinkDir is the directory under the tools dir holding the
	// non-authoritative `docker exec … kiro-cli` convenience symlink. It is
	// co-owned by the toolbelt engine, which publishes bin/<tool> symlinks of
	// its own — which is why the legacy sweep below names its targets instead of
	// scanning it.
	kiroLinkDir = "bin"
	// legacyStagePrefix prefixed the shell installer's staging trees directly
	// under the tools dir. The managed staging trees live under the install root
	// instead, so anything matching this is an orphan its EXIT trap missed on a
	// SIGKILL. It ends in a dot so it cannot match the install root or the
	// marker below.
	legacyStagePrefix = ".kiro-cli-stage."
	// legacyPurgeMarker records on the volume that the one-time migration sweep
	// completed, so it runs ONCE rather than walking the co-owned bin directory
	// on every boot. Dot-prefixed and directly under the tools dir, where the
	// toolbelt engine never looks (it enumerates only bin/, opt/, npm/ and
	// python/).
	legacyPurgeMarker = ".kiro-cli-legacy-purged"
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
	// ready is the /api/health verdict plus the library's TYPED reason, or nil
	// when this app does not own the install (a bare `go run` with no pins) and
	// readiness stays pure-listener. The wording an operator and the browser
	// banner read is vibekit's own, applied at the HTTP boundary
	// (internal/server's kiroReasonText).
	ready func() (bool, pinstall.Reason)
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
		cliPath: func() string { return kirocli.Name },
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
		ready:   func() (bool, pinstall.Reason) { return false, pinstall.ReasonUnavailable },
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
	mgr, err := pinstall.New(kiroInstallConfig(cfg))
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
		cliPath: mgr.Path,
		env:     func() []string { return kiroPathEnv(mgr.PathEntry()) },
		ready:   mgr.Ready,
		rescan:  mgr.Rescan,
		stop:    cancel,
	}
}

// kiroInstallConfig is vibekit's whole deployment of the kiro-cli release: the
// pins from the entrypoint, the tools tree, and the local policy. The release
// PROFILE — the archive URL, the arch tokens, the in-archive installer, the probe
// argv, the licence notice and the mandatory auto-update assertion — is
// kirocli.Release()'s, shared with every other consumer of the same upstream.
//
// It is a function rather than an inline literal so the namespace test can build
// a manager from the EXACT configuration production runs (see
// kirocli_namespace_test.go): the collision it guards is a property of these
// values, not of a copy of them.
func kiroInstallConfig(cfg *Config) *pinstall.Config {
	return &pinstall.Config{
		Release: kirocli.Release(),
		Version: cfg.KiroCLIVersion,
		// Both pins travel, whatever this container runs on; the library
		// validates the digest for the resolved GOARCH and ignores the other.
		Digests: map[string]string{
			"amd64": cfg.KiroCLISHA256,
			"arm64": cfg.KiroCLISHA256ARM64,
		},
		Root:    cfg.ToolsDir,
		LinkDir: kiroLinkDir,
		// Require is deliberately UNSET, and that is not an omission: the
		// library always requires the release's primary artifact, and for
		// vibekit that IS the whole required set. `kiro-cli acp` — the only
		// invocation a chat bridge makes — is served by the main dispatcher, and
		// no Go path here invokes `chat`, so a version directory with no sidecar
		// is a COMPLETE install for this app. web-terminal-kiro requires the
		// chat sidecar because `kiro-cli chat` IS its product; do not copy that
		// set here without a Go caller that needs it.
		Optional: []string{kirocli.Name + "-chat", kirocli.Name + "-term"},
		Assert:   kiroSettings(),
		Purge:    kiroLegacyPurge(),
		// Untrusted is deliberately left unset: it records that the install root
		// was found writable by others, and vibekit has no hardening pass
		// (web-terminal-kiro's secure_tools_dir) that could make that
		// observation. Claiming it here would be a guard with no producer, which
		// reports every boot as clean while looking like a check;
		// tests/shell/pins_export_test.sh asserts neither side claims it.
	}
}

// kiroLegacyPurge describes the layout VIBEKIT's shell installer left on the
// tools volume, which is caller data: the residue is a fact about this app's
// history, not about the kiro-cli release. It is the promoted dispatchers in
// $TOOLS/bin and the orphan staging trees, and nothing else.
//
// There is no journal, no `.prev` backup, no `.absent` tombstone and no
// install/readiness marker in the list, and that is not an omission either:
// vibekit never wrote them. Its promotion was single-commit-point by design (the
// main binary renamed LAST, the drift check keyed on its version), so the
// in-place transaction web-terminal-kiro needed — and the artifacts it left on a
// volume — never existed here. Do not copy that app's larger list back.
//
// The dispatcher names come from the library profile rather than a local slice:
// they are the set a shell-era kiro-cli installer promoted, which is release
// knowledge. Naming three targets is also what makes the sweep safe in a
// directory the toolbelt engine co-owns — a `kiro-cli*` prefix sweep deleted
// every match, including another owner's live symlink.
func kiroLegacyPurge() *pinstall.Purge {
	return &pinstall.Purge{
		Names:       kirocli.ShellEraDispatchers(),
		StagePrefix: legacyStagePrefix,
		Marker:      legacyPurgeMarker,
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

// kiroSettings is vibekit's kiro-cli settings set, re-asserted against the
// active binary on every boot. These are the ten calls entrypoint.sh used to
// make after the install; they moved here with the installer, because the shell
// block was gated on the binary already being present and that gate is false on
// every first boot once the install runs after exec.
//
// app.disableAutoupdates is deliberately NOT in this list: kirocli.Release()
// declares it Mandatory, so the library forces it Required and merges it in
// whatever a deployment passes — the integrity gate cannot be weakened, reworded
// or dropped from here. Every assertion here is best-effort: a failure warns and
// readiness is unaffected. Rationale for each value is in the Settings docs
// (Settings → General surfaces the same keys through /api/kiro-settings).
func kiroSettings() []pinstall.Assertion {
	return []pinstall.Assertion{
		// Features vibekit renders natively.
		kirocli.Setting("chat.enableTodoList", true),
		kirocli.Setting("chat.enableKnowledge", true),
		kirocli.Setting("chat.enableSubagent", true),
		kirocli.Setting("chat.enablePromptHints", true),
		kirocli.Setting("hooks.showStatus", true),
		// Off: duplicates of vibekit's own systems, telemetry, and the two
		// toggles seeded only so the Settings UI reflects reality instead of an
		// unset-means-on fallback.
		kirocli.Setting("chat.enableCheckpoint", false),
		kirocli.Setting("telemetry.enabled", false),
		kirocli.Setting("toolSearch.enabled", false),
		kirocli.Setting("chat.disableInheritingDefaultResources", false),
		// vibekit owns chat retention end to end (its own chat_retention_days),
		// so kiro-cli's competing purge is pinned off: 0 = never. Raw because
		// the value is not a boolean.
		kirocli.SettingRaw("cleanup.periodDays", "0"),
	}
}
