// Package kirocli owns the whole kiro-cli lifecycle: it downloads the pinned
// archive, proves its SHA-256 against the pin, installs it into a
// version-addressed directory under $TOOLS/kiro-cli-versions, selects which
// installed version is active, reasserts the settings the pin depends on,
// prunes superseded versions, and purges the legacy $TOOLS/bin layout the
// shell installer left behind.
//
// The unit of installation is $TOOLS/kiro-cli-versions/<version>/. It is populated
// only from a verified archive, published by a single same-filesystem rename,
// and marked by a `.complete` sentinel written LAST — so an interrupted
// install is detectable by absence of the sentinel and never becomes a
// selection candidate. Nothing outside a container-local temp dir exists
// before the archive digest matches.
//
// Nothing here exits the process. Every failure is returned or logged so the
// HTTP surface and the `docker exec` repair path stay alive, which is this
// app's invariant 6: a broken install degrades readiness, it does not take the
// server down.
//
// vibekit's REQUIRED set has cardinality ONE. `kiro-cli acp` — the only
// invocation a chat bridge makes — is served by the main binary, and no Go path
// in this repo invokes `chat`, so both sidecar dispatchers are Optional here and
// a missing one is a warning. That is the single place this package's
// completeness verdict differs from web-terminal-kiro's, where `kiro-cli chat`
// IS the product.
//
// Every boundary the manager crosses — the archive fetch, subprocess
// execution, fsync, rename and the clock — is a struct field, so the tests
// need no network, no real archive and no real kiro-cli.
package kirocli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// Errors callers can classify. Everything else is wrapped with context.
var (
	// ErrDigestMismatch reports that the downloaded archive is not the
	// artifact the pinned digest names. Nothing is placed on the volume when
	// it is returned.
	ErrDigestMismatch = errors.New("kiro-cli archive SHA-256 mismatch")
	// ErrUnsupportedArch reports a GOARCH with no published kiro-cli archive.
	ErrUnsupportedArch = errors.New("unsupported architecture for kiro-cli")
	// ErrNoVersion reports that no complete kiro-cli version is installed and
	// none could be installed, so there is nothing to activate.
	ErrNoVersion = errors.New("no complete kiro-cli version is installed")
	// ErrVersionMismatch reports that a candidate binary answered --version
	// with something other than the version its directory and sentinel claim.
	ErrVersionMismatch = errors.New("kiro-cli reported a version its install directory does not claim")
)

// Readiness reasons. Callers key on these literals to build the 503 body, so
// each one is a named constant rather than an inline string.
const (
	// ReasonInstalling is the reason while the first install attempt is still
	// in flight and no version has ever been activated.
	ReasonInstalling = "kiro-cli installing"
	// ReasonRetrying is the reason while a failed install is being retried.
	ReasonRetrying = "kiro-cli install retrying"
	// ReasonUnavailable is the reason when no version is active and no further
	// attempt is scheduled: only an in-container repair plus Rescan, or a
	// container recreate, can clear it.
	ReasonUnavailable = "kiro-cli unavailable"
	// ReasonSettings is the reason when a version is active but a REQUIRED
	// setting (app.disableAutoupdates) could not be asserted against it, so
	// the binary may replace itself and invalidate the verified digest.
	ReasonSettings = "kiro-cli required settings not enforced"
)

// Phase reports where the manager is in the install lifecycle. It exists for
// the readiness reason /api/health serves, which must distinguish "still
// installing" from "retrying" from "gave up". Ready is the authority on
// whether kiro-cli may be used; Phase only explains why it may not.
type Phase string

// The lifecycle phases, in the order a boot walks them.
const (
	// PhaseIdle means Ensure has not run yet.
	PhaseIdle Phase = "idle"
	// PhaseInstalling means the first attempt of this process is in flight.
	PhaseInstalling Phase = "installing"
	// PhaseRetrying means an attempt failed and another one is scheduled.
	PhaseRetrying Phase = "retrying"
	// PhaseReady means a version is active and its required settings hold.
	PhaseReady Phase = "ready"
	// PhaseFailed means the bounded retries are exhausted with no active
	// version. Rescan can still clear it without recreating the container.
	PhaseFailed Phase = "failed"
)

// Layout and protocol constants.
const (
	// mainBinary is the dispatcher every version probe and every chat bridge
	// runs. It is always part of the required set.
	mainBinary = "kiro-cli"
	// sentinelName marks a version directory as completely installed. It
	// lives INSIDE the directory it describes, so it cannot drift from the
	// binaries it vouches for, and it is written LAST.
	sentinelName = ".complete"
	// versionsSubdir is the version-addressed installation root, relative to
	// the tools dir.
	//
	// It is a SIBLING of the toolbelt engine's trees, never a child of them.
	// The engine owns `opt/<tool>/<version>/` and `bin/<tool>`, and its
	// pruneOldVersions removes every directory under `opt/<tool>` that is not
	// the version it just installed — so a manifest entry named `kiro-cli`
	// (the engine accepts any name, and its manifest is hand-editable) would
	// have deleted this manager's ACTIVE version and its retained predecessor
	// out from under it. The engine creates and enumerates exactly `bin`,
	// `opt`, `npm` and `python` under the tools dir and never scans the tools
	// dir itself, so no tool of any name can reach a path under this one.
	versionsSubdir = "kiro-cli-versions"
	// binSubdir holds the non-authoritative convenience symlink, relative to
	// the tools dir. It is co-owned by the toolbelt engine.
	binSubdir = "bin"
	// stateFileName holds the diagnostic state record, relative to the tools
	// dir. Nothing in it is an input to readiness.
	stateFileName = "kiro-cli-state.json"
	// stagePrefix prefixes every in-progress staging tree under the
	// installation root. Dot-prefixed so no version scan and no bare-name
	// PATH lookup can reach it.
	stagePrefix = ".stage-"
	// autoUpdateSetting is the one setting the integrity story depends on:
	// with auto-update live the binary can replace itself and invalidate the
	// verified digest. New guarantees it is present and Required.
	autoUpdateSetting = "app.disableAutoupdates"

	dirMode  os.FileMode = 0o755
	fileMode os.FileMode = 0o600
)

// Bounded deadlines for every external command. A wedged binary must not stall
// a boot forever, and these are the same budgets the shell installer used.
const (
	probeTimeout     = 10 * time.Second
	settingTimeout   = 10 * time.Second
	installerTimeout = 120 * time.Second
)

// Retry defaults for EnsureWithRetry.
const (
	defaultMaxAttempts  = 4
	defaultRetryBackoff = 30 * time.Second
	maxRetryBackoff     = 10 * time.Minute
)

// Setting is one `kiro-cli settings <Key> <Value>` call. Required marks a
// setting whose failure is integrity-relevant: it blocks publication of a
// staged install and withholds readiness from an active one. Every other
// setting is best-effort and only warns.
type Setting struct {
	Key      string
	Value    string
	Required bool
}

// Config is the manager's input. Version and both digests come from the
// entrypoint's Renovate-pinned literals; ToolsDir is the persistent tools
// tree. The zero value is not usable — call New.
type Config struct {
	// Version is the pinned kiro-cli version, e.g. "2.14.2".
	Version string
	// SHA256 is the pinned x86_64 archive digest (KIRO_CLI_SHA256).
	SHA256 string
	// SHA256ARM64 is the pinned aarch64 archive digest
	// (KIRO_CLI_SHA256_ARM64).
	SHA256ARM64 string
	// ToolsDir is the persistent tools tree, e.g. "/config/tools". It is the
	// filesystem root of everything this package reads, writes or deletes,
	// and tests point it at a temp dir.
	ToolsDir string
	// Arch overrides the archive architecture target. Empty resolves it from
	// runtime.GOARCH, which is what production does.
	Arch string
	// BaseURL overrides the archive host. Empty uses the AWS release host.
	BaseURL string

	// Required names the dispatchers a version directory MUST contain to be
	// complete. Empty defaults to this app's set, which is the main
	// dispatcher ALONE: it serves `kiro-cli acp` itself. mainBinary is always
	// included.
	Required []string
	// Optional names dispatchers that are installed when present and only
	// warn when absent.
	Optional []string
	// Settings are applied against the active binary on every Ensure. New
	// guarantees the auto-update setting is present and Required.
	Settings []Setting

	// RetryBackoff is the first EnsureWithRetry backoff; it doubles per
	// attempt up to a 10 minute cap. Zero uses 30s.
	RetryBackoff time.Duration
	// MaxAttempts bounds EnsureWithRetry. Zero uses 4. The retries are
	// bounded deliberately: an endless loop re-downloading half a gigabyte
	// is worse than a 503 an operator can see and repair.
	MaxAttempts int
	// Tainted carries the entrypoint's tools-tree-was-writable observation. A
	// sentinel is trivially forgeable, unlike a digest, so when it is set no
	// pre-existing version directory may be activated: only a version this
	// process installed from a verified archive counts.
	Tainted bool
}

// State is the manager's persisted record. It is DIAGNOSTIC ONLY: every field
// is written for an operator reading the volume, and none of them is an input
// to Ready. In particular PinEnforced records the last result of the
// auto-update assertion as history — the live assertion this process performed
// is the only thing that gates readiness (see Ready).
type State struct {
	UpdatedAt     time.Time `json:"updated_at"`
	ActiveVersion string    `json:"active_version"`
	Dir           string    `json:"dir"`
	Pinned        string    `json:"pinned"`
	LastError     string    `json:"last_error,omitempty"`
	PinEnforced   bool      `json:"pin_enforced"`
}

// selection is one activatable version: its directory name, that directory's
// absolute path, and the absolute path of its main dispatcher.
type selection struct {
	version string
	dir     string
	bin     string
}

// Manager installs, selects and maintains the pinned kiro-cli. It is safe for
// concurrent use: Ensure and Rescan serialize against each other on opMu,
// while the readers (Ready, Active, CLIPath, PathEntry, Phase) take only the
// short state lock and never block behind an install.
//
//nolint:govet // fieldalignment: the field order IS this struct's lock-ownership documentation (seams, then config, then each lock immediately above what it guards). Packing it for the ~64 bytes fieldalignment can reclaim puts the version map first and both mutexes last, which is unreadable for a struct allocated exactly once per process.
type Manager struct {
	// Seams. Every one of these is a boundary the tests replace; none is
	// promoted to an interface, per this repo's external-boundary rule.
	fetch  func(ctx context.Context, url string, dst io.Writer) error
	run    runCommand
	fsync  func(path string) error
	rename func(oldpath, newpath string) error
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error

	cfg Config

	// opMu serializes the long filesystem operations (Ensure, Rescan). It is
	// deliberately NOT the state lock: it IS held across I/O, which is the
	// whole point, so readers must never take it.
	opMu sync.Mutex

	// mu guards every field below it -- active, phase, state, installed,
	// settingsOK and purged -- and is never held across I/O. The two flags sit
	// at the end of the struct rather than beside what they describe only
	// because a bool in the middle of this field set costs 8 bytes of padding
	// (govet fieldalignment).
	mu sync.Mutex
	// active is the version currently selected; the zero value means none.
	active selection
	phase  Phase
	state  State
	// installed records the versions this process published from a verified
	// archive, so a tainted tree can still become ready after a reinstall.
	installed map[string]bool
	// settingsOK records whether THIS process asserted every required setting
	// against active.bin. It is the readiness authority, replacing the
	// persisted PinEnforced (finding 3).
	settingsOK bool
	// purged latches the one-shot legacy purge so retries and rescans do not
	// re-delete the convenience symlink this manager publishes.
	purged bool
}

// New validates cfg and returns a manager. It resolves the architecture from
// runtime.GOARCH when Config.Arch is empty, requires a well-formed digest for
// the resolved architecture, and guarantees the pin-enforcing auto-update
// setting is present and Required so no caller can configure the integrity
// gate away. The caller's Config is not modified: defaults are applied to the
// manager's own copy.
func New(cfg *Config) (*Manager, error) {
	if cfg == nil {
		return nil, errors.New("kirocli: Config is required")
	}
	c := *cfg
	if c.Version == "" {
		return nil, errors.New("kirocli: Version is required")
	}
	if c.ToolsDir == "" {
		return nil, errors.New("kirocli: ToolsDir is required")
	}
	if err := validateVersion(c.Version); err != nil {
		return nil, fmt.Errorf("kirocli: Version %q: %w", c.Version, err)
	}
	if c.Arch == "" {
		arch, err := archTarget(runtime.GOARCH)
		if err != nil {
			return nil, err
		}
		c.Arch = arch
	}
	digest, err := expectedDigest(&c)
	if err != nil {
		return nil, err
	}
	if err := validateDigest(digest); err != nil {
		return nil, fmt.Errorf("kirocli: %s digest: %w", c.Arch, err)
	}
	c.Required = withMainBinary(c.Required)
	c.Settings = withPinEnforcement(c.Settings)
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = defaultRetryBackoff
	}
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	return &Manager{
		fetch:     httpFetch,
		run:       execRunner,
		fsync:     fsyncPath,
		rename:    os.Rename,
		now:       time.Now,
		sleep:     sleepCtx,
		cfg:       c,
		phase:     PhaseIdle,
		installed: map[string]bool{},
		state:     State{Pinned: c.Version},
	}, nil
}

// withMainBinary returns required with the main dispatcher guaranteed
// present: readiness probes it and every chat bridge runs it, so a caller
// cannot configure it out of the required set.
func withMainBinary(required []string) []string {
	if len(required) == 0 {
		// vibekit's set is the main dispatcher ALONE. It launches
		// `kiro-cli acp`, which the main binary serves directly, and no Go path
		// here invokes `chat` — so a version directory with no sidecar is a
		// COMPLETE install for this app, and the sidecars belong in Optional.
		// web-terminal-kiro's default is {main, chat sidecar} because its
		// product is `kiro-cli chat`; do not copy that set back here without
		// a Go caller that needs it.
		return []string{mainBinary}
	}
	if slices.Contains(required, mainBinary) {
		return required
	}
	return append([]string{mainBinary}, required...)
}

// withPinEnforcement returns settings with the auto-update setting present and
// Required. Making the integrity gate structural rather than configurable is
// what stops an empty Config.Settings from silently producing an install whose
// binary can replace itself.
func withPinEnforcement(settings []Setting) []Setting {
	out := make([]Setting, 0, len(settings)+1)
	found := false
	for _, s := range settings {
		if s.Key == autoUpdateSetting {
			s.Value, s.Required, found = "true", true, true
		}
		out = append(out, s)
	}
	if !found {
		out = append(out, Setting{Key: autoUpdateSetting, Value: "true", Required: true})
	}
	return out
}

// Ensure brings the pinned kiro-cli version online and is idempotent: on a
// boot that already has the pin complete and activatable it downloads nothing
// and still reasserts every required setting.
//
// The order is the design. Legacy purge first (it deletes the old layout
// outright, so nothing downstream can be fooled by it), then partial
// directories go (a partial is never a selection candidate), then selection
// probes each candidate's own --version before accepting it, then — only when
// the pin is not already activatable — the install runs, then the required
// settings are reasserted against whatever was selected, then the convenience
// symlink is republished, and only then may pruning run.
func (m *Manager) Ensure(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.purgeLegacyOnce()
	m.prunePartials()

	sel, ok := m.selectActive(ctx)
	var installErr error
	if !ok || sel.version != m.cfg.Version {
		installErr = m.install(ctx)
		if installErr == nil {
			sel, ok = m.selectActive(ctx)
		} else {
			slog.Warn("kiro-cli install failed; keeping any previously complete version",
				"pinned", m.cfg.Version, "error", installErr)
		}
	}
	if !ok {
		return m.recordUnavailable(installErr)
	}
	return m.finish(ctx, sel, installErr)
}

// finish activates sel: it reasserts the required settings against the
// SELECTED binary (finding 3 — the settings live in the mutable Kiro home, not
// in the immutable version directory, so a persisted success proves nothing),
// commits the state, republishes the convenience symlink, and prunes.
func (m *Manager) finish(ctx context.Context, sel selection, installErr error) error {
	settingsErr := m.applySettings(ctx, sel.bin)
	m.commit(sel, installErr, settingsErr)
	m.publishConvenienceLink(sel.bin)
	// Pruning runs only after a successful install, and therefore only after
	// publish has synced the parent directory (finding 2). A FAILED install
	// prunes nothing: the versions on the volume are the fallback set that
	// makes the failure survivable.
	if installErr == nil {
		m.pruneSuperseded(sel.version)
	}
	switch {
	case settingsErr != nil:
		return settingsErr
	case installErr != nil:
		return installErr
	}
	return nil
}

// EnsureWithRetry drives Ensure with bounded exponential backoff and is what
// the server runs in the background. It is the healing posture's first half
// (finding 6): a single one-shot attempt would leave a failed first boot
// answering 503 forever with no further effort. It never exits the process,
// and it returns the last error after the attempts are exhausted.
func (m *Manager) EnsureWithRetry(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= m.cfg.MaxAttempts; attempt++ {
		m.setAttemptPhase(attempt)
		lastErr = m.Ensure(ctx)
		if lastErr == nil {
			m.settlePhase()
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		if attempt == m.cfg.MaxAttempts {
			break
		}
		m.setPhase(PhaseRetrying)
		wait := m.backoff(attempt)
		slog.Warn("retrying the kiro-cli install after a failure",
			"attempt", attempt, "of", m.cfg.MaxAttempts,
			"retry_in", wait.String(), "error", lastErr)
		if err := m.sleep(ctx, wait); err != nil {
			lastErr = err
			break
		}
	}
	m.settlePhase()
	if ready, _ := m.Ready(); !ready && lastErr != nil {
		slog.Error("kiro-cli is unavailable and the bounded install retries are exhausted; the server stays up so the install can be repaired in place",
			"pinned", m.cfg.Version, "attempts", m.cfg.MaxAttempts, "error", lastErr,
			"hint", "docker exec into the container, fix or remove /config/tools/kiro-cli-versions, then restart the container or hit the manager's rescan")
	}
	return lastErr
}

// backoff returns the wait before the attempt after n, doubling from
// Config.RetryBackoff up to a 10 minute cap.
func (m *Manager) backoff(n int) time.Duration {
	wait := m.cfg.RetryBackoff
	for range n - 1 {
		wait *= 2
		if wait >= maxRetryBackoff {
			return maxRetryBackoff
		}
	}
	return wait
}

// Rescan re-derives the active version from what is on disk right now,
// without downloading anything, and reasserts the required settings. It is the
// healing posture's second half (finding 6): a repair made INSIDE the
// container — an operator restoring a version directory, or fixing a wedged
// binary — becomes observable without recreating the container. It returns
// whether a version is active afterwards.
func (m *Manager) Rescan(ctx context.Context) (bool, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	sel, ok := m.selectActive(ctx)
	if !ok {
		err := m.recordUnavailable(nil)
		m.settlePhase()
		return false, err
	}
	err := m.finish(ctx, sel, nil)
	m.settlePhase()
	return err == nil, err
}

// Ready reports whether kiro-cli may be used, and why not when it may not.
//
// It is true only when a version is active AND this process asserted every
// required setting against that exact binary. The persisted PinEnforced is
// never consulted: app.disableAutoupdates lives in the mutable Kiro home, so
// remembering that it once succeeded is stale evidence (finding 3).
func (m *Manager) Ready() (ready bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active.bin == "" {
		return false, reasonFor(m.phase)
	}
	if !m.settingsOK {
		return false, ReasonSettings
	}
	return true, ""
}

// reasonFor maps a phase with no active version to the 503 reason, so the gate
// can tell "still installing" from "retrying" from "gave up".
func reasonFor(p Phase) string {
	switch p {
	case PhaseIdle, PhaseInstalling:
		return ReasonInstalling
	case PhaseRetrying:
		return ReasonRetrying
	case PhaseReady, PhaseFailed:
		return ReasonUnavailable
	}
	return ReasonUnavailable
}

// Phase reports the install lifecycle phase for diagnostics and for the 503
// reason. Ready, not Phase, decides whether kiro-cli may be used.
func (m *Manager) Phase() Phase {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.phase
}

// Active returns the current state record and whether a version is active.
func (m *Manager) Active() (State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.active.bin != ""
}

// PathEntry returns the absolute directory to prepend to the PATH of every
// kiro-cli process this app spawns, or "" when no version is active. It holds
// only kiro-cli's own dispatchers, so leading with it shadows nothing else.
func (m *Manager) PathEntry() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active.dir
}

// CLIPath returns the absolute path of the active main dispatcher, or "" when
// no version is active. This — never the convenience symlink — is what the
// product runs.
func (m *Manager) CLIPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active.bin
}

// versionsRoot is the version-addressed installation root.
func (m *Manager) versionsRoot() string {
	return filepath.Join(m.cfg.ToolsDir, versionsSubdir)
}

// versionDir is the absolute directory for one version.
func (m *Manager) versionDir(version string) string {
	return filepath.Join(m.versionsRoot(), version)
}

// applySettings asserts every configured setting against bin. A required
// failure is returned (it withholds readiness); a best-effort failure only
// warns, matching the shell's split between the integrity setting and the
// telemetry/notification/title preferences.
func (m *Manager) applySettings(ctx context.Context, bin string) error {
	var required error
	for _, s := range m.cfg.Settings {
		_, err := m.run(ctx, &command{
			Path:    bin,
			Args:    []string{"settings", s.Key, s.Value},
			Timeout: settingTimeout,
		})
		if err == nil {
			continue
		}
		if !s.Required {
			slog.Warn("kiro-cli settings call failed; dependent feature may misbehave",
				"setting", s.Key, "value", s.Value, "error", err)
			continue
		}
		slog.Error("failed to assert a required kiro-cli setting; withholding readiness because the binary may replace itself and invalidate the pinned digest",
			"setting", s.Key, "path", bin, "error", err)
		if required == nil {
			required = fmt.Errorf("required setting %s: %w", s.Key, err)
		}
	}
	return required
}

// commit records the activation under the state lock and persists the
// diagnostic record. The lock is held only across in-memory assignment; the
// state file is written outside it.
func (m *Manager) commit(sel selection, installErr, settingsErr error) {
	m.mu.Lock()
	m.active = sel
	m.settingsOK = settingsErr == nil
	m.state.ActiveVersion = sel.version
	m.state.Dir = sel.dir
	m.state.Pinned = m.cfg.Version
	// PinEnforced is history for an operator reading the volume. Ready uses
	// m.settingsOK, which only this process's assertion can set.
	m.state.PinEnforced = settingsErr == nil
	m.state.LastError = firstErrText(settingsErr, installErr)
	m.state.UpdatedAt = m.now().UTC()
	snapshot := m.state
	if m.settingsOK {
		m.phase = PhaseReady
	}
	m.mu.Unlock()

	switch {
	case installErr != nil:
		slog.Warn("serving a previously installed kiro-cli version; the pinned version could not be installed",
			"active", sel.version, "pinned", m.cfg.Version, "path", sel.bin)
	default:
		slog.Info("kiro-cli active", "version", sel.version, "path", sel.bin)
	}
	m.saveState(&snapshot)
}

// recordUnavailable clears the active version and records why. It returns the
// install error when there was one, so a caller can distinguish "the install
// failed" from "nothing was ever installed".
func (m *Manager) recordUnavailable(installErr error) error {
	err := installErr
	if err == nil {
		err = ErrNoVersion
	}
	m.mu.Lock()
	m.active = selection{}
	m.settingsOK = false
	m.state.ActiveVersion = ""
	m.state.Dir = ""
	m.state.Pinned = m.cfg.Version
	m.state.PinEnforced = false
	m.state.LastError = err.Error()
	m.state.UpdatedAt = m.now().UTC()
	snapshot := m.state
	m.mu.Unlock()

	slog.Error("no usable kiro-cli version is installed; chats cannot start until one is",
		"pinned", m.cfg.Version, "error", err)
	m.saveState(&snapshot)
	return err
}

// setAttemptPhase marks the first attempt installing and every later one
// retrying, but never downgrades a manager that is already serving.
func (m *Manager) setAttemptPhase(attempt int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase == PhaseReady {
		return
	}
	if attempt == 1 {
		m.phase = PhaseInstalling
		return
	}
	m.phase = PhaseRetrying
}

func (m *Manager) setPhase(p Phase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.phase = p
}

// settlePhase recomputes the terminal phase from the live state: ready when a
// version is active with its settings asserted, failed otherwise.
func (m *Manager) settlePhase() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active.bin != "" && m.settingsOK {
		m.phase = PhaseReady
		return
	}
	m.phase = PhaseFailed
}

// saveState writes the diagnostic record durably. A failure only warns:
// nothing in the record is an input to readiness, so losing it must not fail
// an otherwise good install.
func (m *Manager) saveState(s *State) {
	blob, err := json.Marshal(s)
	if err != nil {
		slog.Warn("failed to encode the kiro-cli state record", "error", err)
		return
	}
	path := filepath.Join(m.cfg.ToolsDir, stateFileName)
	if err := m.writeFileDurably(path, append(blob, '\n'), fileMode); err != nil {
		slog.Warn("failed to persist the kiro-cli state record; it is diagnostic only, so readiness is unaffected",
			"path", path, "error", err)
	}
}

// firstErrText returns the first non-nil error's text, or "".
func firstErrText(errs ...error) string {
	for _, err := range errs {
		if err != nil {
			return err.Error()
		}
	}
	return ""
}

// validateDigest rejects anything that is not a 64-character hex SHA-256, so a
// truncated or mangled pin fails at construction rather than after a 528 MB
// download.
func validateDigest(digest string) error {
	if len(digest) != 64 {
		return fmt.Errorf("want 64 hex characters, got %d", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("not hexadecimal")
	}
	if strings.ToLower(digest) != digest {
		return errors.New("must be lowercase hexadecimal")
	}
	return nil
}

// validateVersion constrains the pin to the characters a version can hold. The
// value comes from the environment and is interpolated into BOTH a download URL
// and a filesystem path under the tools tree, so a separator or a traversal
// component in it would escape the installation root.
func validateVersion(version string) error {
	for _, r := range version {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '.', r == '-', r == '+', r == '_':
		default:
			return fmt.Errorf("illegal character %q (want only letters, digits and .-+_)", r)
		}
	}
	if strings.HasPrefix(version, ".") || strings.Contains(version, "..") {
		return errors.New("must not start with a dot or contain \"..\"")
	}
	return nil
}

// sleepCtx waits d or until ctx is done, whichever comes first. time.Sleep
// would block shutdown.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
