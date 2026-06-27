package filehandler

import (
	"strings"
	"testing"
)

// FuzzEnforceAccess pins the access-control contract across arbitrary
// paths: enforceAccess must deny exactly the paths the policy blocks —
// a blacklisted top-level segment or a sensitive path — and allow
// everything else. The oracle reuses the real isSensitive function and
// the blacklist table (the policy source of truth), so the property
// catches a broken composition (wrong combine, inverted check, missing
// isSensitive call, mis-extracted top segment) rather than restating a
// single copied expression.
func FuzzEnforceAccess(f *testing.F) {
	f.Add("/workspace/file.txt")
	f.Add("/etc/passwd")
	f.Add("/config/chats/a.json")
	f.Add("/config/kiro/steering/vibekit.md")
	f.Add("/../etc/shadow")
	f.Add("/app/../workspace")
	f.Add("/\x00etc")
	f.Add("/CONFIG/CHATS/x")
	f.Add("")
	f.Add("/")

	f.Fuzz(func(t *testing.T, path string) {
		err := enforceAccess(path)

		top := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
		blocked := blacklist[top] || isSensitive(path)

		// Security invariant: anything the policy blocks must be denied.
		if blocked && err == nil {
			t.Fatalf("enforceAccess(%q) = nil, want denial (top=%q blacklisted=%v sensitive=%v)",
				path, top, blacklist[top], isSensitive(path))
		}
		// No-over-block invariant: a denial must be backed by the policy.
		if !blocked && err != nil {
			t.Fatalf("enforceAccess(%q) = %v, want allow (neither blacklisted nor sensitive)", path, err)
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
