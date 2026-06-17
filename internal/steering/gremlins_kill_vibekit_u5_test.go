package steering

// Mutant-killing tests for unit vibekit-u5 (package internal/steering).
//
// Targets the surviving gremlins mutants in workspace.go (readGitOrigin,
// readGitBranch, hostFromGitURL, kindFromHost, readFirstLine,
// isMarkdownHeading, truncateUTF8). Each test names the mutant(s) it kills
// and asserts an observable outcome that flips when the operator at that
// line is mutated. Tests-only; no production code is edited. Helper/identifier
// names are prefixed gk_vibekit_u5_ to avoid colliding with the sibling
// vibekit-u4 file in this same package.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// shared helper (prefixed gk_vibekit_u5_ to avoid sibling-unit collisions)
// ---------------------------------------------------------------------------

func gk_vibekit_u5_writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ===========================================================================
// readGitOrigin / readGitBranch — err-guard negations
// ===========================================================================

// Kills workspace.go:127:9 (CONDITIONALS_NEGATION on `if err != nil` in
// readGitOrigin). On a readable config the original proceeds and returns the
// url; the `err == nil` mutant treats a successful read as failure and
// returns "" immediately.
func TestGKVibekitU5_ReadGitOrigin_ReturnsURLOnSuccessfulRead(t *testing.T) {
	repo := t.TempDir()
	gk_vibekit_u5_writeFile(t, filepath.Join(repo, ".git", "config"),
		"[remote \"origin\"]\n\turl = https://github.com/acme/widget.git\n")

	const want = "https://github.com/acme/widget.git"
	if got := readGitOrigin(repo); got != want {
		t.Errorf("readGitOrigin(valid config) = %q, want %q", got, want)
	}
	// Error path: a missing config still yields "" (read error).
	if got := readGitOrigin(filepath.Join(repo, "does-not-exist")); got != "" {
		t.Errorf("readGitOrigin(missing config) = %q, want \"\"", got)
	}
}

// Kills workspace.go:154:9 (CONDITIONALS_NEGATION on `if err != nil` in
// readGitBranch). A readable HEAD yields the branch; the `err == nil` mutant
// returns "" on success.
func TestGKVibekitU5_ReadGitBranch_ReturnsBranchOnSuccessfulRead(t *testing.T) {
	repo := t.TempDir()
	gk_vibekit_u5_writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")

	if got := readGitBranch(repo); got != "main" {
		t.Errorf("readGitBranch(valid HEAD) = %q, want %q", got, "main")
	}
	if got := readGitBranch(filepath.Join(repo, "does-not-exist")); got != "" {
		t.Errorf("readGitBranch(missing HEAD) = %q, want \"\"", got)
	}
}

// ===========================================================================
// hostFromGitURL — https branch (line 175 family, 176, 178)
// ===========================================================================

// Kills the credential-stripping guard on workspace.go:175 and the index/copy
// that follows it:
//   - 175:41 NEGATION (`at >= 0`) and 175:66 NEGATION (`at < slash`):
//     "creds-stripped" — `user@` is before the first `/`, so the original
//     strips it and yields "github.com"; either negation skips the strip,
//     leaving an "@" in the host so it returns "".
//   - 176:18 ARITHMETIC (`rest[at+1:]`): the `-` mutant slices `rest[at-1:]`,
//     keeping a stray byte + "@" in the host -> "".
//   - 175:41 BOUNDARY (`at >= 0`): "leading-at-creds" has `@` at index 0;
//     the `at > 0` mutant skips the strip -> "".
//   - 175:56 NEGATION (`slash < 0`): "creds-no-path" has an `@` but no `/`;
//     the `slash >= 0` mutant turns the guard false so the strip is skipped
//     and the leftover "@" forces "".
//   - 175:56 BOUNDARY (`slash < 0`): "slash-before-at" puts a `/` at index 0
//     ahead of the `@`; original keeps the leading-slash host and returns "",
//     but the `slash <= 0` mutant strips and wrongly yields "github.com".
//   - 178:39 NEGATION (`i > 0`): "no-creds" reaches the `host := rest[:i]`
//     guard with i==10; the `i <= 0` mutant skips it and returns "".
func TestGKVibekitU5_HostFromGitURL_HTTPS(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"creds-stripped", "https://user@github.com/acme/widget.git", "github.com"},
		{"leading-at-creds", "https://@github.com/acme/widget.git", "github.com"},
		{"creds-no-path", "https://user@github.com", "github.com"},
		{"slash-before-at", "https:///@github.com/foo", ""},
		{"no-creds", "https://github.com/acme/widget.git", "github.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostFromGitURL(tc.url); got != tc.want {
				t.Errorf("hostFromGitURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// ===========================================================================
// hostFromGitURL — scp-style branch (line 190, 192)
// ===========================================================================

// Kills:
//   - 190:37 NEGATION (`i > 0`) and 192:16 ARITHMETIC (`url[i+1:]`):
//     "scp-standard" has `@` at index 3; the original takes the scp branch
//     and returns "github.com". The `i <= 0` mutant skips the branch -> "";
//     the `url[i-1:]` mutant keeps a stray byte + "@" in the host -> "".
//   - 190:37 BOUNDARY (`i > 0`): "scp-leading-at" has `@` at index 0; the
//     original `i > 0` is false so it returns "", but the `i >= 0` mutant
//     enters the branch and wrongly yields "github.com".
func TestGKVibekitU5_HostFromGitURL_SCP(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"scp-standard", "git@github.com:acme/widget.git", "github.com"},
		{"scp-leading-at", "@github.com:acme/repo", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostFromGitURL(tc.url); got != tc.want {
				t.Errorf("hostFromGitURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// ===========================================================================
// kindFromHost — equality-check negations (209, 212, 215, 218)
// ===========================================================================

// Kills the four `host == "..."` equality checks (CONDITIONALS_NEGATION):
//   - 209:10 (`host == ""`): killed by "github.com" — flipping to
//     `host != ""` makes a non-empty host return "" before any classification.
//   - 212:10 / 215:10 / 218:10 (`host == "github.com"` / "gitlab.com" /
//     "codeberg.org"): killed by "example.com" — flipping the first operand
//     of each OR to `!=` makes it true for any non-matching host,
//     short-circuiting the OR so the function wrongly returns
//     "github"/"gitlab"/"codeberg" instead of "".
//
// The positive "gitlab.com"/"codeberg.org" rows pin the classification and
// keep the original behavior honest (they pass under both original and the
// negations, which still match via the suffix/contains operands).
func TestGKVibekitU5_KindFromHost(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"github.com", "github"},
		{"gitlab.com", "gitlab"},
		{"codeberg.org", "codeberg"},
		{"example.com", ""},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			if got := kindFromHost(tc.host); got != tc.want {
				t.Errorf("kindFromHost(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// ===========================================================================
// readFirstLine — length boundary (317)
// ===========================================================================

// Kills workspace.go:317:16 (CONDITIONALS_BOUNDARY on `if len(line) > 100`).
// At exactly 100 bytes the original returns the line verbatim; the `>= 100`
// mutant truncates and appends "...". (truncateUTF8(s,100) returns s unchanged
// when len(s)==100, so the mutant's only visible effect is the trailing
// ellipsis on a 100-char line.)
func TestGKVibekitU5_ReadFirstLine_LengthBoundary100(t *testing.T) {
	dir := t.TempDir()
	line100 := strings.Repeat("a", 100)
	gk_vibekit_u5_writeFile(t, filepath.Join(dir, "README.md"), line100+"\n")

	got := readFirstLine(filepath.Join(dir, "README.md"))
	if got != line100 {
		t.Errorf("readFirstLine(100-byte line) = %q (len %d), want the 100-byte line unchanged", got, len(got))
	}
	if strings.HasSuffix(got, "...") {
		t.Errorf("readFirstLine(100-byte line) wrongly appended ellipsis: %q", got)
	}

	// Sanity: a 101-byte line IS truncated to 100 bytes + "..." under both
	// the original and the mutant (only the ==100 case discriminates).
	dir2 := t.TempDir()
	line101 := strings.Repeat("b", 101)
	gk_vibekit_u5_writeFile(t, filepath.Join(dir2, "README.md"), line101+"\n")
	want2 := strings.Repeat("b", 100) + "..."
	if got2 := readFirstLine(filepath.Join(dir2, "README.md")); got2 != want2 {
		t.Errorf("readFirstLine(101-byte line) = %q, want %q", got2, want2)
	}
}

// ===========================================================================
// isMarkdownHeading — hash-count boundary (337)
// ===========================================================================

// Kills workspace.go:337:7 (CONDITIONALS_BOUNDARY on `if i > 6`). Exactly six
// `#` is a valid ATX heading: the original `6 > 6` is false so it proceeds to
// the trailing-whitespace check and returns true; the `6 >= 6` mutant returns
// false. The bare "######" (six hashes, EOL) case is killed the same way.
func TestGKVibekitU5_IsMarkdownHeading_SixHashBoundary(t *testing.T) {
	if !isMarkdownHeading("###### heading") {
		t.Errorf("isMarkdownHeading(%q) = false, want true", "###### heading")
	}
	if !isMarkdownHeading("######") {
		t.Errorf("isMarkdownHeading(%q) = false, want true", "######")
	}
	// Sanity: seven hashes is not a heading under either original or mutant.
	if isMarkdownHeading("####### heading") {
		t.Errorf("isMarkdownHeading(%q) = true, want false", "####### heading")
	}
}

// ===========================================================================
// truncateUTF8 — boundary guards (348, 354)
// ===========================================================================

// Kills workspace.go:348:12 (CONDITIONALS_BOUNDARY on `if len(s) <= n`). When
// len(s)==n the original returns s unchanged; the `len(s) < n` mutant falls
// through to the rune-walk loop and indexes s[n] (out of range) -> panic.
func TestGKVibekitU5_TruncateUTF8_LenEqualsN(t *testing.T) {
	if got := truncateUTF8("abcd", 4); got != "abcd" {
		t.Errorf("truncateUTF8(%q, 4) = %q, want %q", "abcd", got, "abcd")
	}
}

// Kills workspace.go:354:8 (CONDITIONALS_BOUNDARY on `for n > 0 && ...`). An
// all-continuation-byte input drives the walk loop down to n==0. The original
// `n > 0` stops there and returns ""; the `n >= 0` mutant runs one more
// iteration, decrements n to -1, and slices s[:-1] (out of range) -> panic.
func TestGKVibekitU5_TruncateUTF8_AllContinuationBytes(t *testing.T) {
	if got := truncateUTF8("\x80\x80\x80", 2); got != "" {
		t.Errorf("truncateUTF8(0x80 0x80 0x80, 2) = %q, want \"\"", got)
	}
}
