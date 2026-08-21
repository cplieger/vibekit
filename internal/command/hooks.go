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
	"slices"
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
	if _, known := normalizeTrigger(p.EventType); !known {
		return p, "", http.StatusBadRequest,
			fmt.Errorf("event_type %q is not a trigger kiro-cli loads; expected one of: %s",
				p.EventType, knownHookTriggers())
	}
	safeName = strings.ReplaceAll(strings.ToLower(p.Name), " ", "-")
	if !validHookNameRe.MatchString(safeName) {
		return p, "", http.StatusBadRequest,
			errors.New("hook name must be 1-64 chars, start with a letter or digit, and contain only [a-z0-9_-]")
	}
	return p, safeName, 0, nil
}

// The standalone .kiro/hooks/<name>.json shape. The document wraps a single
// hook in a versioned envelope:
//
//	{ "version": "v1", "hooks": [ { name, trigger, matcher?, action, timeout? } ] }
//
// trigger is PascalCase (see normalizeTrigger); action.type is "command"
// (carries command) or "agent" (carries prompt). This replaces the old
// embedded 2.x shape (a top-level hook object with when/then blocks).
//
// THREE INDEPENDENT v-NUMBERS COLLIDE HERE, so do not reconcile them:
//
//   - The AGENT ENGINE is v1/v2/v3, and vibekit is pinned to v3.
//   - The HOOK ENGINE is v1/v2 and has NO v3. vibekit opts into v2 by
//     declaring _meta.kiro.hooks={enabled:true,v2:true} (internal/kascap).
//   - This DOCUMENT's "version" is "v1", and it is the current spelling.
//
// So `version: "v1"` beside a declared hook engine v2 is correct, not stale.
// Bumping it to "v2" to match the engine makes every hook vibekit writes
// unloadable: KAS's kasHookFileSchema declares this field as a LITERAL type
// (`version: literalType("v1")`, read off 2.18.0), so any other value fails
// validation outright rather than degrading. The engine version is negotiated
// on the wire, not in the file. It is not the agent engine's v3 either.
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
	// Three more spellings KAS itself accepts. Their absence meant a payload
	// using any of them produced a hook file KAS then discarded.
	"agentspawn":    "SessionStart",
	"sessionend":    "Stop",
	"afterfileedit": "PostFileSave",
}

// normalizeTrigger maps a client event-type value to its PascalCase v1
// trigger, reporting whether the value is one KAS will actually load.
//
// It used to pass an unrecognised value through trimmed, on the reasoning that
// vibekit should not block a trigger its map does not yet know. That reasoning
// inverts here, because the permissive branch is not lenient, it is silent:
// KAS's parseHookDocument DROPS a hook whose trigger it does not recognise, so
// create_hook answered 200 with a file path for a hook that loads nowhere, never
// fires, and never appears in /api/hooks. The user is told a hook exists and
// there is no signal anywhere that it does not.
//
// Refusing costs nothing by comparison: the closed set lives in this map, and a
// trigger KAS adds later is one map entry away. Silence was the expensive
// choice.
func normalizeTrigger(eventType string) (trigger string, ok bool) {
	trimmed := strings.TrimSpace(eventType)
	t, ok := hookTriggers[strings.ToLower(trimmed)]
	return t, ok
}

// knownHookTriggers lists the canonical trigger names for an error message, so a
// rejection tells the caller what IS accepted rather than only what is not.
func knownHookTriggers() string {
	seen := make(map[string]struct{}, len(hookTriggers))
	names := make([]string, 0, len(hookTriggers))
	for _, v := range hookTriggers {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		names = append(names, v)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// mustTrigger resolves an event type validateHookPayload has already accepted.
// Separate from normalizeTrigger so the validated path reads as one expression
// while the boundary check keeps its two-value form.
func mustTrigger(eventType string) string {
	t, _ := normalizeTrigger(eventType)
	return t
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
