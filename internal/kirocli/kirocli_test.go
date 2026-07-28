package kirocli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNewValidatesItsPins pins construction: a manager cannot exist without a
// version, a tools dir, a supported architecture and a well-formed digest for
// THAT architecture. Rejecting a mangled pin at construction beats discovering
// it after a half-gigabyte download.
func TestNewValidatesItsPins(t *testing.T) {
	good := strings.Repeat("a", 64)
	base := func() Config {
		return Config{Version: pinnedVersion, ToolsDir: t.TempDir(), Arch: archAMD64, SHA256: good}
	}
	tests := map[string]struct {
		mutate  func(*Config)
		wantErr string
	}{
		"missing version":      {mutate: func(c *Config) { c.Version = "" }, wantErr: "Version"},
		"traversal version":    {mutate: func(c *Config) { c.Version = "../../etc" }, wantErr: "Version"},
		"separator in version": {mutate: func(c *Config) { c.Version = "2.14.2/x" }, wantErr: "illegal character"},
		"dotted version":       {mutate: func(c *Config) { c.Version = ".hidden" }, wantErr: "dot"},
		"missing tools dir":    {mutate: func(c *Config) { c.ToolsDir = "" }, wantErr: "ToolsDir"},
		"unsupported arch":     {mutate: func(c *Config) { c.Arch = "sparc-linux" }, wantErr: "unsupported architecture"},
		"missing digest":       {mutate: func(c *Config) { c.SHA256 = "" }, wantErr: "64 hex"},
		"truncated digest":     {mutate: func(c *Config) { c.SHA256 = good[:63] }, wantErr: "64 hex"},
		"non-hex digest":       {mutate: func(c *Config) { c.SHA256 = strings.Repeat("z", 64) }, wantErr: "hexadecimal"},
		"uppercase digest":     {mutate: func(c *Config) { c.SHA256 = strings.ToUpper(good) }, wantErr: "lowercase"},
		"arm64 digest ignored": {mutate: func(c *Config) { c.SHA256ARM64 = "junk" }},
		"valid":                {mutate: func(*Config) {}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			m, err := New(&cfg)
			switch {
			case tc.wantErr == "":
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if m.cfg.MaxAttempts <= 0 || m.cfg.RetryBackoff <= 0 || m.cfg.BaseURL == "" {
					t.Errorf("New left retry/base defaults unset: %+v", m.cfg)
				}
			case err == nil:
				t.Fatalf("New accepted %s", name)
			case !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("error = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// TestNewAlwaysRequiresTheAutoUpdateSetting pins that the integrity gate is
// structural, not configurable: whatever a caller passes, the auto-update
// setting is present and Required, so no empty or reshaped Settings slice can
// produce an install whose binary may replace itself.
func TestNewAlwaysRequiresTheAutoUpdateSetting(t *testing.T) {
	tests := map[string][]Setting{
		"empty settings":        nil,
		"unrelated settings":    {{Key: "telemetry.enabled", Value: "false"}},
		"declared not required": {{Key: autoUpdateSetting, Value: "true"}},
		"declared with a lie":   {{Key: autoUpdateSetting, Value: "false"}},
	}
	for name, settings := range tests {
		t.Run(name, func(t *testing.T) {
			m, err := New(&Config{
				Version: pinnedVersion, ToolsDir: t.TempDir(), Arch: archAMD64,
				SHA256: strings.Repeat("a", 64), Settings: settings,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			found := 0
			for _, s := range m.cfg.Settings {
				if s.Key != autoUpdateSetting {
					continue
				}
				found++
				if !s.Required || s.Value != "true" {
					t.Errorf("%s = %+v, want Value \"true\" and Required", autoUpdateSetting, s)
				}
			}
			if found != 1 {
				t.Errorf("%s appears %d times, want exactly 1", autoUpdateSetting, found)
			}
			// The main dispatcher is likewise never optional.
			if len(m.cfg.Required) == 0 || m.cfg.Required[0] != mainBinary {
				t.Errorf("Required = %v, want it to lead with %q", m.cfg.Required, mainBinary)
			}
		})
	}
}

// TestNewAlwaysRequiresTheMainDispatcher pins that the main dispatcher is in
// the required set whatever a caller passes: readiness probes it and every chat
// bridge runs it, so a version directory without it must never read complete.
// The empty case also pins vibekit's set as the main dispatcher ALONE.
func TestNewAlwaysRequiresTheMainDispatcher(t *testing.T) {
	tests := map[string]struct {
		required []string
		want     []string
	}{
		"empty defaults to this app's one-file set": {required: nil, want: []string{mainBinary}},
		"main already present is kept":              {required: []string{mainBinary}, want: []string{mainBinary}},
		"main is prepended when absent":             {required: []string{"kiro-cli-chat"}, want: []string{mainBinary, "kiro-cli-chat"}},
		"order is otherwise preserved": {
			required: []string{"kiro-cli-chat", mainBinary},
			want:     []string{"kiro-cli-chat", mainBinary},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			m, err := New(&Config{
				Version: pinnedVersion, ToolsDir: t.TempDir(), Arch: archAMD64,
				SHA256: strings.Repeat("a", 64), Required: tc.required,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if !slices.Equal(m.cfg.Required, tc.want) {
				t.Errorf("Required = %v, want %v", m.cfg.Required, tc.want)
			}
		})
	}
}

// TestEnsureReassertsRequiredSettingsOnABootThatSkipsTheInstall pins finding 3.
// app.disableAutoupdates lives in the mutable Kiro home, not in the immutable
// version directory, so it cannot be remembered: even the fast path where the
// pinned directory is already complete must re-prove it against the selected
// binary, and a failure there must withhold readiness rather than warn.
func TestEnsureReassertsRequiredSettingsOnABootThatSkipsTheInstall(t *testing.T) {
	t.Run("asserted without downloading anything", func(t *testing.T) {
		env := newFakeEnv(t)
		dir := env.placeVersion(pinnedVersion)
		m := env.manager(func(c *Config) {
			c.Settings = []Setting{{Key: "telemetry.enabled", Value: "false"}}
		})

		if err := m.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if env.fetchCount() != 0 {
			t.Errorf("fetches = %d, want 0 on a boot that already has the pin", env.fetchCount())
		}
		want := "settings " + autoUpdateSetting + "=true on " + filepath.Join(dir, mainBinary)
		if env.countCalls(want) != 1 {
			t.Errorf("calls = %v, want exactly one %q against the SELECTED binary", env.called(), want)
		}
		if env.countCalls("settings telemetry.enabled=false") != 1 {
			t.Error("the best-effort settings were not applied on the skip-install boot")
		}
		if ready, reason := m.Ready(); !ready {
			t.Errorf("Ready() = false (%s), want true", reason)
		}
	})

	t.Run("a failed reassertion withholds readiness", func(t *testing.T) {
		env := newFakeEnv(t)
		env.placeVersion(pinnedVersion)
		env.onSetting = func(_, key, _ string) error {
			if key == autoUpdateSetting {
				return errors.New("settings call failed")
			}
			return nil
		}
		m := env.manager()

		err := m.Ensure(context.Background())
		if err == nil || !strings.Contains(err.Error(), autoUpdateSetting) {
			t.Fatalf("Ensure error = %v, want one naming %s", err, autoUpdateSetting)
		}
		ready, reason := m.Ready()
		if ready || reason != ReasonSettings {
			t.Errorf("Ready() = (%v, %q), want (false, %q)", ready, reason, ReasonSettings)
		}
		// The CLI stays reachable for repair even though it is not ready.
		if got := m.CLIPath(); got == "" {
			t.Error("CLIPath() is empty; the usable binary must stay available for an in-container repair")
		}
	})

	t.Run("a best-effort setting failure does not withhold readiness", func(t *testing.T) {
		env := newFakeEnv(t)
		env.placeVersion(pinnedVersion)
		env.onSetting = func(_, key, _ string) error {
			if key == "chat.notificationMethod" {
				return errors.New("settings call failed")
			}
			return nil
		}
		m := env.manager(func(c *Config) {
			c.Settings = []Setting{{Key: "chat.notificationMethod", Value: "osc9"}}
		})

		if err := m.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if ready, reason := m.Ready(); !ready {
			t.Errorf("Ready() = false (%s), want true: a best-effort setting is not integrity-relevant", reason)
		}
	})
}

// TestPersistedPinEnforcedNeverGatesReadiness pins that the persisted record is
// diagnostic history only. A state file claiming the pin was enforced -- the
// exact stale-evidence shape finding 3 rejects -- must not make a manager whose
// live reassertion failed report ready.
func TestPersistedPinEnforcedNeverGatesReadiness(t *testing.T) {
	env := newFakeEnv(t)
	dir := env.placeVersion(pinnedVersion)
	seed, err := json.Marshal(State{
		ActiveVersion: pinnedVersion,
		Dir:           dir,
		Pinned:        pinnedVersion,
		PinEnforced:   true,
		UpdatedAt:     time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.tools, stateFileName), seed, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	env.onSetting = func(_, key, _ string) error {
		if key == autoUpdateSetting {
			return errors.New("settings call failed")
		}
		return nil
	}
	m := env.manager()

	if err := m.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure returned nil although the required setting failed")
	}
	if ready, reason := m.Ready(); ready || reason != ReasonSettings {
		t.Errorf("Ready() = (%v, %q), want (false, %q) -- persisted PinEnforced must never be the authority",
			ready, reason, ReasonSettings)
	}
	if state, _ := m.Active(); state.PinEnforced {
		t.Error("State.PinEnforced stayed true after a failed reassertion; it must record THIS boot's result")
	}
}

// TestStateSaveFailureDoesNotAffectReadiness pins that the diagnostic record is
// not on the readiness path: losing it warns, it does not fail an otherwise good
// install.
func TestStateSaveFailureDoesNotAffectReadiness(t *testing.T) {
	env := newFakeEnv(t)
	env.placeVersion(pinnedVersion)
	statePath := filepath.Join(env.tools, stateFileName)
	env.onRename = func(_, newpath string) error {
		if newpath == statePath {
			return errors.New("injected state save failure")
		}
		return nil
	}
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure error = %v, want nil despite the state save failure", err)
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true", reason)
	}
}

// TestEnsureWithRetryIsBoundedAndReportsTerminalFailure pins the first half of
// the healing posture (finding 6): a failing install is retried with growing
// backoff, the attempts are BOUNDED, and the end state is a distinguishable
// terminal failure rather than an endless loop or a process exit.
func TestEnsureWithRetryIsBoundedAndReportsTerminalFailure(t *testing.T) {
	env := newFakeEnv(t)
	env.installerFails = true
	m := env.manager(func(c *Config) {
		c.MaxAttempts = 3
		c.RetryBackoff = time.Second
	})

	err := m.EnsureWithRetry(context.Background())
	if err == nil {
		t.Fatal("EnsureWithRetry returned nil although every attempt failed")
	}
	if got := env.fetchCount(); got != 3 {
		t.Errorf("fetches = %d, want 3 (one per bounded attempt)", got)
	}
	want := []time.Duration{time.Second, 2 * time.Second}
	if got := env.sleeps(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("backoffs = %v, want %v (doubling, one fewer than the attempts)", got, want)
	}
	if m.Phase() != PhaseFailed {
		t.Errorf("Phase() = %q, want %q", m.Phase(), PhaseFailed)
	}
	if ready, reason := m.Ready(); ready || reason != ReasonUnavailable {
		t.Errorf("Ready() = (%v, %q), want (false, %q)", ready, reason, ReasonUnavailable)
	}
}

// TestReadinessReasonDistinguishesTheLifecyclePhases pins that the session-create
// gate can tell installing from retrying from terminally failed, which is what
// finding 6 asks the 503 reason to carry.
func TestReadinessReasonDistinguishesTheLifecyclePhases(t *testing.T) {
	tests := map[Phase]string{
		PhaseIdle:       ReasonInstalling,
		PhaseInstalling: ReasonInstalling,
		PhaseRetrying:   ReasonRetrying,
		PhaseFailed:     ReasonUnavailable,
	}
	for phase, want := range tests {
		t.Run(string(phase), func(t *testing.T) {
			env := newFakeEnv(t)
			m := env.manager()
			m.setPhase(phase)
			ready, reason := m.Ready()
			if ready {
				t.Fatal("Ready() = true with no active version")
			}
			if reason != want {
				t.Errorf("reason = %q, want %q", reason, want)
			}
		})
	}
}

// TestRescanMakesAnInContainerRepairVisible pins the second half of the healing
// posture (finding 6). A first boot that fails with nothing to fall back on
// leaves the server up and unready; an operator who repairs the install INSIDE
// the container must become observable without recreating it.
func TestRescanMakesAnInContainerRepairVisible(t *testing.T) {
	env := newFakeEnv(t)
	env.installerFails = true
	m := env.manager(func(c *Config) { c.MaxAttempts = 2 })

	if err := m.EnsureWithRetry(context.Background()); err == nil {
		t.Fatal("EnsureWithRetry returned nil although every attempt failed")
	}
	if ready, reason := m.Ready(); ready || reason != ReasonUnavailable {
		t.Fatalf("Ready() = (%v, %q), want (false, %q) before the repair", ready, reason, ReasonUnavailable)
	}
	before := env.fetchCount()

	// The repair: an operator restores a complete version directory by hand.
	dir := env.placeVersion(pinnedVersion)

	ok, err := m.Rescan(context.Background())
	if err != nil || !ok {
		t.Fatalf("Rescan = (%v, %v), want (true, nil)", ok, err)
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true after the in-container repair", reason)
	}
	if m.Phase() != PhaseReady {
		t.Errorf("Phase() = %q, want %q", m.Phase(), PhaseReady)
	}
	if got := m.CLIPath(); got != filepath.Join(dir, mainBinary) {
		t.Errorf("CLIPath() = %q, want %q", got, filepath.Join(dir, mainBinary))
	}
	if env.fetchCount() != before {
		t.Errorf("Rescan downloaded the archive (%d -> %d); it must only re-derive from disk",
			before, env.fetchCount())
	}
	if state, active := m.Active(); !active || state.LastError != "" {
		t.Errorf("State = %+v (active=%v), want the previous failure cleared", state, active)
	}
}

// TestRescanReportsUnreadyWhenTheRepairIsIncomplete pins the negative half: a
// half-restored directory (no sentinel) is not a repair, so Rescan keeps
// readiness withheld instead of activating a partial install.
func TestRescanReportsUnreadyWhenTheRepairIsIncomplete(t *testing.T) {
	env := newFakeEnv(t)
	env.placePartial(pinnedVersion)
	m := env.manager()

	ok, err := m.Rescan(context.Background())
	if ok {
		t.Fatal("Rescan accepted a directory with no completion sentinel")
	}
	if !errors.Is(err, ErrNoVersion) {
		t.Errorf("Rescan error = %v, want ErrNoVersion", err)
	}
	if ready, reason := m.Ready(); ready || reason != ReasonUnavailable {
		t.Errorf("Ready() = (%v, %q), want (false, %q)", ready, reason, ReasonUnavailable)
	}
}

// TestEnsureIsIdempotent pins that a second Ensure on a converged volume
// downloads nothing, keeps the same active version, and still reasserts the
// required settings.
func TestEnsureIsIdempotent(t *testing.T) {
	env := newFakeEnv(t)
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	first := m.CLIPath()
	afterFirst := env.countCalls("settings " + autoUpdateSetting)

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if got := env.fetchCount(); got != 1 {
		t.Errorf("fetches = %d, want 1 -- the second Ensure must not re-download", got)
	}
	if got := m.CLIPath(); got != first {
		t.Errorf("CLIPath() = %q, want the unchanged %q", got, first)
	}
	if got := env.countCalls("settings " + autoUpdateSetting); got <= afterFirst {
		t.Error("the second Ensure did not reassert the required setting")
	}
}

// TestBackoffDoublesAndCaps pins the retry schedule's shape, including the cap
// that stops a long-lived container from waiting hours between attempts.
func TestBackoffDoublesAndCaps(t *testing.T) {
	env := newFakeEnv(t)
	m := env.manager(func(c *Config) { c.RetryBackoff = time.Minute })
	tests := map[int]time.Duration{
		1: time.Minute,
		2: 2 * time.Minute,
		3: 4 * time.Minute,
		4: 8 * time.Minute,
		5: maxRetryBackoff,
		9: maxRetryBackoff,
	}
	for attempt, want := range tests {
		if got := m.backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// TestSleepCtxHonoursCancellation pins that the backoff wait is cancellable, so
// a shutdown during a retry window does not block on a timer.
func TestSleepCtxHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx error = %v, want context.Canceled", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx error = %v, want nil", err)
	}
}
