package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifestFixture drops a tools.json into a temp configDir and
// returns a Server pointed at it.
func newToolsTestServer(t *testing.T, manifest map[string]any) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tools.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return New(WithConfigDir(dir)), path
}

func readBackManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal back: %v", err)
	}
	return m
}

func TestValidToolName(t *testing.T) {
	cases := map[string]bool{
		"gh":                         true,
		"typescript-language-server": true,
		"@typescript/native-preview": false, // slash not allowed
		"pyright-langserver":         true,
		"node":                       true,
		"":                           false,
		"../../etc/passwd":           false,
		"a b":                        false,
		"foo;rm -rf":                 false,
		"under_score":                true,
		"plus+ok":                    true,
		"@scoped":                    true,
	}
	for name, want := range cases {
		if got := validToolName(name); got != want {
			t.Errorf("validToolName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestResolveDepsLinearChain(t *testing.T) {
	m := map[string]any{
		"runtimes": map[string]any{
			"node": map[string]any{"enabled": false, "version": "v20"},
		},
		"lsp": map[string]any{
			"pyright": map[string]any{
				"enabled":  false,
				"version":  "1.1",
				"requires": []any{"runtimes.node"},
			},
		},
	}
	order, err := resolveDeps(m, "lsp", "pyright")
	if err != nil {
		t.Fatalf("resolveDeps: %v", err)
	}
	// node must come before pyright.
	if len(order) != 2 || order[0] != "runtimes.node" || order[1] != "lsp.pyright" {
		t.Fatalf("unexpected order: %v", order)
	}
}

func TestResolveDepsCycleDetected(t *testing.T) {
	m := map[string]any{
		"lsp": map[string]any{
			"a": map[string]any{"enabled": false, "version": "1", "requires": []any{"lsp.b"}},
			"b": map[string]any{"enabled": false, "version": "1", "requires": []any{"lsp.a"}},
		},
	}
	if _, err := resolveDeps(m, "lsp", "a"); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestResolveDepsUnknownRequire(t *testing.T) {
	m := map[string]any{
		"lsp": map[string]any{
			"gopls": map[string]any{"enabled": false, "version": "1", "requires": []any{"runtimes.go"}},
		},
	}
	if _, err := resolveDeps(m, "lsp", "gopls"); err == nil {
		t.Fatal("expected unknown-entry error for missing runtimes.go")
	}
}

func TestDependentsOf(t *testing.T) {
	m := map[string]any{
		"runtimes": map[string]any{
			"node": map[string]any{"enabled": true, "version": "v20"},
		},
		"lsp": map[string]any{
			"pyright": map[string]any{
				"enabled":  true,
				"version":  "1.1",
				"requires": []any{"runtimes.node"},
			},
			"tsls": map[string]any{
				"enabled":  false, // disabled dependents don't count
				"version":  "4",
				"requires": []any{"runtimes.node"},
			},
		},
	}
	deps := dependentsOf(m, "runtimes", "node")
	if len(deps) != 1 || deps[0] != "lsp.pyright" {
		t.Fatalf("dependentsOf = %v, want [lsp.pyright]", deps)
	}
}

// Regression guard for B3: an entry that OMITS the enabled field is
// active (enabled defaults to true), so it must be counted as a
// dependent — otherwise cascade-delete silently orphans it. Also
// verifies non-section keys like _comment are skipped.
func TestDependentsOf_AbsentEnabledCountsAsActive(t *testing.T) {
	m := map[string]any{
		"_comment": []any{"not a section"},
		"runtimes": map[string]any{
			"go": map[string]any{"enabled": true, "version": "1.23"},
		},
		"lsp": map[string]any{
			"gopls": map[string]any{
				// no "enabled" field -> active by default
				"version":  "v0.17",
				"requires": []any{"runtimes.go"},
			},
		},
	}
	deps := dependentsOf(m, "runtimes", "go")
	if len(deps) != 1 || deps[0] != "lsp.gopls" {
		t.Fatalf("dependentsOf = %v, want [lsp.gopls] (absent enabled = active)", deps)
	}
}

func TestHandleToolPatchAutoUpdate(t *testing.T) {
	s, path := newToolsTestServer(t, map[string]any{
		"binary": map[string]any{
			"gh": map[string]any{"enabled": true, "auto_update": true, "version": "v2.93.0"},
		},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/tools/binary/gh", strings.NewReader(`{"auto_update":false}`))
	req.SetPathValue("section", "binary")
	req.SetPathValue("name", "gh")
	rec := httptest.NewRecorder()
	s.handleToolPatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	m := readBackManifest(t, path)
	gh := m["binary"].(map[string]any)["gh"].(map[string]any)
	if gh["auto_update"] != false {
		t.Fatalf("auto_update not persisted: %v", gh["auto_update"])
	}
}

func TestHandleToolPatchRejectsMissingField(t *testing.T) {
	s, _ := newToolsTestServer(t, map[string]any{
		"binary": map[string]any{"gh": map[string]any{"enabled": true, "version": "v1"}},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/tools/binary/gh", strings.NewReader(`{}`))
	req.SetPathValue("section", "binary")
	req.SetPathValue("name", "gh")
	rec := httptest.NewRecorder()
	s.handleToolPatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing auto_update, got %d", rec.Code)
	}
}

func TestHandleToolPatchUnknownSection(t *testing.T) {
	s, _ := newToolsTestServer(t, map[string]any{})
	req := httptest.NewRequest(http.MethodPatch, "/api/tools/bogus/x", strings.NewReader(`{"auto_update":true}`))
	req.SetPathValue("section", "bogus")
	req.SetPathValue("name", "x")
	rec := httptest.NewRecorder()
	s.handleToolPatch(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for bad section, got %d", rec.Code)
	}
}

func TestHandleToolDeleteCascadeConflict(t *testing.T) {
	// Deleting node while pyright (enabled) requires it returns 409
	// with the dependents list, unless force is set.
	s, _ := newToolsTestServer(t, map[string]any{
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
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/tools/runtimes/node", nil)
	req.SetPathValue("section", "runtimes")
	req.SetPathValue("name", "node")
	rec := httptest.NewRecorder()
	s.handleToolDelete(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code       string   `json:"code"`
		Dependents []string `json:"dependents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "has_dependents" || len(resp.Dependents) != 1 || resp.Dependents[0] != "lsp.pyright" {
		t.Fatalf("unexpected conflict body: %+v", resp)
	}
}

func TestParseToolPathValidation(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/tools/runtimes/node", nil)
	req.SetPathValue("section", "runtimes")
	req.SetPathValue("name", "node")
	sec, name, ok := parseToolPath(req)
	if !ok || sec != "runtimes" || name != "node" {
		t.Fatalf("parseToolPath valid case failed: %q %q %v", sec, name, ok)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/tools/bogus/x", nil)
	bad.SetPathValue("section", "bogus")
	bad.SetPathValue("name", "x")
	if _, _, ok := parseToolPath(bad); ok {
		t.Fatal("parseToolPath accepted invalid section")
	}
}

func TestHandleToolStatusShape(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/api/tools/status", nil)
	rec := httptest.NewRecorder()
	s.handleToolStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Every well-known binary should be present as a key.
	for _, b := range statusBinaries {
		if _, ok := out[b]; !ok {
			t.Errorf("status missing key %q", b)
		}
	}
}

// Regression guard for B4 (broken-but-enabled): when setup-tools.sh
// fails (here simulated by pointing it at a missing script path so the
// bash subprocess errors), the target entry's enabled flag must be
// rolled back to false so the UI shows the Enable button again, not a
// false "healthy" state.
func TestHandleToolEnableRollsBackOnFailure(t *testing.T) {
	// Point the subprocess at a script that doesn't exist so bash
	// exits non-zero and runErr is set.
	t.Setenv("PATH", t.TempDir()) // ensures `bash` may not even be found, doesn't matter

	s, path := newToolsTestServer(t, map[string]any{
		"binary": map[string]any{
			"gh": map[string]any{
				"enabled":     false,
				"auto_update": true,
				"version":     "v2.93.0",
			},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tools/binary/gh/enable", nil)
	req.SetPathValue("section", "binary")
	req.SetPathValue("name", "gh")
	rec := httptest.NewRecorder()
	s.handleToolEnable(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// The setup script invocation should have errored (bash exec
	// failed under empty PATH).
	if resp.Error == "" {
		t.Fatalf("expected error in response, got none. body=%s", rec.Body.String())
	}
	// Manifest must show enabled=false again (rollback).
	m := readBackManifest(t, path)
	gh := m["binary"].(map[string]any)["gh"].(map[string]any)
	if gh["enabled"] != false {
		t.Fatalf("enabled not rolled back on failure: %v", gh["enabled"])
	}
}

// serverWithRawManifest writes raw bytes (NOT a JSON-marshaled map) into
// <configDir>/tools.json so callers can produce a genuinely empty (0-byte)
// manifest file, then returns a Server pointed at it.
func serverWithRawManifest(t *testing.T, raw []byte) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write raw manifest: %v", err)
	}
	return New(WithConfigDir(dir)), path
}

// manifestWithDependent returns a manifest where lsp.pyright (enabled)
// requires runtimes.node, so deleting runtimes.node has exactly one enabled
// dependent.
func manifestWithDependent() map[string]any {
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

// TestValidToolName_lengthBoundary pins the inclusive 80-byte upper bound:
// an 80-character name is accepted and an 81-character name is rejected.
func TestValidToolName_lengthBoundary(t *testing.T) {
	if !validToolName(strings.Repeat("a", 80)) {
		t.Errorf("validToolName(80 chars) = false, want true (len==80 upper boundary)")
	}
	if validToolName(strings.Repeat("a", 81)) {
		t.Errorf("validToolName(81 chars) = true, want false (just past upper boundary)")
	}
}

// TestReadManifestEmptyFileIsEmpty verifies that an empty (0-byte) tools.json
// parses to an empty, non-nil manifest with no error: the read path skips
// json.Unmarshal when there are no bytes, so a freshly-created empty file is
// treated as "no tools yet" rather than an "unexpected end of JSON" error.
func TestReadManifestEmptyFileIsEmpty(t *testing.T) {
	s, _ := serverWithRawManifest(t, []byte{})

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

// TestHandleToolDeleteNoDependentsProceeds verifies that deleting an entry
// with no enabled dependents proceeds to the cascade clear (200) rather than
// returning the 409 "has_dependents" conflict reserved for blocked deletes.
func TestHandleToolDeleteNoDependentsProceeds(t *testing.T) {
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
		Code     string   `json:"code"`
		Disabled []string `json:"disabled"`
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

// TestHandleToolDeleteUnknownLengthBodySkipped verifies that a DELETE whose
// body has unknown length (ContentLength == -1, e.g. chunked transfer) does
// NOT have its {"force":true} body decoded: force stays false and a delete
// with an enabled dependent returns 409. Only a positively-sized body is read.
func TestHandleToolDeleteUnknownLengthBodySkipped(t *testing.T) {
	s, _ := newToolsTestServer(t, manifestWithDependent())
	req := httptest.NewRequest(http.MethodDelete, "/api/tools/runtimes/node",
		strings.NewReader(`{"force":true}`))
	req.SetPathValue("section", "runtimes")
	req.SetPathValue("name", "node")
	req.ContentLength = -1 // unknown length: body decode is skipped
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

// TestHandleToolDeleteZeroLengthBodySkipped verifies that a DELETE with
// ContentLength == 0 is treated as "no body": the {"force":true} payload is
// not decoded, force stays false, and a delete with an enabled dependent
// returns 409.
func TestHandleToolDeleteZeroLengthBodySkipped(t *testing.T) {
	s, _ := newToolsTestServer(t, manifestWithDependent())
	req := httptest.NewRequest(http.MethodDelete, "/api/tools/runtimes/node",
		strings.NewReader(`{"force":true}`))
	req.SetPathValue("section", "runtimes")
	req.SetPathValue("name", "node")
	req.ContentLength = 0 // zero length: treated as no body, decode skipped
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

// TestHandleToolStatusPresenceBooleans verifies that handleToolStatus reports
// true exactly for binaries resolvable on PATH and false for absent ones. With
// PATH restricted to a temp dir holding only a fake "node", node reports
// present and an absent binary like "go" reports absent.
func TestHandleToolStatusPresenceBooleans(t *testing.T) {
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
