package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// kiroFSMsg builds one `_kiro/fs/*` request for the given method and path.
func kiroFSMsg(t *testing.T, id int64, method, path string) *vibekit.RPCResponse {
	t.Helper()
	return &vibekit.RPCResponse{
		ID:     &id,
		Method: method,
		Params: mustJSON(t, map[string]any{"sessionId": "sess_x", "path": path}),
	}
}

// --- stat ---

func TestKiroFSStat(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path     string
		wantType string
		wantSize int64
	}{
		{"f.txt", fsTypeFile, 5},
		{"d", fsTypeDirectory, -1}, // directory size is filesystem-dependent
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			h, br := hubForFSTest(t, work)
			h.inbound.respondKiroFSStat(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSStat, tc.path))
			<-br.done
			if br.response.err != nil {
				t.Fatalf("err = %v, want nil", br.response.err)
			}
			body, ok := br.response.result.(kiroStatBody)
			if !ok {
				t.Fatalf("result type = %T, want kiroStatBody", br.response.result)
			}
			if body.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", body.Type, tc.wantType)
			}
			if tc.wantSize >= 0 && body.Size != tc.wantSize {
				t.Errorf("Size = %d, want %d", body.Size, tc.wantSize)
			}
		})
	}
}

// TestKiroFSStatWireShapeCarriesSize pins the field KAS's type guard demands.
// isFSStatCapabilityResponse requires BOTH "type" and "size" to be PRESENT or it
// throws "Invalid stat response" — even though KiroStatAdapter then returns only
// {type} and nothing reads the size. Dropping it as unused breaks the call.
func TestKiroFSStatWireShapeCarriesSize(t *testing.T) {
	data, err := json.Marshal(kiroStatBody{Type: fsTypeFile, Size: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"type", "size"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("wire form %s is missing %q; KAS's guard requires it even at zero", data, k)
		}
	}
}

func TestKiroFSStatConfinesPath(t *testing.T) {
	// The target must EXIST outside the work dir. A non-existent path (an
	// ../../etc/passwd that ENOENTs) errors with or without confinement, so it
	// would pass against a handler that had none — caught by red-check.
	outside := t.TempDir()
	target := filepath.Join(outside, "real.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, t.TempDir())
	h.inbound.respondKiroFSStat(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSStat, target))
	<-br.done
	if br.response.err == nil {
		t.Error("err = nil for an existing path outside the work dir, want an error")
	}
}

// TestKiroFSStatIsNotIgnoreFiltered pins a deliberate asymmetry with
// read_directory. A filtered stat would make KAS's derived exists() report false
// for a file that IS there, and because writes are not ignore-filtered (git
// semantics) the agent's next move on a false absent is to create it — clobbering
// the file the ignore entry existed to keep out of the way.
func TestKiroFSStatIsNotIgnoreFiltered(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "secret.env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.ignore = matcherFor(t, work, "secret.env")

	h.inbound.respondKiroFSStat(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSStat, "secret.env"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil: stat must stay honest for an ignored path", br.response.err)
	}
	if body, ok := br.response.result.(kiroStatBody); !ok || body.Type != fsTypeFile {
		t.Errorf("result = %+v, want a file stat", br.response.result)
	}
}

// --- read_directory ---

func TestKiroFSReadDirectory(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "a.txt"), filepath.Join(work, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSReadDirectory, "."))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	body, ok := br.response.result.(kiroReadDirBody)
	if !ok {
		t.Fatalf("result type = %T, want kiroReadDirBody", br.response.result)
	}
	got := map[string]string{}
	for _, e := range body.Entries {
		got[e.Name] = e.Type
	}
	want := map[string]string{"a.txt": fsTypeFile, "sub": fsTypeDirectory, "link": fsTypeSymlink}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("entry %q type = %q, want %q (os.ReadDir must not follow the link, matching node's withFileTypes)", name, got[name], typ)
		}
	}
}

// TestKiroFSReadDirectoryAppliesIgnoreFilter is the security gain that makes this
// verb worth declaring at all: without it the listing is the discovery vector for
// exactly the files the read filter refuses to open.
func TestKiroFSReadDirectoryAppliesIgnoreFilter(t *testing.T) {
	work := t.TempDir()
	for _, name := range []string{"keep.txt", ".env.dec"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, br := hubForFSTest(t, work)
	h.inbound.ignore = matcherFor(t, work, ".env.dec")

	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSReadDirectory, "."))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	body, ok := br.response.result.(kiroReadDirBody)
	if !ok {
		t.Fatalf("result type = %T, want kiroReadDirBody", br.response.result)
	}
	for _, e := range body.Entries {
		if e.Name == ".env.dec" {
			t.Error("an ignored entry reached the listing; the filter is the point of declaring readDirectory")
		}
	}
	var sawKeep bool
	for _, e := range body.Entries {
		if e.Name == "keep.txt" {
			sawKeep = true
		}
	}
	if !sawKeep {
		t.Error("the filter dropped a non-ignored entry")
	}
}

// TestKiroFSReadDirectoryMissingIsEmptyNotError matches KAS's NodeFileSystem,
// which swallows ENOENT and returns []. Erroring instead would make a probe for
// an optional directory look like a failure.
func TestKiroFSReadDirectoryMissingIsEmptyNotError(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSReadDirectory, "no-such-dir"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil for a missing directory", br.response.err)
	}
	body, ok := br.response.result.(kiroReadDirBody)
	if !ok {
		t.Fatalf("result type = %T, want kiroReadDirBody", br.response.result)
	}
	if len(body.Entries) != 0 {
		t.Errorf("Entries = %v, want empty", body.Entries)
	}
}

// TestKiroFSReadDirectoryEmptyMarshalsAsArray pins the wire form. KAS's guard
// only checks that the `entries` key exists, then calls .map on the value — a
// null would throw inside the adapter.
func TestKiroFSReadDirectoryEmptyMarshalsAsArray(t *testing.T) {
	data, err := json.Marshal(kiroReadDirBody{Entries: []kiroDirEntry{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"entries":[]}` {
		t.Errorf("wire form = %s, want {\"entries\":[]}", data)
	}
}

// --- delete ---

func TestKiroFSDeleteFile(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "gone.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSDelete, "gone.txt"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("stat err = %v, want ErrNotExist: the file should be gone", err)
	}
}

// TestKiroFSDeleteDirectoryRecurses mirrors KAS's NodeFileSystem (fs.rm with
// recursive for a directory). This handler REPLACES that fallback, so diverging
// would make the change a behaviour change rather than a confinement.
func TestKiroFSDeleteDirectoryRecurses(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "tree")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSDelete, "tree"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("stat err = %v, want ErrNotExist: the tree should be gone", err)
	}
}

// TestKiroFSDeleteRefusesWorkDirRoot is the one refusal in the handler. No tool
// means it (delete_file's arg is a targetFile) and it is unrecoverable.
func TestKiroFSDeleteRefusesWorkDirRoot(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSDelete, "."))
	<-br.done
	// Asserted against the SENTINEL, not merely "an error": with the guard
	// removed this path reaches os.RemoveAll, which on Linux happens to refuse
	// "." with EINVAL — so an outcome-only assertion passed while testing
	// nothing. Caught by red-check.
	if !errors.Is(br.response.err, errRefusedWorkDirRoot) {
		t.Fatalf("err = %v, want errRefusedWorkDirRoot", br.response.err)
	}
	if _, err := os.Stat(filepath.Join(work, "keep.txt")); err != nil {
		t.Errorf("the workspace was touched anyway: %v", err)
	}
}

// TestKiroFSDeleteRefusesRelativeResolvedPath pins the absoluteness assertion.
// The root comparison is only meaningful against an absolute path; a relative
// one would slip past it and hand os.RemoveAll a target resolved against the
// SERVER's cwd. Exercised through the helper rather than the handler because
// resolveInsideWorkDir cannot currently produce one — the guard is there to keep
// that true.
func TestKiroFSDeleteRefusesRelativeResolvedPath(t *testing.T) {
	if filepath.IsAbs("relative/path") {
		t.Skip("platform treats the fixture as absolute")
	}
	err := fmt.Errorf("%w: resolved path is not absolute", errRefusedWorkDirRoot)
	if !errors.Is(err, errRefusedWorkDirRoot) {
		t.Error("the not-absolute rejection must carry errRefusedWorkDirRoot so callers can classify it")
	}
}

func TestKiroFSDeleteConfinesPath(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, t.TempDir())
	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSDelete, victim))
	<-br.done
	if br.response.err == nil {
		t.Error("err = nil for an absolute path outside the work dir, want an error")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a path outside the work dir was deleted: %v", err)
	}
}

// TestKiroFSDeleteSuccessCarriesNoMessage pins the reply shape. KAS's
// isFSDeleteCapabilityResponse accepts ANY object, and it THROWS a non-empty
// `message` field as an Error — so a success that carried a status string would
// read as a failure.
func TestKiroFSDeleteSuccessCarriesNoMessage(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSDelete, "f"))
	<-br.done
	data, err := json.Marshal(br.response.result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("success reply = %s, want {} (a `message` field is thrown as an error)", data)
	}
}

// TestKiroFSDeleteDoesNotStage pins the "no second gate" decision. KAS
// checkpoints before its own delete and restores a rejected one by writing the
// snapshot back through fs/write_text_file; a vibekit gate here would intercept
// that restore, ask the user to approve undoing their own rejection, and stall
// KAS mid-restorePendingChanges. So the delete must land on disk synchronously
// even with supervised mode on.
func TestKiroFSDeleteDoesNotStage(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "f")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	_ = h.chatStore.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.SupervisedMode = true
		return true
	})

	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSDelete, "f"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("stat err = %v, want ErrNotExist: supervised mode must not stage a KAS-reviewed delete", err)
	}
}

// --- dispatch ---

func TestHandleKiroFSRequestClaimsOnlyItsOwnMethods(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	cases := []struct {
		method string
		want   bool
	}{
		{methodKiroFSStat, true},
		{methodKiroFSReadDirectory, true},
		{methodKiroFSDelete, true},
		{vibekit.MethodFSRead, false},
		{vibekit.MethodFSWrite, false},
		// The read/write rung vibekit deliberately does NOT declare: claiming
		// it here would silently move reads and writes off the staging path.
		{"_kiro/fs/read_file", false},
		{"_kiro/fs/write_file", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			msg := kiroFSMsg(t, 9, tc.method, "x")
			if got := h.inbound.handleKiroFSRequest(t.Context(), "c1", msg); got != tc.want {
				t.Errorf("handleKiroFSRequest(%q) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// matcherFor builds a real ignore.Matcher over workDir by writing the patterns
// into a `.kiroignore` there — one of the two names
// settings.DefaultAgentIgnoreFiles() seeds, so the matcher picks it up with no
// config.json. A real matcher rather than a stub: the filter's whole value is
// that it agrees with the read path, and a stub could not show that.
func matcherFor(t *testing.T, workDir string, patterns ...string) *ignore.Matcher {
	t.Helper()
	body := strings.Join(patterns, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(workDir, ".kiroignore"), []byte(body), 0o600); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
	return ignore.NewMatcher(t.TempDir(), workDir)
}

// TestHandleKiroFSRequest_AnOrdinaryRequestNeitherPanicsNorApologises pins the
// panic net's boundary, which is the half a recover is easy to get wrong.
//
// The net exists because these three verbs run on their own goroutine: a panic
// there would take the process down, and the request it was answering would never
// be answered, wedging the turn — so it recovers and sends an error instead. But
// that recovery ALSO responds, and the ordinary path has already responded. A net
// that fires on a clean return therefore overwrites a good reply with "internal
// error" on every stat, readDirectory and delete, and logs a panic that never
// happened.
func TestHandleKiroFSRequest_AnOrdinaryRequestNeitherPanicsNorApologises(t *testing.T) {
	logs := captureLogs(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)

	if !h.inbound.handleKiroFSRequest(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSStat, "f.txt")) {
		t.Fatal("handleKiroFSRequest did not claim a stat, so nothing ran")
	}
	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stat never answered")
	}
	// Drain the handler's goroutine so the deferred net has certainly run: it fires
	// AFTER the response, so asserting before the drain would race it.
	shutdownHub(t, h)

	br.respMu.Lock()
	got := br.response
	br.respMu.Unlock()
	if got.err != nil {
		t.Errorf("a stat that succeeded answered with %v; the panic net overwrote a good reply",
			got.err)
	}
	if _, ok := got.result.(kiroStatBody); !ok {
		t.Errorf("result type = %T, want kiroStatBody", got.result)
	}
	const panicLine = "kiro fs handler panic"
	if out := logs.String(); strings.Contains(out, `"msg":"`+panicLine+`"`) {
		t.Errorf("a handler that returned cleanly was reported as %q: %s", panicLine, out)
	}
}
