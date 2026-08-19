// Recursive file-CONTENT search: GET /api/files/search.
//
// LEXICAL AND INDEX-FREE, the decision internal/chat's two searches already
// record for the transcript. A persistent inverted index would be exactly the
// second store this architecture exists to avoid, and a WORKSPACE index is the
// worse case of the two: the agent writes into these trees constantly, so every
// write would have to invalidate it, and there is no watcher anywhere in this
// codebase to hang that invalidation on. So the scan runs inside the request,
// bounded by the caps below and by the request context, and keeps nothing.
//
// IT SAYS WHAT IT DID NOT READ, in the vocabulary the cross-chat search already
// uses (`scanned` / `truncated`). A repo holds far more files than a chat store,
// so the cap is reached routinely rather than exotically, and a bare "no
// matches" over a capped scan tells the reader the text is nowhere when most of
// the tree was simply never opened.
//
// CONFINEMENT IS INHERITED, never re-derived. The search ROOT goes through
// resolveOrForbid, so all four defense layers on paths.go apply to it; the walk
// then stays inside that mount BY CONSTRUCTION. Three rules follow and all
// three are load-bearing:
//
//   - EVERY open is one NAME against the open DESCRIPTOR of the directory that
//     name was read from, O_NOFOLLOW (openChild). No path is ever resolved a
//     second time, so a symlink swapped in for an accepted file — or for one of
//     its ANCESTORS — after the walk classified it is refused by the kernel
//     rather than followed. That is what makes the absolute path on a result the
//     path of the object actually read, which is the premise the denylist below
//     stands on: a check-then-reopen sequence cannot have that property at any
//     price, because an os.Root deliberately follows a symlink whose target
//     stays inside its mount. It is also why a directory handle stays open for
//     as long as the walk is visiting it, and therefore why depth is capped.
//   - IsSensitive runs on EVERY entry. An os.Root cannot deny a sub-path, and a
//     recursive walk reaches /config/mcp-secrets.json from ABOVE rather than by
//     being asked for it, so the root check alone would hand its OAuth refresh
//     tokens back as search hits.
//   - resolvePath is NOT re-run per entry. It calls EvalSymlinks on every call
//     (a stat storm on a walk) and can return a DIFFERENT mount when an in-tree
//     symlink crosses grants, which would silently re-root the walk mid-flight.

package filebrowse

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/parallel"
	"github.com/cplieger/webhttp"
)

const (
	// maxSearchFiles bounds how many files one search opens. The cross-chat
	// search reads at most 500 chat files, each potentially megabytes; a source
	// tree is the opposite shape, many small files, so the file budget is larger
	// and the per-file budget smaller. 5000 covers this repo's own 1167 tracked
	// files several times over, which is the scale the box is for.
	maxSearchFiles = 5000
	// maxSearchDirs bounds directories VISITED, which maxSearchFiles does not: a
	// tree of empty directories costs one ReadDir each and would otherwise walk
	// forever without ever filling the file budget.
	maxSearchDirs = 20_000
	// maxSearchDepth bounds how deep the walk descends. It is a DESCRIPTOR
	// budget rather than a taste judgement: a directory handle is what pins its
	// children's opens (see openChild), so it stays open for as long as the walk
	// is inside it, and the depth of the descent IS the number of directory
	// handles one search holds at once. 128 is past any real source tree and far
	// short of any process descriptor limit, and a tree deeper than it reports
	// `truncated` instead of failing opens across the whole server.
	maxSearchDepth = 128
	// maxSearchMatches caps the response. Past this a search is not a search,
	// it is a listing the reader will refine instead of paging through — the
	// same reasoning and the same number as chat.maxSearchHits.
	maxSearchMatches = 200
	// maxFileMatches caps ONE file's contribution, so a generated or minified
	// file cannot spend the whole match budget before the reader's own code is
	// reached.
	maxFileMatches = 20
	// maxSearchFileSize is the per-file read ceiling. Deliberately smaller than
	// maxFileSize (the editor's 2 MB): a text file larger than this is generated
	// or vendored, a match inside one is not actionable, and the editor's
	// ceiling times the file budget would be a gigabyte of transient reads.
	maxSearchFileSize = 512 * 1024
	// searchExcerptRadius is how much of a long line surrounds the match. A
	// minified bundle is one line, so the excerpt has to be windowed even though
	// the unit is a line.
	searchExcerptRadius = 80
	// searchReadDirChunk is how many directory entries one ReadDir call takes.
	//
	// A whole-directory ReadDir(-1) allocates and reads every entry before the
	// first cancellation or budget check, and a directory can hold arbitrarily
	// many entries that never consume the FILE budget (excluded names, symlinks,
	// special files, oversized files), so that budget does not bound the work
	// either. A fixed chunk bounds the per-directory allocation whatever the
	// directory holds and gives cancellation somewhere to land. Same number as
	// atomicfile.WalkDirInRoot's batch, for the same reason.
	searchReadDirChunk = 256
)

// searchWorkers matches chat.searchWorkers: the bound is disk, not CPU.
const searchWorkers = 8

const (
	// searchDirFlags opens a directory for the walk. O_DIRECTORY makes a
	// non-directory swapped in under a directory's name a refusal rather than a
	// read, O_NOFOLLOW refuses a symlink AT that name, and O_NONBLOCK is what
	// keeps a FIFO planted there from parking the walk in open(2) forever.
	// O_CLOEXEC so a walk in flight cannot leak a descriptor into a bridge spawn.
	searchDirFlags = os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	// searchFileFlags is the same open for a leaf, which may legitimately be any
	// file type; the type is then refused off the DESCRIPTOR, never the name.
	searchFileFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
)

// errSearchBinary marks a candidate rejected by the binary sniff. It is not a
// failure and never logged: a match inside a binary is noise the reader cannot
// act on, so the file is opened, sniffed and dropped.
var errSearchBinary = errors.New("filebrowse: binary file")

// FileMatch is one matching LINE. A line number rather than a byte offset
// because the client opens the result at `/file/{path}#L<line>`, which is the
// editor's existing deep-link form.
type FileMatch struct {
	// Path is the container-absolute path, the same namespace every other
	// /api/file* route speaks.
	Path    string `json:"path"`
	Excerpt string `json:"excerpt"`
	Line    int    `json:"line"`
}

// FileSearchResult is GET /api/files/search's reply.
//
// Scanned and Truncated are chat.SearchAllResult's two words, deliberately
// spelled the same: three surfaces reporting one fact under three names is the
// drift those two avoided on purpose.
type FileSearchResult struct {
	Matches []FileMatch `json:"matches"`
	// Scanned is how many files the scan OPENED. A file that vanished mid-scan
	// counts, and so does one the descriptor turned out to say was oversized: a
	// concurrent agent write is normal here, and flapping this number on one
	// would be noise rather than information. Opened rather than read, because
	// the shape and size of a candidate are only knowable once it is open — see
	// classify.
	Scanned int `json:"scanned"`
	// Truncated says the answer is INCOMPLETE: the walk hit its file, directory
	// or depth cap, the match cap stopped it opening the rest, or something it
	// meant to read could not be read — a search root, directory or file the
	// kernel refused for a reason the walk did not choose. Either way files were
	// left unread, so the UI must say so rather than let a short result imply the
	// text is nowhere else.
	//
	// One field rather than two, because a caller can do exactly one thing with
	// either answer: say the result is partial. A second word for "and some of it
	// was unreadable" would split one fact across two names for a distinction no
	// reader of this endpoint acts on, and the reason WHY belongs in the log line
	// that already names the path. A DELIBERATE skip — an excluded glob, a
	// symlink, a sensitive path, a binary, a file that vanished under the walk —
	// is not a loss and never sets this: the answer covers everything the search
	// was asked to cover.
	Truncated bool `json:"truncated"`
}

// --- /api/files/search (GET recursive content search) ---

func (h *Handler) handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	q := r.URL.Query()
	needle := q.Get("q")
	if strings.TrimSpace(needle) == "" {
		webhttp.WriteJSON(w, FileSearchResult{Matches: []FileMatch{}})
		return
	}
	include, err := parseGlobs(q["include"])
	if err != nil {
		httpreply.BadRequest(w, "invalid include pattern")
		return
	}
	exclude, err := parseGlobs(q["exclude"])
	if err != nil {
		httpreply.BadRequest(w, "invalid exclude pattern")
		return
	}
	roots, ok := h.searchRoots(w, q.Get("path"))
	if !ok {
		return
	}
	// `case=1` only when asked, and anything else reads as insensitive — the
	// exact rule handleSearch already applies to the transcript search, so the
	// two boxes cannot disagree about what the checkbox means.
	sc := newFileScan(r.Context(), needle, q.Get("case") == "1", include, exclude)
	for _, root := range roots {
		if !sc.addRoot(root) {
			break
		}
	}
	res := sc.results()
	// A cancelled request gets no body: the client is gone, and a half-scan
	// reported as a whole one is the lie the caps exist to prevent.
	if r.Context().Err() != nil {
		return
	}
	webhttp.WriteJSON(w, res)
}

// searchRoots resolves the request's `path` into the locations to walk.
//
// "/" is not a real directory in the allow-list model, so it fans out over every
// granted mount: "search everything" is the honest reading of the root listing,
// and the caps are shared across the fan-out rather than multiplied by it.
func (h *Handler) searchRoots(w http.ResponseWriter, reqPath string) (roots []loc, ok bool) {
	if reqPath == "" || reqPath == "." || filepath.Clean("/"+reqPath) == "/" {
		roots = make([]loc, 0, len(h.mounts))
		for i := range h.mounts {
			m := &h.mounts[i]
			roots = append(roots, loc{m: m, abs: m.dir})
		}
		return roots, true
	}
	l, resolved := h.resolveOrForbid(w, reqPath)
	if !resolved {
		return nil, false
	}
	return []loc{l}, true
}

// parseGlobs flattens repeated and comma-separated pattern parameters, and
// rejects a malformed one.
//
// Rejecting rather than ignoring, because path.Match answers "no match" for a
// bad pattern: an unparseable include would silently match nothing and an
// unparseable exclude would silently exclude nothing, and both look identical to
// a search that simply found nothing.
func parseGlobs(raw []string) (patterns []string, err error) {
	for _, entry := range raw {
		for pat := range strings.SplitSeq(entry, ",") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if _, mErr := path.Match(pat, ""); mErr != nil {
				return nil, mErr
			}
			patterns = append(patterns, pat)
		}
	}
	return patterns, nil
}

// matchGlob applies ONE pattern under this app's stated convention: a pattern
// holding no "/" matches the file's BASENAME, and a pattern holding one matches
// the whole path UNDER THE FOLDER SEARCHED.
//
// The convention exists because path.Match's `*` does not cross "/", so
// `path.Match("*.go", "src/a.go")` is FALSE — the opposite of what anyone typing
// `*.go` into a search box means. Matching the basename for the separator-free
// spelling is what makes the common case do the common thing; keeping the path
// form for patterns that DO name directories is what keeps `internal/*/x.go`
// expressible. There is no `**`, because that needs a glob library and a new
// dependency for one operator does not survive "what does this cost us forever".
//
// The subject is relative to the SEARCH ROOT, not to the mount: a reader looking
// at /workspace/project/src types `deep/*.go` for what is under the folder in
// front of them, and the README promises exactly that. Matching a mount-relative
// path here would silently require them to spell the folder's own prefix.
func matchGlob(pattern, rel string) bool {
	subject := rel
	if !strings.Contains(pattern, "/") {
		subject = path.Base(rel)
	}
	matched, err := path.Match(pattern, subject)
	return err == nil && matched
}

func matchAnyGlob(patterns []string, rel string) bool {
	for _, pat := range patterns {
		if matchGlob(pat, rel) {
			return true
		}
	}
	return false
}

// searchDir is one directory the walk is inside: the open handle that PINS it
// plus the two coordinate spaces its children are named in.
//
// The handle is the security-relevant field. Every child is opened as a single
// name against it, so nothing below this point re-resolves a path and no
// ancestor swap can redirect a read. Its cost is one descriptor per level of
// descent, which is what maxSearchDepth bounds.
type searchDir struct {
	f *os.File
	// abs is the container-absolute path, for the response and the denylist.
	abs string
	// srel is the path relative to the SEARCH ROOT ("" at the root), which is
	// what the globs are matched against.
	srel string
	// depth is 0 at the search root and counts the open handles held.
	depth int
}

// child names one entry of d in both coordinate spaces.
func (d searchDir) child(name string) (abs, srel string) {
	return filepath.Join(d.abs, name), path.Join(d.srel, name)
}

// searchCandidate is one file the walk accepted, named RELATIVE to the directory
// handle it was found in — a name is all the read needs and all it is allowed to
// use, because the handle is what confines it.
type searchCandidate struct {
	name string
	abs  string
}

// fileScan carries one search's accounting. Hoisting the walk onto named
// methods (rather than a deeply-nested recursive closure) is the shape
// zipStream uses for the same reason: it keeps the handler's control flow flat
// and each method inside gocognit's ceiling.
//
// Every field is owned by the WALK goroutine. The read fan-out is per chunk and
// collects into a pre-sized local slice by index, so a worker touches no shared
// state and the scan needs neither mutex nor atomic.
type fileScan struct {
	ctx context.Context
	// needle is folded when the search is case-insensitive, so the haystack is
	// folded to match and the two can never disagree.
	needle        []byte
	include       []string
	exclude       []string
	matches       []FileMatch
	files         int
	dirs          int
	matched       int
	caseSensitive bool
	truncated     bool
}

func newFileScan(ctx context.Context, needle string, caseSensitive bool, include, exclude []string) *fileScan {
	raw := []byte(needle)
	if !caseSensitive {
		raw = bytes.ToLower(raw)
	}
	return &fileScan{
		ctx:           ctx,
		needle:        raw,
		include:       include,
		exclude:       exclude,
		caseSensitive: caseSensitive,
	}
}

// capped reports whether the walk should stop: the request was cancelled, or a
// budget is spent. Reaching a budget also marks the answer truncated, because
// the whole point of the field is that a stopped walk left files unread.
func (s *fileScan) capped() bool {
	if s.ctx.Err() != nil {
		return true
	}
	if s.files >= maxSearchFiles || s.dirs >= maxSearchDirs || s.matched >= maxSearchMatches {
		s.truncated = true
		return true
	}
	return false
}

// openChild opens one child of an already-open directory BY NAME, refusing a
// symlink at that name.
//
// This is the whole of the search's confinement, and the reason the walk holds
// its directory open. openat(2) against a directory descriptor resolves exactly
// one component, so no ancestor is named and none can be substituted; the kernel
// refuses a symlink at the final component under O_NOFOLLOW, which no
// check-then-open sequence can do without a race. dirfd comes through
// SyscallConn so it stays valid for the duration of the call even though the
// read fan-out uses it from another goroutine.
//
// name must be a single component. Every caller passes a DirEntry name or a
// component of an already-cleaned path, and openSearchRoot rejects the three
// spellings that would not be one.
func openChild(dir *os.File, name, displayPath string, flags int) (*os.File, error) {
	conn, err := dir.SyscallConn()
	if err != nil {
		return nil, err
	}
	var fd int
	var openErr error
	if ctlErr := conn.Control(func(dirFD uintptr) {
		for {
			fd, openErr = syscall.Openat(int(dirFD), name, flags, 0)
			if !errors.Is(openErr, syscall.EINTR) {
				return
			}
		}
	}); ctlErr != nil {
		return nil, ctlErr
	}
	if openErr != nil {
		return nil, &os.PathError{Op: "openat", Path: displayPath, Err: openErr}
	}
	return os.NewFile(uintptr(fd), displayPath), nil
}

// openSearchRoot walks from the mount's own root handle down to the search root,
// one component at a time, refusing a symlink at every step.
//
// The descent exists because the walk's guarantee has to start somewhere. loc
// carries an already symlink-RESOLVED path (resolvePath), so a component of it
// that is a symlink now was substituted after that resolution — exactly the
// swap the walk refuses further down, and it reaches the same denied files
// through the root instead of through a leaf. Opening the root by path would
// hand that swap back, because an os.Root follows a link whose target stays in
// its mount.
//
// The final component is opened without O_DIRECTORY: a root that is a FILE is a
// single-file search, and the same endpoint serves it.
func openSearchRoot(l loc) (*os.File, error) {
	dir, err := l.m.root.OpenFile(".", searchDirFlags, 0)
	if err != nil {
		return nil, err
	}
	rel := l.rel()
	if rel == "." {
		return dir, nil
	}
	names := strings.Split(rel, "/")
	for i, name := range names {
		if name == "" || name == "." || name == ".." {
			_ = dir.Close()
			return nil, fmt.Errorf("filebrowse: search root %q has a non-component segment %q", l.abs, name)
		}
		flags := searchDirFlags
		if i == len(names)-1 {
			flags = searchFileFlags
		}
		child, childErr := openChild(dir, name, filepath.Join(l.m.dir, filepath.Join(names[:i+1]...)), flags)
		_ = dir.Close()
		if childErr != nil {
			return nil, childErr
		}
		dir = child
	}
	return dir, nil
}

// addRoot enqueues one search root and everything under it. Returns false to
// stop the whole scan.
func (s *fileScan) addRoot(l loc) bool {
	f, err := openSearchRoot(l)
	if err != nil {
		// A root that cannot be opened contributes nothing, and on the "/" fan-out
		// the other mounts still answer — so the reply would otherwise present a
		// scan of some mounts as a scan of all of them.
		s.truncated = true
		slog.Warn("filebrowse: search open failed", "path", l.abs, "error", err)
		return true
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		s.truncated = true
		slog.Warn("filebrowse: search stat failed", "path", l.abs, "error", err)
		return true
	}
	if !info.IsDir() {
		defer func() { _ = f.Close() }()
		return s.searchRootFile(f, l.abs, info)
	}
	return s.walkDir(searchDir{f: f, abs: l.abs})
}

// searchRootFile searches a root that turned out to be a file, from the
// descriptor openSearchRoot already holds — so the reopen this walk exists to
// avoid does not sneak back in at the root.
func (s *fileScan) searchRootFile(f *os.File, abs string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Size() > maxSearchFileSize {
		return true
	}
	if s.files >= maxSearchFiles {
		s.truncated = true
		return false
	}
	s.files++
	data, err := readSearchFile(s.ctx, f, info.Size())
	if err != nil {
		if !errors.Is(err, errSearchBinary) {
			logSearchReadError(abs, err)
		}
		return true
	}
	s.collect(s.matchLines(abs, data))
	return true
}

// walkDir consumes an already-open directory handle: it enumerates the directory
// in bounded chunks and, for every chunk, reads that chunk's candidates and then
// descends into its subdirectories. The handle is closed on the way out, after
// everything that had to be opened against it has been.
//
// Entries arrive in DIRECTORY order, sorted only within a chunk. That is the
// deliberate half of the bounded-memory trade: a global order over an untrusted
// directory means holding its whole inventory, so WHICH files a spent budget
// happened to include is not stable across runs on a directory larger than one
// chunk. The RESULT order is unaffected — results are sorted by path and line
// before they are returned.
func (s *fileScan) walkDir(d searchDir) bool {
	defer func() { _ = d.f.Close() }()
	s.dirs++
	for {
		if s.capped() {
			return false
		}
		entries, err := d.f.ReadDir(searchReadDirChunk)
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		if !s.consumeChunk(d, entries) {
			return false
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// The chunk in hand was consumed, but the REST of this directory was
				// never enumerated, so entries the search would have matched are
				// unaccounted for. EOF is the ordinary end and says nothing.
				s.truncated = true
				slog.Warn("filebrowse: search readdir failed", "path", d.abs, "error", err)
			}
			return true
		}
		if len(entries) == 0 {
			return true
		}
	}
}

// consumeChunk applies every gate to one chunk of directory entries, then reads
// the files it accepted and descends into the directories it accepted.
//
// Files before subdirectories, so the candidates are read while d's handle is
// held at its shallowest and the fan-out is not competing with a descent for
// descriptors.
func (s *fileScan) consumeChunk(d searchDir, entries []fs.DirEntry) bool {
	var cands []searchCandidate
	var subdirs []string
	for _, e := range entries {
		if s.capped() {
			s.readChunk(d, cands)
			return false
		}
		switch verdict, name := s.classify(d, e); verdict {
		case entryCandidate:
			if s.files >= maxSearchFiles {
				// Checking BEFORE the accept is what makes `truncated` exact: it
				// is set only when there really was one more file to read.
				s.truncated = true
				s.readChunk(d, cands)
				return false
			}
			s.files++
			abs, _ := d.child(name)
			cands = append(cands, searchCandidate{name: name, abs: abs})
		case entryDir:
			subdirs = append(subdirs, name)
		case entrySkip:
		}
	}
	s.readChunk(d, cands)
	for _, name := range subdirs {
		if !s.descend(d, name) {
			return false
		}
	}
	return true
}

// entryVerdict is what the gates decided about one directory entry.
type entryVerdict int

const (
	entrySkip entryVerdict = iota
	entryCandidate
	entryDir
)

// classify applies every gate to one directory entry, in the order that keeps
// the file budget from being spent on entries that were never going to match:
// entry type, then the sensitive-path denial, then the globs, and only then an
// open.
//
// The size ceiling is deliberately NOT one of these gates. fs.DirEntry.Info
// answers it with an lstat of the entry's PATHNAME, which is both a stat storm
// over a walk and the one path resolution this walk exists to have removed — the
// size that decides the read is taken off the descriptor in openCandidate, where
// it cannot describe a different file than the one about to be read. The visible
// consequence is that an oversized file is opened, measured and put down, so it
// counts against `scanned` and the file budget where it used to be skipped
// unseen.
func (s *fileScan) classify(d searchDir, e fs.DirEntry) (verdict entryVerdict, name string) {
	name = e.Name()
	// Symlinks are skipped rather than followed. An out-of-mount target is
	// already refused by the os.Root, so this is about the two things that
	// remain: an in-mount directory link can make the walk cycle until the cap
	// absorbs it, and a file link reports the same content twice under two
	// names. fs.WalkDir makes the same choice.
	if e.Type()&fs.ModeSymlink != 0 {
		return entrySkip, name
	}
	abs, srel := d.child(name)
	if IsSensitive(abs) {
		return entrySkip, name
	}
	if e.IsDir() {
		// An exclude prunes a DIRECTORY as well as a file, which is what makes
		// `exclude=node_modules` skip the tree instead of matching nothing. An
		// include stays files-only: applied here, `include=*.go` would prune
		// every directory and the search would never reach a Go file.
		if matchAnyGlob(s.exclude, srel) {
			return entrySkip, name
		}
		return entryDir, name
	}
	if !e.Type().IsRegular() {
		return entrySkip, name
	}
	if matchAnyGlob(s.exclude, srel) {
		return entrySkip, name
	}
	if len(s.include) > 0 && !matchAnyGlob(s.include, srel) {
		return entrySkip, name
	}
	return entryCandidate, name
}

// descend opens one subdirectory against its parent's handle and walks it.
func (s *fileScan) descend(d searchDir, name string) bool {
	abs, srel := d.child(name)
	if d.depth+1 > maxSearchDepth {
		// Refusing to descend leaves files unread, which is what Truncated says.
		s.truncated = true
		slog.Debug("filebrowse: search depth cap reached", "path", abs)
		return true
	}
	f, err := openChild(d.f, name, abs, searchDirFlags)
	if err != nil {
		// A directory that vanished under the walk is normal on a tree the agent
		// is writing to, and so is one replaced by a symlink or a non-directory
		// between the ReadDir that classified it and this open — the refusal IS
		// the guarantee. Anything else (a permission wall, an I/O error) means the
		// whole subtree went unread, which is what Truncated says: the operator
		// gets the reason from the log, the caller gets told the answer is partial.
		if !errors.Is(err, fs.ErrNotExist) && !isSwapRefusal(err) {
			s.truncated = true
			slog.Warn("filebrowse: search opendir failed", "path", abs, "error", err)
		}
		return true
	}
	return s.walkDir(searchDir{f: f, abs: abs, srel: srel, depth: d.depth + 1})
}

// isSwapRefusal reports whether err is the kernel refusing an open because the
// name no longer holds what the walk classified: a symlink under O_NOFOLLOW
// (ELOOP), or a non-directory under O_DIRECTORY (ENOTDIR).
func isSwapRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR)
}

// readChunk reads one chunk's candidates and folds their hits into the scan.
//
// Parallel because the bound is disk, not CPU — the same reasoning and the same
// degree as chat.SearchAll. Each worker collects into its own index of a
// pre-sized slice, so the fan-out needs no mutex, and every open goes through
// d's descriptor, which is why the fan-out has to finish before walkDir closes
// it.
func (s *fileScan) readChunk(d searchDir, cands []searchCandidate) {
	if len(cands) == 0 {
		return
	}
	per := make([]candidateRead, len(cands))
	parallel.Bounded(s.ctx, cands, searchWorkers, func(i int, c searchCandidate) {
		per[i].hits, per[i].unread = s.readCandidate(d.f, c)
	})
	for i := range per {
		s.collect(per[i].hits)
		if per[i].unread {
			s.truncated = true
		}
	}
}

// candidateRead is one candidate's outcome, carried back by index rather than
// folded in by the worker: an unreadable file has to reach `truncated`, and
// `truncated` is the walk goroutine's to write — which is what keeps the fan-out
// free of a mutex.
type candidateRead struct {
	hits   []FileMatch
	unread bool
}

// collect folds one file's hits into the reply.
func (s *fileScan) collect(hits []FileMatch) {
	s.matches = append(s.matches, hits...)
	s.matched += len(hits)
}

// readCandidate opens one candidate against its directory's descriptor and
// returns its hits, plus whether the file was left UNREAD for a reason the walk
// did not choose — a file counted in `scanned` that contributed no lines because
// the kernel refused it is a hole in the answer, not a skip.
func (s *fileScan) readCandidate(dir *os.File, c searchCandidate) (hits []FileMatch, unread bool) {
	f, size, err := openCandidate(dir, c)
	if err != nil {
		return nil, logSearchReadError(c.abs, err)
	}
	defer func() { _ = f.Close() }()
	data, err := readSearchFile(s.ctx, f, size)
	switch {
	case errors.Is(err, errSearchBinary):
		// Not a loss: a binary holds no lines to report, so the answer covers it.
		return nil, false
	case err != nil:
		return nil, logSearchReadError(c.abs, err)
	}
	return s.matchLines(c.abs, data), false
}

// openCandidate opens one accepted candidate as a single name against the
// directory handle it was found in, and takes its shape and size off THAT
// descriptor: a mode or a size read from the pathname again describes whatever
// currently wears the name, not what is about to be read.
func openCandidate(dir *os.File, c searchCandidate) (*os.File, int64, error) {
	f, err := openChild(dir, c.name, c.abs, searchFileFlags)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	// atomicfile's sentinels rather than local ones, so "not a regular file" and
	// "too large" mean one thing across this package and logSearchReadError
	// triages both halves of the read path the same way.
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%w: %s (type %s)", atomicfile.ErrNotRegular, c.abs, info.Mode().Type())
	}
	if info.Size() > maxSearchFileSize {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%w: %d bytes (max %d)", atomicfile.ErrFileTooLarge, info.Size(), maxSearchFileSize)
	}
	return f, info.Size(), nil
}

// readSearchFile reads one candidate from an OPEN descriptor: the binary sniff
// prefix first and on its own, then the rest only if the file is text.
//
// The two-step read is the point. A binary costs the sniff window rather than
// the whole per-file ceiling, so eight workers cannot each be holding 512 KiB of
// bytes that were never going to be reported. Reading the file whole and then
// asking whether it was binary is the same answer at 64x the memory and the I/O.
func readSearchFile(ctx context.Context, f *os.File, size int64) ([]byte, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	sniff := make([]byte, min(int64(binarySniffN), size))
	n, err := io.ReadFull(f, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	// Sliced to what was actually read: ReadFull leaves the tail of a short read
	// zeroed, and a zero byte is exactly what looksBinary looks for.
	sniff = sniff[:n]
	if looksBinary(sniff) {
		return nil, errSearchBinary
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	// The REST from the same descriptor. One byte past the ceiling, so a file
	// that grew during the read is reported rather than silently truncated into
	// a result.
	rest, err := io.ReadAll(io.LimitReader(f, maxSearchFileSize-int64(n)+1))
	if err != nil {
		return nil, err
	}
	if int64(n+len(rest)) > maxSearchFileSize {
		return nil, fmt.Errorf("%w: file grew past %d bytes during the search read",
			atomicfile.ErrFileTooLarge, maxSearchFileSize)
	}
	return append(sniff, rest...), nil
}

// results folds the walk's hits into the reply.
func (s *fileScan) results() FileSearchResult {
	flat := s.matches
	slices.SortFunc(flat, func(a, b FileMatch) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
	truncated := s.truncated
	if len(flat) > maxSearchMatches {
		flat = flat[:maxSearchMatches]
		truncated = true
	}
	if flat == nil {
		// A nil slice serialises as JSON null, which the client must not have to
		// narrow.
		flat = []FileMatch{}
	}
	return FileSearchResult{
		Matches:   flat,
		Scanned:   s.files,
		Truncated: truncated,
	}
}

// logSearchReadError records why a candidate contributed nothing, and reports
// whether that was a LOSS. The two answers come from one switch because they are
// one judgement: the Debug cases are SKIPS the search chose or expected, and
// anything reaching Warn is a file the search meant to read and could not.
//
// ErrNotRegular covers the FIFO, device node and socket refused off the
// descriptor (which is also what keeps /proc special files out, something an
// os.Root explicitly does not do), ELOOP and ENOTDIR are the kernel refusing a
// name that was swapped after the walk classified it, and a vanished file is the
// ordinary consequence of searching a tree the agent is writing to. A cancelled
// request is neither: the handler discards the body, so nothing is reported at
// all.
func logSearchReadError(abs string, err error) (lost bool) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, atomicfile.ErrNotRegular),
		errors.Is(err, atomicfile.ErrFileTooLarge), isSwapRefusal(err):
		slog.Debug("filebrowse: search skipped file", "path", abs, "error", err)
		return false
	default:
		slog.Warn("filebrowse: search read failed", "path", abs, "error", err)
		return true
	}
}

// matchLines finds every matching line in one file's bytes, capped per file.
func (s *fileScan) matchLines(abs string, data []byte) []FileMatch {
	var out []FileMatch
	line := 0
	for rest := data; len(rest) > 0; {
		seg := rest
		if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
			seg, rest = rest[:idx], rest[idx+1:]
		} else {
			rest = nil
		}
		line++
		hay := seg
		if !s.caseSensitive {
			hay = bytes.ToLower(seg)
		}
		at := bytes.Index(hay, s.needle)
		if at < 0 {
			continue
		}
		out = append(out, FileMatch{Path: abs, Excerpt: excerptLine(seg, at), Line: line})
		if len(out) >= maxFileMatches {
			return out
		}
	}
	return out
}

// excerptLine renders one matching line for a result row: the trailing CR of a
// CRLF file dropped, and the line windowed around the match when it is long
// enough that shipping it whole would cost more than it tells. Rune-indexed so a
// multi-byte character is never split; the ellipsis is the U+2026 the transcript
// search uses.
func excerptLine(seg []byte, at int) string {
	runes := []rune(strings.TrimRight(string(seg), "\r"))
	hit := utf8.RuneCount(seg[:min(at, len(seg))])
	start := max(hit-searchExcerptRadius, 0)
	end := min(hit+searchExcerptRadius, len(runes))
	var b strings.Builder
	if start > 0 {
		b.WriteString("\u2026")
	}
	b.WriteString(strings.TrimSpace(string(runes[start:end])))
	if end < len(runes) {
		b.WriteString("\u2026")
	}
	return b.String()
}
