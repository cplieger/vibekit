package server

import (
	"context"
	"log/slog"
	"os/exec"
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
}

func (r *execCLIRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, r.cliPath(), args...).CombinedOutput() //nolint:gosec // G204: binary path from the install manager, never user input
}

func (r *execCLIRunner) RunStdoutCapped(ctx context.Context, limit int, args ...string) (out []byte, truncated bool, err error) {
	stdout := procout.NewBuffer(limit)
	stderr := procout.NewBuffer(cliStderrCap)
	cmd := exec.CommandContext(ctx, r.cliPath(), args...) //nolint:gosec // G204: binary path from the install manager, never user input
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
