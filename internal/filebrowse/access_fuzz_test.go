package filebrowse

import (
	"strings"
	"testing"
)

// FuzzEnforce pins the access-control contract across arbitrary paths:
// enforce must deny exactly the paths the policy blocks — anything
// outside the granted mounts (allow-list, deny-by-default) or a
// sensitive path — and allow everything else. The oracle reuses the
// real mountFor and IsSensitive functions (the policy sources of
// truth), so the property catches a broken composition (wrong combine,
// inverted check, missing IsSensitive call, wrong prefix match) rather
// than restating a single copied expression.
func FuzzEnforce(f *testing.F) {
	f.Add("/workspace/file.txt")
	f.Add("/workspace")
	f.Add("/etc/passwd")
	f.Add("/config/chats/a.json")
	f.Add("/config/kiro/steering/vibekit.md")
	f.Add("/config")
	f.Add("/configextra/x") // prefix of a mount name, NOT inside it
	f.Add("/../etc/shadow")
	f.Add("/app/../workspace")
	f.Add("/\x00etc")
	f.Add("/CONFIG/CHATS/x")
	f.Add("")
	f.Add("/")

	// Policy-level mounts (never touched by enforce, which is purely
	// lexical): the standard container pair.
	h := &Handler{mounts: []mount{
		{dir: "/workspace", name: "workspace"},
		{dir: "/config", name: "config"},
	}}

	f.Fuzz(func(t *testing.T, path string) {
		m, err := h.enforce(path)

		granted := h.mountFor(path) != nil
		blocked := !granted || IsSensitive(path)

		// Security invariant: anything the policy blocks must be denied.
		if blocked && err == nil {
			t.Fatalf("enforce(%q) = nil, want denial (granted=%v sensitive=%v)",
				path, granted, IsSensitive(path))
		}
		// No-over-block invariant: a denial must be backed by the policy.
		if !blocked && err != nil {
			t.Fatalf("enforce(%q) = %v, want allow (granted and not sensitive)", path, err)
		}
		// The returned mount is the owning mount.
		if err == nil && m != h.mountFor(path) {
			t.Fatalf("enforce(%q) returned mount %v, want %v", path, m, h.mountFor(path))
		}
	})
}

// FuzzIsProtectedDir pins the trailing-slash normalisation of the
// protected-directory guard: the verdict must not depend on how many
// trailing slashes the caller passes, since the guard normalises to a
// single trailing slash before matching. A regression in that
// normalisation would let `/config/chats` slip past while
// `/config/chats/` is blocked (or vice versa).
func FuzzIsProtectedDir(f *testing.F) {
	f.Add("/config")
	f.Add("/config/chats")
	f.Add("/config/chats/")
	f.Add("/workspace")
	f.Add("/config/kiro/agents")
	f.Add("/")
	f.Add("")
	f.Add("/config/chats///")

	f.Fuzz(func(t *testing.T, path string) {
		got := isProtectedDir(path)
		if trimmed := isProtectedDir(strings.TrimRight(path, "/")); trimmed != got {
			t.Fatalf("isProtectedDir(%q)=%v but trailing-slash-trimmed form=%v", path, got, trimmed)
		}
		if slashed := isProtectedDir(path + "/"); slashed != got {
			t.Fatalf("isProtectedDir(%q)=%v but trailing-slash-added form=%v", path, got, slashed)
		}
	})
}
