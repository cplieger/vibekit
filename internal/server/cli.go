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
	// diagnosticsMaxBytes caps the report returned to the browser. stdout past
	// this is dropped and the report is marked "[truncated]".
	diagnosticsMaxBytes = 256 * 1024 // 256 KiB

	// cliStderrCap bounds stderr capture in RunStdoutCapped. stderr is logged for
	// diagnosis and never returned, so a tail is enough to bound a hostile child.
	cliStderrCap = 32 * 1024 // 32 KiB

	// settingsListMaxBytes caps the `settings list` document. The whole object is
	// a few hundred bytes, so this is the hostile-output bound, not a budget.
	settingsListMaxBytes = 64 * 1024 // 64 KiB
)

// CLIRunner abstracts subprocess execution for kiro-cli commands.
type CLIRunner interface {
	// Run executes the CLI and returns combined stdout+stderr.
	Run(ctx context.Context, args ...string) ([]byte, error)
	// RunStdoutCapped executes the CLI capturing STDOUT only, stopping at limit
	// bytes and reporting whether it truncated. stderr is captured separately,
	// bounded and logged — never returned, never merged into the result.
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
	// Load-bearing rather than symmetric with cliPath: kiro-cli is a multi-call
	// binary and `settings` re-execs a SIBLING (kiro-cli-chat) resolved by a plain
	// PATH search, so the absolute path alone does not reach it. Measured on 2.20.2,
	// `settings list` exits 1 with "No such file or directory" when the version
	// directory does not lead PATH. OPTIONAL: nil inherits the parent environment.
	env func() []string
}

// command builds the spawn both methods run.
func (r *execCLIRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.cliPath(), args...) //nolint:gosec // G204: binary path from the install manager, never user input
	if r.env != nil {
		// The overlay lands LAST: os/exec keeps the last value for a repeated key,
		// so the container's own PATH would otherwise win the search.
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

// cliTimeouts holds the timeout budget for each kiro-cli subprocess invocation.
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
// No key in allowedKiroSettings currently declares settingInt. The kind and its
// arm in safeKiroSettingValueFor stay anyway: they are the endpoint's
// value-validation vocabulary, so the next numeric setting arrives already bounded.
type settingKind int

const (
	settingBool settingKind = iota
	settingInt
	_settingKindCount // must remain last — compile-time exhaustiveness guard
)

// Fails to compile if a settingKind is added without updating safeKiroSettingValueFor.
var _ = [1]struct{}{}[_settingKindCount-2]

// settingMeta carries validation metadata for an allowed kiro-cli setting.
type settingMeta struct {
	Kind settingKind
}

// allowedKiroSettings bounds what /api/kiro-settings can read and write.
//
// A key belongs here only if it has a kiro-cli-SIDE role. KAS's ACP path reads no
// kiro-cli setting at all — measured on the stock 2.19.2 bundle, whose only
// `chat.*` occurrences are `@see kiro-cli:` cross-references in the settings schema
// rather than reads. So a write here reaches the TUI, the index builder and
// vibekit's own suppression logic, and can never reach a running vibekit chat.
// Anything that must change a chat goes through `internal/kascap`'s table instead.
var allowedKiroSettings = map[string]settingMeta{
	"chat.enableKnowledge":   {Kind: settingBool},
	"chat.enableSubagent":    {Kind: settingBool},
	"chat.enablePromptHints": {Kind: settingBool},
	"hooks.showStatus":       {Kind: settingBool},
	"telemetry.enabled":      {Kind: settingBool},
	// cleanup.periodDays is deliberately NOT here: vibekit pins it to 0/never at
	// boot and owns chat retention itself, so exposing it would let the UI
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
// value. `before != ""` is the guard for a value that is entirely parenthesized,
// which is a value rather than a suffix.
func parseKiroSettingOutput(s string) string {
	s = strings.TrimSpace(s)
	if before, _, found := strings.CutLast(s, "("); found && before != "" {
		s = strings.TrimSpace(before)
	}
	return s
}

// settingsListArgs reads EVERY kiro-cli setting in ONE invocation: measured on
// 2.20.2 with the version directory leading PATH, it exits 0 and writes one flat
// JSON object of every key, dotted names with native JSON types. The per-key form
// cost one spawn per key, three of them concurrently on the General panel.
var settingsListArgs = []string{"settings", "list", "--format", "json"}

// parseKiroSettingsList maps the settings-list document to the string values the
// per-key form answers, keeping only the allowlisted keys. One spelling for two
// doors: values are native JSON here and a scope-suffixed string in the per-key
// form, and the client compares against "true"/"false" either way.
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
// does not read. An ignored parameter is indistinguishable from no selection,
// which here means "answer the whole allowlist" — so ignoring one fails OPEN.
func unknownKiroSettingsQuery(q url.Values) bool {
	for name := range q {
		if name != kiroSettingsKeysParam {
			return true
		}
	}
	return false
}

// requestedKiroSettings resolves the ?keys= parameter to the allowlisted keys to
// answer, sorted so one request over one set always answers the same document. An
// absent parameter means every allowlisted key, and the one spawn behind it costs
// the same either way. Unknown names are dropped rather than answered.
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
