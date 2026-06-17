package server

// Tests added by mutant-killing unit vibekit-u13. Each test targets one or
// more surviving gremlins mutants in internal/server/tools_handlers.go and is
// named/keyed with the gk_vibekit_u13_ prefix to avoid colliding with sibling
// units sharing this package. New helpers carry the same prefix; existing
// package helpers (newToolsTestServer/readBackManifest from
// tools_handlers_test.go) are reused, not redefined.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gk_vibekit_u13_serverWithRawManifest writes raw bytes (NOT a JSON-marshaled
// map) into <configDir>/tools.json so callers can produce a genuinely empty
// (0-byte) manifest file, then returns a Server pointed at it.
func gk_vibekit_u13_serverWithRawManifest(t *testing.T, raw []byte) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write raw manifest: %v", err)
	}
	return New(WithConfigDir(dir)), path
}

// gk_vibekit_u13_manifestWithDependent returns a manifest where lsp.pyright
// (enabled) requires runtimes.node, so deleting runtimes.node has exactly one
// enabled dependent.
func gk_vibekit_u13_manifestWithDependent() map[string]any {
	return map[string]any{
		"runtimes": map[string]any{
			"node": map[string]any{"enabled": true, "version": "v20"},
		},
		"lsp": map[string]any{
			"pyright": map[string]any{
				"enabled":  true,
				"version":  "1.1",
				"requires": []any{"runtimes.node"},
			},
		},
	}
}

// tools_handlers.go:133:15 CONDITIONALS_BOUNDARY — `if len(data) > 0` guards
// json.Unmarshal. An empty (0-byte) tools.json must parse to an empty manifest
// with NO error because the `> 0` guard skips Unmarshal. If the boundary flips
// to `>= 0`, Unmarshal runs on empty input and returns "unexpected end of JSON
// input", so manifest would be nil and err non-nil.
func Test_gk_vibekit_u13_readManifestEmptyFileIsEmpty(t *testing.T) {
	s, _ := gk_vibekit_u13_serverWithRawManifest(t, []byte{})

	manifest, _, err := s.readManifest()

	if err != nil {
		t.Fatalf("readManifest(empty file) err = %v, want nil", err)
	}
	if manifest == nil {
		t.Fatalf("readManifest(empty file) manifest = nil, want empty non-nil map")
	}
	if len(manifest) != 0 {
		t.Fatalf("readManifest(empty file) len = %d, want 0", len(manifest))
	}
}

// tools_handlers.go:448:21 CONDITIONALS_BOUNDARY — `if len(dependents) > 0 &&
// !body.Force` gates the 409 "has_dependents" response. With NO dependents and
// force=false the boundary must be FALSE so the handler proceeds to the cascade
// (200). If `>` flips to `>=`, `len(dependents) >= 0` is always true and a
// dependent-free delete wrongly returns 409.
func Test_gk_vibekit_u13_handleToolDeleteNoDependentsProceeds(t *testing.T) {
	// No bash on PATH: the cascade clear loop's bash exec fails and is
	// ignored, so the 200 response is produced without running any script.
	t.Setenv("PATH", t.TempDir())

	s, _ := newToolsTestServer(t, map[string]any{
		"binary": map[string]any{
			"gh": map[string]any{"enabled": true, "version": "v2.93.0"},
		},
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/tools/binary/gh", nil)
	req.SetPathValue("section", "binary")
	req.SetPathValue("name", "gh")
	rec := httptest.NewRecorder()

	s.handleToolDelete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleToolDelete(no dependents) status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Disabled []string `json:"disabled"`
		Code     string   `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code == "has_dependents" {
		t.Fatalf("dependent-free delete returned has_dependents conflict: %s", rec.Body.String())
	}
	if len(resp.Disabled) != 1 || resp.Disabled[0] != "binary.gh" {
		t.Fatalf("disabled = %v, want [binary.gh]", resp.Disabled)
	}
}

// tools_handlers.go:442:21 CONDITIONALS_NEGATION — `if r.ContentLength > 0`
// gates whether the {"force"} body is decoded. A request with unknown length
// (ContentLength == -1, e.g. chunked) must NOT be decoded, so force stays false
// and a delete with a dependent returns 409. If `>` flips to `<=`, an
// unknown-length body WOULD be decoded, force becomes true, and the cascade
// proceeds (200) instead.
func Test_gk_vibekit_u13_handleToolDeleteUnknownLengthBodySkipped(t *testing.T) {
	s, _ := newToolsTestServer(t, gk_vibekit_u13_manifestWithDependent())
	req := httptest.NewRequest(http.MethodDelete, "/api/tools/runtimes/node",
		strings.NewReader(`{"force":true}`))
	req.SetPathValue("section", "runtimes")
	req.SetPathValue("name", "node")
	req.ContentLength = -1 // unknown length: original skips body decode
	rec := httptest.NewRecorder()

	s.handleToolDelete(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("handleToolDelete(unknown-length force body) status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "has_dependents" {
		t.Fatalf("code = %q, want has_dependents", resp.Code)
	}
}

// tools_handlers.go:442:21 CONDITIONALS_BOUNDARY — at the ContentLength == 0
// boundary the body must NOT be decoded (the `> 0` guard is false), so force
// stays false and a delete with a dependent returns 409. If `>` flips to `>=`,
// a zero-ContentLength request WOULD decode the {"force":true} body, force
// becomes true, and the cascade proceeds (200) instead.
func Test_gk_vibekit_u13_handleToolDeleteZeroLengthBodySkipped(t *testing.T) {
	s, _ := newToolsTestServer(t, gk_vibekit_u13_manifestWithDependent())
	req := httptest.NewRequest(http.MethodDelete, "/api/tools/runtimes/node",
		strings.NewReader(`{"force":true}`))
	req.SetPathValue("section", "runtimes")
	req.SetPathValue("name", "node")
	req.ContentLength = 0 // boundary: original treats 0 as "no body", skips decode
	rec := httptest.NewRecorder()

	s.handleToolDelete(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("handleToolDelete(zero-length force body) status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "has_dependents" {
		t.Fatalf("code = %q, want has_dependents", resp.Code)
	}
}

// tools_handlers.go:570:16 CONDITIONALS_NEGATION — `out[b] = err == nil` marks
// a binary present when LookPath succeeds. With PATH restricted to a temp dir
// containing only a fake "node", node must report true and an absent binary
// like "go" must report false. If `==` flips to `!=`, both presence booleans
// invert.
func Test_gk_vibekit_u13_handleToolStatusPresenceBooleans(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	t.Setenv("PATH", dir) // only "node" resolves; "go" does not

	s := New()
	req := httptest.NewRequest(http.MethodGet, "/api/tools/status", nil)
	rec := httptest.NewRecorder()

	s.handleToolStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleToolStatus status = %d, want 200", rec.Code)
	}
	var out map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["node"] != true {
		t.Errorf("status[node] = %v, want true (fake node on PATH)", out["node"])
	}
	if out["go"] != false {
		t.Errorf("status[go] = %v, want false (go absent from restricted PATH)", out["go"])
	}
}
