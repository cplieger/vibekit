package composition

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/pinstall/v3/kirocli"
)

// The layout facts vibekit brings to the install: where the convenience symlink goes, and
// what its own SHELL-era installer left on the volume.
const (
	// kiroLinkDir holds the non-authoritative `docker exec … kiro-cli` symlink. Co-owned
	// by the toolbelt engine, which is why the legacy sweep names its targets.
	kiroLinkDir = "bin"
	// legacyStagePrefix prefixed the shell installer's staging trees, so a match is an
	// orphan its EXIT trap missed. Ends in a dot so it cannot match the install root.
	legacyStagePrefix = ".kiro-cli-stage."
	// legacyPurgeMarker records that the one-time migration sweep ran, so it does not walk
	// the co-owned bin directory every boot. Where the toolbelt engine never looks.
	legacyPurgeMarker = ".kiro-cli-legacy-purged"
)

// kiroRuntime is the running kiro-cli subsystem the rest of the wiring consumes. Every
// field is a FUNCTION because the install completes after the listener binds, so a path or
// an environment captured at construction would freeze the first empty answer forever.
type kiroRuntime struct {
	// cliPath resolves the active kiro-cli's absolute path, or "" when no version is
	// active. Called per use, never captured.
	cliPath func() string
	// env is the environment overlay for a spawned kiro-cli, nil when there is none.
	env func() []string
	// ready is the /api/health verdict plus the library's TYPED reason, or nil when this
	// app does not own the install and readiness stays pure-listener. The wording an
	// operator reads is applied at the HTTP boundary.
	ready func() (bool, pinstall.Reason)
	// rescan re-derives the active version from disk without downloading, or nil when there
	// is no manager. It backs the loopback repair hook.
	rescan func(context.Context) (bool, error)
	// installed is closed once a version is ACTIVE, nil when no install can ever complete.
	// It reports SUCCESS rather than "the installer stopped trying", because its consumers
	// need a kiro-cli: a utility bridge cannot start before it closes. A channel rather
	// than the manager's POLL, which nothing can wake on.
	installed <-chan struct{}
	// stop cancels the background install AND waits, so a caller that reshapes the tools
	// tree afterwards cannot race the installer — a cancelled attempt still writes its
	// state record and creates directories on the way out, which fails a t.TempDir cleanup
	// with "directory not empty".
	stop func()
}

// unmanagedKiroRuntime is the runtime for a process with no pins: a bare `go run` outside
// the container. kiro-cli resolves by bare name through the developer's own PATH and there
// is no readiness gate, so /api/health reflects only that the listener is up.
func unmanagedKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return kirocli.Name },
		// Non-nil on purpose: cliPath, env and stop are called unconditionally, so only
		// ready and rescan may be nil, and their nil-ness is what MEANS "no manager".
		env:  func() []string { return nil },
		stop: func() {},
	}
}

// unavailableKiroRuntime is the runtime for a container whose pins are unusable, so no
// version can ever activate. It reports unready rather than letting every chat fail one at a
// time — degraded, never fatal, so the repair paths stay alive (invariant 6).
func unavailableKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return "" },
		env:     func() []string { return nil },
		ready:   func() (bool, pinstall.Reason) { return false, pinstall.ReasonUnavailable },
		stop:    func() {},
	}
}

// startKiroCLI builds the install manager and starts the install in the background,
// bind-first: only READINESS waits, so a first-boot download answers /api/health with a
// reason instead of refusing connections.
//
// Three shapes come out: no pins at all, pins the manager cannot use (unready, so the fault
// is reported rather than hidden), and the managed install. No operator input selects among
// them, and inside the container the pins are always exported.
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
	done := make(chan struct{})
	installed := make(chan struct{})
	go func() {
		defer close(done)
		// Not ACTED on — EnsureWithRetry logged it and the server stays up either way — but
		// READ, because it is the one signal separating "a version is active" from "the
		// installer gave up", and the boot work waiting on `installed` needs the former.
		if err := mgr.EnsureWithRetry(ensureCtx); err == nil {
			close(installed)
		}
	}()
	return kiroRuntime{
		cliPath:   mgr.Path,
		env:       mgr.PathEnv,
		ready:     mgr.Ready,
		rescan:    mgr.Rescan,
		installed: installed,
		stop: func() {
			cancel()
			// Bounded because stop runs on the shutdown path: cancellation is honoured in
			// milliseconds, and the timeout only stops a library that ever stopped
			// honouring it from wedging shutdown.
			select {
			case <-done:
			case <-time.After(kiroStopGrace):
				slog.Warn("the kiro-cli installer did not stop within the shutdown grace; continuing",
					"grace", kiroStopGrace)
			}
		},
	}
}

// kiroStopGrace bounds how long shutdown waits for the background install to notice
// cancellation. Generous, because expiring it means giving up on a guarantee.
const kiroStopGrace = 5 * time.Second

// kiroInstallConfig is vibekit's whole deployment of the kiro-cli release: the pins, the
// tools tree, and the local policy. The release PROFILE is kirocli.Release()'s, shared with
// every other consumer of the same upstream. A function rather than an inline literal so the
// namespace test builds a manager from the EXACT configuration production runs — the
// collision it guards is a property of these values, not of a copy.
func kiroInstallConfig(cfg *Config) *pinstall.Config {
	return &pinstall.Config{
		Release: kirocli.Release(),
		Version: cfg.KiroCLIVersion,
		// Both pins travel whatever this container runs on; the library validates the digest
		// for the resolved GOARCH and ignores the other.
		Digests: map[string]string{
			"amd64": cfg.KiroCLISHA256,
			"arm64": cfg.KiroCLISHA256ARM64,
		},
		Root:    cfg.ToolsDir,
		LinkDir: kiroLinkDir,
		// Require names the chat sidecar because `kiro-cli acp` IS the sidecar: kiro-cli is
		// a multi-call binary and `acp` re-execs a sibling found by a plain PATH search, so
		// every chat bridge invokes it. `--version` is answered by the MAIN binary, so
		// without this a sidecar-less directory passed the boot probe, published
		// `.complete`, reported READY, and then failed at every chat spawn. -term stays
		// Optional: no subcommand vibekit uses re-execs it.
		Require:  []string{kirocli.Name + "-chat"},
		Optional: []string{kirocli.Name + "-term"},
		Assert:   kiroSettings(),
		Purge:    kiroLegacyPurge(),
		// Untrusted stays unset: it records that the install root was found writable by
		// others, and vibekit runs no hardening pass that could make that observation, so
		// claiming it would be a guard with no producer reporting every boot clean.
		// TrustedUIDs is a different kind of statement and IS vibekit's to make — a fact
		// about who the volume's ACL names. Empty by default, so custody fully enforces.
		TrustedUIDs: cfg.TrustedInstallUIDs,
	}
}

// kiroLegacyPurge describes the layout VIBEKIT's shell installer left on the tools volume,
// which is caller data: the promoted dispatchers and the orphan staging trees, nothing else.
// The absent journal, backup and tombstone entries are not an omission — vibekit never wrote
// them, so do not copy the sibling app's larger list back. Naming three targets rather than
// a `kiro-cli*` prefix is what makes the sweep safe in a directory the toolbelt engine
// co-owns, where a prefix sweep took another owner's live symlink.
func kiroLegacyPurge() *pinstall.Purge {
	return &pinstall.Purge{
		Names:       kirocli.ShellEraDispatchers(),
		StagePrefix: legacyStagePrefix,
		Marker:      legacyPurgeMarker,
	}
}

// kiroSettings is vibekit's kiro-cli settings set, re-asserted against the active binary on
// every boot rather than by entrypoint.sh, whose gate is false on every first boot.
//
// A key belongs here ONLY if it has a kiro-cli-SIDE role: KAS's ACP path reads no kiro-cli
// setting at all, so a seed reaches the TUI, the knowledge index and vibekit's own
// suppression logic, never a chat. app.disableAutoupdates is deliberately absent —
// kirocli.Release() declares it Mandatory and the library merges it in, so the integrity
// gate cannot be dropped from here. Every assertion is best-effort; a failure warns.
func kiroSettings() []pinstall.Assertion {
	return []pinstall.Assertion{
		// Features vibekit renders natively.
		kirocli.Setting("chat.enableKnowledge", true),
		kirocli.Setting("chat.enableSubagent", true),
		kirocli.Setting("chat.enablePromptHints", true),
		kirocli.Setting("hooks.showStatus", true),
		// Off: telemetry, and the resource-inheritance switch seeded so the Settings UI
		// reflects reality rather than an unset-means-on fallback.
		kirocli.Setting("telemetry.enabled", false),
		kirocli.Setting("chat.disableInheritingDefaultResources", false),
		// vibekit owns chat retention end to end, so kiro-cli's competing purge is pinned
		// off: 0 = never. Raw because the value is not a boolean.
		kirocli.SettingRaw("cleanup.periodDays", "0"),
	}
}
