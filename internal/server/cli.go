package server

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

const (
	jsonKeyOutput = api.JSONKeyOutput
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
	stdout := &cappedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.cliPath(), args...) //nolint:gosec // G204: binary path from the install manager, never user input
	cmd.Stdout = stdout
	cmd.Stderr = &api.LimitedWriter{W: &stderr, N: cliStderrCap}
	err = cmd.Run()
	if stderr.Len() > 0 {
		slog.Debug("cli stderr captured", "args", args, "stderr", stderr.String())
	}
	return stdout.data, stdout.overflow, err
}

// cappedBuffer is an io.Writer that collects up to limit bytes and drops
// the rest, recording whether any bytes were dropped (overflow). Write
// always reports a full write, so a subprocess streaming into it via
// os/exec is never killed by a short write once the cap is reached.
type cappedBuffer struct {
	data     []byte
	limit    int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	switch room := c.limit - len(c.data); {
	case room <= 0:
		if n > 0 {
			c.overflow = true
		}
	case n > room:
		c.overflow = true
		c.data = append(c.data, p[:room]...)
	default:
		c.data = append(c.data, p...)
	}
	return n, nil
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

var allowedKiroSettings = map[string]settingMeta{
	"chat.enableCheckpoint":                  {Kind: settingBool},
	"chat.enableTodoList":                    {Kind: settingBool},
	"chat.enableKnowledge":                   {Kind: settingBool},
	"chat.enableSubagent":                    {Kind: settingBool},
	"chat.enablePromptHints":                 {Kind: settingBool},
	"chat.disableAutoCompaction":             {Kind: settingBool},
	"hooks.showStatus":                       {Kind: settingBool},
	"telemetry.enabled":                      {Kind: settingBool},
	"toolSearch.enabled":                     {Kind: settingBool},
	"compaction.excludeContextWindowPercent": {Kind: settingInt},
	"compaction.excludeMessages":             {Kind: settingInt},
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

func parseKiroSettingOutput(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '('); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
