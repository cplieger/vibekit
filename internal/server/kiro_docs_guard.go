// What the `.kiro` docs scan is allowed to open.
//
// The scan walks a directory tree and reads the first FrontMatterReadCap (64 KiB)
// of every markdown and hook file it finds, putting the parsed head into a JSON
// list the browser renders. That makes "which files can this walk reach" a
// disclosure question, and until this guard it had no answer: the walk followed
// whatever the filesystem handed it and read whatever it landed on.
//
// TWO LAYERS, and they are ONE mechanism rather than two independent checks.
//
//  1. The escape refusal. os.DirFS does not confine: its own documentation says
//     a symlink pointing outside the tree is not stopped any more than os.Open
//     would stop it, and fs.WalkDir walks the TARGET of a symlinked root. So
//     `.kiro/steering -> /config` is walked as if it were the steering
//     directory. Every entry is resolved and refused if it leaves the root.
//
//  2. filebrowse.IsSensitive, the SAME predicate the browser file surface
//     applies — called, not copied. Two scanners disagreeing about what is off
//     limits is the inconsistency that becomes a leak the next time a root is
//     widened.
//
// Layer 1 is what closes the case that motivated this, and layer 2 alone would
// not have: IsSensitive matches absolute `/config/...` paths, while the walk
// holds paths relative to a root under /workspace. Testing the UNRESOLVED path
// can therefore never match, so the resolution is what gives the denylist
// anything to match on. Layer 2 then covers the other direction — a root that
// legitimately resolves into /config, which invariant 6 permits an operator to
// arrange.
//
// A refusal is silent per entry beyond one Warn, and the entry is simply absent
// from the list. There is no requester to refuse here and no repair to suggest:
// the operator's own tree is the input, and a page listing a file it declined to
// read would be a worse answer than a page that does not list it.

package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/filebrowse"
)

// docVerdict is the guard's answer about one entry: whether the scan may read it,
// and whether a row built from it may offer a DELETE.
//
// The second field is here rather than in a second pass because the FIRST field
// already required the resolution that answers it. rootGuard.allow calls
// EvalSymlinks to decide the escape question; noticing that the resolved path
// differs from the joined one costs a comparison, and a separate resolver would be
// a second copy of the same syscall chain.
//
// # There is no writability bit here, and there was: D67a is WITHDRAWN
//
// It claimed a symlinked entry could not be saved, because internal/filebrowse
// opens with syscall.O_NOFOLLOW. That is not what happens. resolvePath calls
// EvalSymlinks and returns the RESOLVED path as loc.abs (filebrowse/paths.go), and
// writeFile applies O_NOFOLLOW to that already-canonical target — so the flag
// refuses a symlink PLANTED at the final component between resolution and open, not
// a link the user deliberately followed. Saving an aliased steering doc works
// today, and `resolved != full` was therefore not a writability test at all: it also
// marked every file beneath an in-root symlinked DIRECTORY read-only, and those
// writes work for exactly the same reason.
type docVerdict struct {
	allowed bool
	// deleteProtected marks an entry whose DELETE must not be offered.
	//
	// True when the entry's OWN final component is a symlink, and the precision
	// matters: `resolved != full` is the test D67a used, and it is true for every
	// file beneath an in-root symlinked DIRECTORY as well. Those are not the same
	// case. Deleting `steering/inner.md` where `steering` links to `elsewhere`
	// removes `elsewhere/inner.md`, which IS the file the row names — one row, one
	// file, nothing surprising. Deleting `steering/alias.md` where the FILE links to
	// `shared/canonical.md` removes canonical.md, and canonical.md is its own row on
	// the same page: a second entry the reader never touched silently disappears.
	//
	// That is why this bit survived the withdrawal while the read-only one did not.
	// The delete route canonicalizes exactly as the write route does, so editing
	// through a link writes the target — which is what following a link MEANS — while
	// deleting through it destroys a file the user was addressing by another name.
	//
	// It is an advisory for the page, not a boundary: the delete route still runs
	// resolveOrForbid, mount confinement, the sensitive-path list, the
	// protected-directory check and the mount-root refusal, none of which trust
	// anything the client was told.
	deleteProtected bool
}

// pathGuard reports what the scan may do with one entry, named by its path
// WITHIN the fs.FS being walked.
//
// A function rather than a method on the walker so the fs.FS seam survives: the
// scanners stay testable with fstest.MapFS (which cannot express a symlink
// escaping a real root) while production passes a guard closed over the real
// directory. A nil guard admits everything with every affordance, which is what the
// MapFS tests want: absent provenance means unrestricted, and a restriction is
// asserted rather than inferred (the same default direction as vibekit.Origin's).
type pathGuard func(rel string) docVerdict

func (g pathGuard) allows(rel string) bool {
	return g == nil || g(rel).allowed
}

// rootGuard is the production guard for one `.kiro` tree.
type rootGuard struct {
	// dir is the root as EvalSymlinks resolved it. The root itself may be a
	// symlink — an operator symlinking `.kiro` in is exactly the kind of
	// reshaping invariant 6 protects — so it is resolved once and its target
	// becomes the boundary. Its own contents then stay readable while an entry
	// pointing further out does not.
	dir string
	// category names the tree in the log line, so a refusal is attributable
	// without the operator having to guess which walk produced it.
	category string
}

// newRootGuard resolves dir and returns a guard over it. A dir that cannot be
// resolved yields a guard that refuses everything: the walk is about to read
// files out of a tree nothing can name, and admitting them would be the one
// case where an unreadable root is treated as a permissive one.
func newRootGuard(dir, category string) pathGuard {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		slog.Warn("kiro docs: root not resolvable, skipping", "dir", dir, "error", err)
		return func(string) docVerdict { return docVerdict{} }
	}
	g := &rootGuard{dir: resolved, category: category}
	return g.allow
}

func (g *rootGuard) allow(rel string) docVerdict {
	full := filepath.Join(g.dir, rel)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		// A dangling symlink, a path component that vanished mid-walk, or a
		// permission wall. Unreadable either way, and refusing costs nothing
		// the read would not have failed on anyway.
		return docVerdict{}
	}
	if !g.inRoot(resolved) {
		slog.Warn("kiro docs: refusing a link out of the scanned tree",
			"category", g.category, "path", rel, "root", g.dir)
		return docVerdict{}
	}
	if filebrowse.IsSensitive(resolved) {
		slog.Warn("kiro docs: refusing a path on the sensitive denylist",
			"category", g.category, "path", rel)
		return docVerdict{}
	}
	// A link that STAYS inside the root passes both refusals above and is listed,
	// which is correct — the operator arranged it and the content is theirs. It
	// remains editable (the write resolves to the target, which is what following
	// the link means); what it cannot offer is the delete that would unlink a file
	// listed under its own name elsewhere on the page. See
	// docVerdict.deleteProtected.
	return docVerdict{allowed: true, deleteProtected: finalComponentIsLink(full)}
}

// finalComponentIsLink reports whether the last component of full is itself a
// symlink, which is the only shape whose delete removes a DIFFERENT row's file.
//
// One extra Lstat per document, alongside the EvalSymlinks chain allow already runs;
// the alternative was reusing `resolved != full`, which cannot distinguish a linked
// file from a file under a linked directory and so withheld the delete from every
// document in a reshaped tree.
//
// An unreadable entry withholds the affordance. That is the safe direction for a
// destructive control, and it costs nothing real: allow has already resolved this
// path successfully, so an error here means the tree changed mid-walk.
func finalComponentIsLink(full string) bool {
	fi, err := os.Lstat(full)
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// inRoot reports whether an already-resolved absolute path is the root or sits
// beneath it.
//
// The separator is part of the comparison deliberately: a prefix test without it
// admits `/workspace/.kiro-evil` against a `/workspace/.kiro` root, which is the
// sibling-lookalike mistake this kind of check exists to avoid.
func (g *rootGuard) inRoot(resolved string) bool {
	return resolved == g.dir || strings.HasPrefix(resolved, g.dir+string(filepath.Separator))
}

// errRefused is what a guarded read returns for a path the guard declined. It
// travels the same error channel as an unreadable file because the outcome IS
// the same at every call site — no row, keep scanning — and giving a refusal its
// own branch in five scanners would be five chances to forget one.
var errRefused = errors.New("kiro docs: path refused by the scan guard")

// readGuardedFS is readCappedFS with the guard in front of it. Every read the
// docs scan performs goes through here; readCappedFS is left unwrapped because
// the ENTITY scanner (kiro_config.go) shares it and takes no guard.
//
// It returns the verdict alongside the bytes so the caller building the row does
// not have to ask the guard a second time — that second call would repeat the
// EvalSymlinks chain for every one of the ~200 documents in this workspace.
func readGuardedFS(root fs.FS, name string, guard pathGuard) ([]byte, docVerdict, error) {
	v := docVerdict{allowed: true}
	if guard != nil {
		v = guard(name)
	}
	if !v.allowed {
		return nil, v, errRefused
	}
	data, err := readCappedFS(root, name)
	return data, v, err
}

// readGuardedDir is fs.ReadDir with the guard in front of it, for the three
// categories that enumerate one flat directory instead of walking it.
//
// The ORDER is the whole point, and it is the same rule the recursive walk
// already applies at each directory: refuse AT the directory, so a category
// symlinked out of the tree is never enumerated. Guarding only the per-file
// reads is not equivalent, because a listing is itself a disclosure — the skills
// scanner turns each target subdirectory into an undescribed row and the agents
// scanner turns each target `.md`/`.json` filename into one, both AFTER the
// guarded read of the manifest has already refused. The names reach the browser
// either way.
func readGuardedDir(root fs.FS, name string, guard pathGuard) ([]fs.DirEntry, error) {
	if !guard.allows(name) {
		return nil, errRefused
	}
	return fs.ReadDir(root, name)
}
