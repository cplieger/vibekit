package filebrowse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/atomicfile/v3"
)

// searchHandlerAt builds a handler whose single mount CLAIMS policyDir
// (e.g. "/config", so the real sensitive prefixes apply) while its os.Root is
// backed by a throwaway tree the test can populate. testHandlerAt does the same
// thing but discards the backing path, and a content search has to write files
// into it.
func searchHandlerAt(t *testing.T, policyDir string) (h *Handler, backing string) {
	t.Helper()
	backing = t.TempDir()
	root, err := os.OpenRoot(backing)
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{mounts: []mount{{
		root: root,
		dir:  policyDir,
		name: strings.TrimPrefix(policyDir, "/"),
	}}}, backing
}

// writeTree creates every named file (relative to dir) with its content,
// including any parent directories.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// searchReq runs one GET /api/files/search with the given query parameters.
func searchReq(t *testing.T, h *Handler, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return searchReqCtx(t, h, t.Context(), params)
}

func searchReqCtx(t *testing.T, h *Handler, ctx context.Context, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/files/search?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeSearch(t *testing.T, rec *httptest.ResponseRecorder) FileSearchResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res FileSearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return res
}

// matchPaths is the set of paths a result names, for order-insensitive asserts.
func matchPaths(res FileSearchResult) []string {
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, m.Path)
	}
	return out
}

func TestSearch_FindsMatchRecursively(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"top.txt":            "nothing here\n",
		"a/b/deep.go":        "package b\n\nfunc needle() {}\n",
		"a/b/c/deeper.txt":   "line one\nsecond needle line\n",
		"a/unrelated.md":     "prose\n",
		"a/b/case.txt":       "NEEDLE shouting\n",
		"a/b/c/d/e/far.conf": "needle\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	want := []string{
		filepath.Join(dir, "a/b/c/d/e/far.conf"),
		filepath.Join(dir, "a/b/c/deeper.txt"),
		filepath.Join(dir, "a/b/case.txt"),
		filepath.Join(dir, "a/b/deep.go"),
	}
	got := matchPaths(res)
	if len(got) != len(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if res.Truncated {
		t.Error("truncated = true on a tree well under every cap")
	}
	if res.Scanned != 6 {
		t.Errorf("scanned = %d, want 6 (every file was read)", res.Scanned)
	}
	if res.Matched != 4 {
		t.Errorf("matched = %d, want 4: one matching line per hit, nothing cut", res.Matched)
	}
	// Line numbers are 1-based and the excerpt is the matching line.
	for _, m := range res.Matches {
		if m.Line < 1 {
			t.Errorf("%s: line = %d, want >= 1", m.Path, m.Line)
		}
	}
	deeper := res.Matches[1]
	if deeper.Line != 2 || deeper.Excerpt != "second needle line" {
		t.Errorf("deeper.txt hit = line %d %q, want line 2 %q",
			deeper.Line, deeper.Excerpt, "second needle line")
	}
}

func TestSearch_CaseSensitivity(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"lower.txt": "needle\n",
		"upper.txt": "NEEDLE\n",
	})

	insensitive := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	if len(insensitive.Matches) != 2 {
		t.Errorf("insensitive matches = %v, want both files", matchPaths(insensitive))
	}

	sensitive := decodeSearch(t, searchReq(t, h,
		map[string]string{"path": prefix, "q": "needle", "case": "1"}))
	if len(sensitive.Matches) != 1 || sensitive.Matches[0].Path != filepath.Join(dir, "lower.txt") {
		t.Errorf("case=1 matches = %v, want only lower.txt", matchPaths(sensitive))
	}

	// Anything other than "1" reads as insensitive, matching the transcript
	// search's rule for the same parameter.
	other := decodeSearch(t, searchReq(t, h,
		map[string]string{"path": prefix, "q": "needle", "case": "true"}))
	if len(other.Matches) != 2 {
		t.Errorf("case=true matches = %v, want both (only \"1\" enables it)", matchPaths(other))
	}
}

// The file budget is spent at admission and names how many files the walk
// READS: a tree of exactly that many is read whole and is a complete answer, and
// one more is the file the walk refuses, which is what Truncated says. The files
// match nothing, so the reply cap cannot be what stops the walk.
func TestSearch_FileBudgetMarksOnlyTheFileItRefuses(t *testing.T) {
	tests := []struct {
		name          string
		files         int
		wantTruncated bool
	}{
		{name: "exactly_the_budget", files: maxSearchFiles, wantTruncated: false},
		{name: "one_file_past_the_budget", files: maxSearchFiles + 1, wantTruncated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			for i := range tc.files {
				name := filepath.Join(dir, fmt.Sprintf("f%05d.txt", i))
				if err := os.WriteFile(name, []byte("nothing matching\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
			if res.Truncated != tc.wantTruncated {
				t.Errorf("truncated = %v over %d files, want %v", res.Truncated, tc.files, tc.wantTruncated)
			}
			if res.Scanned != maxSearchFiles {
				t.Errorf("scanned = %d over %d files, want %d: the budget is read at admission, before any file is skipped",
					res.Scanned, tc.files, maxSearchFiles)
			}
			if len(res.Matches) != 0 {
				t.Errorf("matches = %v, want none: the fixture holds the needle nowhere", matchPaths(res))
			}
		})
	}
}

// The reply cap crossed in the LAST chunk of a tree the walk read whole is a
// cut and not a hole: every file was read, so Matched says what was cut and
// Truncated stays false. A directory reports its end only on the read after
// its last entries, which a stop before that read would mistake for more to do.
func TestSearch_ReplyCapOnAFullyReadTreeIsACutNotAHole(t *testing.T) {
	h, dir, prefix := testDir(t)
	const perFile = maxFileMatches + 5
	files := maxSearchMatches/maxFileMatches + 2
	for i := range files {
		name := filepath.Join(dir, fmt.Sprintf("m%03d.txt", i))
		if err := os.WriteFile(name, []byte(strings.Repeat("needle\n", perFile)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	if len(res.Matches) != maxSearchMatches {
		t.Errorf("matches = %d, want exactly the cap %d", len(res.Matches), maxSearchMatches)
	}
	if want := files * perFile; res.Matched != want {
		t.Errorf("matched = %d, want %d: every matching line counted, past both caps", res.Matched, want)
	}
	if res.Scanned != files {
		t.Errorf("scanned = %d, want %d: every file was read", res.Scanned, files)
	}
	if res.Truncated {
		t.Error("truncated = true after a walk that read every file: a full reply is a cut, not a hole")
	}
	// Per-file cap: no single file may contribute more than maxFileMatches.
	byPath := map[string]int{}
	for _, m := range res.Matches {
		byPath[m.Path]++
	}
	for p, n := range byPath {
		if n > maxFileMatches {
			t.Errorf("%s contributed %d matches, want at most %d", p, n, maxFileMatches)
		}
	}
}

func TestSearch_SensitivePathNeverInResults(t *testing.T) {
	// The mount claims /config, so the REAL sensitive prefixes apply. The walk
	// reaches these from ABOVE — nobody asked for them by name — which is the
	// case an os.Root cannot cover, since it has no sub-path denial.
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{
		"mcp-secrets.json":             `{"token":"needle-secret"}`,
		"mcp.json":                     `{"server":"needle-server"}`,
		"push-subs.json":               `{"sub":"needle"}`,
		"vapid-keys.json":              `{"key":"needle"}`,
		"home/.aws/sso/cache/tok.json": `{"accessToken":"needle"}`,
		"home/.ssh/id_rsa":             "needle private key\n",
		"chats/c1.json":                `{"messages":"needle"}`,
		"kiro/steering/x.md":           "needle\n",
		"visible.txt":                  "needle in a browsable file\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": "config", "q": "needle"}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != "/config/visible.txt" {
		t.Fatalf("matches = %v, want only /config/visible.txt", got)
	}
	// The bytes themselves must never reach the wire, whatever the path list says.
	body := res.Matches[0].Excerpt
	for _, leak := range []string{"needle-secret", "needle-server", "accessToken", "private key"} {
		if strings.Contains(body, leak) {
			t.Errorf("excerpt leaked %q", leak)
		}
	}
}

func TestSearch_OutsideGrantedRootsRefused(t *testing.T) {
	h, _, _ := testDir(t)
	for _, p := range []string{"etc", "etc/passwd", "../etc/passwd"} {
		rec := searchReq(t, h, map[string]string{"path": p, "q": "root"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("path=%q status = %d, want 403", p, rec.Code)
		}
	}
}

func TestSearch_SymlinkOutOfMountNotWalked(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"own.txt": "needle here\n"})
	// A symlink to a real out-of-mount tree, planted where the walk will meet
	// it. Symlinks are skipped outright, so its contents are never read; the
	// os.Root would refuse the target anyway, and this closes the in-mount
	// cycle and duplicate-report cases as well.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	// And a self-referential in-mount link, which would cycle if followed.
	if err := os.Symlink(dir, filepath.Join(dir, "loop")); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	got := matchPaths(res)
	if len(got) != 1 || got[0] != filepath.Join(dir, "own.txt") {
		t.Fatalf("matches = %v, want only own.txt", got)
	}
	if res.Truncated {
		t.Error("truncated = true: the symlink loop was followed until a cap absorbed it")
	}
}

func TestSearch_SkipsBinaryAndNonRegular(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"text.txt":   "needle\n",
		"binary.bin": "needle\x00 with a NUL\n",
	})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.txt"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	got := matchPaths(res)
	if len(got) != 1 || got[0] != filepath.Join(dir, "text.txt") {
		t.Fatalf("matches = %v, want only text.txt", got)
	}
	// Two files were read and one contributed nothing: the binary was sniffed
	// and dropped before its bytes were read, and the answer covers it. The FIFO
	// is never opened — its name is all the search may consult — so it is not
	// scanned, and a skip the search chose is not a hole in the answer.
	if res.Scanned != 2 {
		t.Errorf("scanned = %d, want 2 (text + binary read; the FIFO is name-only)", res.Scanned)
	}
	if res.Truncated {
		t.Error("truncated = true: a binary and a FIFO are skips, not losses")
	}
}

func TestSearch_CancelledWritesNothing(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"a.txt": "needle\n"})

	// Deliberately pre-cancelled: the property is that a scan whose caller is
	// already gone writes no body, rather than reporting a half-scan as whole.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := searchReqCtx(t, h, ctx, map[string]string{"path": prefix, "q": "needle"})
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty on a cancelled request", rec.Body.String())
	}
}

func TestSearch_Globs(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"root.go":                   "needle\n",
		"root.md":                   "needle\n",
		"src/a.go":                  "needle\n",
		"src/a.md":                  "needle\n",
		"src/deep/b.go":             "needle\n",
		"node_modules/pkg/index.js": "needle\n",
		"node_modules/pkg/x.go":     "needle\n",
	})
	rel := func(names ...string) []string {
		out := make([]string, 0, len(names))
		for _, n := range names {
			out = append(out, filepath.Join(dir, n))
		}
		return out
	}

	tests := []struct {
		name    string
		include string
		exclude string
		want    []string
	}{{
		// A separator-free pattern matches the BASENAME, which is what makes
		// the common spelling do the common thing: path.Match's `*` does not
		// cross "/", so a path-form `*.go` would match root.go alone.
		name:    "basename pattern reaches every depth",
		include: "*.go",
		want:    rel("node_modules/pkg/x.go", "root.go", "src/a.go", "src/deep/b.go"),
	}, {
		name:    "pattern with a separator matches the mount-relative path",
		include: "src/*.go",
		want:    rel("src/a.go"),
	}, {
		name:    "exclude prunes a directory by basename",
		include: "*.go",
		exclude: "node_modules",
		want:    rel("root.go", "src/a.go", "src/deep/b.go"),
	}, {
		name:    "exclude outranks include on a file",
		include: "*.go",
		exclude: "a.go",
		want:    rel("node_modules/pkg/x.go", "root.go", "src/deep/b.go"),
	}, {
		name:    "comma-separated patterns are one list",
		include: "*.md,*.js",
		want:    rel("node_modules/pkg/index.js", "root.md", "src/a.md"),
	}, {
		name:    "no include means everything",
		exclude: "node_modules",
		want:    rel("root.go", "root.md", "src/a.go", "src/a.md", "src/deep/b.go"),
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]string{"path": prefix, "q": "needle"}
			if tc.include != "" {
				params["include"] = tc.include
			}
			if tc.exclude != "" {
				params["exclude"] = tc.exclude
			}
			got := matchPaths(decodeSearch(t, searchReq(t, h, params)))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearch_MalformedPatternIsRejected(t *testing.T) {
	h, _, prefix := testDir(t)
	for _, key := range []string{"include", "exclude"} {
		rec := searchReq(t, h, map[string]string{"path": prefix, "q": "x", key: "[a-"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s=[a- status = %d, want 400 (a bad pattern must not read as \"no match\")",
				key, rec.Code)
		}
	}
}

func TestSearch_RootFansOutOverMounts(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeTree(t, a, map[string]string{"in-a.txt": "needle\n"})
	writeTree(t, b, map[string]string{"in-b.txt": "needle\n"})
	h, err := New(a, b)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/", "", "."} {
		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": p, "q": "needle"}))
		if len(res.Matches) != 2 {
			t.Errorf("path=%q matches = %v, want both mounts searched", p, matchPaths(res))
		}
	}
}

func TestSearch_EmptyQueryAndMethod(t *testing.T) {
	h, _, prefix := testDir(t)

	for _, q := range []string{"", "   "} {
		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": q}))
		if len(res.Matches) != 0 || res.Scanned != 0 || res.Truncated {
			t.Errorf("q=%q result = %+v, want an empty scan", q, res)
		}
		// A nil slice would serialise as JSON null, which the client must not
		// have to narrow.
		if !strings.Contains(searchReq(t, h,
			map[string]string{"path": prefix, "q": q}).Body.String(), `"matches":[]`) {
			t.Errorf("q=%q body must carry an empty array, not null", q)
		}
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/files/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestSearch_SingleFileRoot(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"one.txt": "alpha\nneedle\n"})

	res := decodeSearch(t, searchReq(t, h,
		map[string]string{"path": filepath.Join(prefix, "one.txt"), "q": "needle"}))
	if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
		t.Fatalf("matches = %+v, want one hit on line 2", res.Matches)
	}
}

func TestExcerptLine(t *testing.T) {
	long := strings.Repeat("a", 200) + "needle" + strings.Repeat("b", 200)
	tests := []struct {
		name    string
		line    string
		hitRune int
		want    string
	}{
		{name: "short line comes back whole", line: "  hello needle  ", hitRune: 8, want: "hello needle"},
		{name: "CRLF loses its CR", line: "needle\r", hitRune: 0, want: "needle"},
		// The hit sits at byte 7 and rune 6: the index is a RUNE index.
		{name: "multi-byte is not split", line: "héllo needle", hitRune: 6, want: "héllo needle"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := excerptLine(tc.line, tc.hitRune); got != tc.want {
				t.Errorf("excerptLine(%q, %d) = %q, want %q", tc.line, tc.hitRune, got, tc.want)
			}
		})
	}

	t.Run("long line is windowed around the match", func(t *testing.T) {
		got := excerptLine(long, 200)
		if !strings.Contains(got, "needle") {
			t.Errorf("excerpt %q lost the match", got)
		}
		if !strings.HasPrefix(got, "\u2026") || !strings.HasSuffix(got, "\u2026") {
			t.Errorf("excerpt %q must be elided on both sides", got)
		}
		if len([]rune(got)) > 2*searchExcerptRadius+8 {
			t.Errorf("excerpt is %d runes, want roughly the window", len([]rune(got)))
		}
	})
}

// TestMatchLines_ExcerptWindowFollowsTheOriginalBytes: a case fold can change a
// rune's byte length (U+212A KELVIN SIGN, three bytes, folds to a one-byte `k`),
// so an offset measured in the FOLDED line points somewhere else in the original.
// The window is placed by rune index, which the fold preserves.
func TestMatchLines_ExcerptWindowFollowsTheOriginalBytes(t *testing.T) {
	line := strings.Repeat("\u212a", 300) + "needle" + strings.Repeat("z", 50)
	sc := newFileScan(t.Context(), "NEEDLE", false, nil, nil)

	hits, matched := sc.matchLines("/x/kelvin.txt", line+"\n")
	if matched != 1 || len(hits) != 1 {
		t.Fatalf("matchLines = %d hits, matched %d, want one of each", len(hits), matched)
	}
	if !strings.Contains(hits[0].Excerpt, "needle") {
		t.Errorf("excerpt = %q, want it to hold the match: the window drifted off the hit", hits[0].Excerpt)
	}
}

// --- confinement: the object READ is the object the walk admitted ---

// searchDirAt opens one directory the way the walk opens it: through the mount's
// own root handle, one component at a time, refusing a symlink at every step.
func searchDirAt(t *testing.T, h *Handler, abs string) *os.File {
	t.Helper()
	f, err := openSearchRoot(loc{m: &h.mounts[0], abs: abs})
	if err != nil {
		t.Fatalf("openSearchRoot(%q): %v", abs, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestSearch_SwappedNameIsNotReadAfterAdmission is the confinement property the
// whole walk is built for: between the moment an entry is CLASSIFIED as a
// browsable regular file and the moment its bytes are read, a process able to
// write into the granted tree can replace that name — or one of its ancestors —
// with a symlink to a denied file. The mount boundary survives that (the target
// is inside the mount), so the only thing standing between the swap and an
// excerpt of /config/mcp-secrets.json on the wire is that the read never
// resolves the name a second time.
//
// Both halves are exercised at the exact boundary the swap targets, because the
// window between admission and read cannot be opened deterministically from
// outside: the test performs the swap in it directly.
func TestSearch_SwappedNameIsNotReadAfterAdmission(t *testing.T) {
	const secret = "needle-secret-token"

	t.Run("the admitted file itself", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{
			"mcp-secrets.json": `{"refresh_token":"` + secret + `"}`,
			"pub/notes.txt":    "needle in a browsable file\n",
		})
		dir := searchDirAt(t, h, "/config/pub")

		// The walk has read the dirent, applied IsSensitive("/config/pub/notes.txt")
		// and the globs, and admitted it. This is that candidate.
		cand := searchCandidate{name: "notes.txt", abs: "/config/pub/notes.txt"}

		// The swap, in the window the old check-then-reopen sequence left open.
		notes := filepath.Join(backing, "pub", "notes.txt")
		if err := os.Remove(notes); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../mcp-secrets.json", notes); err != nil {
			t.Fatal(err)
		}

		sc := newFileScan(t.Context(), "needle", false, nil, nil)
		got := sc.readCandidate(dir, cand)
		hits := got.hits
		if len(hits) != 0 {
			t.Fatalf("read %d hits from a name swapped to a symlink: %+v", len(hits), hits)
		}
		// The refusal IS the guarantee, so it is not a hole in the answer: reporting
		// it as truncation would tell every caller its result was partial whenever a
		// symlink sat in a searched tree.
		if got.unread {
			t.Error("readCandidate reported a swap refusal as an unread file; a refused symlink is a deliberate skip, not a loss")
		}
		for _, m := range hits {
			if strings.Contains(m.Excerpt, secret) {
				t.Errorf("excerpt leaked the credential store: %q", m.Excerpt)
			}
		}
	})

	t.Run("an ancestor of the admitted file", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{
			"chats/c1.json": `{"messages":"` + secret + `"}`,
			"pub/notes.txt": "needle in a browsable file\n",
		})
		dir := searchDirAt(t, h, "/config")
		d := searchDir{f: dir, abs: "/config"}

		// The walk has classified "pub" as a browsable directory. Now it becomes a
		// link into the chat store, whose contents IsSensitive would have denied
		// under their own names but cannot deny under /config/pub/...
		pub := filepath.Join(backing, "pub")
		if err := os.Rename(pub, pub+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("chats", pub); err != nil {
			t.Fatal(err)
		}

		sc := newFileScan(t.Context(), "needle", false, nil, nil)
		if !sc.descend(d, "pub") {
			t.Error("a refused descent must skip the entry, not stop the whole scan")
		}
		if len(sc.matches) != 0 {
			t.Fatalf("walked into a swapped ancestor and produced %+v", sc.matches)
		}
	})

	// The PREMISE, pinned so a future simplification cannot quietly remove the
	// reason openChild exists: an *os.Root follows a symlink whose target stays
	// inside its mount, and passing O_NOFOLLOW to Root.OpenFile does not change
	// that — the root adds O_NOFOLLOW to every component itself and then resolves
	// the link it finds. So neither the confined read nor the confined open can
	// express "refuse a symlink here"; only an openat against the containing
	// directory's descriptor can. If this test ever fails, the dependency's
	// behaviour changed and this file's design note is what to re-read.
	t.Run("the mount root follows an in-root symlink", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{
			"mcp-secrets.json": `{"refresh_token":"` + secret + `"}`,
		})
		if err := os.Symlink("mcp-secrets.json", filepath.Join(backing, "decoy.txt")); err != nil {
			t.Fatal(err)
		}
		root := h.mounts[0].root

		data, err := atomicfile.ReadBoundedInRoot(t.Context(), root, "decoy.txt", maxSearchFileSize)
		if err != nil || !strings.Contains(string(data), secret) {
			t.Fatalf("confined read of a symlink = %q, %v; want the target's bytes", data, err)
		}
		f, err := root.OpenFile("decoy.txt", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatalf("Root.OpenFile with O_NOFOLLOW refused a symlink: %v", err)
		}
		_ = f.Close()

		// And the primitive the search actually uses refuses it.
		dir := searchDirAt(t, h, "/config")
		if got, openErr := openChild(dir, "decoy.txt", "/config/decoy.txt", searchFileFlags); openErr == nil {
			_ = got.Close()
			t.Error("openChild followed a symlink; the search's whole confinement rests on it not doing that")
		} else if !isSwapRefusal(openErr) {
			t.Errorf("openChild refused with %v, want the kernel's symlink refusal", openErr)
		}
	})
}

// --- bounded work: one directory cannot defeat cancellation or the caps ---

// allocatedBy reports how many bytes fn allocated. TotalAlloc is cumulative and
// unaffected by collection, so the measurement is stable; every assertion on it
// below compares against a bound orders of magnitude away from the value the
// defect produced, never against a tight budget.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestSearch_HugeDirectoryIsNotReadBeforeCancellationIsChecked(t *testing.T) {
	h, dir, _ := testDir(t)
	// Entries that would never consume the FILE budget even if the scan ran:
	// the point is that the directory's inventory is not materialised before the
	// walk gets a chance to refuse it, and the file cap cannot bound that work.
	const entries = 20_000
	for i := range entries {
		name := filepath.Join(dir, fmt.Sprintf("e%05d.bin", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Already gone: the walk must observe that before it reads the directory.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	sc := newFileScan(ctx, "needle", false, nil, nil)

	grew := allocatedBy(func() { sc.addRoot(loc{m: &h.mounts[0], abs: dir}) })

	// A whole-directory ReadDir allocates one entry per name before the first
	// check, which for this directory is megabytes. One chunk is 256 entries, and
	// the walk should not even reach that.
	const bound = 256 << 10
	if grew > bound {
		t.Errorf("a cancelled scan allocated %d bytes over a %d-entry directory, want under %d: "+
			"the directory was read before the context was consulted", grew, entries, bound)
	}
	if sc.files != 0 {
		t.Errorf("files = %d, want 0 on an already-cancelled scan", sc.files)
	}
}

func TestSearch_DirectoryLargerThanOneChunkIsFullyWalked(t *testing.T) {
	h, dir, prefix := testDir(t)
	// More than one ReadDir chunk, with the matches deliberately in the SECOND
	// chunk: a loop that stops after the first read, or that miscounts the EOF
	// boundary, finds none of them.
	const total = searchReadDirChunk + 7
	hitFrom, hitTo := searchReadDirChunk, searchReadDirChunk+5
	for i := range total {
		body := "nothing here\n"
		if i >= hitFrom && i < hitTo {
			body = "needle\n"
		}
		name := filepath.Join(dir, fmt.Sprintf("f%04d.txt", i))
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	if len(res.Matches) != hitTo-hitFrom {
		t.Errorf("matches = %d, want %d: %v", len(res.Matches), hitTo-hitFrom, matchPaths(res))
	}
	if res.Scanned != total {
		t.Errorf("scanned = %d, want every one of the %d entries", res.Scanned, total)
	}
	if res.Truncated {
		t.Error("truncated = true on a directory well under every cap")
	}
}

// --- a binary costs the sniff prefix, not the per-file ceiling ---

func TestSearch_BinaryIsRejectedBeforeItsBytesAreRead(t *testing.T) {
	h, dir, _ := testDir(t)
	// One binary per read worker, each at the per-file ceiling. Reading first and
	// asking "was that binary?" afterwards means the fan-out holds
	// searchWorkers * maxSearchFileSize of bytes that were never going to be
	// reported; sniffing first means it holds searchWorkers * binarySniffN.
	payload := make([]byte, maxSearchFileSize)
	copy(payload, "needle\x00binary")
	for i := range searchWorkers {
		name := filepath.Join(dir, fmt.Sprintf("b%02d.bin", i))
		if err := os.WriteFile(name, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// allocatedBy collects first and TotalAlloc is cumulative, so the fixture's
	// own 512 KiB is already behind the measurement window.
	sc := newFileScan(t.Context(), "needle", false, nil, nil)
	grew := allocatedBy(func() { sc.addRoot(loc{m: &h.mounts[0], abs: dir}) })

	if sc.files != searchWorkers {
		t.Fatalf("files = %d, want %d (every binary is opened and counted)", sc.files, searchWorkers)
	}
	if sc.matched != 0 {
		t.Errorf("matched = %d, want 0: a match inside a binary is not reportable", sc.matched)
	}
	// The whole-read order needed searchWorkers * 512 KiB = 4 MiB. The sniff
	// order needs searchWorkers * 8 KiB plus the walk's own overhead.
	bound := uint64(searchWorkers*binarySniffN) + (256 << 10)
	if grew > bound {
		t.Errorf("scanning %d ceiling-sized binaries allocated %d bytes, want under %d: "+
			"the bytes were read before the sniff decided they were unreportable",
			searchWorkers, grew, bound)
	}
}

// --- a text file costs one ceiling, once ---

// A file at the ceiling is read into ONE buffer that the scan then reads as a
// string without copying, so a worker holds about one ceiling and
// searchReadBudget bounds what the fan-out holds at once. Case-sensitive, so the
// fold allocates nothing and the read is the only thing measured.
func TestSearch_TextFileIsReadIntoOneBuffer(t *testing.T) {
	h, dir, _ := testDir(t)
	writeTree(t, dir, map[string]string{"big.txt": atCeiling()})

	sc := newFileScan(t.Context(), "needle", true, nil, nil)
	grew := allocatedBy(func() { sc.addRoot(loc{m: &h.mounts[0], abs: dir}) })

	if sc.files != 1 || sc.matched != 1 {
		t.Fatalf("files = %d, matched = %d, want 1 and 1 (the file was read to its last line)", sc.files, sc.matched)
	}
	// One ceiling for the file, plus the sniff window, the copy's scratch buffer
	// and the walk's own overhead. A second copy of the file lands well past it.
	const bound = maxSearchFileSize + (256 << 10)
	if grew > bound {
		t.Errorf("reading one %d-byte text file allocated %d bytes, want under %d: the file was copied after it was read",
			maxSearchFileSize, grew, bound)
	}
}

// --- globs are relative to the folder searched, not to the mount ---

func TestSearch_GlobsMatchThePathUnderTheSearchedFolder(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"project/src/a.go":                "needle\n",
		"project/src/deep/x.go":           "needle\n",
		"project/src/node_modules/p/y.go": "needle\n",
	})
	rel := func(names ...string) []string {
		out := make([]string, 0, len(names))
		for _, n := range names {
			out = append(out, filepath.Join(dir, n))
		}
		return out
	}

	tests := []struct {
		name    string
		at      string
		include string
		exclude string
		want    []string
	}{{
		// The reader is looking at project/src and types what is under it. A
		// mount-relative subject would silently require them to spell the
		// searched folder's own prefix, and answer "no matches" when they don't.
		name:    "include with a separator is relative to the searched folder",
		at:      "project/src",
		include: "deep/*.go",
		want:    rel("project/src/deep/x.go"),
	}, {
		name:    "exclude with a separator prunes a directory under the searched folder",
		at:      "project",
		include: "*.go",
		exclude: "src/node_modules",
		want:    rel("project/src/a.go", "project/src/deep/x.go"),
	}, {
		// At the mount root the two coordinate spaces coincide, which is why the
		// original glob tests could not see the difference.
		name:    "at the mount root the searched folder IS the mount",
		at:      "",
		include: "project/src/*.go",
		want:    rel("project/src/a.go"),
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]string{"path": filepath.Join(prefix, tc.at), "q": "needle"}
			if tc.include != "" {
				params["include"] = tc.include
			}
			if tc.exclude != "" {
				params["exclude"] = tc.exclude
			}
			got := matchPaths(decodeSearch(t, searchReq(t, h, params)))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSearch_DepthCapStopsAndSaysSo pins the descriptor budget: the walk holds
// one directory handle per level of descent, so a tree deeper than the cap
// reports an incomplete answer rather than spending the process's descriptors.
func TestSearch_DepthCapStopsAndSaysSo(t *testing.T) {
	h, dir, prefix := testDir(t)
	deep := dir
	for range maxSearchDepth + 2 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "buried.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shallow := filepath.Join(dir, "shallow.txt")
	if err := os.WriteFile(shallow, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	got := matchPaths(res)
	if len(got) != 1 || got[0] != shallow {
		t.Fatalf("matches = %v, want only the shallow file", got)
	}
	if !res.Truncated {
		t.Error("truncated = false after refusing to descend: files were left unread")
	}
}

// The reply budget is INCLUSIVE and is read with an entry in hand: the scan
// whose reply is full stops at that entry, and the entry going unread is what
// makes the answer partial. Driven at the counter, because the real budget is
// 200 rows.
func TestFileScan_ReplyBudgetIsInclusive(t *testing.T) {
	tests := []struct {
		name       string
		rows       int
		wantCapped bool
	}{
		{name: "nothing_spent", rows: 0, wantCapped: false},
		{name: "one_row_below_the_reply_budget", rows: maxSearchMatches - 1, wantCapped: false},
		{name: "reply_budget_spent", rows: maxSearchMatches, wantCapped: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc := newFileScan(t.Context(), "needle", false, nil, nil)
			sc.matches = make([]FileMatch, tc.rows)

			if got := sc.capped(); got != tc.wantCapped {
				t.Errorf("capped() with rows=%d = %v, want %v", tc.rows, got, tc.wantCapped)
			}
			if got := sc.truncated; got != tc.wantCapped {
				t.Errorf("capped() with rows=%d left truncated = %v, want %v: a stop at an entry leaves it unread",
					tc.rows, got, tc.wantCapped)
			}
		})
	}
}

// The directory budget names the last directory the walk still ENTERS: the
// directory that spends it is listed and its files read, and the one after it
// is refused before its listing, which is what Truncated reports. Driven at the
// counter, because the real budget is 20 000 directories.
func TestWalkDir_DirectoryBudgetIsInclusive(t *testing.T) {
	tests := []struct {
		name          string
		dirs          int
		wantWalked    bool
		wantMatches   int
		wantTruncated bool
	}{
		{name: "the_last_directory_of_the_budget", dirs: maxSearchDirs - 1, wantWalked: true, wantMatches: 1, wantTruncated: false},
		{name: "one_directory_past_the_budget", dirs: maxSearchDirs, wantWalked: false, wantMatches: 0, wantTruncated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "hit.txt"), []byte("needle\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			// walkDir closes the handle on the way out.

			sc := newFileScan(t.Context(), "needle", false, nil, nil)
			sc.dirs = tc.dirs
			if got := sc.walkDir(searchDir{f: f, abs: dir}); got != tc.wantWalked {
				t.Errorf("walkDir() with dirs=%d = %v, want %v", tc.dirs, got, tc.wantWalked)
			}
			res := sc.results()
			if len(res.Matches) != tc.wantMatches {
				t.Errorf("walkDir() with dirs=%d found %d rows, want %d: %+v", tc.dirs, len(res.Matches), tc.wantMatches, res.Matches)
			}
			if res.Truncated != tc.wantTruncated {
				t.Errorf("walkDir() with dirs=%d left truncated = %v, want %v", tc.dirs, res.Truncated, tc.wantTruncated)
			}
		})
	}
}

// atCeiling builds content of exactly maxSearchFileSize bytes whose final line
// is the needle, unterminated. A read that stopped one byte short would leave a
// word that no longer matches, which is the difference between a bounded read
// and a silently truncated one.
func atCeiling() string {
	const tail = "needle"
	return strings.Repeat("a", maxSearchFileSize-len(tail)-1) + "\n" + tail
}

// A file at exactly the per-file ceiling is searched, to its last byte, whether
// it is reached by walking a directory or named directly as the search root.
func TestSearch_FileAtExactCeilingIsSearchedWhole(t *testing.T) {
	t.Run("named_as_the_search_root", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		writeTree(t, dir, map[string]string{"big.txt": atCeiling()})

		res := decodeSearch(t, searchReq(t, h,
			map[string]string{"path": filepath.Join(prefix, "big.txt"), "q": "needle"}))
		if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
			t.Fatalf("matches = %+v, want one hit on line 2 of a %d-byte file", res.Matches, maxSearchFileSize)
		}
		if res.Scanned != 1 {
			t.Errorf("scanned = %d, want 1: the root file was opened and read", res.Scanned)
		}
		if res.Truncated {
			t.Error("truncated = true: a file AT the ceiling was read whole, nothing was cut")
		}
	})

	t.Run("reached_by_walking_its_directory", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		writeTree(t, dir, map[string]string{"sub/big.txt": atCeiling()})

		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
		if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
			t.Fatalf("matches = %+v, want one hit on line 2 of a %d-byte file", res.Matches, maxSearchFileSize)
		}
		if res.Truncated {
			t.Error("truncated = true: a file AT the ceiling was read whole, nothing was cut")
		}
	})
}

// readSearchFile bounds the bytes it actually reads, off the descriptor: a file
// past the ceiling is read to the ceiling and REPORTED as partial, never refused
// and never passed off as whole.
func TestReadSearchFile_PastTheCeilingIsCutAndReportedPartial(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		wantRead    int
		wantPartial bool
	}{
		{name: "exactly_at_the_ceiling", size: maxSearchFileSize, wantRead: maxSearchFileSize, wantPartial: false},
		{name: "one_byte_past_the_ceiling", size: maxSearchFileSize + 1, wantRead: maxSearchFileSize, wantPartial: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(strings.Repeat("a", tc.size)), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			data, partial, err := readSearchFile(t.Context(), f, int64(tc.size))
			if err != nil {
				t.Fatalf("readSearchFile(a %d-byte file) error = %v, want nil: the ceiling is a bound, not a refusal", tc.size, err)
			}
			if len(data) != tc.wantRead {
				t.Errorf("readSearchFile(a %d-byte file) read %d bytes, want %d", tc.size, len(data), tc.wantRead)
			}
			if partial != tc.wantPartial {
				t.Errorf("readSearchFile(a %d-byte file) partial = %v, want %v", tc.size, partial, tc.wantPartial)
			}
		})
	}
}

// A blank line is a line: the match after one is reported at the line it is
// really on, and everything past the blank line is still searched.
func TestSearch_BlankLinesAreCounted(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"gap.txt": "alpha\n\nneedle\n"})

	res := decodeSearch(t, searchReq(t, h,
		map[string]string{"path": filepath.Join(prefix, "gap.txt"), "q": "needle"}))
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly one hit", res.Matches)
	}
	if got := res.Matches[0].Line; got != 3 {
		t.Errorf("match line = %d, want 3 (the blank second line counts)", got)
	}
	if got := res.Matches[0].Excerpt; got != "needle" {
		t.Errorf("match excerpt = %q, want %q", got, "needle")
	}
}

// The depth budget names the deepest directory the walk still ENTERS: a file
// sitting at exactly that depth is found, and finding it is not a partial
// answer. The refusal one level deeper is pinned separately.
func TestSearch_FileAtExactDepthBudgetIsFound(t *testing.T) {
	h, dir, prefix := testDir(t)
	deep := dir
	for range maxSearchDepth {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	buried := filepath.Join(deep, "buried.txt")
	if err := os.WriteFile(buried, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	if got := matchPaths(res); len(got) != 1 || got[0] != buried {
		t.Fatalf("matches = %v, want the file at depth %d", got, maxSearchDepth)
	}
	if res.Truncated {
		t.Error("truncated = true after a walk that read every file it should have")
	}
}

// --- Name search ----------------------------------------------------------
//
// The reported defect: a search covered a file's BYTES and never the name it is
// called, so a tree of images was unsearchable and a file the content path
// deliberately skips (a binary, an oversized file) could not be found at all.
// Each case below names the decision it pins.

// TestSearch_FindsAFileByName is the reported bug: the one file whose name holds
// the needle is a binary, so the content path opens it, sniffs it and drops it.
func TestSearch_FindsAFileByName(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"cover-book.png": "\x89PNG\r\n\x1a\n\x00\x00not text at all",
		"notes.txt":      "prose that says nothing about it\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "book"}))

	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly one name hit", res.Matches)
	}
	m := res.Matches[0]
	if m.Path != filepath.Join(dir, "cover-book.png") {
		t.Errorf("path = %q, want %q", m.Path, filepath.Join(dir, "cover-book.png"))
	}
	if m.Kind != MatchKindName {
		t.Errorf("kind = %q, want %q", m.Kind, MatchKindName)
	}
	if m.Line != 0 || m.Excerpt != "" {
		t.Errorf("hit = line %d excerpt %q, want line 0 and an empty excerpt: a name has neither",
			m.Line, m.Excerpt)
	}
}

// TestSearch_NameMatchesRankAheadOfContent pins the ranking on a fixture where
// PATH order alone would answer the other way round, so the comparator's first
// rule is the only thing that can produce it.
func TestSearch_NameMatchesRankAheadOfContent(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"a-prose.txt": "the needle is in here\n",
		"b-prose.txt": "and the needle is here too\n",
		"needle.txt":  "nothing matching inside this one\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	want := []string{
		filepath.Join(dir, "needle.txt"),
		filepath.Join(dir, "a-prose.txt"),
		filepath.Join(dir, "b-prose.txt"),
	}
	if got := matchPaths(res); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("matches = %v, want the name hit first: %v", got, want)
	}
	if res.Matches[0].Kind != MatchKindName {
		t.Errorf("first kind = %q, want %q", res.Matches[0].Kind, MatchKindName)
	}
}

// TestSearch_NameAndContentBothMatch is the most common real query, and the case
// the ranking test deliberately avoids: one file answers TWO rows, because the
// two answer different questions and dropping either loses one of them.
func TestSearch_NameAndContentBothMatch(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle.txt": "first line\nthe needle is on line two\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if len(res.Matches) != 2 {
		t.Fatalf("matches = %+v, want two rows for one file (its name and its line)", res.Matches)
	}
	name, content := res.Matches[0], res.Matches[1]
	if name.Kind != MatchKindName || name.Line != 0 {
		t.Errorf("row 0 = kind %q line %d, want the name row first with line 0", name.Kind, name.Line)
	}
	if content.Kind != MatchKindContent || content.Line != 2 {
		t.Errorf("row 1 = kind %q line %d, want the content row on line 2", content.Kind, content.Line)
	}
	if content.Excerpt != "the needle is on line two" {
		t.Errorf("content excerpt = %q, want the matching line", content.Excerpt)
	}
}

// TestSearch_DirectoryNameMatches: a folder is a first-class hit, and it shares
// the name rank, so it sorts ahead of its own matching child by path with no
// third comparator rule.
func TestSearch_DirectoryNameMatches(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle-dir/needle-child.txt": "nothing matching in here\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	want := []string{
		filepath.Join(dir, "needle-dir"),
		filepath.Join(dir, "needle-dir/needle-child.txt"),
	}
	if got := matchPaths(res); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("matches = %v, want the directory ahead of its child: %v", got, want)
	}
	if res.Matches[0].Kind != MatchKindDir {
		t.Errorf("directory kind = %q, want %q", res.Matches[0].Kind, MatchKindDir)
	}
	if res.Matches[1].Kind != MatchKindName {
		t.Errorf("child kind = %q, want %q", res.Matches[1].Kind, MatchKindName)
	}
}

// TestSearch_NameMatchHonoursCaseSensitivity: the name test reads the SAME
// folded needle the content test reads, so the toggle cannot mean two things.
func TestSearch_NameMatchHonoursCaseSensitivity(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"Needle-upper.txt": "no match inside\n",
		"needle-lower.txt": "no match inside\n",
	})
	lower := filepath.Join(dir, "needle-lower.txt")
	upper := filepath.Join(dir, "Needle-upper.txt")

	tests := map[string]struct {
		q        string
		caseFlag string
		want     []string
	}{
		"insensitive reaches both spellings": {
			q: "needle", want: []string{upper, lower},
		},
		// The NEEDLE is folded once per scan, not per entry, so an uppercase
		// query has to reach a lowercase name. Without this row the fold is
		// unobservable: every other insensitive case types the needle already
		// folded, so a name test reading the RAW needle would pass them all.
		"insensitive folds the needle as well as the name": {
			q: "NEEDLE", want: []string{upper, lower},
		},
		"case=1 with a lowercase needle reaches only the lowercase name": {
			q: "needle", caseFlag: "1", want: []string{lower},
		},
		"case=1 with an uppercase needle reaches only the uppercase name": {
			q: "Needle", caseFlag: "1", want: []string{upper},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			params := map[string]string{"path": prefix, "q": tc.q}
			if tc.caseFlag != "" {
				params["case"] = tc.caseFlag
			}
			got := matchPaths(decodeSearch(t, searchReq(t, h, params)))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSearch_ExcludeSkipsANameMatch: a name hit is only ever recorded for an
// entry classify ADMITTED, so every gate the content path applies applies here.
func TestSearch_ExcludeSkipsANameMatch(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle-keep.txt": "no match inside\n",
		"needle-drop.txt": "no match inside\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{
		"path": prefix, "q": "needle", "exclude": "needle-drop.txt",
	}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != filepath.Join(dir, "needle-keep.txt") {
		t.Fatalf("matches = %v, want only needle-keep.txt", got)
	}
}

// TestSearch_IncludeGatesANameMatch pins the asymmetry: include gates the MATCH
// and never the DESCENT. An include applied to directories would prune every one
// of them and the walk would never reach the file the reader asked for — so the
// directory is not reported AND the Go file inside it still is.
func TestSearch_IncludeGatesANameMatch(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle-src/needle.go": "package src\n",
		"needle-src/needle.md": "prose\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{
		"path": prefix, "q": "needle", "include": "*.go",
	}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != filepath.Join(dir, "needle-src/needle.go") {
		t.Fatalf("matches = %v, want only the Go file: the directory is gated out and the walk still descended into it", got)
	}
	if res.Matches[0].Kind != MatchKindName {
		t.Errorf("kind = %q, want %q", res.Matches[0].Kind, MatchKindName)
	}
}

// TestSearch_SensitivePathNeverMatchesByName: every sensitive file below is NAMED
// for the needle, so the name gate is the only thing that can keep it out. A name
// hit on one of these would disclose a path the content path is careful never to
// report.
func TestSearch_SensitivePathNeverMatchesByName(t *testing.T) {
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{
		"mcp-secrets.json":      "opaque\n",
		"mcp.json":              "server config\n",
		"push-subs.json":        "subscriptions\n",
		"vapid-keys.json":       "keys\n",
		"home/.ssh/id_rsa.json": "private key\n",
		"chats/c1.json":         "transcript\n",
		"kiro/steering/x.json":  "steering\n",
		"visible.json":          "a browsable file\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": "config", "q": "json"}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != "/config/visible.json" {
		t.Fatalf("matches = %v, want only /config/visible.json", got)
	}
	if res.Matches[0].Kind != MatchKindName {
		t.Errorf("kind = %q, want %q", res.Matches[0].Kind, MatchKindName)
	}
}

// TestSearch_SymlinkNameMatchesAndItsContentIsNotFollowed: the listing shows a
// symlink under its own name, so a reader typing that name expects the row the
// listing would give them. Its TARGET is never opened — an out-of-mount target
// stays unread, an in-mount one is reported under its own name or not at all —
// and the sensitive-path denial governs the link's name like any other entry's.
func TestSearch_SymlinkNameMatchesAndItsContentIsNotFollowed(t *testing.T) {
	t.Run("the_name_is_a_row_and_the_target_stays_unread", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("the needle is in the target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "needle-link.txt")
		if err := os.Symlink(filepath.Join(outside, "target.txt"), link); err != nil {
			t.Fatal(err)
		}

		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

		if len(res.Matches) != 1 {
			t.Fatalf("matches = %+v, want exactly one name row for the link", res.Matches)
		}
		m := res.Matches[0]
		if m.Path != link || m.Kind != MatchKindName || m.Line != 0 || m.Excerpt != "" {
			t.Errorf("hit = %+v, want a name row for %s with no line and no excerpt", m, link)
		}
		if res.Scanned != 0 {
			t.Errorf("scanned = %d, want 0: a symlink's bytes are never read", res.Scanned)
		}
		if res.Matched != 1 {
			t.Errorf("matched = %d, want 1: the line inside the target must not be counted", res.Matched)
		}
		if res.Truncated {
			t.Error("truncated = true: not following a symlink is a skip the answer covers")
		}
	})

	t.Run("a_link_at_a_sensitive_path_is_not_a_row", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{"visible.txt": "nothing in here\n"})
		// Two links to the same harmless file: one wearing a sensitive NAME, one
		// not. Only the second may be a row.
		for _, name := range []string{"mcp-secrets.json", "other-secrets.txt"} {
			if err := os.Symlink("visible.txt", filepath.Join(backing, name)); err != nil {
				t.Fatal(err)
			}
		}

		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": "config", "q": "secrets"}))

		if got := matchPaths(res); len(got) != 1 || got[0] != "/config/other-secrets.txt" {
			t.Errorf("matches = %v, want only /config/other-secrets.txt: the denial governs a link's name too", got)
		}
	})
}

// TestSearch_OversizeFileIsFoundByNameAndInItsHead: the ceiling is a bound on
// the READ, not a gate on the file. The head of a large file is searched and
// its hits reported; the tail past the ceiling is not, and Truncated says so.
func TestSearch_OversizeFileIsFoundByNameAndInItsHead(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"big-needle.txt": "needle\n" + strings.Repeat("x", maxSearchFileSize) + "\nneedle",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if len(res.Matches) != 2 {
		t.Fatalf("matches = %+v, want two rows: the name and the line in the head", res.Matches)
	}
	name, head := res.Matches[0], res.Matches[1]
	if name.Kind != MatchKindName || name.Line != 0 {
		t.Errorf("row 0 = kind %q line %d, want the name row", name.Kind, name.Line)
	}
	if head.Kind != MatchKindContent || head.Line != 1 {
		t.Errorf("row 1 = kind %q line %d, want the content row on line 1", head.Kind, head.Line)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1: the file was read, to its ceiling", res.Scanned)
	}
	if res.Matched != 2 {
		t.Errorf("matched = %d, want 2: the needle past the ceiling was never read", res.Matched)
	}
	if !res.Truncated {
		t.Error("truncated = false after a file was read only to its ceiling")
	}
}

// TestSearch_PerFileCapReportsTheCut: one file with more matching lines than a
// file may contribute answers the cap's worth of rows, counts every line, and
// leaves Truncated alone — a cap is not a hole in what was searched.
func TestSearch_PerFileCapReportsTheCut(t *testing.T) {
	h, dir, prefix := testDir(t)
	const lines = maxFileMatches + 5
	writeTree(t, dir, map[string]string{"many.txt": strings.Repeat("needle\n", lines)})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if len(res.Matches) != maxFileMatches {
		t.Errorf("matches = %d, want the per-file cap %d", len(res.Matches), maxFileMatches)
	}
	if res.Matched != lines {
		t.Errorf("matched = %d, want every one of the %d matching lines", res.Matched, lines)
	}
	if res.Truncated {
		t.Error("truncated = true: the per-file cap is a cut the count reports, not a hole in the scan")
	}
}

// TestSearch_NameMatchSpendsNoFileBudget: a tree of DIRECTORIES answers name hits
// while opening nothing, so `scanned` stays at zero and still means "how many
// files the scan opened".
func TestSearch_NameMatchSpendsNoFileBudget(t *testing.T) {
	h, dir, prefix := testDir(t)
	for i := range 5 {
		if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("needle-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if len(res.Matches) != 5 {
		t.Fatalf("matches = %d, want one per directory", len(res.Matches))
	}
	for _, m := range res.Matches {
		if m.Kind != MatchKindDir {
			t.Errorf("%s: kind = %q, want %q", m.Path, m.Kind, MatchKindDir)
		}
	}
	if res.Scanned != 0 {
		t.Errorf("scanned = %d, want 0: a name hit opens nothing", res.Scanned)
	}
	if res.Truncated {
		t.Error("truncated = true on a tree well under every cap")
	}
}

// TestSearch_SearchRootIsNotItsOwnNameMatch: a root is the FRAME of the search
// rather than a result inside it, and a hit on it would open the folder the
// reader is already in. Both doors are covered — a resolved path, and each mount
// on the "/" fan-out.
func TestSearch_SearchRootIsNotItsOwnNameMatch(t *testing.T) {
	t.Run("a resolved search root", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		writeTree(t, dir, map[string]string{"needle-root/inner.txt": "no match inside\n"})

		res := decodeSearch(t, searchReq(t, h, map[string]string{
			"path": filepath.Join(prefix, "needle-root"), "q": "needle",
		}))
		if got := matchPaths(res); len(got) != 0 {
			t.Errorf("matches = %v, want none: the root itself is not a result", got)
		}
	})

	t.Run("each mount on the / fan-out", func(t *testing.T) {
		a := filepath.Join(t.TempDir(), "needle-mount-a")
		b := filepath.Join(t.TempDir(), "needle-mount-b")
		for _, root := range []string{a, b} {
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			writeTree(t, root, map[string]string{"inner.txt": "no match inside\n"})
		}
		h, err := New(a, b)
		if err != nil {
			t.Fatal(err)
		}

		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": "/", "q": "needle"}))
		if got := matchPaths(res); len(got) != 0 {
			t.Errorf("matches = %v, want none: a mount is a frame too", got)
		}
	})
}

// --- the wire contract, shared with the TypeScript consumer ---

type fileSearchFixture struct {
	Comment []string         `json:"_comment"`
	Query   string           `json:"query"`
	Result  FileSearchResult `json:"result"`
}

var fileSearchFixtureComment = []string{
	"A GET /api/files/search reply, produced by the real handler over a seeded tree",
	"mounted at /workspace.",
	"",
	"filebrowse.FileSearchResult, FileMatch and FileMatchKind are wiregen-registered;",
	"TestFileSearchWireContract (Go) asserts the handler's reply marshals to exactly these",
	"bytes, and files-search.node.test.ts (TypeScript) decodes it through the generated",
	"decodeFileSearchResult. All three kinds appear; one file is cut at the per-file cap,",
	"so `matched` exceeds the row count while `truncated` stays false.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/filebrowse/ -run TestFileSearchWireContract",
	"then re-run the TS half: npx vitest --run files-search.node.test.ts (from static-src/).",
}

// TestFileSearchWireContract pins the marshaled shape of the file-search reply.
// The tree gives every row kind: a directory named for the needle, a file that
// matches by name and on one line, a file with more matching lines than the
// per-file cap, and a file that matches nothing but is still scanned.
func TestFileSearchWireContract(t *testing.T) {
	h, backing := searchHandlerAt(t, "/workspace")
	manyLines := maxFileMatches + 1
	writeTree(t, backing, map[string]string{
		"needle-dir/inner.txt": "nothing in here\n",
		"notes-needle.md":      "# notes\n\nthe needle is on line three\n",
		"many.txt":             strings.Repeat("needle again\n", manyLines),
		"plain.txt":            "nothing matching\n",
	})

	fx := fileSearchFixture{Comment: fileSearchFixtureComment, Query: "needle"}
	fx.Result = decodeSearch(t, searchReq(t, h, map[string]string{"path": "workspace", "q": fx.Query}))

	wantRows := 1 + 2 + maxFileMatches
	if len(fx.Result.Matches) != wantRows {
		t.Fatalf("matches = %d rows, want %d: %+v", len(fx.Result.Matches), wantRows, fx.Result.Matches)
	}
	kinds := map[FileMatchKind]int{}
	for _, m := range fx.Result.Matches {
		kinds[m.Kind]++
	}
	if kinds[MatchKindDir] != 1 || kinds[MatchKindName] != 1 || kinds[MatchKindContent] != 1+maxFileMatches {
		t.Fatalf("fixture needs every kind: dir %d name %d content %d", kinds[MatchKindDir], kinds[MatchKindName], kinds[MatchKindContent])
	}
	if want := 1 + 2 + manyLines; fx.Result.Matched != want || fx.Result.Scanned != 4 || fx.Result.Truncated {
		t.Fatalf("tally = %+v, want matched %d, scanned 4, not truncated", fx.Result.Tally, want)
	}

	pinGolden(t, "testdata/file_search.json", fx, "TestFileSearchWireContract", "files-search.node.test.ts")
}

// pinGolden marshals v, rewrites path behind UPDATE_GOLDEN=1, and compares the
// bytes. The failure names the regeneration command and the TypeScript consumer
// to re-run, because a cross-language fixture is one atomic change.
func pinGolden(t *testing.T, path string, v any, regen, consumer string) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/filebrowse/ -run %s): %v", path, regen, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/filebrowse/ -run %s, "+
			"then re-run the TS half: npx vitest --run %s (from static-src/).",
			path, want, got, regen, consumer)
	}
}
