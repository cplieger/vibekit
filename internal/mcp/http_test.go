package mcp

// HTTP handler coverage for /api/mcp (collection) and /api/mcp/{id}
// (one). Drives the full mux end-to-end via httptest so the
// decode → validate → writeErr pipeline is exercised including the
// 413 oversize, 405 method-not-allowed, 409 name-conflict, and
// Content-Type guards.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/httpreply"
)

func newRoutedStore(t *testing.T) (*Store, *http.ServeMux) {
	t.Helper()
	s := newTestStore(t)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return s, mux
}

func doJSON(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- handleCollection (GET /api/mcp, POST /api/mcp) ---

func TestHandleCollection_GET_listsEmpty(t *testing.T) {
	_, mux := newRoutedStore(t)

	rec := doJSON(t, mux, http.MethodGet, "/api/mcp", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mcp status = %d, want 200", rec.Code)
	}
	var body struct {
		Servers []*Server `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Servers) != 0 {
		t.Errorf("empty store listed %d servers, want 0", len(body.Servers))
	}
}

func TestHandleCollection_POST_createsAndPersists(t *testing.T) {
	s, mux := newRoutedStore(t)

	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", &Server{
		Transport: TransportStdio, Name: "gh", Command: "bash", Enabled: true,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got Server
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID == "" || got.Name != "gh" {
		t.Errorf("POST response = %+v, want non-empty ID + Name=gh", got)
	}
	if len(s.List(t.Context())) != 1 {
		t.Errorf("store after POST has %d servers, want 1", len(s.List(t.Context())))
	}
}

func TestHandleCollection_POST_invalidJSON_is400(t *testing.T) {
	_, mux := newRoutedStore(t)

	req := httptest.NewRequest(http.MethodPost, "/api/mcp",
		strings.NewReader("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST invalid json status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid json") {
		t.Errorf("POST invalid json body = %q, want 'invalid json'", rec.Body.String())
	}
}

func TestHandleCollection_POST_validationFail_is400(t *testing.T) {
	_, mux := newRoutedStore(t)

	// Missing command on stdio → Validate error → writeErr default → 400.
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", &Server{
		Transport: TransportStdio, Name: "x", Command: "",
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST validation fail status = %d, want 400", rec.Code)
	}
}

// Same name, DIFFERENT spec is still 409. The identical-spec re-POST is now a
// 200 no-op — the item that changed it also removed the only workaround the 409
// had, which was delete-then-re-add, and that discarded the user's API keys.
func TestHandleCollection_POST_nameConflict_is409(t *testing.T) {
	_, mux := newRoutedStore(t)

	_ = doJSON(t, mux, http.MethodPost, "/api/mcp", &Server{
		Transport: TransportStdio, Name: "dup", Command: "bash",
	})
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", &Server{
		Transport: TransportStdio, Name: "dup", Command: "zsh",
	})

	if rec.Code != http.StatusConflict {
		t.Errorf("POST name conflict status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("conflict body = %q, want 'already exists'", rec.Body.String())
	}
}

func TestHandleCollection_MethodNotAllowed(t *testing.T) {
	_, mux := newRoutedStore(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/mcp status = %d, want 405", rec.Code)
	}
}

// Content-Type gate (SEC-u12c1-002): POST without an application/json
// Content-Type should be rejected before the decoder runs.
func TestHandleCollection_POST_wrongContentType_is400(t *testing.T) {
	_, mux := newRoutedStore(t)

	req := httptest.NewRequest(http.MethodPost, "/api/mcp",
		strings.NewReader(`{"transport":"stdio","name":"x","command":"bash"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST text/plain status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "application/json") {
		t.Errorf("body = %q, want mention of application/json", rec.Body.String())
	}
}

// 413-on-oversize (Q8): feed MaxJSONBody+1 bytes; decoder returns
// *http.MaxBytesError which the helper must detect and turn into 413.
func TestHandleCollection_POST_oversize_is413(t *testing.T) {
	_, mux := newRoutedStore(t)

	// Build a JSON object whose Name field is big enough that the full
	// body exceeds httpreply.MaxJSONBody (1 MiB). Using a field that Validate
	// will also reject is fine; we're not expecting the body to parse.
	big := strings.Repeat("a", int(httpreply.MaxJSONBody)+1)
	body := `{"transport":"stdio","name":"` + big + `","command":"bash"}`
	req := httptest.NewRequest(http.MethodPost, "/api/mcp",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize POST status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("body = %q, want 'too large'", rec.Body.String())
	}
}

// --- handleOne (GET/PUT/PATCH/DELETE /api/mcp/{id}) ---

func TestHandleOne_GET_returnsMaskedSecrets(t *testing.T) {
	s, mux := newRoutedStore(t)
	orig, err := s.Create(t.Context(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx", Enabled: true,
		Env: []KeyPair{{Name: "TOKEN", Value: "secret-abc"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, mux, http.MethodGet, "/api/mcp/"+string(orig.ID), nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var got Server
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != orig.ID || got.Name != "gh" {
		t.Errorf("GET /api/mcp/%s = %+v, want ID=%s Name=gh", orig.ID, got, orig.ID)
	}
	if len(got.Env) != 1 || got.Env[0].Value != SecretMask {
		t.Errorf("GET secret value = %q, want %q (mask)", got.Env[0].Value, SecretMask)
	}
}

func TestHandleOne_ErrorPaths(t *testing.T) {
	type testCase struct {
		name         string
		method       string
		path         string
		body         string
		contentType  string
		wantBody     string
		wantStatus   int
		needServer   bool
		needConflict bool
	}

	cases := []testCase{
		{
			name:       "GET_unknownID_is404",
			method:     http.MethodGet,
			path:       "/api/mcp/does-not-exist",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "GET_basePath_is404",
			method:     http.MethodGet,
			path:       "/api/mcp/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "PUT_unknownID_is404",
			method:     http.MethodPut,
			path:       "/api/mcp/does-not-exist",
			body:       `{"transport":"stdio","name":"x","command":"bash"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:         "PUT_nameConflict_is409",
			method:       http.MethodPut,
			needServer:   true,
			needConflict: true,
			body:         `{"transport":"stdio","name":"alpha","command":"bash"}`,
			wantStatus:   http.StatusConflict,
		},
		{
			name:       "PUT_invalidJSON_is400",
			method:     http.MethodPut,
			needServer: true,
			body:       "{not-json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "PATCH_missingEnabledField_is400",
			method:     http.MethodPatch,
			needServer: true,
			body:       "{}",
			wantStatus: http.StatusBadRequest,
			wantBody:   "enabled required",
		},
		{
			name:       "PATCH_invalidJSON_is400",
			method:     http.MethodPatch,
			needServer: true,
			body:       "{bad",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "PATCH_unknownID_is404",
			method:     http.MethodPatch,
			path:       "/api/mcp/does-not-exist",
			body:       `{"enabled":true}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "MethodNotAllowed",
			method:     "PROPFIND",
			needServer: true,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:        "PUT_wrongContentType_is400",
			method:      http.MethodPut,
			needServer:  true,
			body:        `{"transport":"stdio","name":"x","command":"bash"}`,
			contentType: "text/plain",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "PATCH_wrongContentType_is400",
			method:      http.MethodPatch,
			needServer:  true,
			body:        `{"enabled":true}`,
			contentType: "application/xml",
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mux := newRoutedStore(t)

			path := tc.path
			if tc.needServer {
				orig, err := s.Create(t.Context(), &Server{
					Transport: TransportStdio, Name: "x", Command: "bash",
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if path == "" {
					path = "/api/mcp/" + string(orig.ID)
				}
			}
			if tc.needConflict {
				orig, err := s.Create(t.Context(), &Server{
					Transport: TransportStdio, Name: "alpha", Command: "bash",
				})
				if err != nil {
					t.Fatalf("Create alpha: %v", err)
				}
				// For conflict tests, use the first server's ID (needServer creates "x").
				_ = orig
			}

			ct := tc.contentType
			if ct == "" {
				ct = "application/json"
			}

			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}

			req := httptest.NewRequest(tc.method, path, bodyReader)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("%s %s status = %d, want %d; body = %s",
					tc.method, path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want substring %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestHandleOne_PUT_updatesAndRespectsMask(t *testing.T) {
	s, mux := newRoutedStore(t)
	orig, err := s.Create(t.Context(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx", Enabled: true,
		Env: []KeyPair{{Name: "TOKEN", Value: "secret-abc"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Send mask back; store must preserve the stored value.
	rec := doJSON(t, mux, http.MethodPut, "/api/mcp/"+string(orig.ID), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx", Enabled: true,
		Env: []KeyPair{{Name: "TOKEN", Value: SecretMask}},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rec.Code, rec.Body.String())
	}
	raw := s.EnabledRaw(t.Context())
	if len(raw) != 1 || len(raw[0].Env) != 1 || raw[0].Env[0].Value != "secret-abc" {
		t.Errorf("PUT with mask clobbered secret: got %+v, want secret-abc",
			raw[0].Env)
	}
}

func TestHandleOne_PATCH_togglesEnabled(t *testing.T) {
	s, mux := newRoutedStore(t)
	orig, err := s.Create(t.Context(), &Server{
		Transport: TransportStdio, Name: "x", Command: "bash", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, mux, http.MethodPatch, "/api/mcp/"+string(orig.ID),
		map[string]bool{"enabled": false})

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := s.Get(t.Context(), orig.ID)
	if got == nil || got.Enabled {
		t.Errorf("PATCH did not disable the server: %+v", got)
	}
}

func TestHandleOne_DELETE_removes(t *testing.T) {
	s, mux := newRoutedStore(t)
	orig, err := s.Create(t.Context(), &Server{Transport: TransportStdio, Name: "x", Command: "bash"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, mux, http.MethodDelete, "/api/mcp/"+string(orig.ID), nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d", rec.Code)
	}
	if s.Get(t.Context(), orig.ID) != nil {
		t.Error("DELETE did not remove server")
	}
}

// u12c2-f3: DELETE's persist-failure branch routes through writeErr
// (cycle 2 Q-u12c2-001 collapsed the hand-rolled 500) to 500 with
// the generic "persist failed" body. No filesystem path leaks in the
// response; full detail stays in slog.Error via writeErr's ErrPersist
// case. Rollback leaves the record in place so a retry is possible.
func TestHandleOne_DELETE_persistFailure_is500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0o500; Delete never fails")
	}
	s, mux := newRoutedStore(t)
	orig, err := s.Create(t.Context(), &Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Chmod the store dir read-only so persist fails on slices.Delete.
	dir := filepath.Dir(s.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	rec := doJSON(t, mux, http.MethodDelete, "/api/mcp/"+string(orig.ID), nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE persist-failure status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "persist failed") {
		t.Errorf("body = %q, want 'persist failed'", body)
	}
	// Contract: no filesystem path leaks in the response.
	if strings.Contains(body, dir) || strings.Contains(body, s.path) {
		t.Errorf("DELETE 500 body leaked filesystem path: %q", body)
	}
	// Server must still hold the record (rollback on persist failure).
	if s.Get(t.Context(), orig.ID) == nil {
		t.Error("DELETE rollback failed: record disappeared despite 500")
	}
}

// u12c2-f3: writeErr's ErrPersist branch (cycle 1 q1) routes to
// 500 with a generic body, NOT a leaked err.Error() string. Provoked
// via POST with a writable-then-read-only dir.
func TestHandleCollection_POST_persistFailure_is500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0o500; Create never fails via persist")
	}
	s, mux := newRoutedStore(t)

	dir := filepath.Dir(s.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", &Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST persist-failure status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "persist failed") {
		t.Errorf("body = %q, want 'persist failed'", body)
	}
	if strings.Contains(body, dir) || strings.Contains(body, "rename") ||
		strings.Contains(body, "EACCES") {
		t.Errorf("POST 500 body leaked filesystem detail: %q", body)
	}
}

// --- D80: the 400 carries a per-field breakdown ---

// decodeValidation400 reads the validation envelope off a 400 recorder.
func decodeValidation400(t *testing.T, rec *httptest.ResponseRecorder) validationErrorBody {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var got validationErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 400 body %q: %v", rec.Body.String(), err)
	}
	return got
}

// TestPost_ValidationErrorNamesEveryBadField is the wire half of D80: one
// response, three fields, so the form can mark three inputs.
func TestPost_ValidationErrorNamesEveryBadField(t *testing.T) {
	_, mux := newRoutedStore(t)
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", map[string]any{
		"name":      "1bad name",
		"transport": "http",
		"url":       "ftp://example.test/mcp",
		"headers":   []map[string]string{{"name": "bad header", "value": "x"}},
	})
	got := decodeValidation400(t, rec)
	if got.Error == "" {
		t.Error("the 400 carries no message text; a client reading only `error` would show nothing")
	}
	want := map[string]bool{"name": false, "url": false, "headers": false}
	for _, f := range got.Fields {
		if _, known := want[f.Field]; !known {
			t.Errorf("unexpected field %q in the breakdown", f.Field)
			continue
		}
		want[f.Field] = true
		if f.Msg == "" {
			t.Errorf("field %q carries no message", f.Field)
		}
	}
	for field, seen := range want {
		if !seen {
			t.Errorf("field %q missing from the breakdown: %+v", field, got.Fields)
		}
	}
}

// TestImport_ValidationErrorNamesEveryBadField is the case the item calls
// load-bearing: a pasted block, several fields wrong, none of them typed.
func TestImport_ValidationErrorNamesEveryBadField(t *testing.T) {
	_, mux := newRoutedStore(t)
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp/import", map[string]any{
		"mcpServers": map[string]any{
			"broken": map[string]any{
				"url":     "ftp://example.test/mcp",
				"headers": map[string]any{"bad header": "x"},
			},
		},
	})
	got := decodeValidation400(t, rec)
	if len(got.Fields) < 2 {
		t.Fatalf("a pasted block with two bad fields reported %d: %+v", len(got.Fields), got.Fields)
	}
	seen := map[string]bool{}
	for _, f := range got.Fields {
		seen[f.Field] = true
	}
	for _, field := range []string{"url", "headers"} {
		if !seen[field] {
			t.Errorf("field %q missing from a pasted block's breakdown: %+v", field, got.Fields)
		}
	}
}

// TestNonValidation400CarriesNoFieldList pins the negative: a parse failure has
// no field to attribute, so `fields` is ABSENT rather than an empty array. A
// caller can therefore read the presence of the list as "these inputs are wrong".
func TestNonValidation400CarriesNoFieldList(t *testing.T) {
	_, mux := newRoutedStore(t)
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp/import", map[string]any{
		"mcpServers": map[string]any{"x": map[string]any{"comand": "npx"}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"fields"`) {
		t.Errorf("a parse failure carried a field list: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "comand") {
		t.Errorf("the unknown-key message lost the key it names: %s", rec.Body.String())
	}
}

// TestNameConflictStillRoutesTo409 is the sentinel guard at the HTTP boundary:
// errors.Is has to keep reaching a sentinel through the join, or every store
// precondition would answer 400 with a field list.
func TestNameConflictStillRoutesTo409(t *testing.T) {
	_, mux := newRoutedStore(t)
	body := map[string]any{"name": "dup", "transport": "stdio", "command": "bash"}
	if rec := doJSON(t, mux, http.MethodPost, "/api/mcp", body); rec.Code != http.StatusOK {
		t.Fatalf("first create status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body["command"] = "sh"
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", body)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate-name create status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}
