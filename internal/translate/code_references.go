package translate

// v3 (KAS) _kiro/code_references handler.
//
// KAS emits _kiro/code_references when a completion reproduces a recognizable
// chunk of a referenced open-source file AND the account's code-reference
// tracker is enabled (governance.features.codeReferenceTracker — enterprise
// profiles with recommendationsWithReferences=ALLOW, or the non-enterprise
// construction opt-in). On a Builder ID / individual login the flag defaults
// off, so this handler is defensive: correct against the verified wire shape,
// but only exercised when the tracker is enabled server-side.
//
// Wire shape (verified against the KAS 2.12 acp-server bundle):
//
//	{ "sessionId": "sess_…", "references": [ { "licenseName", "repository", "url" } ] }
//
// The KAS ACP layer maps every reference down to those three fields
// (licenseName/repository/url); the raw CodeWhisperer recommendationContentSpan
// and information fields are stripped upstream before emission, so there is no
// span to attach a reference to a specific message region and no message/tool
// id — attributions are turn-scoped. KAS also broadcasts the SAME references
// under EVERY live session id in the bridge process (`for (sessionId of
// this.sessions.keys())`), so a chat with a subagent session receives
// duplicates; we process only the parent-session copy.

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// v3CodeReferences is the _kiro/code_references notification payload.
type v3CodeReferences struct {
	SessionID  string            `json:"sessionId"`
	References []v3CodeReference `json:"references"`
}

// v3CodeReference is one entry in the references list. Only licenseName,
// repository, and url survive KAS's ACP-layer mapping.
type v3CodeReference struct {
	LicenseName string `json:"licenseName"`
	Repository  string `json:"repository"`
	URL         string `json:"url"`
}

// HandleCodeReferences accumulates the turn's licensed-code attributions onto
// the in-flight assistant buffer and broadcasts a code_references SSE so the
// client attaches an attribution chip to that turn. The references are also
// persisted onto the finalized assistant message at turn end (bridge_coord.go)
// so the chip survives reload — the streamed assistant turn is never
// re-broadcast as message_appended.
func (t *Translator) HandleCodeReferences(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[v3CodeReferences](msg, "code_references")
	if !ok {
		return
	}
	// Dedup the KAS fan-out: skip copies keyed to a subagent session; the
	// parent-session copy carries the same references (KAS broadcasts the
	// identical list to every session). deriveSubSession returns "" for the
	// parent (or when the parent session is unknown), non-empty for a subagent.
	if t.deriveSubSession(chatID, p.SessionID) != "" {
		return
	}
	refs := make([]api.CodeReference, 0, len(p.References))
	for _, r := range p.References {
		// Match KAS's own filter: a reference with no license name carries
		// no attribution value.
		if r.LicenseName == "" {
			continue
		}
		refs = append(refs, api.CodeReference{
			LicenseName: r.LicenseName,
			Repository:  r.Repository,
			URL:         r.URL,
		})
	}
	if len(refs) == 0 {
		return
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	// Only attach to an in-flight turn. References fire mid-completion (the
	// model must generate the licensed code first), so by the time one
	// arrives the assistant buffer is Started with a message id. Dropping a
	// reference that somehow precedes the turn avoids contaminating the next
	// turn's message via the reused buffer.
	if !buf.Started || buf.MessageID == "" {
		return
	}
	all := buf.AppendCodeReferences(refs)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventCodeReferences, chatID, api.CodeReferencesPayload{
		MessageID:  buf.MessageID,
		References: all,
	}))
}
