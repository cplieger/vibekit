package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

// settingsCLI answers the two settings reads separately, so a test can assert
// WHICH door answered and how many subprocesses that took.
type settingsCLI struct {
	// list is the settings-list document. Empty makes that read fail, which is
	// the older-kiro-cli case the fallback exists for.
	list string
	// perKey answers `settings <key>`; a key absent from the map fails.
	perKey map[string]string

	listCalls   int
	perKeyCalls []string
	// deadlines is the deadline every spawn of the request saw, in call order, so
	// a test can assert the whole read shares one rather than minting one each.
	deadlines []time.Time
}

var _ CLIRunner = (*settingsCLI)(nil)

// recordDeadline notes what the spawn's context bounds it to. A context with none
// records the zero time, which no test expects, so an unbounded spawn is visible.
func (f *settingsCLI) recordDeadline(ctx context.Context) {
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
}

func (f *settingsCLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 2 && args[0] == "settings" {
		f.recordDeadline(ctx)
		f.perKeyCalls = append(f.perKeyCalls, args[1])
		v, ok := f.perKey[args[1]]
		if !ok {
			return nil, errors.New("settingsCLI: no such setting")
		}
		return []byte(v), nil
	}
	return nil, fmt.Errorf("settingsCLI: unexpected Run args %v", args)
}

func (f *settingsCLI) RunStdoutCapped(ctx context.Context, limit int, args ...string) ([]byte, bool, error) {
	if !slices.Equal(args, settingsListArgs) {
		return nil, false, fmt.Errorf("settingsCLI: unexpected RunStdoutCapped args %v", args)
	}
	f.recordDeadline(ctx)
	f.listCalls++
	if f.list == "" {
		return nil, false, errors.New("settingsCLI: unknown subcommand")
	}
	if len(f.list) > limit {
		return []byte(f.list[:limit]), true, nil
	}
	return []byte(f.list), false, nil
}

// The whole document 2.20.2 answers, verbatim from the measurement.
const settingsListFixture = `{"app.disableAutoupdates":true,` +
	`"chat.disableInheritingDefaultResources":false,"chat.enableCheckpoint":false,` +
	`"chat.enableKnowledge":true,"chat.enablePromptHints":true,"chat.enableSubagent":true,` +
	`"chat.enableTodoList":true,"cleanup.periodDays":0,"hooks.showStatus":true,` +
	`"telemetry.enabled":false,"toolSearch.enabled":false}`

// getKiroSettings drives the read handler and decodes its answer.
func getKiroSettings(t *testing.T, runner CLIRunner, query string) (int, map[string]string) {
	t.Helper()
	s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts()}
	req := httptest.NewRequest(http.MethodGet, "/api/kiro-settings"+query, nil)
	rec := httptest.NewRecorder()
	s.handleKiroSettings(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var body struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body.Settings
}

// The item: the Settings → General panel's three flags cost ONE subprocess, not
// one each. It used to answer a single key per request off `settings <key>`, so
// opening the panel fired three concurrent requests and three spawns of a 3 s
// budget each.
//
// It also pins the value spelling across the two doors: the list document carries
// native JSON (`true`, `false`), the per-key form carries "true (global)", and the
// client compares against "true"/"false" without knowing which answered.
func TestReadKiroSettings_OneSubprocessAnswersEveryKey(t *testing.T) {
	f := &settingsCLI{list: settingsListFixture}

	code, got := getKiroSettings(t,
		f, "?keys=hooks.showStatus,telemetry.enabled,chat.disableInheritingDefaultResources")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	want := map[string]string{
		"hooks.showStatus":                       "true",
		"telemetry.enabled":                      "false",
		"chat.disableInheritingDefaultResources": "false",
	}
	if !maps.Equal(got, want) {
		t.Errorf("settings = %v, want %v", got, want)
	}
	if f.listCalls != 1 {
		t.Errorf("settings list ran %d times, want exactly 1", f.listCalls)
	}
	if len(f.perKeyCalls) != 0 {
		t.Errorf("per-key reads ran for %v, want none: the list read answered every key",
			f.perKeyCalls)
	}
}

// A build pinned to a kiro-cli without `settings list` must still fill the panel,
// so a failed list read falls back to the per-key invocation, for the keys the
// request named and no others. What bounds its COST is the shared deadline, not the
// selector — an absent ?keys= names the whole allowlist.
func TestReadKiroSettings_FallsBackPerKeyWhenTheListReadFails(t *testing.T) {
	f := &settingsCLI{
		perKey: map[string]string{
			"hooks.showStatus":  "true (global)",
			"telemetry.enabled": "false (global)",
		},
	}

	code, got := getKiroSettings(t, f, "?keys=hooks.showStatus,telemetry.enabled")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	want := map[string]string{"hooks.showStatus": "true", "telemetry.enabled": "false"}
	if !maps.Equal(got, want) {
		t.Errorf("settings = %v, want %v", got, want)
	}
	if f.listCalls != 1 {
		t.Errorf("settings list ran %d times, want 1 attempt before the fallback", f.listCalls)
	}
	asked := slices.Clone(f.perKeyCalls)
	slices.Sort(asked)
	if wantAsked := []string{"hooks.showStatus", "telemetry.enabled"}; !slices.Equal(asked, wantAsked) {
		t.Errorf("per-key reads = %v, want %v — the fallback reads what the request named "+
			"and nothing else", asked, wantAsked)
	}
}

// A document that answers some of the keys costs one per-key spawn for the rest,
// not one for all of them.
func TestReadKiroSettings_AKeyTheListOmitsFallsBackAlone(t *testing.T) {
	f := &settingsCLI{
		list:   `{"hooks.showStatus":true,"telemetry.enabled":false}`,
		perKey: map[string]string{"chat.enableSubagent": "true (global)"},
	}

	code, got := getKiroSettings(t,
		f, "?keys=hooks.showStatus,telemetry.enabled,chat.enableSubagent")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got["chat.enableSubagent"] != "true" {
		t.Errorf("chat.enableSubagent = %q, want %q", got["chat.enableSubagent"], "true")
	}
	if !slices.Equal(f.perKeyCalls, []string{"chat.enableSubagent"}) {
		t.Errorf("per-key reads = %v, want only the key the document omitted", f.perKeyCalls)
	}
}

// Neither door answers, and the panel still renders: an empty value is what the
// client reads as unset, which shows the control's default. Failing the request
// would blank three checkboxes instead.
func TestReadKiroSettings_AnUnreadableKeyAnswersEmptyRatherThanFailing(t *testing.T) {
	f := &settingsCLI{}

	code, got := getKiroSettings(t, f, "?keys=hooks.showStatus")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an unreadable setting must not fail the panel", code)
	}
	if want := map[string]string{"hooks.showStatus": ""}; !maps.Equal(got, want) {
		t.Errorf("settings = %v, want %v", got, want)
	}
}

// No selection means the whole document, still in one spawn.
func TestReadKiroSettings_AbsentKeysAnswersTheWholeAllowlist(t *testing.T) {
	f := &settingsCLI{list: settingsListFixture}

	code, got := getKiroSettings(t, f, "")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(got) != len(allowedKiroSettings) {
		t.Errorf("answered %d keys, want the %d allowlisted ones: %v",
			len(got), len(allowedKiroSettings), got)
	}
	for key := range allowedKiroSettings {
		if _, ok := got[key]; !ok {
			t.Errorf("allowlisted key %q is missing from the answer", key)
		}
	}
	if f.listCalls != 1 || len(f.perKeyCalls) != 0 {
		t.Errorf("list=%d per-key=%v, want one list read and no per-key reads",
			f.listCalls, f.perKeyCalls)
	}
}

// A key outside the allowlist is refused rather than answered, so a typo fails
// loudly the way it did when the parameter named one key.
func TestReadKiroSettings_RefusesARequestNamingNothingAllowed(t *testing.T) {
	f := &settingsCLI{list: settingsListFixture}

	code, _ := getKiroSettings(t, f, "?keys=cleanup.periodDays,nope")

	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a request naming no allowlisted key", code)
	}
	if f.listCalls != 0 {
		t.Errorf("settings list ran %d times for a refused request, want 0", f.listCalls)
	}
}

// A parameter this endpoint does not read is refused, not ignored.
//
// Ignoring one fails OPEN, because "no selection" means the whole allowlist here:
// the selector used to be spelled `key` and take a single name, so the old spelling
// — and any typo of the new one — would answer all six keys while the caller
// believed it had named one. A typo in a VALUE already refuses, so a typo in a NAME
// has to as well.
func TestReadKiroSettings_RefusesAQueryParameterItDoesNotRead(t *testing.T) {
	for _, query := range []string{"?key=telemetry.enabled", "?keys=telemetry.enabled&kyes=x"} {
		t.Run(query, func(t *testing.T) {
			f := &settingsCLI{list: settingsListFixture}

			code, _ := getKiroSettings(t, f, query)

			if code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %q names no parameter this endpoint reads, "+
					"and answering the whole allowlist for it fails open", code, query)
			}
			if f.listCalls != 0 {
				t.Errorf("settings list ran %d times for a refused request, want 0", f.listCalls)
			}
		})
	}
}

// ONE DEADLINE FOR THE WHOLE READ. The per-key fallback is sequential and an
// absent ?keys= means the entire allowlist, so a budget minted per spawn made the
// cost of a degraded read a multiple of the key count — six spawns at 3 s each is
// 18 s of the client's 30 s API timeout for one panel.
//
// One shared deadline is an INSTANT every spawn reports identically, where a
// per-spawn budget computes its own from its own time.Now() and no two agree —
// which is what makes this observable without measuring elapsed time.
func TestReadKiroSettings_TheWholeReadSharesOneDeadline(t *testing.T) {
	// The list read fails, so every requested key takes the per-key door: four
	// spawns for one request, which is the shape a per-spawn budget multiplied.
	f := &settingsCLI{perKey: map[string]string{
		"hooks.showStatus":    "true (global)",
		"telemetry.enabled":   "false (global)",
		"chat.enableSubagent": "true (global)",
	}}

	code, _ := getKiroSettings(t,
		f, "?keys=hooks.showStatus,telemetry.enabled,chat.enableSubagent")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(f.deadlines) != 4 {
		t.Fatalf("saw %d spawns, want 4 (one failed list read plus three per-key reads)",
			len(f.deadlines))
	}
	for i, dl := range f.deadlines {
		if dl.IsZero() {
			t.Errorf("spawn %d ran with no deadline at all", i)
			continue
		}
		if !dl.Equal(f.deadlines[0]) {
			t.Errorf("spawn %d is bounded at %v, spawn 0 at %v: a per-spawn budget makes a "+
				"degraded read cost N × cliTimeouts.Settings", i, dl, f.deadlines[0])
		}
	}
}

// A non-allowlisted key rides in the document kiro-cli answers (cleanup.periodDays
// and toolSearch.enabled are both in it and neither is exposed), so the filter has
// to run on the way out too, not only on the way in.
func TestReadKiroSettings_DropsDocumentKeysOutsideTheAllowlist(t *testing.T) {
	f := &settingsCLI{list: settingsListFixture}

	_, got := getKiroSettings(t, f, "")

	for _, key := range []string{"cleanup.periodDays", "toolSearch.enabled", "app.disableAutoupdates"} {
		if _, ok := got[key]; ok {
			t.Errorf("%q reached the client; the allowlist bounds what this endpoint serves", key)
		}
	}
}

func TestRequestedKiroSettings(t *testing.T) {
	tests := map[string]struct {
		spec string
		want []string
	}{
		"a single key": {
			spec: "telemetry.enabled",
			want: []string{"telemetry.enabled"},
		},
		"whitespace around the names is trimmed": {
			spec: " telemetry.enabled , hooks.showStatus ",
			want: []string{"hooks.showStatus", "telemetry.enabled"},
		},
		"a repeated key is asked for once": {
			spec: "telemetry.enabled,telemetry.enabled",
			want: []string{"telemetry.enabled"},
		},
		"an unknown name is dropped, its siblings survive": {
			spec: "telemetry.enabled,nope",
			want: []string{"telemetry.enabled"},
		},
		"nothing allowed answers nothing, which the handler refuses": {
			spec: "nope,cleanup.periodDays",
			want: nil,
		},
		"an empty entry is not a key": {
			spec: ",,",
			want: nil,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := requestedKiroSettings(tc.spec); !slices.Equal(got, tc.want) {
				t.Errorf("requestedKiroSettings(%q) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}

	// Absent is its own case: every allowlisted key, sorted, so one request over
	// one set always answers the same document.
	got := requestedKiroSettings("")
	want := slices.Sorted(maps.Keys(allowedKiroSettings))
	if !slices.Equal(got, want) {
		t.Errorf("requestedKiroSettings(\"\") = %v, want the sorted allowlist %v", got, want)
	}
}

func TestParseKiroSettingsList(t *testing.T) {
	t.Run("native JSON types become the strings the per-key form answers", func(t *testing.T) {
		got, err := parseKiroSettingsList([]byte(
			`{"hooks.showStatus":true,"telemetry.enabled":false,"chat.enableKnowledge":"true"}`,
		))
		if err != nil {
			t.Fatalf("parseKiroSettingsList: %v", err)
		}
		want := map[string]string{
			"hooks.showStatus":     "true",
			"telemetry.enabled":    "false",
			"chat.enableKnowledge": "true",
		}
		if !maps.Equal(got, want) {
			t.Errorf("parsed = %v, want %v", got, want)
		}
	})

	t.Run("output that is not a JSON object is an error, not an empty answer", func(t *testing.T) {
		if _, err := parseKiroSettingsList([]byte("error: No such file or directory")); err == nil {
			t.Error("parseKiroSettingsList accepted a non-JSON document; the caller could not " +
				"tell it apart from a document with no keys and would never fall back")
		}
	})
}

// The overlay is what makes `settings` work at all: kiro-cli is a multi-call
// binary and that subcommand re-execs a sibling resolved through PATH, so a spawn
// carrying only the absolute path exits 1 with "No such file or directory".
//
// Asserted on the CHILD's environment rather than on kiro-cli, which this
// container's test run does not have: the property is that the resolver's names
// reach the process, and that they land LAST so an inherited value of the same
// name loses.
func TestExecCLIRunner_AppliesTheEnvironmentOverlay(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	t.Setenv("VIBEKIT_OVERLAY_PROBE", "inherited")
	r := &execCLIRunner{
		cliPath: func() string { return sh },
		env:     func() []string { return []string{"VIBEKIT_OVERLAY_PROBE=overlaid"} },
	}

	out, _, err := r.RunStdoutCapped(t.Context(), 1024, "-c", `printf %s "$VIBEKIT_OVERLAY_PROBE"`)
	if err != nil {
		t.Fatalf("RunStdoutCapped: %v", err)
	}
	if got := string(out); got != "overlaid" {
		t.Errorf("child read VIBEKIT_OVERLAY_PROBE=%q, want %q: the overlay has to reach the "+
			"spawn and has to win over an inherited value of the same name", got, "overlaid")
	}
}

// A nil resolver means inherit implicitly, which is the shape with no install
// manager wired. It must not blank the child's environment.
func TestExecCLIRunner_NoOverlayInheritsTheParentEnvironment(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	t.Setenv("VIBEKIT_OVERLAY_PROBE", "inherited")
	r := &execCLIRunner{cliPath: func() string { return sh }}

	out, _, err := r.RunStdoutCapped(t.Context(), 1024, "-c", `printf %s "$VIBEKIT_OVERLAY_PROBE"`)
	if err != nil {
		t.Fatalf("RunStdoutCapped: %v", err)
	}
	if got := string(out); got != "inherited" {
		t.Errorf("child read VIBEKIT_OVERLAY_PROBE=%q, want %q", got, "inherited")
	}
	// Guard the premise: this test says nothing if the variable never made it
	// into the test process either.
	if os.Getenv("VIBEKIT_OVERLAY_PROBE") != "inherited" {
		t.Fatal("Setup: the probe variable is not set in the test process")
	}
}
