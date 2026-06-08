package server

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

const (
	jsonKeyOutput = api.JSONKeyOutput
	jsonKeyModels = "models"
)

// CLIRunner abstracts subprocess execution for kiro-cli commands,
// enabling unit testing of handler logic without a real binary.
type CLIRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// execCLIRunner is the production CLIRunner that shells out to cliPath.
type execCLIRunner struct {
	cliPath string
}

func (r *execCLIRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, r.cliPath, args...).CombinedOutput() //nolint:gosec // G204: binary path from config
}

// cliTimeouts holds the timeout budget for each kiro-cli subprocess
// invocation. Named fields make the budget inspectable and tunable.
type cliTimeouts struct {
	Models       time.Duration
	Version      time.Duration
	Diagnostics  time.Duration
	Settings     time.Duration
	ToolsInstall time.Duration
}

// defaultCLITimeouts returns the production timeout budget.
func defaultCLITimeouts() cliTimeouts {
	return cliTimeouts{
		Models:       5 * time.Second,
		Version:      2 * time.Second,
		Diagnostics:  20 * time.Second,
		Settings:     3 * time.Second,
		ToolsInstall: 10 * time.Minute,
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
	"chat.enableContextUsageIndicator":       {Kind: settingBool},
	"chat.disableAutoCompaction":             {Kind: settingBool},
	"hooks.showStatus":                       {Kind: settingBool},
	"telemetry.enabled":                      {Kind: settingBool},
	"toolSearch.enabled":                     {Kind: settingBool},
	"compaction.excludeContextWindowPercent": {Kind: settingInt},
	"compaction.excludeMessages":             {Kind: settingInt},
	"cleanup.periodDays":                     {Kind: settingInt},
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
