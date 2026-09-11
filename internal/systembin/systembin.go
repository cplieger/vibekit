// Package systembin resolves an image-baked system binary to an absolute path
// from a fixed directory set, so a spawn's argv[0] does not come from PATH.
//
// # The trust argument, and where it holds
//
// PATH[0] in this image is /config/tools/bin, the toolbelt engine's link
// directory on the PERSISTENT VOLUME. An agent with an approved shell command
// can write a file there, and a bare-name spawn then executes it. For a binary
// the SERVER spawns on its own schedule that is an escalation with no
// supervision attached: it is not a tool call, it raises no
// session/request_permission, it appears in no transcript, no user is present,
// and it survives container recreation because the volume does.
//
// This package answers that for exactly the binaries whose trust argument
// holds: git and bash are apt packages baked into the final image at
// /usr/bin, nothing at runtime writes that directory, and it is not on the
// persistent volume. So "root owns it and an unprivileged writer cannot reach
// it" is a true statement about /usr/bin here, where it is NOT a true statement
// about a tool the toolbelt engine installs.
//
// It is therefore deliberately NOT a general resolver. A toolbelt-managed
// binary (npm, gh, glab, tea) lives in the writable directory that leads PATH,
// so pinning it to an absolute path would name the same file a PATH lookup
// finds and would confine nothing. Those sites bind their probe's answer
// instead and say so; closing that leg is custody on the toolbelt bin tree,
// which is toolbelt's question.
//
// # What it reads
//
// Nothing from the environment. Not PATH — that is the point — and no
// per-binary override either: a settable variable would import the property
// that anything able to set the server's environment chooses its binary, to buy
// an install shape neither git nor bash has here.
//
// /usr/local/bin is deliberately excluded. Nothing vibekit bakes lands there,
// and it is admin-group writable on some hosts, so including it would widen the
// trusted set for no binary this package serves.
package systembin

import (
	"os"
	"path/filepath"
	"strings"
)

// systemDirs is the trusted candidate set, in order. A var rather than a const
// slice so a test can point it at a temp dir; production never reassigns it.
var systemDirs = []string{"/usr/bin", "/bin"}

// Resolve returns the absolute path of an image-baked binary, and whether it
// was found. A miss returns ("", false) and the caller must REFUSE: falling
// back to the bare name would void the pin at that site, and one site's silent
// fallback voids it everywhere, because an attacker picks the site.
//
// A name containing a path separator is refused outright — a resolver that
// accepts "../x" is not a pin.
func Resolve(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", false
	}
	for _, dir := range systemDirs {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		// Regular AND executable: a directory, a FIFO or a 0644 file at the
		// candidate name is not the binary, and spawning one of them fails in a
		// way that reads as the binary being broken rather than absent.
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}
