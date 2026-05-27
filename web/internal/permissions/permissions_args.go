package permissions

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	cfgsettings "vibekit/internal/settings"
)

type mode string

const (
	modePrompt    mode = "prompt"
	modeTrustList mode = "trust-list"
	modeTrustAll  mode = "trust-all"
)

// Valid reports whether m is a recognised permission mode value.
func (m mode) Valid() bool {
	switch m {
	case modePrompt, modeTrustList, modeTrustAll:
		return true
	}
	return false
}

// maxToolNameLen caps hand-edited config.json entries to avoid
// pathological CLI-arg blow-up. Real kiro-cli tool names are well
// under 20 chars, but MCP-namespaced tools follow the
// `mcp__<server>__<tool>` shape where both components are
// user-chosen — a server name like "github-enterprise-read-write"
// plus a long tool name can legitimately push past 64. 128 covers
// every MCP name observed upstream with safe headroom while
// staying well below any CLI argv limit (argv is at most ~128 KiB
// on Linux, and kiro-cli joins names with commas, so even hundreds
// of entries at 128 fit comfortably).
const maxToolNameLen = 128

type settings struct {
	Mode       mode     `json:"permission_mode,omitempty"`
	TrustTools []string `json:"trust_tools,omitempty"`
}

// readSettingsRaw is the shared I/O + parse path for all permission
// readers. It returns the parsed top-level keys from config.json.
// Callers apply their own fail-mode policy to the returned error.
func readSettingsRaw(ctx context.Context, configDir string) (map[string]json.RawMessage, error) {
	data, err := cfgsettings.ReadBytes(ctx, configDir)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Args returns the kiro-cli CLI flags corresponding to the current
// permission settings. Never returns nil; an empty slice means "no flag,
// prompt for everything".
func Args(ctx context.Context, configDir string) []string {
	s := read(ctx, configDir)
	switch s.Mode {
	case modePrompt:
		return []string{}
	case modeTrustList:
		clean := cleanList(s.TrustTools)
		if len(clean) == 0 {
			if len(s.TrustTools) > 0 {
				slog.Warn("permissions: all trust_tools entries rejected, downgrading to prompt",
					"raw_count", len(s.TrustTools))
			}
			return []string{}
		}
		if dropped := len(s.TrustTools) - len(clean); dropped > 0 {
			slog.Debug("permissions: trust_tools entries dropped by sanitiser",
				"raw_count", len(s.TrustTools), "kept", len(clean), "dropped", dropped)
		}
		return []string{"--trust-tools", strings.Join(clean, ",")}
	case modeTrustAll:
		return []string{"--trust-all-tools"}
	}
	// Default: fail open to trust-all.
	return []string{"--trust-all-tools"}
}

func read(ctx context.Context, configDir string) settings {
	raw, err := readSettingsRaw(ctx, configDir)
	if err != nil {
		slog.Warn("permissions: read config.json", "error", err)
		return settings{Mode: modeTrustAll}
	}
	if raw == nil {
		return settings{Mode: modeTrustAll}
	}
	var s settings
	if m, ok := raw["permission_mode"]; ok {
		if err := json.Unmarshal(m, &s.Mode); err != nil {
			slog.Warn("permissions: parse mode", "error", err)
			return settings{Mode: modeTrustAll}
		}
		if s.Mode != "" && !s.Mode.Valid() {
			slog.Warn("permissions: unknown permission_mode, defaulting to trust-all", "value", string(s.Mode))
			s.Mode = modeTrustAll
		}
	}
	if t, ok := raw["trust_tools"]; ok {
		if err := json.Unmarshal(t, &s.TrustTools); err != nil {
			slog.Warn("permissions: parse trust_tools", "error", err)
		}
	}
	return s
}

// cleanList trims whitespace, drops empty strings, dedups, and filters to
// sane tool-name characters. Defensive against hand-edited config.json.
func cleanList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !validToolName(n) {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func validToolName(s string) bool {
	if s == "" || len(s) > maxToolNameLen {
		return false
	}
	if !isToolNameIdentStart(rune(s[0])) {
		return false
	}
	for _, r := range s {
		if !isToolNameChar(r) {
			return false
		}
	}
	return true
}

// isToolNameIdentStart reports whether r is a valid first rune of a
// tool name: letter, digit, or underscore. Excludes `-` and `.`
// (see validToolName).
func isToolNameIdentStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '_'
}

// isToolNameChar reports whether r is a valid rune anywhere in a
// tool name: letter, digit, `_`, `-`, or `.`.
func isToolNameChar(r rune) bool {
	return isToolNameIdentStart(r) || r == '-' || r == '.'
}

// SupervisedDefault reports the settings-file-wide default for the
// Supervised-mode flag applied to newly-auto-created chats.
func SupervisedDefault(ctx context.Context, configDir string) bool {
	var b bool
	if !cfgsettings.FieldInto(ctx, configDir, "supervised_default", "supervised_default", &b) {
		return false
	}
	return b
}
