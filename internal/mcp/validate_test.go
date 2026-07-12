package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidate_AcceptReject(t *testing.T) {
	cases := []struct {
		srv     *Server
		name    string
		wantErr bool
	}{
		{
			name: "StdioAccepts",
			srv: &Server{
				Transport: TransportStdio, Name: "ok",
				Command: "bash", Args: []string{"-c", "echo"},
				Env: []KeyPair{{Name: "FOO", Value: "bar"}},
			},
			wantErr: false,
		},
		{
			name:    "StdioMissingCommand",
			srv:     &Server{Transport: TransportStdio, Name: "x", Command: ""},
			wantErr: true,
		},
		{
			name:    "StdioRejectsURL",
			srv:     &Server{Transport: TransportStdio, Name: "x", Command: "bash", URL: "https://bad"},
			wantErr: true,
		},
		{
			name: "RemoteHTTP",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok", URL: "https://x.example/mcp",
				Headers: []KeyPair{{Name: "Authorization", Value: "Bearer x"}},
			},
			wantErr: false,
		},
		{
			name:    "RemoteRejectsNonHTTP",
			srv:     &Server{Transport: TransportHTTP, Name: "x", URL: "file:///tmp/x"},
			wantErr: true,
		},
		{
			name: "RemoteRejectsCommand",
			srv: &Server{
				Transport: TransportHTTP, Name: "x",
				URL: "https://x.example", Command: "leaked",
			},
			wantErr: true,
		},
		{
			name: "ArgsRejectControlChars",
			srv: &Server{
				Transport: TransportStdio, Name: "x", Command: "bash",
				Args: []string{"ok", "bad\nline"},
			},
			wantErr: true,
		},
		{
			name: "EnvValueAllowsSymbols",
			srv: &Server{
				Transport: TransportStdio, Name: "x", Command: "bash",
				Env: []KeyPair{{Name: "KEY", Value: `{"nested":"json"}`}},
			},
			wantErr: false,
		},
		{
			name: "HeaderRejectControlChars",
			srv: &Server{
				Transport: TransportHTTP, Name: "x", URL: "https://x.example",
				Headers: []KeyPair{{Name: "X-Foo", Value: "line1\rline2"}},
			},
			wantErr: true,
		},
		{
			name:    "UnknownTransport",
			srv:     &Server{Transport: "grpc", Name: "x"},
			wantErr: true,
		},
		{
			name: "SSEAccepted",
			srv: &Server{
				Transport: TransportSSE, Name: "ok", URL: "https://x.example/sse",
				Headers: []KeyPair{{Name: "Authorization", Value: "Bearer x"}},
			},
			wantErr: false,
		},
		{
			name:    "SSERejectsNonHTTPScheme",
			srv:     &Server{Transport: TransportSSE, Name: "x", URL: "file:///tmp/x"},
			wantErr: true,
		},
		{
			name: "SSERejectsCommand",
			srv: &Server{
				Transport: TransportSSE, Name: "x",
				URL: "https://x.example", Command: "leaked",
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.srv)
			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Errorf("Validate(%s) err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestValidate_BadName(t *testing.T) {
	cases := []string{"", "1leading-digit", "has space", "with/slash", "dot.separated"}
	for _, n := range cases {
		if err := Validate(&Server{Transport: TransportStdio, Name: n, Command: "bash"}); err == nil {
			t.Errorf("expected error for name %q", n)
		}
	}
}

func TestValidate_GoodName(t *testing.T) {
	cases := []string{"a", "foo", "Foo", "my_server", "my-server", "s1"}
	for _, n := range cases {
		if err := Validate(&Server{Transport: TransportStdio, Name: n, Command: "bash"}); err != nil {
			t.Errorf("unexpected error for name %q: %v", n, err)
		}
	}
}

// F7: Name length boundaries (nameRe cap at 64 chars).
func TestValidate_NameLengthBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"a", false},                     // 1 char (minimum)
		{strings.Repeat("a", 64), false}, // max
		{strings.Repeat("a", 65), true},  // 1 over max
		{"", true},                       // empty
	}
	for _, tc := range cases {
		err := Validate(&Server{Transport: TransportStdio, Name: tc.name, Command: "bash"})
		gotErr := err != nil
		if gotErr != tc.wantErr {
			t.Errorf("Validate(name len=%d) err = %v, wantErr = %v", len(tc.name), err, tc.wantErr)
		}
	}
}

// F7: Env key length boundary (keyRe cap at 128 chars).
func TestValidate_EnvHeaderKeyLengthBoundary(t *testing.T) {
	cases := []struct {
		keyName string
		wantErr bool
	}{
		{strings.Repeat("K", 128), false},
		{strings.Repeat("K", 129), true},
	}
	for _, tc := range cases {
		err := Validate(&Server{
			Transport: TransportStdio, Name: "ok", Command: "bash",
			Env: []KeyPair{{Name: tc.keyName, Value: "v"}},
		})
		gotErr := err != nil
		if gotErr != tc.wantErr {
			t.Errorf("Validate(env key len=%d) err = %v, wantErr = %v",
				len(tc.keyName), err, tc.wantErr)
		}
	}
}

// F8: URL shape edge cases.
func TestValidate_RemoteURLShapes(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"empty", "", true},
		{"scheme_only", "https://", true},
		{"schemeless_double_slash", "//example.com/mcp", true},
		{"uppercase_scheme", "HTTPS://x.example/mcp", false},
		{"with_port_and_path", "https://x.example:8443/mcp?k=v", false},
		{"valid_http", "http://x.example/mcp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&Server{
				Transport: TransportHTTP, Name: "ok", URL: tc.url,
			})
			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Errorf("Validate(URL=%q) err = %v, wantErr = %v", tc.url, err, tc.wantErr)
			}
		})
	}
}

// Regression: URLs with userinfo must be rejected so tokens never leak
// through the masking boundary. Ops-mcp-005.
func TestValidate_RejectsURLUserinfo(t *testing.T) {
	cases := []string{
		"https://token@mcp.example.com/v1",
		"https://user:pass@mcp.example.com/v1",
	}
	for _, u := range cases {
		err := Validate(&Server{Transport: TransportHTTP, Name: "x", URL: u})
		if err == nil {
			t.Errorf("Validate(URL=%q) = nil, want userinfo error", u)
		}
	}
}

// Regression: Command must reject control characters. Sec-u11c1-02.
func TestValidate_CommandRejectsControlChars(t *testing.T) {
	for _, c := range []string{"bash\nrogue", "bash\rfoo", "bash\x00"} {
		err := Validate(&Server{Transport: TransportStdio, Name: "x", Command: c})
		if err == nil {
			t.Errorf("Validate(Command=%q) = nil, want control-char error", c)
		}
	}
}

// Regression: Env value length cap.
func TestValidate_EnvValueLengthCap(t *testing.T) {
	big := strings.Repeat("x", envValueMax+1)
	err := Validate(&Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
		Env: []KeyPair{{Name: "K", Value: big}},
	})
	if err == nil {
		t.Error("Validate accepted oversized env value")
	}
}

// Regression: Duplicate env names must be rejected (closes the ambiguity
// between mergeSecrets's map-based lookup and the KeyPair ordered
// contract). Q11 / root-cause fix for Q6.
func TestValidate_RejectsDuplicateEnvNames(t *testing.T) {
	err := Validate(&Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
		Env: []KeyPair{
			{Name: "FOO", Value: "a"},
			{Name: "FOO", Value: "b"},
		},
	})
	if err == nil {
		t.Error("Validate accepted duplicate env name")
	}
}

// Regression: Duplicate header names (case-insensitive) must be rejected.
func TestValidate_RejectsDuplicateHeaderNames(t *testing.T) {
	err := Validate(&Server{
		Transport: TransportHTTP, Name: "x", URL: "https://x.example",
		Headers: []KeyPair{
			{Name: "Authorization", Value: "Bearer a"},
			{Name: "authorization", Value: "Bearer b"},
		},
	})
	if err == nil {
		t.Error("Validate accepted duplicate header name (case-insensitive)")
	}
}

// Regression: DisabledTools entries must reject control chars and respect
// length caps. Sec-u11c1-03.
func TestValidate_DisabledToolsRejectsBadEntries(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
	}{
		{"control_char", []string{"ok\nbad"}},
		{"too_long", []string{strings.Repeat("x", disabledToolMax+1)}},
		{"too_many", make([]string, maxDisabledTools+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&Server{
				Transport: TransportStdio, Name: "x", Command: "bash",
				DisabledTools: tc.tools,
			})
			if err == nil {
				t.Errorf("Validate accepted bad DisabledTools (%s)", tc.name)
			}
		})
	}
}

// F1 (test-review u12c1): nine length-cap + cross-transport-field
// error branches that were 0% covered. Each asserts on the concrete
// error substring so a reword that weakens the check (e.g.
// "too long" → "invalid") would still be caught by the outer
// len-error assertion.
func TestValidate_LengthAndCrossTransportErrorBranches(t *testing.T) {
	cases := []struct {
		srv        *Server
		name       string
		wantSubstr string
	}{
		{
			name:       "transport_empty",
			srv:        &Server{Transport: "", Name: "ok", Command: "bash"},
			wantSubstr: "transport required",
		},
		{
			name: "stdio_command_too_long",
			srv: &Server{
				Transport: TransportStdio, Name: "ok",
				Command: strings.Repeat("x", commandMax+1),
			},
			wantSubstr: "command too long",
		},
		{
			name: "stdio_with_url_rejected",
			srv: &Server{
				Transport: TransportStdio, Name: "ok", Command: "bash",
				URL: "https://leaked.example",
			},
			wantSubstr: "stdio transport cannot have url",
		},
		{
			name: "stdio_with_headers_rejected",
			srv: &Server{
				Transport: TransportStdio, Name: "ok", Command: "bash",
				Headers: []KeyPair{{Name: "X-Foo", Value: "bar"}},
			},
			wantSubstr: "stdio transport cannot have url or headers",
		},
		{
			name: "stdio_too_many_args",
			srv: &Server{
				Transport: TransportStdio, Name: "ok", Command: "bash",
				Args: make([]string, maxArgs+1),
			},
			wantSubstr: "args: too many entries",
		},
		{
			name: "stdio_arg_too_long",
			srv: &Server{
				Transport: TransportStdio, Name: "ok", Command: "bash",
				Args: []string{strings.Repeat("x", argMax+1)},
			},
			wantSubstr: "args[0] too long",
		},
		{
			name: "remote_with_args_rejected",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok", URL: "https://x.example",
				Args: []string{"leaked"},
			},
			wantSubstr: "remote transport cannot have command, args or env",
		},
		{
			name: "remote_with_env_rejected",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok", URL: "https://x.example",
				Env: []KeyPair{{Name: "LEAK", Value: "v"}},
			},
			wantSubstr: "remote transport cannot have command, args or env",
		},
		{
			name: "remote_url_too_long",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok",
				URL: "https://x.example/" + strings.Repeat("a", urlMax),
			},
			wantSubstr: "url too long",
		},
		{
			name: "remote_url_control_char",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok",
				URL: "https://x.example/\npath",
			},
			wantSubstr: "url contains a control character",
		},
		{
			name: "remote_header_value_too_long",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok", URL: "https://x.example",
				Headers: []KeyPair{{
					Name:  "Authorization",
					Value: strings.Repeat("x", headerValueMax+1),
				}},
			},
			wantSubstr: "headers[0]: value too long",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.srv)
			if err == nil {
				t.Fatalf("Validate(%s) = nil, want error containing %q",
					tc.name, tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("Validate(%s) = %q, want substring %q",
					tc.name, err.Error(), tc.wantSubstr)
			}
		})
	}
}

// u12c2-f2: remaining error branches in validate that cycle 1's F1
// table did not cover. Each exercises a distinct production-code
// line that coverage showed at 0% after cycle 1.
func TestValidate_MoreErrorBranches(t *testing.T) {
	// Helpers build N unique KeyPairs so the length cap fires before
	// the duplicate-name detection inside the shared helper.
	envN := func(n int) []KeyPair {
		out := make([]KeyPair, n)
		for i := range out {
			out[i] = KeyPair{Name: fmt.Sprintf("K%d", i), Value: "v"}
		}
		return out
	}
	headerN := func(n int) []KeyPair {
		out := make([]KeyPair, n)
		for i := range out {
			out[i] = KeyPair{Name: fmt.Sprintf("H%d", i), Value: "v"}
		}
		return out
	}

	cases := []struct {
		srv        *Server
		name       string
		wantSubstr string
	}{
		{
			name: "stdio_env_too_many_entries",
			srv: &Server{
				Transport: TransportStdio, Name: "ok", Command: "bash",
				Env: envN(maxEnvEntries + 1),
			},
			wantSubstr: "env: too many entries",
		},
		{
			name: "stdio_env_value_control_char",
			srv: &Server{
				Transport: TransportStdio, Name: "ok", Command: "bash",
				Env: []KeyPair{{Name: "K", Value: "line1\nline2"}},
			},
			wantSubstr: "env[0]: value contains a control character",
		},
		{
			name: "remote_wrong_scheme",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok",
				URL: "ftp://host.example/path",
			},
			wantSubstr: "url scheme must be http or https",
		},
		{
			name: "remote_headers_too_many_entries",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok",
				URL:     "https://x.example",
				Headers: headerN(maxHeaderEntries + 1),
			},
			wantSubstr: "headers: too many entries",
		},
		{
			name: "remote_header_bad_name",
			srv: &Server{
				Transport: TransportHTTP, Name: "ok",
				URL:     "https://x.example",
				Headers: []KeyPair{{Name: "bad header!", Value: "v"}},
			},
			wantSubstr: "headers[0]: bad name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.srv)
			if err == nil {
				t.Fatalf("Validate(%s) = nil, want error containing %q",
					tc.name, tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("Validate(%s) = %q, want substring %q",
					tc.name, err.Error(), tc.wantSubstr)
			}
		})
	}
}

// FuzzValidate exercises Validate with random Server structs. Asserts:
// (1) Validate never panics, (2) if Validate returns nil the Server
// satisfies basic invariants (name matches nameRe, transport is known,
// transport-specific fields are consistent), (3) idempotent — a second
// Validate call on the same struct returns the same result.
func FuzzValidate(f *testing.F) {
	// Seed corpus with representative valid/invalid inputs.
	f.Add("ok", "stdio", "bash", "", "arg1", "KEY", "val", "", "", false)
	f.Add("srv", "http", "", "https://x.example/mcp", "", "Authorization", "Bearer t", "X-Foo", "bar", true)
	f.Add("srv", "sse", "", "https://x.example/sse", "", "", "", "", "", false)
	f.Add("", "stdio", "bash", "", "", "", "", "", "", false)
	f.Add("ok", "grpc", "", "", "", "", "", "", "", false)
	f.Add("ok", "stdio", "", "", "", "", "", "", "", false)
	f.Add("ok", "http", "", "ftp://bad", "", "", "", "", "", false)

	f.Fuzz(func(t *testing.T, name, transport, command, rawURL, arg, envKey, envVal, hdrKey, hdrVal string, prewarm bool) {
		s := &Server{
			Name:      name,
			Transport: Transport(transport),
			Command:   command,
			URL:       rawURL,
			Prewarm:   prewarm,
			Enabled:   true,
		}
		if arg != "" {
			s.Args = []string{arg}
		}
		if envKey != "" {
			s.Env = []KeyPair{{Name: envKey, Value: envVal}}
		}
		if hdrKey != "" {
			s.Headers = []KeyPair{{Name: hdrKey, Value: hdrVal}}
		}

		err1 := Validate(s)

		// Invariant: if Validate accepts, basic structural properties hold.
		if err1 == nil {
			if !nameRe.MatchString(s.Name) {
				t.Fatal("Validate returned nil but name does not match nameRe")
			}
			if _, ok := transportValidators[s.Transport]; !ok {
				t.Fatal("Validate returned nil but transport is unknown")
			}
			switch s.Transport {
			case TransportStdio:
				if strings.TrimSpace(s.Command) == "" {
					t.Fatal("Validate returned nil for stdio but command is empty")
				}
				if s.URL != "" || len(s.Headers) > 0 {
					t.Fatal("Validate returned nil for stdio but url/headers set")
				}
			case TransportHTTP, TransportSSE:
				if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 {
					t.Fatal("Validate returned nil for remote but command/args/env set")
				}
			}
		}

		// Idempotency: calling Validate again on the same struct yields
		// the same pass/fail outcome.
		err2 := Validate(s)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("Validate not idempotent: first=%v second=%v", err1, err2)
		}
	})
}

// TestValidate_AcceptsValuesAtCap pins the upper boundary of every
// length/count cap Validate enforces: a value of exactly the cap must be
// accepted (the checks are len > cap, not >=). A boundary mutation
// (> to >=) on any of these would reject the at-cap value.
func TestValidate_AcceptsValuesAtCap(t *testing.T) {
	const urlPrefix = "https://x.example/"
	urlAtMax := urlPrefix + strings.Repeat("a", urlMax-len(urlPrefix))

	cases := []struct {
		srv  *Server
		name string
	}{
		{
			name: "disabled_tools_at_max_count",
			srv:  &Server{Transport: TransportStdio, Name: "ok", Command: "bash", DisabledTools: make([]string, maxDisabledTools)},
		},
		{
			name: "disabled_tool_at_max_len",
			srv:  &Server{Transport: TransportStdio, Name: "ok", Command: "bash", DisabledTools: []string{strings.Repeat("a", disabledToolMax)}},
		},
		{
			name: "command_at_max_len",
			srv:  &Server{Transport: TransportStdio, Name: "ok", Command: strings.Repeat("a", commandMax)},
		},
		{
			name: "args_at_max_count",
			srv:  &Server{Transport: TransportStdio, Name: "ok", Command: "bash", Args: make([]string, maxArgs)},
		},
		{
			name: "arg_at_max_len",
			srv:  &Server{Transport: TransportStdio, Name: "ok", Command: "bash", Args: []string{strings.Repeat("a", argMax)}},
		},
		{
			name: "url_at_max_len",
			srv:  &Server{Transport: TransportHTTP, Name: "ok", URL: urlAtMax},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.srv); err != nil {
				t.Errorf("Validate(%s) = %v, want nil (at-cap value must be accepted)", tc.name, err)
			}
		})
	}
}

// TestValidate_OAuthClientIDLengthBoundary pins the oauth_client_id cap
// in validateRemote: a client id of exactly oauthClientIDMax bytes is
// accepted and one byte over is rejected with "oauth_client_id too long".
func TestValidate_OAuthClientIDLengthBoundary(t *testing.T) {
	atMax := &Server{
		Transport:     TransportHTTP,
		Name:          "ok",
		URL:           "https://x.example/mcp",
		OAuthClientID: strings.Repeat("a", oauthClientIDMax),
	}
	if err := Validate(atMax); err != nil {
		t.Errorf("Validate(OAuthClientID len=%d) = %v, want nil", oauthClientIDMax, err)
	}

	over := &Server{
		Transport:     TransportHTTP,
		Name:          "ok",
		URL:           "https://x.example/mcp",
		OAuthClientID: strings.Repeat("a", oauthClientIDMax+1),
	}
	err := Validate(over)
	if err == nil {
		t.Fatalf("Validate(OAuthClientID len=%d) = nil, want too-long error", oauthClientIDMax+1)
	}
	if !strings.Contains(err.Error(), "oauth_client_id too long") {
		t.Errorf("Validate(OAuthClientID over cap) = %q, want substring %q",
			err.Error(), "oauth_client_id too long")
	}
}

// TestValidateKeyPairs_entryCountBoundary pins the entry-count cap in
// validateKeyPairs: exactly maxEntries pairs are accepted and one over
// is rejected with "too many entries". Called directly so the boundary
// is pinned by the maxEntries parameter, not a package-level cap.
func TestValidateKeyPairs_entryCountBoundary(t *testing.T) {
	const maxEntries = 2
	const maxValue = 1024

	atMax := []KeyPair{{Name: "Ga", Value: "v"}, {Name: "Gb", Value: "v"}}
	if err := validateKeyPairs("env", atMax, maxEntries, maxValue, false); err != nil {
		t.Errorf("validateKeyPairs(%d pairs, max %d) = %v, want nil", len(atMax), maxEntries, err)
	}

	over := []KeyPair{{Name: "Ga", Value: "v"}, {Name: "Gb", Value: "v"}, {Name: "Gc", Value: "v"}}
	err := validateKeyPairs("env", over, maxEntries, maxValue, false)
	if err == nil {
		t.Fatalf("validateKeyPairs(%d pairs, max %d) = nil, want too-many-entries error",
			len(over), maxEntries)
	}
	if !strings.Contains(err.Error(), "too many entries") {
		t.Errorf("validateKeyPairs(%d pairs) = %q, want substring %q",
			len(over), err.Error(), "too many entries")
	}
}

// TestValidateKeyPairs_valueLengthBoundary pins the per-value length cap
// in validateKeyPairs: a value of exactly maxValue bytes is accepted and
// one byte over is rejected with "value too long". maxEntries is kept
// above the pair count so the entry-count check passes first.
func TestValidateKeyPairs_valueLengthBoundary(t *testing.T) {
	const maxEntries = 8
	const maxValue = 4

	atMax := []KeyPair{{Name: "Gk", Value: strings.Repeat("v", maxValue)}}
	if err := validateKeyPairs("env", atMax, maxEntries, maxValue, false); err != nil {
		t.Errorf("validateKeyPairs(value len=%d, max %d) = %v, want nil", maxValue, maxValue, err)
	}

	over := []KeyPair{{Name: "Gk", Value: strings.Repeat("v", maxValue+1)}}
	err := validateKeyPairs("env", over, maxEntries, maxValue, false)
	if err == nil {
		t.Fatalf("validateKeyPairs(value len=%d, max %d) = nil, want value-too-long error",
			maxValue+1, maxValue)
	}
	if !strings.Contains(err.Error(), "value too long") {
		t.Errorf("validateKeyPairs(value len=%d) = %q, want substring %q",
			maxValue+1, err.Error(), "value too long")
	}
}
