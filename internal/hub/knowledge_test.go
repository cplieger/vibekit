package hub

// Tests for knowledge.go: the _kiro/knowledge result parse, the path
// resolution, the list/add/remove HTTP handlers, and the bridge-targeting
// invariant (knowledge is issued WITHOUT a sessionId so it hits the global
// default store). The utility bridge that serves these calls is the shared
// fakeBridge from newTestHub, seeded with a canned _kiro/knowledge result.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseKnowledgeResult(t *testing.T) {
	t.Run("ShowEntries", func(t *testing.T) {
		raw := `{"success":true,"entries":[` +
			`{"name":"docs","id":"abc12345","description":"d","item_count":7,"path":"/w/docs"},` +
			`{"name":"big","id":"op1","description":"","item_count":0,"items_display":"42%","indexing":true,"path":"/w/big"}]}`
		res, err := parseKnowledgeResult(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("parseKnowledgeResult: %v", err)
		}
		if !res.Success || len(res.Entries) != 2 {
			t.Fatalf("res = %+v", res)
		}
		if res.Entries[1].Indexing != true || res.Entries[1].ItemsDisplay != "42%" {
			t.Errorf("active-op entry = %+v", res.Entries[1])
		}
	})

	t.Run("Message", func(t *testing.T) {
		res, err := parseKnowledgeResult(json.RawMessage(`{"success":true,"message":"Removed 'docs'"}`))
		if err != nil || !res.Success || res.Message != "Removed 'docs'" {
			t.Errorf("res = %+v err = %v", res, err)
		}
	})

	t.Run("Failure", func(t *testing.T) {
		res, err := parseKnowledgeResult(json.RawMessage(`{"success":false,"message":"Entry not found: x"}`))
		if err != nil || res.Success {
			t.Errorf("res = %+v err = %v", res, err)
		}
	})

	t.Run("Empty", func(t *testing.T) {
		if _, err := parseKnowledgeResult(nil); err == nil {
			t.Fatal("expected error for empty result")
		}
	})

	t.Run("Malformed", func(t *testing.T) {
		if _, err := parseKnowledgeResult(json.RawMessage(`{nope`)); err == nil {
			t.Fatal("expected error for malformed JSON")
		}
	})
}

func TestResolveKnowledgePath(t *testing.T) {
	h, _, _ := newTestHub() // workDir = /tmp/work
	if got := h.config.resolveKnowledgePath("docs"); got != "/tmp/work/docs" {
		t.Errorf("relative resolve = %q, want /tmp/work/docs", got)
	}
	if got := h.config.resolveKnowledgePath("a/../b"); got != "/tmp/work/b" {
		t.Errorf("relative clean = %q, want /tmp/work/b", got)
	}
	if got := h.config.resolveKnowledgePath("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute passthrough = %q, want /abs/path", got)
	}
}

// seedKnowledge wires a canned _kiro/knowledge result onto the shared fake so
// the utility-bridge call the handlers make returns it.
func seedKnowledge(br *fakeBridge, result string) {
	br.callResults = map[string]json.RawMessage{methodKiroKnowledge: json.RawMessage(result)}
}

func TestHandleKnowledgeList_OK(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"entries":[`+
		`{"name":"docs","id":"abc12345","item_count":7,"path":"/w/docs"},`+
		`{"name":"big","id":"op1","item_count":0,"items_display":"42%","indexing":true}]}`)

	rec := httptest.NewRecorder()
	h.config.handleKnowledgeList(rec, httptest.NewRequest(http.MethodGet, "/api/knowledge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body knowledgeListResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Contexts) != 2 {
		t.Fatalf("contexts = %+v", body.Contexts)
	}
	if body.Contexts[0].Name != "docs" || body.Contexts[0].ItemCount != 7 {
		t.Errorf("context[0] = %+v", body.Contexts[0])
	}
	if !body.Contexts[1].Indexing || body.Contexts[1].ItemsDisplay != "42%" {
		t.Errorf("context[1] = %+v (indexing/progress not preserved)", body.Contexts[1])
	}
	// Bridge-targeting: show must omit sessionId (global default store).
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "show" {
		t.Errorf("subcommand = %v, want show", params["subcommand"])
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge show must NOT carry a sessionId (targets the global default store)")
	}
}

func TestHandleKnowledgeList_BridgeError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"boom"}`)

	rec := httptest.NewRecorder()
	h.config.handleKnowledgeList(rec, httptest.NewRequest(http.MethodGet, "/api/knowledge", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleKnowledgeAdd_MissingPath(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleKnowledgeAdd_OK_ResolvesPathAndDerivesName(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Indexing 'docs' in background\nFiles: 3"}`)

	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":"docs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// Bridge-targeting + resolution + name derivation.
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "add" {
		t.Errorf("subcommand = %v, want add", params["subcommand"])
	}
	if params["name"] != "docs" {
		t.Errorf("name = %v, want docs (derived from base)", params["name"])
	}
	if params["path"] != "/tmp/work/docs" {
		t.Errorf("path = %v, want /tmp/work/docs (resolved against workDir)", params["path"])
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge add must NOT carry a sessionId")
	}
}

func TestHandleKnowledgeAdd_ExplicitName(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Indexing 'Project docs' in background"}`)

	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":"/abs/docs","name":"Project docs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["name"] != "Project docs" || params["path"] != "/abs/docs" {
		t.Errorf("params = %+v, want name='Project docs' path=/abs/docs", params)
	}
}

func TestHandleKnowledgeAdd_Failure(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"Usage: /knowledge add <name> <path>"}`)

	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":"docs"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleKnowledgeRemove_MissingName(t *testing.T) {
	h, _, _ := newTestHub()
	req := httptest.NewRequest(http.MethodDelete, "/api/knowledge/", nil)
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeRemove(rec, req) // no path value set
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleKnowledgeRemove_OK(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Removed 'docs'"}`)

	req := httptest.NewRequest(http.MethodDelete, "/api/knowledge/docs", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeRemove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "remove" || params["target"] != "docs" {
		t.Errorf("params = %+v, want subcommand=remove target=docs", params)
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge remove must NOT carry a sessionId")
	}
}

func TestHandleKnowledgeRemove_NotFound(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"Entry not found: gone"}`)

	req := httptest.NewRequest(http.MethodDelete, "/api/knowledge/gone", nil)
	req.SetPathValue("name", "gone")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeRemove(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}
