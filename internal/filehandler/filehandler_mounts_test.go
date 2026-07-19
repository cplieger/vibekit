package filehandler

// Tests for the allow-list mount machinery itself: grant parsing,
// mount opening, longest-prefix matching for nested grants, and
// cross-mount operation semantics. The conversion of the handler from
// deny-list-over-/ to per-mount os.Root is P3 (ws-file-allowlist-roots).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseBrowseRoots(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantRoots   []string
		wantInvalid int
	}{
		{name: "empty", raw: "", wantRoots: nil},
		{name: "single", raw: "/tmp", wantRoots: []string{"/tmp"}},
		{name: "multiple", raw: "/tmp:/data", wantRoots: []string{"/tmp", "/data"}},
		{name: "whitespace_and_empty_entries", raw: " /tmp : :/data ", wantRoots: []string{"/tmp", "/data"}},
		{name: "relative_rejected", raw: "tmp:/data", wantRoots: []string{"/data"}, wantInvalid: 1},
		{name: "root_grant_rejected", raw: "/:/data", wantRoots: []string{"/data"}, wantInvalid: 1},
		{name: "cleaned_and_deduped", raw: "/tmp/:/tmp//x/..:/tmp", wantRoots: []string{"/tmp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roots, invalid := ParseBrowseRoots(tc.raw)
			if !slices.Equal(roots, tc.wantRoots) {
				t.Errorf("roots = %v, want %v", roots, tc.wantRoots)
			}
			if len(invalid) != tc.wantInvalid {
				t.Errorf("invalid = %v, want %d entries", invalid, tc.wantInvalid)
			}
		})
	}
}

func TestNew_SkipsUnusableRoots_FailsOnZero(t *testing.T) {
	good := t.TempDir()
	// A missing dir and a "/" grant are both skipped with a warning;
	// the good mount survives.
	h, err := New("/does-not-exist-vibekit-test", "/", good)
	if err != nil {
		t.Fatalf("New with one good root: %v", err)
	}
	if len(h.mounts) != 1 || h.mounts[0].dir != good {
		t.Fatalf("mounts = %+v, want exactly the good root", h.mounts)
	}
	// Zero usable roots is a hard error.
	if _, err := New("/does-not-exist-vibekit-test"); err == nil {
		t.Fatal("New with zero usable roots = nil error, want failure")
	}
}

func TestNew_DedupesGrants(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir, dir, dir+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.mounts) != 1 {
		t.Fatalf("mounts = %+v, want 1 after dedupe", h.mounts)
	}
}

// Nested grants: the deeper mount wins prefix matching (mounts are
// sorted longest-first), so operations inside it run through ITS root.
func TestMountFor_NestedGrantWins(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "media")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := New(outer, inner)
	if err != nil {
		t.Fatal(err)
	}
	if m := h.mountFor(inner + "/x"); m == nil || m.dir != inner {
		t.Errorf("mountFor(inner/x) = %+v, want the nested mount %q", m, inner)
	}
	if m := h.mountFor(outer + "/other"); m == nil || m.dir != outer {
		t.Errorf("mountFor(outer/other) = %+v, want the outer mount %q", m, outer)
	}
	// A sibling whose name merely PREFIXES the mount name is outside.
	if m := h.mountFor(inner + "extra/x"); m != nil && m.dir == inner {
		t.Errorf("mountFor(%q) matched the %q mount; prefix match must be segment-aware", inner+"extra/x", inner)
	}
}

// The synthetic root listing renders a nested grant by its full
// slash-joined name, so the client can navigate straight into it.
func TestListFiles_Root_NestedGrantName(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "media")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := New(inner) // grant ONLY the nested dir
	if err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/files?path=/")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Files []fileEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := strings.TrimPrefix(inner, "/")
	if len(resp.Files) != 1 || resp.Files[0].Name != want {
		t.Errorf("root listing = %+v, want single mount named %q", resp.Files, want)
	}
}

// Cross-mount move is refused with an actionable 400 (a rename cannot
// cross os.Root handles; it already failed with EXDEV across the
// shipped container's volumes). Cross-mount copy works.
func TestAction_CrossMount_MoveRefusedCopyWorks(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	h, err := New(dirA, dirB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	prefixA := strings.TrimPrefix(dirA, "/")
	prefixB := strings.TrimPrefix(dirB, "/")

	move := `{"action":"move","path":"` + prefixA + `/src.txt","dest":"` + prefixB + `/dst.txt"}`
	rec := postReq(t, h, "/api/files/action", move)
	if rec.Code != 400 {
		t.Fatalf("cross-mount move status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "use copy") {
		t.Errorf("cross-mount move body = %s, want the use-copy hint", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dirA, "src.txt")); err != nil {
		t.Errorf("source vanished after refused move: %v", err)
	}

	cp := `{"action":"copy","path":"` + prefixA + `/src.txt","dest":"` + prefixB + `/dst.txt"}`
	rec = postReq(t, h, "/api/files/action", cp)
	if rec.Code != 200 {
		t.Fatalf("cross-mount copy status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dirB, "dst.txt"))
	if err != nil || string(data) != "payload" {
		t.Errorf("cross-mount copy dest = %q err=%v, want payload", data, err)
	}
}

// An in-tree symlink crossing from one granted mount into another is
// legal: the loc lands on the TARGET's mount, and the operation runs
// through that mount's root.
func TestResolvePath_SymlinkAcrossGrantedMounts(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	h, err := New(dirA, dirB)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dirB, "real.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dirA, "link.txt")); err != nil {
		t.Fatal(err)
	}
	l, err := h.resolvePath(strings.TrimPrefix(dirA, "/") + "/link.txt")
	if err != nil {
		t.Fatalf("resolvePath across granted mounts: %v", err)
	}
	if l.abs != target {
		t.Errorf("abs = %q, want the symlink target %q", l.abs, target)
	}
	if l.m == nil || l.m.dir != dirB {
		t.Errorf("mount = %+v, want the target's mount %q", l.m, dirB)
	}
}

// The upload default dir ("workspace") only works when /workspace is
// granted; on a handler without it the upload is refused, never
// silently redirected.
func TestHandleUpload_DefaultDirRequiresWorkspaceGrant(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := multipartUpload(t, "", map[string][]byte{"x.txt": []byte("x")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403 (no /workspace grant)", rec.Code)
	}
}
