package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/procout"
)

const (
	jsonKeyOutput = httpreply.JSONKeyOutput
)

const (
	// diagnosticsMaxBytes caps the diagnostics report returned to the
	// browser so a runaway `kiro-cli diagnostic` dump can't bloat the HTTP
	// response. stdout past this is dropped and the report is marked
	// "[truncated]".
	diagnosticsMaxBytes = 256 * 1024 // 256 KiB

	// cliStderrCap bounds stderr capture in RunStdoutCapped. stderr is
	// logged for diagnosis, never returned to the client, so a modest tail
	// is enough and a hostile subprocess can't OOM the container.
	cliStderrCap = 32 * 1024 // 32 KiB

	// settingsListMaxBytes caps the `settings list` document. The whole object
	// is a few hundred bytes on 2.20.2 (eleven keys of scalars), so this is the
	// hostile-output bound rather than a working budget.
	settingsListMaxBytes = 64 * 1024 // 64 KiB
)

// CLIRunner abstracts subprocess execution for kiro-cli commands,
// enabling unit testing of handler logic without a real binary.
type CLIRunner interface {
	// Run executes the CLI and returns combined stdout+stderr.
	Run(ctx context.Context, args ...string) ([]byte, error)
	// RunStdoutCapped executes the CLI capturing STDOUT only (stderr is
	// captured separately, bounded, and logged — never returned or merged
	// into the result), stopping the captured stdout at limit bytes. It
	// returns the possibly-truncated stdout, whether truncation occurred,
	// and any exec error.
	RunStdoutCapped(ctx context.Context, limit int, args ...string) (out []byte, truncated bool, err error)
}

// execCLIRunner is the production CLIRunner that shells out to the kiro-cli
// binary cliPath resolves to.
//
// The path is a FUNCTION, not a string: the install manager selects the active
// version after the listener binds and can switch it later, so a value captured
// at construction would pin every shell-out to whatever was installed first —
// and on a first boot that is the empty string.
type execCLIRunner struct {
	cliPath func() string
	// env is the environment overlay for a kiro-cli spawn — pinstall's
	// Manager.PathEnv, which leads PATH with the active version directory.
	//
	// Load-bearing rather than symmetric with cliPath. kiro-cli is a multi-call
	// binary and `settings` re-execs a SIBLING (kiro-cli-chat) resolved by a
	// plain PATH search, so the absolute path alone does not reach it: measured
	// on the installed 2.20.2, `<version-dir>/kiro-cli settings list --format
	// json` exits 1 with "No such file or directory (os error 2)" when the
	// version directory does not lead PATH and exits 0 with the whole object
	// when it does. `--version` and `diagnostic` are answered by the main binary
	// and read the same either way.
	//
	// OPTIONAL: nil means inherit the parent environment implicitly, which is
	// what a test driving this runner at /bin/sh wants.
	env func() []string
}

// command builds the spawn both methods run.
func (r *execCLIRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.cliPath(), args...) //nolint:gosec // G204: binary path from the install manager, never user input
	if r.env != nil {
		// The overlay lands LAST: os/exec keeps the last value for a repeated
		// key, so the container's own PATH would otherwise win the search that
		// has to land inside the verified install.
		cmd.Env = append(os.Environ(), r.env()...)
	}
	return cmd
}

func (r *execCLIRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, args...).CombinedOutput()
}

func (r *execCLIRunner) RunStdoutCapped(ctx context.Context, limit int, args ...string) (out []byte, truncated bool, err error) {
	stdout := procout.NewBuffer(limit)
	stderr := procout.NewBuffer(cliStderrCap)
	cmd := r.command(ctx, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if stderr.Len() > 0 {
		slog.Debug("cli stderr captured", "args", args, "stderr", stderr.String())
	}
	return stdout.Bytes(), stdout.Truncated(), err
}

// cliTimeouts holds the timeout budget for each kiro-cli subprocess
// invocation. Named fields make the budget inspectable and tunable.
type cliTimeouts struct {
	Version     time.Duration
	Diagnostics time.Duration
	Settings    time.Duration
}

// defaultCLITimeouts returns the production timeout budget.
func defaultCLITimeouts() cliTimeouts {
	return cliTimeouts{
		Version:     2 * time.Second,
		Diagnostics: 20 * time.Second,
		Settings:    3 * time.Second,
	}
}

// settingKind distinguishes boolean-only from numeric-only kiro-cli settings.
//
// No key in allowedKiroSettings currently declares settingInt: the only two that
// ever did were the compaction number fields, removed in 2026-08 once the object
// they wrote measured as having no reader upstream. The kind and its arm in
// safeKiroSettingValueFor STAY, because they are the endpoint's value-validation
// vocabulary rather than a live code path — a reader should not have to
// rediscover how a numeric setting is bounded when the next one arrives.
type settingKind int

const (
	settingBool settingKind = iota
	settingInt
	_settingKindCount // must remain last — compile-time exhaustiveness guard
)

// Compile-time assertion: if a new settingKind is added without updating
// safeKiroSettingValueFor, this line will fail to compile.
var _ = [1]struct{}{}[_settingKindCount-2]

// settingMeta carries validation metadata for an allowed kiro-cli setting.
type settingMeta struct {
	Kind settingKind
}

// allowedKiroSettings bounds what /api/kiro-settings can read and write.
//
// A key belongs here only if it has a kiro-cli-SIDE role. KAS's ACP path reads
// no kiro-cli setting at all — measured on the stock 2.19.2 bundle, which
// contains zero occurrences of `cli.json`, `kiro-cli/settings`,
// `readSettingsFile` and `loadCliSettings`, and exactly one occurrence of each
// `chat.*` literal, every one of them a `@see kiro-cli:` cross-reference inside
// the settings schema rather than a read. So a write through this endpoint
// reaches the TUI, the index builder and vibekit's own suppression logic, and it
// can never reach a running vibekit chat. Anything that has to change a chat
// goes through `_meta.kiro.settings`, which is `internal/kascap`'s table.
//
// Four keys were REMOVED on that measurement (2026-08-26), and they split two
// ways. `compaction.excludeContextWindowPercent` and `compaction.excludeMessages`
// map onto a `compaction` object KAS declares in its own schema and never reads,
// so no wiring could have made them work. `chat.disableAutoCompaction` and
// `toolSearch.enabled` DO have live ACP readers, so their controls were not
// deleted for the same reason: tool search moved onto the ACP key that reaches
// its reader, and auto-compaction was dropped because its ON state breaks a
// documented vibekit invariant (see internal/kascap/table.go's
// disableAutoCompaction row for the three prerequisites a future attempt needs).
var allowedKiroSettings = map[string]settingMeta{
	"chat.enableKnowledge":   {Kind: settingBool},
	"chat.enableSubagent":    {Kind: settingBool},
	"chat.enablePromptHints": {Kind: settingBool},
	"hooks.showStatus":       {Kind: settingBool},
	"telemetry.enabled":      {Kind: settingBool},
	// cleanup.periodDays is deliberately NOT here: vibekit pins it to 0/never
	// at boot and owns chat retention itself (Settings → General writes
	// vibekit's own chat_retention_days). Exposing it would let the UI
	// re-enable kiro-cli's competing purge.
	"chat.disableInheritingDefaultResources": {Kind: settingBool},
}

func safeKiroSetting(k string) string {
	if _, ok := allowedKiroSettings[k]; ok {
		return k
	}
	return ""
}

func safeKiroSettingValueFor(v string, kind settingKind) string {
	switch kind {
	case settingBool:
		if v == "true" || v == "false" {
			return v
		}
		return ""
	case settingInt:
		for _, c := range v {
			if c < '0' || c > '9' {
				return ""
			}
		}
		if v != "" && len(v) <= 4 {
			return v
		}
		return ""
	}
	return ""
}

// parseKiroSettingOutput strips the scope suffix kiro-cli appends to every
// non-empty setting value ("true (global)", "0 (local)") and returns the bare
// value.
//
// strings.CutLast (Go 1.27) rather than LastIndexByte plus a manual slice: the
// operation IS a split around the last separator, keeping the before half.
// `before != ""` is the same guard as the old `i > 0` — before is empty exactly
// when the "(" is at index 0 — and it says why the guard exists (a value that is
// entirely parenthesized is a value, not a suffix) instead of leaving a reader to
// infer it from an index comparison. found is required as well: on no match
// CutLast returns the whole input as before, which would strip nothing but would
// re-TrimSpace an already-trimmed string.
func parseKiroSettingOutput(s string) string {
	s = strings.TrimSpace(s)
	if before, _, found := strings.CutLast(s, "("); found && before != "" {
		s = strings.TrimSpace(before)
	}
	return s
}

// settingsListArgs reads EVERY kiro-cli setting in ONE invocation.
//
// Measured on the installed 2.20.2 with the version directory leading PATH:
// exits 0 and writes one flat JSON object of every key — dotted names, native
// JSON types:
//
//	{"app.disableAutoupdates":true,"chat.enableKnowledge":true,
//	 "cleanup.periodDays":0,"telemetry.enabled":false, …}
//
// So the whole allowlist costs one spawn, where the per-key form cost one spawn
// per key and the Settings → General panel opened three of them concurrently,
// each with its own 3 s budget.
var settingsListArgs = []string{"settings", "list", "--format", "json"}

// parseKiroSettingsList maps the settings-list document to the string values the
// per-key form answers, keeping only the allowlisted keys.
//
// One spelling for two doors: values are native JSON here and a scope-suffixed
// string in the per-key form ("false (global)"), and the client compares against
// "true"/"false" without knowing which door answered.
func parseKiroSettingsList(raw []byte) (map[string]string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(allowedKiroSettings))
	for k, v := range obj {
		if safeKiroSetting(k) == "" {
			continue
		}
		out[k] = kiroSettingValueText(v)
	}
	return out, nil
}

// kiroSettingValueText renders one JSON setting value as the string the wire
// carries: a JSON string is unquoted, a bool or number is its own literal.
func kiroSettingValueText(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(v))
}

// kiroSettingsKeysParam is the ONE query parameter GET /api/kiro-settings reads.
// Named so unknownKiroSettingsQuery and the reader cannot disagree about it.
const kiroSettingsKeysParam = "keys"

// unknownKiroSettingsQuery reports whether q carries a parameter this endpoint
// does not read.
//
// An ignored parameter is indistinguishable from no selection, which here means
// "answer the whole allowlist" — so ignoring one fails OPEN. The selector used to
// be `key` and take a single name, so `?key=telemetry.enabled`, and any typo of the
// new spelling, would answer all six keys. A typo in a VALUE already refuses.
func unknownKiroSettingsQuery(q url.Values) bool {
	for name := range q {
		if name != kiroSettingsKeysParam {
			return true
		}
	}
	return false
}

// requestedKiroSettings resolves the ?keys= parameter to the allowlisted keys to
// answer, sorted so one request over one set always answers the same document.
//
// An absent parameter means every allowlisted key: a settings read with no
// selection is the whole document, and the one spawn behind it costs the same
// either way. Unknown names are dropped rather than answered, so a caller that
// names nothing allowed gets the same refusal a typo used to get.
func requestedKiroSettings(spec string) []string {
	if strings.TrimSpace(spec) == "" {
		return slices.Sorted(maps.Keys(allowedKiroSettings))
	}
	var out []string
	for name := range strings.SplitSeq(spec, ",") {
		key := safeKiroSetting(strings.TrimSpace(name))
		if key == "" || slices.Contains(out, key) {
			continue
		}
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
