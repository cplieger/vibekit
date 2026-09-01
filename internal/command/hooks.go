package command

// Hook creation command handler.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
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
func validateHookPayload(cmd *vibekit.ClientCommand) (p hookCreatePayload, safeName string, code int, err error) {
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
	trigger, known := vibekit.NormalizeHookTrigger(p.EventType)
	if !known {
		return p, "", http.StatusBadRequest,
			fmt.Errorf("event_type %q is not a trigger kiro-cli loads; expected one of: %s",
				p.EventType, vibekit.KnownHookTriggers())
	}
	// A matcher on a trigger that has nothing to match on is always a typo,
	// and upstream will not say so — KAS logs its own warning with nothing
	// on the wire, so the hook silently governs nothing. Cheap to catch
	// here.
	//
	// The sibling condition — a PreToolUse/PostToolUse hook with no matcher
	// — is deliberately not refused: "run on every tool call" is legitimate
	// and gets a badge on the read surface instead.
	if vibekit.ClassifyHookMatcher(trigger.Name, p.Patterns) == vibekit.HookMatcherIneffective {
		return p, "", http.StatusBadRequest,
			fmt.Errorf("trigger %s has nothing to match against, so its matcher %q would be ignored; leave patterns empty for this trigger",
				trigger.Name, strings.TrimSpace(p.Patterns))
	}
	safeName = strings.ReplaceAll(strings.ToLower(p.Name), " ", "-")
	if !validHookNameRe.MatchString(safeName) {
		return p, "", http.StatusBadRequest,
			errors.New("hook name must be 1-64 chars, start with a letter or digit, and contain only [a-z0-9_-]")
	}
	return p, safeName, 0, nil
}

// The standalone .kiro/hooks/<name>.json shape:
//
//	{ "version": "v1", "hooks": [ { name, trigger, matcher?, action, timeout? } ] }
//
// trigger is PascalCase (see vibekit.NormalizeHookTrigger); action.type is
// "command" (carries command) or "agent" (carries prompt).
//
// Three independent v-numbers collide here, so do not reconcile them: the
// agent engine is v1/v2/v3 (vibekit pins v3), the hook engine is v1/v2 with
// no v3 (vibekit declares v2 via _meta.kiro.hooks), and this document's
// "version" field is a literal "v1" KAS's schema requires — bumping it to
// "v2" to match the hook engine makes every hook unloadable.
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

// mustTrigger resolves an event type validateHookPayload has already
// accepted.
func mustTrigger(eventType string) string {
	t, _ := vibekit.NormalizeHookTrigger(eventType)
	return t.Name
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
// .kiro/hooks/<name>.json.
func buildHookDoc(p *hookCreatePayload) hookDoc {
	entry := hookEntry{
		Name:        p.Name,
		Trigger:     mustTrigger(p.EventType),
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
func CmdCreateHook(ctx context.Context, ws Workspace, cmd *vibekit.ClientCommand) (any, error) {
	p, safeName, code, vErr := validateHookPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}
	hook := buildHookDoc(&p)

	hookPath := filepath.Join(ws.Dir, ".kiro", "hooks", safeName+".json")
	if _, err := os.Stat(hookPath); err == nil {
		return nil, StatusError(http.StatusConflict,
			errors.New("a hook with this name already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	data, err := json.MarshalIndent(hook, "", "  ")
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if _, err := atomicfile.WriteFile(ctx, hookPath, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	relPath := filepath.Join(".kiro", "hooks", safeName+".json")
	slog.Info("hook created from chat", keyName, p.Name, "path", relPath)
	return responseWith(map[string]any{"path": relPath}), nil
}
