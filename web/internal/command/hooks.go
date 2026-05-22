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

	"vibekit/internal/api"
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
}

// validateHookPayload decodes + validates a CmdCreateHook payload.
func validateHookPayload(cmd *api.ClientCommand) (p hookCreatePayload, safeName string, code int, err error) {
	if uErr := json.Unmarshal(cmd.Payload, &p); uErr != nil || p.Name == "" || p.EventType == "" {
		return p, "", http.StatusBadRequest, errInvalidPayload
	}
	if len(p.Name) > MaxHookField || len(p.Description) > MaxHookField ||
		len(p.EventType) > MaxHookField || len(p.ActionType) > MaxHookField ||
		len(p.Prompt) > MaxHookField || len(p.Command) > MaxHookField ||
		len(p.Patterns) > MaxHookField {
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

// buildHookDoc renders the JSON document written to .kiro/hooks/<name>.json.
func buildHookDoc(p *hookCreatePayload) map[string]any {
	when := map[string]any{keyType: p.EventType}
	then := map[string]any{keyType: p.ActionType}
	hook := map[string]any{
		keyName:   p.Name,
		"version": "1.0.0",
		"when":    when,
		"then":    then,
	}
	if p.Description != "" {
		hook["description"] = p.Description
	}
	if p.Patterns != "" {
		when["patterns"] = strings.Split(p.Patterns, ",")
	}
	if p.ActionType == "askAgent" && p.Prompt != "" {
		then["prompt"] = p.Prompt
	}
	if p.ActionType == "runCommand" && p.Command != "" {
		then["command"] = p.Command
	}
	return hook
}

// CmdCreateHook creates a hook file from chat context.
func CmdCreateHook(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	_ = ctx // reserved for future use
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
	if err := api.SaveBytes(hookPath, data, 0o600); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	relPath := filepath.Join(".kiro", "hooks", safeName+".json")
	slog.Info("hook created from chat", keyName, p.Name, "path", relPath)
	d.Respond(w, cmd.RequestID, map[string]any{"ok": true, "path": relPath})
}
