// Package workspace provides path-resolution primitives for
// workspace-scoped file operations. These are security primitives that
// prevent symlink escape and ".." traversal.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/pathinside/v2"
)

// ResolveInsideAbs confines p to absWork, which must already be absolute — the
// caller stores it once at startup (Runtime.workDir, Handler.workDir), so an Abs
// call per request would be work with no answer to give.
//
// It answers a LEXICAL question and nothing more. EvalSymlinks runs once, the
// containment is checked once, and the absolute path comes back; the operation
// happens later, and if it happens through ambient os calls the kernel re-resolves
// every component with no boundary attached. A caller that goes on to touch the
// filesystem must name the operation through an os.Root rooted at absWork — see
// agent's lifetime.confineInWorkDir and filebrowse's mount.root for the two
// consumers that do. This function's job is the verdict, not the enforcement.
//
// The containment rule itself is confine, shared with ResolveInsideAnyAbs.
//
// An empty absWork refuses everything rather than confining to the process
// working directory: Root("") contains nothing, and an unset workDir is a missing
// configuration value, not a request to sandbox onto whatever directory the
// process happens to be in.
func ResolveInsideAbs(absWork, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(absWork, p)
	}
	return confine(absWork, p)
}

// ResolveInsideAnyAbs confines p to the FIRST of roots that contains it. Every
// root must already be absolute, and roots[0] is the primary one.
//
// It exists for one consumer: a chat ATTACHMENT path, which may name a file under
// the workspace or one the composer's upload endpoint just wrote into the uploads
// directory. Those are two directories rather than one tree, so a single-root
// confinement refuses half of them — and the refusal is silent at the user, since
// the prompt builder degrades an unresolvable attachment to a text block naming
// the filename and nothing else.
//
// Two rules keep this narrow rather than a general widening:
//
//   - An ABSOLUTE p is tried against each root in ORDER and returns the first
//     containment success. When no root contains it the error is roots[0]'s, so
//     the message keeps naming the workspace.
//   - A RELATIVE p resolves against roots[0] ONLY. ResolveInsideAbs joins a
//     relative path onto its root, so with two roots a relative path would name
//     two different files; keeping it single-valued preserves today's behaviour
//     exactly.
//
// An empty root contains nothing (pathinside.Root("") matches no path), so a
// caller that has no second root may pass "" rather than branching. An empty
// roots[0] therefore refuses everything, which is ResolveInsideAbs's own rule for
// an unset workspace.
//
// This is NOT the agent's own fs confinement: internal/agent's fs handlers stay
// workspace-only, because an agent asking to read a file is not the user handing
// one over.
func ResolveInsideAnyAbs(roots []string, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if len(roots) == 0 {
		return "", errors.New("no roots configured")
	}
	if !filepath.IsAbs(p) {
		return ResolveInsideAbs(roots[0], p)
	}
	var firstErr error
	for _, root := range roots {
		resolved, err := confine(root, p)
		if err == nil {
			return resolved, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", firstErr
}

// confine is the containment rule itself, applied to an already-absolute p.
//
// The boundary is built ONCE, before anything is compared against it, which is
// what keeps the three containment questions below from being asked backwards: a
// pathinside.Root held in a named variable has no pair to transpose, unlike a
// two-argument predicate whose own wrapper read in the opposite order from the
// filepath.Rel it called.
//
// Both callers share this body rather than each carrying a copy, because a
// symlink-escape fix applied to one copy leaves the other asserting the old
// shape — the same trap the retired test-local duplicate of this resolver was.
func confine(absRoot, p string) (string, error) {
	root := pathinside.Root(absRoot)
	clean := filepath.Clean(p)
	if !root.Contains(clean) {
		return "", errors.New("path escapes workspace")
	}
	if resolved, resErr := filepath.EvalSymlinks(clean); resErr == nil {
		if !root.Contains(resolved) {
			return "", fmt.Errorf("path %q escapes workspace via symlink", p)
		}
		return resolved, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		if os.IsNotExist(err) {
			return clean, nil
		}
		return "", err
	}
	if !root.Contains(parent) {
		return "", fmt.Errorf("path %q escapes workspace via symlink", p)
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

// RelPath returns the workspace-relative, forward-slash-normalized path
// for abs under workDir. Returns ("", error) when filepath.Rel fails.
// Use this instead of inline filepath.Rel + filepath.ToSlash sequences
// to ensure consistent normalization across fs handlers.
func RelPath(workDir, abs string) (string, error) {
	rel, err := filepath.Rel(workDir, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
