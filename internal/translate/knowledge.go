package translate

// v3 (KAS) _kiro/knowledge/indexingStarted + indexingCompleted handlers.
//
// KAS emits these while indexing a knowledge base. Verified live: they fire
// ONLY for agent-declared knowledge_bases sync at session start, NOT for a
// user-initiated `_kiro/knowledge add` (whose progress the client polls via
// the `show` active-operations list — see hub/knowledge.go). Wired anyway so
// the agent-sync case surfaces as the knowledge_indexing SSE; harmless when it
// never fires.
//
// Wire shapes (verified against the KAS 2.12 acp-server bundle + a live probe):
//
//	indexingStarted:   { "sessionId", "name", "fileCount" }
//	indexingCompleted: { "sessionId", "name", "status", "itemCount"? }  // itemCount only on status=="success"
//
// The knowledge store is workspace-global, so the SSE is broadcast with an
// empty chatID (global, like the MCP events); the client refetches
// GET /api/knowledge on receipt.

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// knowledgeStatusStarted is the synthetic status vibekit stamps on the
// indexingStarted notification (the wire carries no status field there).
const knowledgeStatusStarted = "started"

// v3KnowledgeIndexing is the superset payload for both indexing notifications.
type v3KnowledgeIndexing struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	FileCount int    `json:"fileCount"`
	ItemCount int    `json:"itemCount"`
}

// HandleKnowledgeIndexing translates a _kiro/knowledge/indexingStarted
// (started == true) or indexingCompleted (started == false) notification into
// a knowledge_indexing SSE. started stamps Status="started" and carries
// FileCount; completed carries the wire Status ("success"/failure) and, on
// success, ItemCount.
func (t *Translator) HandleKnowledgeIndexing(ctx context.Context, _ api.ChatID, msg *api.RPCResponse, started bool) {
	p, ok := unmarshalParams[v3KnowledgeIndexing](msg, "knowledge/indexing")
	if !ok {
		return
	}
	if p.Name == "" {
		return
	}
	payload := api.KnowledgeIndexingPayload{Name: p.Name}
	if started {
		payload.Status = knowledgeStatusStarted
		payload.FileCount = p.FileCount
	} else {
		payload.Status = p.Status
		payload.ItemCount = p.ItemCount
	}
	// Knowledge is workspace-global: broadcast with an empty chatID so every
	// client refetches, regardless of which chat's bridge emitted it.
	t.deps.Broadcast(ctx, api.NewEvent(api.EventKnowledgeIndexing, "", payload))
}
