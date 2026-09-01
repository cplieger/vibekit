package settings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// errStoredNull marks a key whose stored value is the JSON literal null. It is
// package-private because no caller branches on it: EffectiveFrom reports the KEY,
// and why that key was refused is the same answer either way.
var errStoredNull = errors.New("settings: stored value is null")

// DefaultKnowledgeEnabled is the knowledge switch's default, TRUE, and it is a
// constant so the two readers that need it cannot disagree: the effective view
// below and internal/agent's knowledgeEnabled, which resolves the same absence
// for the session door. It defaults on because the index, its REST surface and
// its UI all predate the switch, so reading an absent key as the zero value takes
// the knowledge tool away from every existing install.
const DefaultKnowledgeEnabled = true

// EffectiveDefaults is the value in force for every client-rendered preference
// when config.json says nothing about it.
//
// This is the ONE statement of those values, and it answers a narrower question
// than the per-consumer defaults in internal/agent and internal/composition: this
// is "the document is silent, what is true", uniform per key, while a consumer
// answers "I could not read the document at all, what do I do", which differs
// per consumer. internal/composition's chatRetention is the case that proves they
// must not be merged — it uses FieldStrict, so an UNREADABLE file yields 0 (purge
// nothing this pass) where an ABSENT key yields DefaultChatRetentionDays.
func EffectiveDefaults() vibekit.EffectiveSettings {
	return vibekit.EffectiveSettings{
		AgentIgnoreFiles:  DefaultAgentIgnoreFiles(),
		ChatRetentionDays: DefaultChatRetentionDays,
		KnowledgeEnabled:  DefaultKnowledgeEnabled,
		// The two per-kind push switches default ON, mirroring push.kindRegistry,
		// while the master switch below defaults OFF. The polarity genuinely differs
		// between them, which is why neither is safe for a client to guess.
		NotifyAgentFinished: true,
		NotifyPRStatus:      true,
		// Everything else is its zero value, and each one is the right answer rather
		// than an omission: no theme or browser path chosen, no remembered model or
		// effort, push off until asked for, tool search off to match kiro-cli, memory
		// off by standing veto, supervised and scheduled-auto-approve off (the latter
		// fail-closed by decision), and info-level logs.
	}
}

// RetentionEnabled reports whether chat retention keeps a closed chat's record.
// It is the CLOSE path's read of chat_retention_days, and it FAILS TOWARD
// KEEPING: an unreadable config.json answers ON (delete nothing), an absent key
// or file takes DefaultChatRetentionDays (ON), days == 0 answers OFF (ephemeral),
// and any other value — -1 = forever included — answers ON.
//
// Must never share a reader with the retention PURGE
// (internal/composition.chatRetention): the 0-sentinel's safe direction INVERTS
// between the two — for the purge, 0 means "purge nothing", so unreadable maps
// to 0; for close, 0 means "delete now", so unreadable must map to ON.
func RetentionEnabled(ctx context.Context, configDir string) bool {
	days, ok, err := FieldStrict[int](ctx, configDir, KeyChatRetentionDays)
	if err != nil {
		slog.Error("chat retention: config.json is present but unreadable; treating retention as ON so this close deletes nothing",
			"key", KeyChatRetentionDays, "error", err)
		return true
	}
	if !ok {
		days = DefaultChatRetentionDays
	}
	return days != 0
}

// EffectiveFrom resolves the stored document into the view the client reads:
// EffectiveDefaults with every stored value that FITS ITS FIELD overlaid, and
// the keys whose stored value did not fit returned so the caller can say so.
//
// Value validity is deliberately NOT checked, only type validity: `theme:
// "purple"` is a well-typed string and passes, and the client's asThemeChoice
// rejects it. The wire owns the type, the reader with the vocabulary owns the
// value. A nil or empty stored map yields the defaults unchanged.
func EffectiveFrom(stored map[string]json.RawMessage) (effective vibekit.EffectiveSettings, rejected []string) {
	out := EffectiveDefaults()
	for key, set := range effectiveSetters(&out) {
		raw, ok := stored[key]
		if !ok {
			continue
		}
		if err := set(raw); err != nil {
			rejected = append(rejected, key)
		}
	}
	return out, rejected
}

// effectiveKeys is every config.json key the effective view can carry.
//
// Exists for two mechanical properties: each key is in KnownKeys, so a response
// round-tripped as a PATCH raises no unknown-key warning; and each FIELD of
// vibekit.EffectiveSettings has one, so adding a field without a setter fails
// rather than silently becoming unsettable.
func effectiveKeys() []string {
	var out vibekit.EffectiveSettings
	return slices.Sorted(maps.Keys(effectiveSetters(&out)))
}

// effectiveSetters maps each key to the one decode-and-assign for its field.
//
// Each setter decodes into a scratch value and assigns only on success, so a value
// that does not fit leaves the destination untouched and there is nothing to undo.
// Built per call over the caller's own struct rather than declared once, because
// each closure has to bind that struct's field address.
func effectiveSetters(out *vibekit.EffectiveSettings) map[string]func(json.RawMessage) error {
	return map[string]func(json.RawMessage) error{
		KeyAgentIgnoreFiles:     func(r json.RawMessage) error { return decodeInto(&out.AgentIgnoreFiles, r) },
		KeyChatRetentionDays:    func(r json.RawMessage) error { return decodeInto(&out.ChatRetentionDays, r) },
		KeyTheme:                func(r json.RawMessage) error { return decodeInto(&out.Theme, r) },
		KeyFBPath:               func(r json.RawMessage) error { return decodeInto(&out.FBPath, r) },
		KeyLastModel:            func(r json.RawMessage) error { return decodeInto(&out.LastModel, r) },
		KeyLastEffort:           func(r json.RawMessage) error { return decodeInto(&out.LastEffort, r) },
		KeyLastEffortModel:      func(r json.RawMessage) error { return decodeInto(&out.LastEffortModel, r) },
		KeyKnowledgeEnabled:     func(r json.RawMessage) error { return decodeInto(&out.KnowledgeEnabled, r) },
		KeyToolSearchEnabled:    func(r json.RawMessage) error { return decodeInto(&out.ToolSearchEnabled, r) },
		KeyMemoryEnabled:        func(r json.RawMessage) error { return decodeInto(&out.MemoryEnabled, r) },
		KeyNotificationsEnabled: func(r json.RawMessage) error { return decodeInto(&out.NotificationsEnabled, r) },
		KeyNotifyAgentFinished:  func(r json.RawMessage) error { return decodeInto(&out.NotifyAgentFinished, r) },
		KeyNotifyPRStatus:       func(r json.RawMessage) error { return decodeInto(&out.NotifyPRStatus, r) },
		KeySupervisedDefault:    func(r json.RawMessage) error { return decodeInto(&out.SupervisedDefault, r) },
		KeyScheduledAutoApprove: func(r json.RawMessage) error { return decodeInto(&out.ScheduledAutoApprove, r) },
		KeyDebugLogs:            func(r json.RawMessage) error { return decodeInto(&out.DebugLogs, r) },
	}
}

// decodeInto decodes raw into a scratch T and assigns it only on success, so a
// caller's field keeps whatever it already held when the value does not fit.
//
// A stored JSON null is refused rather than accepted: encoding/json treats null
// as a no-op for most targets, so decoding it would silently assign the
// scratch's zero value — which for chat_retention_days is 0, "delete chats on
// close". Refusing means the default stands and the key is reported.
func decodeInto[T any](dst *T, raw json.RawMessage) error {
	if string(raw) == "null" {
		return errStoredNull
	}
	var scratch T
	if err := json.Unmarshal(raw, &scratch); err != nil {
		return err
	}
	*dst = scratch
	return nil
}
