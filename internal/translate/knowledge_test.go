package translate

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// knowledgeIndexingPayload extracts the single EventKnowledgeIndexing payload,
// failing if there isn't exactly one.
func knowledgeIndexingPayload(t *testing.T, events *[]api.ServerEvent) (api.KnowledgeIndexingPayload, api.ChatID) {
	t.Helper()
	var got []api.KnowledgeIndexingPayload
	var chatID api.ChatID
	for _, e := range *events {
		if e.Type != api.EventKnowledgeIndexing {
			continue
		}
		p, ok := e.Payload.(api.KnowledgeIndexingPayload)
		if !ok {
			t.Fatalf("EventKnowledgeIndexing payload type = %T, want api.KnowledgeIndexingPayload", e.Payload)
		}
		got = append(got, p)
		chatID = e.ChatID
	}
	if len(got) != 1 {
		t.Fatalf("EventKnowledgeIndexing broadcast count = %d, want 1", len(got))
	}
	return got[0], chatID
}

func countKnowledgeEvents(events *[]api.ServerEvent) int {
	n := 0
	for _, e := range *events {
		if e.Type == api.EventKnowledgeIndexing {
			n++
		}
	}
	return n
}

func knowledgeMsg(t *testing.T, m map[string]any) *api.RPCResponse {
	t.Helper()
	return &api.RPCResponse{Params: mustJSON(t, m)}
}

// TestHandleKnowledgeIndexing_Started pins that an indexingStarted notification
// broadcasts one global (empty chatID) knowledge_indexing event with
// Status="started" and the file count, no item count.
func TestHandleKnowledgeIndexing_Started(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandleKnowledgeIndexing(context.Background(), api.ChatID("c1"), knowledgeMsg(t, map[string]any{
		"sessionId": "sess-1", "name": "docs", "fileCount": 12,
	}), true)

	p, chatID := knowledgeIndexingPayload(t, events)
	if p.Name != "docs" || p.Status != "started" || p.FileCount != 12 || p.ItemCount != 0 {
		t.Errorf("payload = %+v, want {docs started fileCount=12 itemCount=0}", p)
	}
	if chatID != "" {
		t.Errorf("event ChatID = %q, want empty (knowledge is global)", chatID)
	}
}

// TestHandleKnowledgeIndexing_Completed pins that an indexingCompleted
// notification carries the wire status and, on success, the item count.
func TestHandleKnowledgeIndexing_Completed(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandleKnowledgeIndexing(context.Background(), api.ChatID("c1"), knowledgeMsg(t, map[string]any{
		"sessionId": "sess-1", "name": "docs", "status": "success", "itemCount": 34,
	}), false)

	p, _ := knowledgeIndexingPayload(t, events)
	if p.Name != "docs" || p.Status != "success" || p.ItemCount != 34 || p.FileCount != 0 {
		t.Errorf("payload = %+v, want {docs success itemCount=34 fileCount=0}", p)
	}
}

// TestHandleKnowledgeIndexing_MissingName pins that a nameless notification is
// dropped (no broadcast).
func TestHandleKnowledgeIndexing_MissingName(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandleKnowledgeIndexing(context.Background(), api.ChatID("c1"), knowledgeMsg(t, map[string]any{
		"sessionId": "sess-1", "fileCount": 3,
	}), true)

	if n := countKnowledgeEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (missing name)", n)
	}
}

// TestHandleKnowledgeIndexing_MalformedNoop pins that malformed params are
// dropped without a broadcast.
func TestHandleKnowledgeIndexing_MalformedNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandleKnowledgeIndexing(context.Background(), api.ChatID("c1"), &api.RPCResponse{Params: []byte("{")}, true)
	if n := countKnowledgeEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (malformed params)", n)
	}
}
