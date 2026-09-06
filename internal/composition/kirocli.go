package composition

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/pinstall/v3/kirocli"
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
	// installed is closed once a version is ACTIVE, and nil when no install can
	// ever complete (no pins, or pins the manager refused). It exists for the boot
	// work that needs a kiro-cli and must not block the listener on one: a utility
	// bridge cannot start before this closes, so anything driven by an RPC at boot
	// either happens after it or does not happen at all. The orphan sweep is the
	// one such caller today (composition.go startOrphanSweep), and it is why the
	// channel reports SUCCESS rather than "the installer stopped trying": a
	// retry after an exhausted install would ask a bridge that still cannot start.
	//
	// A channel rather than the manager's own Ready(), which is a POLL — nothing
	// wakes on it, so a consumer would have to invent an interval and a ceiling for
	// an event the installer already knows the instant it happens.
	installed <-chan struct{}
	// stop cancels the background install AND waits for it to finish, so a caller
	// that reshapes or removes the tools tree afterwards cannot race the
	// installer. Bare cancellation would return while EnsureWithRetry was still
	// mid-attempt: the manager writes its state record and creates directories
	// under ToolsDir on the way out of a cancelled attempt, which is invisible in
	// production (nothing deletes that tree at shutdown) but makes any test that
	// hands it a t.TempDir intermittently fail its own cleanup with "directory not
	// empty".
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
	done := make(chan struct{})
	installed := make(chan struct{})
	go func() {
		defer close(done)
		// The error is not ACTED on here — EnsureWithRetry has already logged it
		// with the attempt count and the in-container repair hint, and the server
		// stays up either way — but it is READ, because it is the one signal that
		// separates "a version is active" from "the installer gave up", and the
		// boot work waiting on `installed` needs a kiro-cli rather than an ending.
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
			// Bounded, because stop runs on the shutdown path: EnsureWithRetry
			// honours cancellation promptly (its retry wait and its HTTP fetch both
			// select on the context), so this returns in milliseconds in practice.
			// The timeout exists so a library that ever stopped honouring it could
			// not wedge shutdown — a slow exit beats a hung one, and beats a
			// silently unbounded wait either way.
			select {
			case <-done:
			case <-time.After(kiroStopGrace):
				slog.Warn("the kiro-cli installer did not stop within the shutdown grace; continuing",
					"grace", kiroStopGrace)
			}
		},
	}
}

// kiroStopGrace bounds how long shutdown waits for the background install to
// notice cancellation. Generous relative to the milliseconds it actually takes,
// because expiring it means giving up on a guarantee rather than merely waiting.
const kiroStopGrace = 5 * time.Second

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
		// Require names the chat sidecar because `kiro-cli acp` IS the sidecar.
		//
		// This corrects a load-bearing wrong belief. The previous comment here
		// said `acp` "is served by the main dispatcher, and no Go path here
		// invokes `chat`, so a version directory with no sidecar is a COMPLETE
		// install for this app". Both halves are false, measured against the
		// pinned 2.16.0 install:
		//
		//	$ kiro-cli acp --help
		//	Usage: kiro-cli-chat acp [OPTIONS]      <- not this binary
		//	$ env -i PATH=<dir with ONLY kiro-cli> kiro-cli acp --help
		//	error: No such file or directory (os error 2)
		//
		// kiro-cli is a multi-call binary and `acp` re-execs a SIBLING, resolved
		// by a plain PATH search (not relative to its own executable). So the Go
		// caller the old comment asked for does exist: every chat bridge is
		// `bridge.startProcess` running `kiro-cli acp`, which is an invocation of
		// this sidecar by another name. pinstall's Manager.PathEnv prepending the
		// version directory is what makes that search land inside the verified
		// install.
		//
		// Why this matters more than a stale comment: `--version` is answered by
		// the MAIN binary, so a sidecar-less directory passed the boot probe,
		// was published `.complete`, reported READY on /api/health and in the
		// browser banner, and then failed at EVERY chat spawn. Requiring the
		// sidecar makes such a directory not a selection candidate, which is the
		// designed behaviour: readiness is withheld WITH A REASON and the
		// install retries. It does not abort boot, so invariant 6 is untouched.
		//
		// -term stays Optional: nothing in this repo invokes it, and unlike
		// `acp` no subcommand vibekit uses re-execs it.
		Require:  []string{kirocli.Name + "-chat"},
		Optional: []string{kirocli.Name + "-term"},
		Assert:   kiroSettings(),
		Purge:    kiroLegacyPurge(),
		// Untrusted is deliberately left unset: it records that the install root
		// was found writable by others, and vibekit has no hardening pass
		// (web-terminal-kiro's secure_tools_dir) that could make that
		// observation. Claiming it here would be a guard with no producer, which
		// reports every boot as clean while looking like a check;
		// tests/shell/pins_export_test.sh asserts neither side claims it.
		//
		// TrustedUIDs is a different kind of statement and IS vibekit's to make:
		// not an observation about what the tree turned out to be, but a fact
		// about who the volume's access-control list names. Empty by default,
		// so the custody check is fully enforcing unless an operator names a
		// uid; see parseTrustedInstallUIDs for what setting one asserts.
		TrustedUIDs: cfg.TrustedInstallUIDs,
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

// kiroSettings is vibekit's kiro-cli settings set, re-asserted against the
// active binary on every boot. These moved here from entrypoint.sh with the
// installer, because the shell block was gated on the binary already being
// present and that gate is false on every first boot once the install runs
// after exec.
//
// A key belongs here only if it has a kiro-cli-SIDE role, and that qualifier is
// load-bearing rather than descriptive: KAS's ACP path reads no kiro-cli setting
// at all (measured on the stock 2.19.2 bundle — see internal/server/cli.go's
// allowlist comment for the counts), so a seed here reaches the TUI, the
// knowledge index and vibekit's own suppression logic, never a vibekit chat.
//
// app.disableAutoupdates is deliberately NOT in this list: kirocli.Release()
// declares it Mandatory, so the library forces it Required and merges it in
// whatever a deployment passes — the integrity gate cannot be weakened, reworded
// or dropped from here. Every assertion here is best-effort: a failure warns and
// readiness is unaffected.
//
// Three seeds were REMOVED in 2026-08 and each is worth naming, because deleting
// a seed changes what a hand-run `docker exec … kiro-cli chat` session sees:
//
//   - chat.enableTodoList and chat.enableCheckpoint. Their ACP counterparts
//     (`todoList`, `checkpoint`) are declared in KAS's settings schema with ZERO
//     readers in any of its three reader shapes, so neither key could change a
//     chat through either door. Both were seeded only so the Settings toggles
//     rendered against a real value; the toggles are gone with them.
//   - toolSearch.enabled. Its ACP counterpart `toolSearch` IS read, so this one
//     was not inert — it was pointed at the wrong door. The control now drives
//     the ACP key through kascap's gate, and seeding the kiro-cli key beside it
//     would leave two writers for one user-visible switch.
//
// chat.enableSubagent is deliberately KEPT even though its ACP sibling
// `_subagent` has no reader either. The LIVE key is `subagentOrchestration`,
// which kascap already sends, so behaviour is correct today and dropping the
// seed would only change what the TUI does. It is not a vibekit toggle.
func kiroSettings() []pinstall.Assertion {
	return []pinstall.Assertion{
		// Features vibekit renders natively.
		kirocli.Setting("chat.enableKnowledge", true),
		kirocli.Setting("chat.enableSubagent", true),
		kirocli.Setting("chat.enablePromptHints", true),
		kirocli.Setting("hooks.showStatus", true),
		// Off: telemetry, and the resource-inheritance switch seeded so the
		// Settings UI reflects reality instead of an unset-means-on fallback.
		kirocli.Setting("telemetry.enabled", false),
		kirocli.Setting("chat.disableInheritingDefaultResources", false),
		// vibekit owns chat retention end to end (its own chat_retention_days),
		// so kiro-cli's competing purge is pinned off: 0 = never. Raw because
		// the value is not a boolean.
		kirocli.SettingRaw("cleanup.periodDays", "0"),
	}
}
