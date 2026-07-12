package command

// Hook creation command handler.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// MaxHookField caps the per-field size for CmdCreateHook payloads.
const MaxHookField = 8 * 1024

// validHookNameRe restricts hook filenames to a strict single-segment
// allowlist: lowercase ASCII, digits, underscore, hyphen, 1-64 chars,
// must start with an alphanumeric.
var validHookNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// hookCreatePayload is the decoded shape for CmdCreateHook.
type hookCreatePayload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	EventType   string `json:"event_type"`
	ActionType  string `json:"action_type"` // "askAgent" or "runCommand"
	Prompt      string `json:"prompt,omitempty"`
	Command     string `json:"command,omitempty"`
	Patterns    string `json:"patterns,omitempty"`
	Timeout     int    `json:"timeout,omitempty"` // optional per-hook timeout (seconds)
}

// hookFieldsExceedLimit reports whether any CmdCreateHook string field
// exceeds the per-field MaxHookField cap.
func hookFieldsExceedLimit(p *hookCreatePayload) bool {
	return len(p.Name) > MaxHookField || len(p.Description) > MaxHookField ||
		len(p.EventType) > MaxHookField || len(p.ActionType) > MaxHookField ||
		len(p.Prompt) > MaxHookField || len(p.Command) > MaxHookField ||
		len(p.Patterns) > MaxHookField
}

// validateHookPayload decodes + validates a CmdCreateHook payload.
func validateHookPayload(cmd *api.ClientCommand) (p hookCreatePayload, safeName string, code int, err error) {
	if uErr := json.Unmarshal(cmd.Payload, &p); uErr != nil || p.Name == "" || p.EventType == "" {
		return p, "", http.StatusBadRequest, ErrInvalidPayload
	}
	if hookFieldsExceedLimit(&p) {
		return p, "", http.StatusRequestEntityTooLarge,
			errors.New("hook field too large")
	}
	switch p.ActionType {
	case "askAgent":
		if strings.TrimSpace(p.Prompt) == "" {
			return p, "", http.StatusBadRequest,
				errors.New("askAgent hook requires a non-empty prompt")
		}
	case "runCommand":
		if strings.TrimSpace(p.Command) == "" {
			return p, "", http.StatusBadRequest,
				errors.New("runCommand hook requires a non-empty command")
		}
	default:
		return p, "", http.StatusBadRequest,
			errors.New("action_type must be askAgent or runCommand")
	}
	safeName = strings.ReplaceAll(strings.ToLower(p.Name), " ", "-")
	if !validHookNameRe.MatchString(safeName) {
		return p, "", http.StatusBadRequest,
			errors.New("hook name must be 1-64 chars, start with a letter or digit, and contain only [a-z0-9_-]")
	}
	return p, safeName, 0, nil
}

// v1 hook schema (the standalone KAS/v3 .kiro/hooks/<name>.json shape).
// The document wraps a single hook in a versioned envelope:
//
//	{ "version": "v1", "hooks": [ { name, trigger, matcher?, action, timeout? } ] }
//
// trigger is PascalCase (see normalizeTrigger); action.type is "command"
// (carries command) or "agent" (carries prompt). This replaces the old
// embedded 2.x shape (a top-level hook object with when/then blocks).
type hookAction struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

type hookEntry struct {
	Name        string     `json:"name"`
	Trigger     string     `json:"trigger"`
	Description string     `json:"description,omitempty"`
	Matcher     string     `json:"matcher,omitempty"`
	Action      hookAction `json:"action"`
	Timeout     int        `json:"timeout,omitempty"`
}

type hookDoc struct {
	Version string      `json:"version"`
	Hooks   []hookEntry `json:"hooks"`
}

// hookTriggers maps event-type payload values (vibekit's own vocabulary
// plus v2 / Kiro-IDE camelCase aliases) to the PascalCase trigger names
// KAS v1 hooks expect. Keys are lowercased for case-insensitive lookup.
//
//nolint:goconst // alias lookup table: the PascalCase trigger names are map values, not scattered magic strings.
var hookTriggers = map[string]string{
	// Canonical PascalCase names (self-map via their lowercase key).
	"sessionstart":     "SessionStart",
	"stop":             "Stop",
	"pretooluse":       "PreToolUse",
	"posttooluse":      "PostToolUse",
	"pretaskexec":      "PreTaskExec",
	"posttaskexec":     "PostTaskExec",
	"userpromptsubmit": "UserPromptSubmit",
	"postfilecreate":   "PostFileCreate",
	"postfilesave":     "PostFileSave",
	"postfiledelete":   "PostFileDelete",
	"manual":           "Manual",
	// v2 / Kiro-IDE camelCase aliases.
	"agentstop":         "Stop",
	"promptsubmit":      "UserPromptSubmit",
	"userprompt":        "UserPromptSubmit",
	"pretaskexecution":  "PreTaskExec",
	"posttaskexecution": "PostTaskExec",
	"filecreate":        "PostFileCreate",
	"filecreated":       "PostFileCreate",
	"filesave":          "PostFileSave",
	"filesaved":         "PostFileSave",
	"fileedit":          "PostFileSave",
	"fileedited":        "PostFileSave",
	"filedelete":        "PostFileDelete",
	"filedeleted":       "PostFileDelete",
	"usertriggered":     "Manual",
}

// normalizeTrigger maps a client event-type value to its PascalCase v1
// trigger. Unrecognised values pass through trimmed (best effort) so a
// trigger this map doesn't yet know about isn't blocked server-side.
func normalizeTrigger(eventType string) string {
	trimmed := strings.TrimSpace(eventType)
	if t, ok := hookTriggers[strings.ToLower(trimmed)]; ok {
		return t
	}
	return trimmed
}

// buildHookAction maps vibekit's action_type payload ("askAgent" /
// "runCommand") to the v1 action shape. validateHookPayload guarantees
// one of the two values, so the default arm is defensive only.
func buildHookAction(p *hookCreatePayload) hookAction {
	switch p.ActionType {
	case "runCommand":
		return hookAction{Type: "command", Command: p.Command}
	case "askAgent":
		return hookAction{Type: "agent", Prompt: p.Prompt}
	default:
		return hookAction{Type: p.ActionType}
	}
}

// buildHookDoc renders the v1 hook document written to
// .kiro/hooks/<name>.json. vibekit's event-type/action-type payload is
// mapped to the KAS v1 schema: a PascalCase trigger, an
// action.{type, command|prompt}, and optional matcher/timeout. Patterns
// become the single-regex matcher; an unset/zero timeout is omitted.
func buildHookDoc(p *hookCreatePayload) hookDoc {
	entry := hookEntry{
		Name:        p.Name,
		Trigger:     normalizeTrigger(p.EventType),
		Description: p.Description,
		Matcher:     strings.TrimSpace(p.Patterns),
		Action:      buildHookAction(p),
	}
	if p.Timeout > 0 {
		entry.Timeout = p.Timeout
	}
	return hookDoc{Version: "v1", Hooks: []hookEntry{entry}}
}

// CmdCreateHook creates a hook file from chat context.
func CmdCreateHook(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	p, safeName, code, vErr := validateHookPayload(cmd)
	if vErr != nil {
		d.RespondErr(w, code, vErr)
		return
	}
	hook := buildHookDoc(&p)

	hookPath := filepath.Join(deps.WorkDir(), ".kiro", "hooks", safeName+".json")
	if _, err := os.Stat(hookPath); err == nil {
		d.RespondErr(w, http.StatusConflict,
			errors.New("a hook with this name already exists"))
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	data, err := json.MarshalIndent(hook, "", "  ")
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := atomicfile.WriteFile(ctx, hookPath, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	relPath := filepath.Join(".kiro", "hooks", safeName+".json")
	slog.Info("hook created from chat", keyName, p.Name, "path", relPath)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{"path": relPath}))
}
