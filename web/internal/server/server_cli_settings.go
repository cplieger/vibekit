package server

import "strings"

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
