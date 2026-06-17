package gitexec

// Mutant-killing tests for unit vibekit-u25 (package internal/gitexec).
// Targets surviving gremlins mutants in gitexec.go (firstSubcommand,
// ParseRemoteHost, sanitizeHost, ParseSCPStyle).
// All new identifiers are prefixed gk_vibekit_u25_ to avoid collisions.

import "testing"

// --- gitexec.go:148:9 and 148:22 CONDITIONALS_NEGATION ---
// firstSubcommand: `if a == "-c" || a == "-C"` makes the loop skip the VALUE
// argument after a -c/-C flag. Negating either equality stops the skip, so the
// flag's value is mistaken for the subcommand.
func Test_gk_vibekit_u25_firstSubcommand_skipsFlagValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
		args []string
	}{
		// "-c" consumes its value ("status"); the real subcommand is "commit".
		// Kills 148:9 (a == "-c"): the negation returns "status".
		{name: "dash_c_skips_value", args: []string{"-c", "status", "commit"}, want: "commit"},
		// "-C" consumes its value ("somedir"); the real subcommand is "log".
		// Kills 148:22 (a == "-C"): the negation returns "somedir".
		{name: "dash_C_skips_value", args: []string{"-C", "somedir", "log"}, want: "log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := firstSubcommand(tt.args); got != tt.want {
				t.Errorf("firstSubcommand(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// --- gitexec.go:238:9 CONDITIONALS_NEGATION ---
// ParseRemoteHost: `if raw == "" { return "" }` is the empty guard. Negating to
// `!= ""` returns "" for every NON-empty input, dropping real host extraction.
func Test_gk_vibekit_u25_ParseRemoteHost_extractsHostFromURL(t *testing.T) {
	t.Parallel()
	const in = "https://github.com/foo/bar.git"
	if got := ParseRemoteHost(in); got != "github.com" {
		t.Errorf("ParseRemoteHost(%q) = %q, want %q", in, got, "github.com")
	}
}

// --- gitexec.go:245:9 CONDITIONALS_NEGATION ---
// ParseRemoteHost: after url.Parse, `if err != nil { return "" }`. Negating to
// `== nil` skips the guard on a parse error and dereferences the nil *url.URL.
// A NUL byte makes url.Parse fail deterministically (invalid control char),
// and the leading "://" routes past the scp-style branch to url.Parse.
func Test_gk_vibekit_u25_ParseRemoteHost_parseErrorReturnsEmpty(t *testing.T) {
	t.Parallel()
	const in = "http://ho\x00st.com" // control char → url.Parse error
	if got := ParseRemoteHost(in); got != "" {
		t.Errorf("ParseRemoteHost(%q) = %q, want %q", in, got, "")
	}
}

// --- gitexec.go:254:8 CONDITIONALS_NEGATION + CONDITIONALS_BOUNDARY ---
// --- gitexec.go:254:20, 254:33, 254:45, 254:57 CONDITIONALS_NEGATION ---
// sanitizeHost returns "" if any char is a control byte (< 0x20), 0x7f, '@',
// ':' or '/'. Each forbidden char below is the ONLY clause that matches it, so
// negating that clause makes sanitizeHost return the raw host instead of "".
// The space case (0x20) anchors the boundary: '<' keeps it, '<=' rejects it.
func Test_gk_vibekit_u25_sanitizeHost_rejectsForbiddenChars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "control_low", in: "\x01", want: ""},     // c < 0x20   (254:8 NEGATION)
		{name: "space_allowed", in: "a b", want: "a b"}, // 0x20 kept  (254:8 BOUNDARY: '<' not '<=')
		{name: "del_0x7f", in: "\x7f", want: ""},        // c == 0x7f  (254:20)
		{name: "at_sign", in: "@", want: ""},            // c == '@'   (254:33)
		{name: "colon", in: ":", want: ""},              // c == ':'   (254:45)
		{name: "slash", in: "/", want: ""},              // c == '/'   (254:57)
		{name: "clean_host", in: "github.com", want: "github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeHost(tt.in); got != tt.want {
				t.Errorf("sanitizeHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- gitexec.go:270:8 CONDITIONALS_BOUNDARY ---
// ParseSCPStyle: `if at <= 0` rejects a missing '@' (at==-1) or an empty user
// ('@' at index 0). The boundary mutant `at < 0` accepts the empty-user form.
func Test_gk_vibekit_u25_ParseSCPStyle_rejectsEmptyUser(t *testing.T) {
	t.Parallel()
	const in = "@host:path" // '@' at index 0 → empty user → must be rejected
	if _, _, ok := ParseSCPStyle(in); ok {
		t.Errorf("ParseSCPStyle(%q) ok = true, want false (leading '@' = empty user must be rejected)", in)
	}
}

// --- gitexec.go:277:16 ARITHMETIC_BASE ---
// ParseSCPStyle: `rest := raw[at+1:]` slices the path AFTER the '@'. The '-'
// mutant (raw[at-1:]) includes the byte before '@', so the host gains a stray
// prefix character.
// --- gitexec.go:279:17 CONDITIONALS_NEGATION ---
// Same function: `if !found || h == "" || ...` rejects a bad split. Negating
// `h == ""` to `h != ""` rejects every VALID (non-empty host) scp URL.
// One valid input distinguishes both: host must be exactly "github.com" and ok true.
func Test_gk_vibekit_u25_ParseSCPStyle_extractsHostAfterAt(t *testing.T) {
	t.Parallel()
	const in = "git@github.com:foo"
	host, path, ok := ParseSCPStyle(in)
	if !ok {
		t.Fatalf("ParseSCPStyle(%q) ok = false, want true", in)
	}
	if host != "github.com" {
		t.Errorf("ParseSCPStyle(%q) host = %q, want %q", in, host, "github.com")
	}
	if path != "foo" {
		t.Errorf("ParseSCPStyle(%q) path = %q, want %q", in, path, "foo")
	}
}
