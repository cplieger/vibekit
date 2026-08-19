package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
)

// drainOne reads a single message from urlCh with a sensible budget so the
// test never blocks on a buggy parser.
func drainOne(t *testing.T, ch <-chan map[string]string) map[string]string {
	t.Helper()
	select {
	case m := <-ch:
		return m
	default:
		t.Fatal("urlCh empty after scanLoginOutput returned")
		return nil
	}
}

// writeFakeCLI writes an executable /bin/sh script to t.TempDir that
// emits the given stdout and exits with the given code. Returns the
// absolute path usable as NewHandler's cliPath. Unix-only; callers
// must t.Skip on Windows.
func writeFakeCLI(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-kiro-cli")
	// Write stdout content to a sibling file and cat it from the
	// script. This avoids all quoting/escaping issues with printf
	// and special characters in the output.
	dataPath := filepath.Join(dir, "stdout-data")
	if err := os.WriteFile(dataPath, []byte(stdout), 0o644); err != nil {
		t.Fatalf("writeFakeCLI data: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\ncat %q\nexit %d\n", dataPath, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writeFakeCLI: %v", err)
	}
	return path
}

// writeFakeCLIScript writes an executable /bin/sh script with the
// given body (shebang added automatically) to t.TempDir and returns
// its absolute path. Prefer writeFakeCLI when the script is just
// `cat + exit`; use this variant for hang/sleep/multi-line shapes.
// Unix-only; callers must t.Skip on Windows.
func writeFakeCLIScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-kiro-cli")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writeFakeCLIScript: %v", err)
	}
	return path
}

// skipIfNotUnix skips the test on Windows. Every subprocess / signal
// helper in this package is unix-only (process groups, /bin/sh fake
// CLI, loginKill). Factored so the skip reason doesn't drift across
// the 14 tests that need this gate.
func skipIfNotUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("auth subprocess helpers are unix-only")
	}
}

// ---------------------------------------------------------------------------
// scanLoginOutput tests (pre-review, preserved)
// ---------------------------------------------------------------------------

// fakeErrReader returns its canned error on the first Read so
// bufio.Scanner surfaces it via scanner.Err() without producing
// any lines.
type fakeErrReader struct{ err error }

func (f *fakeErrReader) Read([]byte) (int, error) { return 0, f.err }

func TestScanLoginOutput(t *testing.T) {
	// buildLineCap generates maxLoginLines+5 lines of noise to trigger the cap.
	buildLineCap := func() string {
		var b strings.Builder
		for i := range maxLoginLines + 5 {
			fmt.Fprintf(&b, "noise line %d\n", i)
		}
		return b.String()
	}
	// buildLongLines generates maxLoginLines+5 lines of 200-byte 'x' runs
	// to exercise the per-line cap inside lineRing.
	buildLongLines := func() string {
		longLine := strings.Repeat("x", 200)
		var b strings.Builder
		for range maxLoginLines + 5 {
			b.WriteString(longLine)
			b.WriteByte('\n')
		}
		return b.String()
	}

	tests := []struct {
		name       string
		input      string
		buildInput func() string // used instead of input when non-nil
		wantURL    string
		wantCode   string
		wantError  string
		errContain []string // if non-nil, error must contain each substring
	}{
		{
			name:     "CodeAndURL",
			input:    "Code: ABCD-1234\nOpen this URL: https://example.com/auth\n",
			wantURL:  "https://example.com/auth",
			wantCode: "ABCD-1234",
		},
		{
			name:    "BareHTTPSToken",
			input:   "Please visit https://example.com/verify?token=xyz now\n",
			wantURL: "https://example.com/verify?token=xyz",
		},
		{
			name:     "StripsANSI",
			input:    "\x1b[1mCode:\x1b[0m ZZZZ\n\x1b[32mOpen this URL:\x1b[0m https://example.com/path\n",
			wantURL:  "https://example.com/path",
			wantCode: "ZZZZ",
		},
		{
			name:     "CodeBeforeURL",
			input:    "Starting login...\nConnecting to identity provider\nCode: 4242\nExtra noise\nOpen this URL: https://idp.example.com/\n",
			wantURL:  "https://idp.example.com/",
			wantCode: "4242",
		},
		{
			name:      "NoURL",
			input:     "Some boring output\nNo link here\nJust text\n",
			wantError: "non-empty",
		},
		{
			name:      "EmptyInput",
			input:     "",
			wantError: "non-empty",
		},
		{
			name:    "ExplicitPrefixWinsOverBareToken",
			input:   "Open this URL: https://primary.example.com/\nAlso visit https://secondary.example.com/\n",
			wantURL: "https://primary.example.com/",
		},
		{
			name:    "URLEmbeddedWithTrailingText",
			input:   "Go to https://example.com/path then come back\n",
			wantURL: "https://example.com/path",
		},
		{
			name:       "LineCap",
			buildInput: buildLineCap,
			wantError:  "CLI produced too much output without auth URL",
		},
		{
			name:       "LineCapTruncatesLongLines",
			buildInput: buildLongLines,
			wantError:  "CLI produced too much output without auth URL",
		},
		{
			// Boundary: exactly maxLoginLines URL-free lines must trip
			// the line cap (the cap is `lineCount >= maxLoginLines`),
			// distinct from the maxLoginLines+5 LineCap case above.
			name: "LineCapBoundaryExactlyMax",
			buildInput: func() string {
				var b strings.Builder
				for i := range maxLoginLines {
					fmt.Fprintf(&b, "noise line %d\n", i)
				}
				return b.String()
			},
			wantError: "CLI produced too much output without auth URL",
		},
		{
			name:      "AlreadyLoggedIn",
			input:     "Error: already logged in; run `kiro-cli logout` first\n",
			wantError: "already_logged_in",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.input
			if tc.buildInput != nil {
				in = tc.buildInput()
			}
			ch := make(chan map[string]string, 1)
			scanLoginOutput(strings.NewReader(in), ch)
			got := drainOne(t, ch)

			if tc.wantURL != "" {
				if got["url"] != tc.wantURL {
					t.Errorf("url = %q, want %q", got["url"], tc.wantURL)
				}
			} else {
				if got["url"] != "" {
					t.Errorf("url = %q, want empty", got["url"])
				}
			}
			if tc.wantCode != "" {
				if got["code"] != tc.wantCode {
					t.Errorf("code = %q, want %q", got["code"], tc.wantCode)
				}
			}
			if tc.wantError == "non-empty" {
				if got["error"] == "" {
					t.Errorf("expected error field, got %v", got)
				}
			} else if tc.wantError != "" {
				if got["error"] != tc.wantError {
					t.Errorf("error = %q, want %q", got["error"], tc.wantError)
				}
			}
			for _, sub := range tc.errContain {
				if !strings.Contains(got["error"], sub) {
					t.Errorf("error = %q, want to contain %q", got["error"], sub)
				}
			}
		})
	}

	// ReaderError requires a custom io.Reader, tested separately.
	t.Run("ReaderError", func(t *testing.T) {
		ch := make(chan map[string]string, 1)
		scanLoginOutput(&fakeErrReader{err: errors.New("pipe exploded")}, ch)
		got := drainOne(t, ch)
		if got["error"] == "" {
			t.Fatalf("scanLoginOutput(errReader) = %v, want error key", got)
		}
		if !strings.Contains(got["error"], "scanner error") {
			t.Errorf("error = %q, want scanner-error sentinel", got["error"])
		}
		if !strings.Contains(got["error"], "pipe exploded") {
			t.Errorf("error = %q, want to include underlying cause", got["error"])
		}
		if got["url"] != "" {
			t.Errorf("url = %q, want empty on scanner error", got["url"])
		}
	})

	// AlreadyLoggedIn_CaseInsensitive exercises case-insensitive matching.
	t.Run("AlreadyLoggedIn_CaseInsensitive", func(t *testing.T) {
		inputs := []string{
			"Already Logged In as foo@example.com\n",
			"ALREADY LOGGED IN as foo@example.com\n",
			"error: Already logged in\n",
		}
		for _, in := range inputs {
			t.Run(in, func(t *testing.T) {
				ch := make(chan map[string]string, 1)
				scanLoginOutput(strings.NewReader(in), ch)
				got := drainOne(t, ch)
				if got["error"] != "already_logged_in" {
					t.Errorf("scanLoginOutput(%q) error = %q, want %q",
						in, got["error"], "already_logged_in")
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// humanizeAccountType tests (cycle 1 t-1)
// ---------------------------------------------------------------------------

func TestHumanizeAccountType(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"BuilderId", "Logged in with Builder ID"},
		{"builderid", "Logged in with Builder ID"},
		{"BUILDERID", "Logged in with Builder ID"},
		{"IdentityCenter", "Logged in with IAM Identity Center"},
		{"identitycenter", "Logged in with IAM Identity Center"},
		{"IAMIdentityCenter", "Logged in with IAM Identity Center"},
		{"iamidentitycenter", "Logged in with IAM Identity Center"},
		{"Social", "Logged in with social login"},
		{"social", "Logged in with social login"},
		{"Unknown", "Logged in with Unknown"},
		{"custom-provider", "Logged in with custom-provider"},
		{"", "Logged in with "},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := humanizeAccountType(tc.in)
			if got != tc.want {
				t.Errorf("humanizeAccountType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// whoamiInfo tests (cycle 1 t-2)
// ---------------------------------------------------------------------------

func TestWhoamiInfo(t *testing.T) {
	tests := []struct {
		check   func(t *testing.T, got *WhoamiResponse)
		name    string
		in      string
		wantErr bool
	}{
		{
			name: "BuilderId normalises account_type to auth",
			in:   `{"account_type":"BuilderId","email":"a@b.com","start_url":"https://view.awsapps.com/start","region":"us-east-1"}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Auth != "Logged in with Builder ID" {
					t.Errorf("Auth = %q, want %q", got.Auth, "Logged in with Builder ID")
				}
				if got.Email != "a@b.com" {
					t.Errorf("Email = %q, want preserved", got.Email)
				}
				if got.Region != "us-east-1" {
					t.Errorf("Region = %q, want preserved", got.Region)
				}
				if got.StartURL != "https://view.awsapps.com/start" {
					t.Errorf("StartURL = %q, want preserved (snake_case input)", got.StartURL)
				}
			},
		},
		{
			name: "IdentityCenter drops extra profile fields",
			// Per AUTH-01: arbitrary kiro-cli fields not on the
			// WhoamiResponse struct are dropped at the wire
			// boundary. profile and account_id were preserved
			// verbatim under the old map[string]any return; the
			// typed struct narrows the surface intentionally.
			in: `{"account_type":"IdentityCenter","email":"u@example.com","profile":"admin","account_id":"123"}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Auth != "Logged in with IAM Identity Center" {
					t.Errorf("Auth = %q", got.Auth)
				}
				if got.Email != "u@example.com" {
					t.Errorf("Email = %q", got.Email)
				}
				if got.AccountType != "IdentityCenter" {
					t.Errorf("AccountType = %q, want preserved verbatim", got.AccountType)
				}
				// Marshalled output must not carry profile or
				// account_id (locked-down wire surface).
				b, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(b, []byte(`"profile"`)) ||
					bytes.Contains(b, []byte(`"account_id"`)) {
					t.Errorf("WhoamiResponse marshal leaked extra fields: %s", b)
				}
			},
		},
		{
			name: "missing account_type leaves Auth unset",
			in:   `{"email":"x@y.com"}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Auth != "" {
					t.Errorf("Auth = %q, want empty without account_type", got.Auth)
				}
				if got.Email != "x@y.com" {
					t.Errorf("Email = %q", got.Email)
				}
			},
		},
		{
			name: "empty account_type is ignored",
			in:   `{"account_type":"","email":"x@y.com"}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Auth != "" {
					t.Errorf("Auth = %q, want empty for empty account_type", got.Auth)
				}
				if got.AccountType != "" {
					t.Errorf("AccountType = %q, want empty", got.AccountType)
				}
			},
		},
		{
			name: "non-string account_type is ignored",
			in:   `{"account_type":42,"email":"x@y.com"}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Auth != "" {
					t.Errorf("Auth = %q, want empty for non-string account_type", got.Auth)
				}
			},
		},
		{
			name: "null json object becomes zero-valued WhoamiResponse",
			in:   `null`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got == nil {
					t.Fatal("got nil pointer, want zero-valued WhoamiResponse")
				}
				if (*got != WhoamiResponse{}) {
					t.Errorf("WhoamiResponse = %+v, want zero value", got)
				}
			},
		},
		{
			name: "empty json object stays empty",
			in:   `{}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if (*got != WhoamiResponse{}) {
					t.Errorf("WhoamiResponse = %+v, want zero value", got)
				}
			},
		},
		{
			name:    "malformed json returns error",
			in:      `not-json`,
			wantErr: true,
		},
		{
			name:    "empty bytes returns error",
			in:      ``,
			wantErr: true,
		},
		{
			name: "kiro-cli 2.0.1: camelCase accountType",
			in:   `{"accountType":"IamIdentityCenter","email":"u@example.com","region":"us-east-1","startUrl":"https://view.awsapps.com/start"}`,
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Auth != "Logged in with IAM Identity Center" {
					t.Errorf("Auth = %q, want IAM Identity Center", got.Auth)
				}
				if got.Email != "u@example.com" {
					t.Errorf("Email = %q", got.Email)
				}
				if got.StartURL != "https://view.awsapps.com/start" {
					t.Errorf("StartURL = %q", got.StartURL)
				}
				if got.AccountType != "IamIdentityCenter" {
					t.Errorf("AccountType = %q", got.AccountType)
				}
			},
		},
		{
			name: "kiro-cli 2.0.1: trailing non-JSON footer is ignored",
			// This is the exact shape the runtime container returned
			// on 2026-04-23: JSON followed by a Profile: / ARN
			// banner. Before json.Decoder, the whole buffer was
			// passed to json.Unmarshal and failed on the `P`.
			in: "{\"accountType\":\"IamIdentityCenter\"," +
				"\"email\":\"u@example.com\"," +
				"\"region\":\"us-east-1\"," +
				"\"startUrl\":\"https://view.awsapps.com/start\"}\n" +
				"Profile:\nKiroProfile-us-east-1\n" +
				"arn:aws:codewhisperer:us-east-1:123:profile/ABC\n",
			check: func(t *testing.T, got *WhoamiResponse) {
				if got.Email != "u@example.com" {
					t.Errorf("Email = %q, want u@example.com (trailing footer should be ignored)", got.Email)
				}
				if got.Auth != "Logged in with IAM Identity Center" {
					t.Errorf("Auth = %q", got.Auth)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := whoamiInfo([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Errorf("whoamiInfo(%q) err = nil, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("whoamiInfo(%q) err = %v, want nil", tc.in, err)
			}
			tc.check(t, got)
		})
	}
}

// ---------------------------------------------------------------------------
// validateProvider / validateRegion tests (cycle 1 security findings)
// ---------------------------------------------------------------------------

func TestValidateProvider(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is allowed", "", false},
		{"valid https URL", "https://view.awsapps.com/start", false},
		{"http rejected", "http://evil.com", true},
		{"bare host rejected", "evil.com", true},
		{"flag smuggling rejected", "--identity-provider", true},
		{"over length rejected", "https://example.com/" + strings.Repeat("a", maxProviderLen), true},
		{"userinfo rejected", "https://u:[email protected]/", true},
		{"empty-user userinfo rejected", "https://@evil.com/", true},
		{"boundary length exactly max accepted", "https://example.com/" + strings.Repeat("a", maxProviderLen-len("https://example.com/")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProvider(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateProvider(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateProvider(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestValidateRegion(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is allowed", "", false},
		{"us-east-1", "us-east-1", false},
		{"eu-west-2", "eu-west-2", false},
		{"ap-southeast-1", "ap-southeast-1", false},
		{"cn-north-1 (China)", "cn-north-1", false},
		{"us-gov-west-1 (GovCloud)", "us-gov-west-1", false},
		{"us-iso-east-1 (ISO)", "us-iso-east-1", false},
		{"us-isob-east-1 (ISO-B)", "us-isob-east-1", false},
		{"eu-isoe-west-1 (ISO-E)", "eu-isoe-west-1", false},
		{"flag smuggling rejected", "--help", true},
		{"uppercase rejected", "US-EAST-1", true},
		{"over length rejected", strings.Repeat("a", maxRegionLen+1), true},
		{"empty interior segment rejected", "us--east-1", true},
		{"boundary length exactly max accepted", "aa-" + strings.Repeat("b", maxRegionLen-5) + "-1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRegion(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateRegion(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateRegion(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleWhoami tests (cycle 1 t-3, fake-CLI harness)
// ---------------------------------------------------------------------------

func TestHandleWhoami_Success(t *testing.T) {
	skipIfNotUnix(t)

	cli := writeFakeCLI(t, `{"account_type":"BuilderId","email":"u@example.com"}`, 0)
	h := NewHandler(fixedPath(cli))

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	rr := httptest.NewRecorder()
	h.handleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, rr.Body.String())
	}
	if body["email"] != "u@example.com" {
		t.Errorf("email = %v, want u@example.com", body["email"])
	}
	if body["auth"] != "Logged in with Builder ID" {
		t.Errorf("auth = %v, want humanized label", body["auth"])
	}
}

func TestHandleWhoami_CLIFails(t *testing.T) {
	skipIfNotUnix(t)

	cli := writeFakeCLI(t, "", 1)
	h := NewHandler(fixedPath(cli))

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	rr := httptest.NewRecorder()
	h.handleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-soft banner)", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, rr.Body.String())
	}
	if body["error"] != "whoami unavailable" {
		t.Errorf("error = %v, want generic sentinel", body["error"])
	}
}

func TestHandleWhoami_MalformedJSON(t *testing.T) {
	skipIfNotUnix(t)

	cli := writeFakeCLI(t, "not-json-garbage", 0)
	h := NewHandler(fixedPath(cli))

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	rr := httptest.NewRecorder()
	h.handleWhoami(rr, req)

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "whoami unavailable" {
		t.Errorf("error = %v, want generic sentinel", body["error"])
	}
}

func TestHandleWhoami_RejectsNonGET(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist"))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/whoami", nil)
			rr := httptest.NewRecorder()
			h.handleWhoami(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("handleWhoami(%s) status = %d, want 405", method, rr.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleLogin tests (cycle 1 t-4 method gates + cycle 2 happy paths)
// ---------------------------------------------------------------------------

func TestHandleLogin_RejectsNonPOST(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist-will-not-be-called"))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/login", nil)
			rr := httptest.NewRecorder()
			h.handleLogin(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("handleLogin(%s) status = %d, want 405", method, rr.Code)
			}
		})
	}
}

func TestHandleLogin_SuccessWithProviderAndRegion(t *testing.T) {
	skipIfNotUnix(t)
	cli := writeFakeCLI(t,
		"Code: WXYZ-9999\nOpen this URL: https://idp.example.com/auth\n",
		0)
	h := NewHandler(fixedPath(cli))

	body := `{"provider":"https://view.awsapps.com/start","region":"us-east-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handleLogin success status = %d, want 200; body=%s",
			rr.Code, rr.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("handleLogin response not JSON: %v; body=%s", err, rr.Body.String())
	}
	if got["url"] != "https://idp.example.com/auth" {
		t.Errorf("url = %q, want %q", got["url"], "https://idp.example.com/auth")
	}
	if got["code"] != "WXYZ-9999" {
		t.Errorf("code = %q, want %q", got["code"], "WXYZ-9999")
	}
}

func TestHandleLogin_SuccessWithEmptyProviderAndRegion(t *testing.T) {
	skipIfNotUnix(t)
	cli := writeFakeCLI(t,
		"Open this URL: https://builder.example.com/\n", 0)
	h := NewHandler(fixedPath(cli))

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handleLogin status = %d, want 200", rr.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if got["url"] != "https://builder.example.com/" {
		t.Errorf("url = %q, want builder URL", got["url"])
	}
	if got["code"] != "" {
		t.Errorf("code = %q, want empty (no Code: line)", got["code"])
	}
}

func TestHandleLogin_MalformedBody(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist"))
	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{invalid json`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("handleLogin malformed body status = %d, want 400", rr.Code)
	}
}

func TestHandleLogin_InvalidProvider(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist"))
	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"provider":"http://evil.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("handleLogin invalid provider status = %d, want 400", rr.Code)
	}
}

func TestHandleLogin_InvalidRegion(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist"))
	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"region":"--help"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("handleLogin invalid region status = %d, want 400", rr.Code)
	}
}

func TestHandleLogin_TimesOutWhenCLIProducesNoURL(t *testing.T) {
	skipIfNotUnix(t)
	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: 50 * time.Millisecond,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   DefaultConfig.LogoutTimeout,
		WhoamiTimeout:   DefaultConfig.WhoamiTimeout,
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("handleLogin timeout status = %d, want 504; body=%s",
			rr.Code, rr.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if !strings.Contains(got["error"], "timeout") {
		t.Errorf("error = %q, want to contain 'timeout'", got["error"])
	}
}

// ---------------------------------------------------------------------------
// HandleLogout tests (cycle 1 t-4 method gate + t-5 happy/error paths)
// ---------------------------------------------------------------------------

func TestHandleLogout_RejectsNonPOST(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist-will-not-be-called"))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/logout", nil)
			rr := httptest.NewRecorder()
			h.handleLogout(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("handleLogout(%s) status = %d, want 405", method, rr.Code)
			}
		})
	}
}

func TestHandleLogout_Success(t *testing.T) {
	skipIfNotUnix(t)

	cli := writeFakeCLI(t, "\x1b[32mLogged out\x1b[0m\n", 0)
	h := NewHandler(fixedPath(cli))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rr := httptest.NewRecorder()
	h.handleLogout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, rr.Body.String())
	}
	if strings.Contains(body["output"], "\x1b[") {
		t.Errorf("output contains ANSI escape: %q", body["output"])
	}
	if !strings.Contains(body["output"], "Logged out") {
		t.Errorf("output = %q, want to contain %q", body["output"], "Logged out")
	}
	if _, has := body["error"]; has {
		t.Errorf("unexpected error key on success: %v", body)
	}
}

// TestHandleLogout_CLIFails moved to TestHandleLogout_CLIFailsReturnsGenericSentinel
// (below) with a stricter sentinel + guardrail against err.Error() leakage.

// ---------------------------------------------------------------------------
// RegisterRoutes smoke test (cycle 1 t-6)
// ---------------------------------------------------------------------------

func TestRegisterRoutes_WiresAllEndpoints(t *testing.T) {
	h := NewHandler(fixedPath("/bin/false"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/whoami"},
		{http.MethodPost, "/api/login"},
		{http.MethodPost, "/api/logout"},
		// Non-POST on login/logout is 405, which also proves the
		// handler was reached (mux found the pattern).
		{http.MethodGet, "/api/login"},
		{http.MethodGet, "/api/logout"},
	}
	for _, tc := range tests {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code == http.StatusNotFound {
				t.Errorf("%s %s -> 404: route not registered", tc.method, tc.path)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// killLoginProcess tests (cycle 2 test-c2-5)
// ---------------------------------------------------------------------------

func TestKillLoginProcess_NilProcess(t *testing.T) {
	cmd := exec.Command("/bin/true")
	// Deliberately do NOT call Start; Process stays nil.
	killLoginProcess(cmd)
	if cmd.Process != nil {
		t.Errorf("Process = %v, want nil (Start was not called)", cmd.Process)
	}
}

func TestKillLoginProcess_AlreadyExited(t *testing.T) {
	skipIfNotUnix(t)
	cmd := exec.Command("/bin/true")
	setLoginProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Process has exited and been reaped; must not panic.
	killLoginProcess(cmd)
}

// ---------------------------------------------------------------------------
// NewHandler smoke test (pre-review, preserved)
// ---------------------------------------------------------------------------

func TestNewHandler(t *testing.T) {
	h := NewHandler(fixedPath("/bin/true"))
	if h == nil {
		t.Fatal("NewHandler returned nil")
	}
	if h.cliPath() != "/bin/true" {
		t.Errorf("cliPath = %q, want %q", h.cliPath(), "/bin/true")
	}
}

// ---------------------------------------------------------------------------
// limitedWriter tests (cycle 3 t-1)
// ---------------------------------------------------------------------------

func TestLimitedWriter_WithinCap(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{W: &buf, N: 100}

	n, err := lw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != 5 {
		t.Errorf("Write(%q) n = %d, want 5", "hello", n)
	}
	if buf.String() != "hello" {
		t.Errorf("buf = %q, want %q", buf.String(), "hello")
	}
	if lw.N != 95 {
		t.Errorf("remaining N = %d, want 95", lw.N)
	}
}

func TestLimitedWriter_TruncatesAtCap(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{W: &buf, N: 3}

	n, err := lw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != 3 {
		t.Errorf("Write(%q) n = %d, want 3 (truncated to cap)", "hello", n)
	}
	if buf.String() != "hel" {
		t.Errorf("buf = %q, want %q", buf.String(), "hel")
	}
	if lw.N != 0 {
		t.Errorf("remaining N = %d, want 0 (fully consumed)", lw.N)
	}
}

func TestLimitedWriter_SaturatedDropsSilently(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{W: &buf, N: 0}

	// Contract: when saturated, return len(p) with no error so upstream
	// io.Copy / cmd.Wait don't report a short write, but drop the bytes.
	n, err := lw.Write([]byte("overflow"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil (saturated writer drops silently)", err)
	}
	if n != 8 {
		t.Errorf("Write(%q) n = %d, want 8 (pretend full write)", "overflow", n)
	}
	if buf.Len() != 0 {
		t.Errorf("buf = %q, want empty (bytes dropped)", buf.String())
	}
	if lw.N != 0 {
		t.Errorf("remaining N = %d, want 0 (unchanged)", lw.N)
	}
}

func TestLimitedWriter_SequentialWritesAcrossCap(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{W: &buf, N: 5}

	n1, err1 := lw.Write([]byte("abc"))
	if err1 != nil || n1 != 3 {
		t.Fatalf("first Write = (%d, %v), want (3, nil)", n1, err1)
	}
	n2, err2 := lw.Write([]byte("defgh"))
	if err2 != nil || n2 != 2 {
		t.Fatalf("second Write = (%d, %v), want (2, nil) — partial at cap", n2, err2)
	}
	n3, err3 := lw.Write([]byte("ij"))
	if err3 != nil || n3 != 2 {
		t.Fatalf("third Write = (%d, %v), want (2, nil) — post-saturation no-op returning full len", n3, err3)
	}

	if buf.String() != "abcde" {
		t.Errorf("buf = %q, want %q (cap=5, rest dropped)", buf.String(), "abcde")
	}
}

// ---------------------------------------------------------------------------
// handleWhoami timeout branch (cycle 3 t-2)
// ---------------------------------------------------------------------------

func TestHandleWhoami_TimesOutWhenCLIHangs(t *testing.T) {
	skipIfNotUnix(t)

	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   DefaultConfig.LogoutTimeout,
		WhoamiTimeout:   50 * time.Millisecond,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	rr := httptest.NewRecorder()
	h.handleWhoami(rr, req)

	// Fail-soft: always 200 with "whoami unavailable" so the UI
	// renders "not logged in" regardless of timeout vs failure.
	if rr.Code != http.StatusOK {
		t.Fatalf("handleWhoami timeout status = %d, want 200 (fail-soft banner)", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "whoami unavailable" {
		t.Errorf("error = %v, want %q", body["error"], "whoami unavailable")
	}
}

// TestHandleWhoami_BinaryMissingFailsSoft pins the ErrNotFound branch
// of handleWhoami (line 184). Mirrors the structural pattern already
// tested for handleLogout (TestHandleLogout_BinaryMissing) and
// handleLogin (TestHandleLogin_BinaryMissingReturns503). A future
// refactor that accidentally made this branch fall through to the
// default case (losing the dedicated "kiro-cli binary not found" log
// that Grafana alerts on) would still pass CI without this.
func TestHandleWhoami_BinaryMissingFailsSoft(t *testing.T) {
	// Path that doesn't exist and isn't on PATH — triggers
	// exec.ErrNotFound from cmd.Run, which must route through
	// the dedicated "binary not found" log branch and still
	// surface as the generic fail-soft "whoami unavailable"
	// sentinel so the UI renders "not logged in" regardless.
	h := NewHandler(fixedPath(filepath.Join(t.TempDir(), "no-such-kiro-cli")))

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	rr := httptest.NewRecorder()
	h.handleWhoami(rr, req)

	// Fail-soft: always 200 so the banner caller sees a
	// consistent shape regardless of the failure class.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-soft banner)", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "whoami unavailable" {
		t.Errorf("error = %v, want %q", body["error"], "whoami unavailable")
	}
	// Must not leak filesystem paths or exec error shape.
	errStr, _ := body["error"].(string)
	if strings.Contains(errStr, "fork/exec") ||
		strings.Contains(errStr, "no-such-kiro-cli") ||
		strings.Contains(errStr, "not found") {
		t.Errorf("error = %q, leaks binary path / exec details", errStr)
	}
}

// ---------------------------------------------------------------------------
// handleLogout 504/503 branches (cycle 3 t-4)
// ---------------------------------------------------------------------------

func TestHandleLogout_TimesOut(t *testing.T) {
	skipIfNotUnix(t)

	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   50 * time.Millisecond,
		WhoamiTimeout:   DefaultConfig.WhoamiTimeout,
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rr := httptest.NewRecorder()
	h.handleLogout(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("handleLogout timeout status = %d, want 504; body=%s",
			rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "logout timed out" {
		t.Errorf("error = %q, want %q", body["error"], "logout timed out")
	}
}

func TestHandleLogout_BinaryMissing(t *testing.T) {
	// Path that doesn't exist and isn't on PATH — triggers
	// exec.ErrNotFound from cmd.Run.
	h := NewHandler(fixedPath(filepath.Join(t.TempDir(), "no-such-kiro-cli")))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rr := httptest.NewRecorder()
	h.handleLogout(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleLogout missing-binary status = %d, want 503; body=%s",
			rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "logout unavailable" {
		t.Errorf("error = %q, want %q", body["error"], "logout unavailable")
	}
}

// Generic CLI-failure branch must return a sanitised sentinel, not
// err.Error() (which would leak the binary path or OS message).
func TestHandleLogout_CLIFailsReturnsGenericSentinel(t *testing.T) {
	skipIfNotUnix(t)

	cli := writeFakeCLI(t, "auth error: no session\n", 2)
	h := NewHandler(fixedPath(cli))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rr := httptest.NewRecorder()
	h.handleLogout(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "logout failed" {
		t.Errorf("error = %q, want %q (generic sentinel, not err.Error())",
			body["error"], "logout failed")
	}
	// Guardrail: err.Error() would look like "exit status 2" — make
	// sure we never emit that shape (path leaks, OS messages).
	if strings.Contains(body["error"], "exit status") {
		t.Errorf("error = %q, must not contain raw err.Error() prefix", body["error"])
	}
	// Output preservation: the CLI's own stdout/stderr must still
	// reach the client so operators can diagnose. This was tested
	// by the older TestHandleLogout_CLIFails; keep coverage here.
	if !strings.Contains(body["output"], "auth error") {
		t.Errorf("output = %q, want to contain CLI output", body["output"])
	}
}

// ---------------------------------------------------------------------------
// handleLogin generic-sentinel error paths (cycle 3 F1/F2)
// ---------------------------------------------------------------------------

func TestHandleLogin_BinaryMissingReturns503(t *testing.T) {
	// Path that doesn't exist and isn't on PATH — triggers
	// exec.ErrNotFound from cmd.Start.
	h := NewHandler(fixedPath(filepath.Join(t.TempDir(), "no-such-kiro-cli")))

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleLogin missing-binary status = %d, want 503; body=%s",
			rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "login unavailable" {
		t.Errorf("error = %q, want %q", body["error"], "login unavailable")
	}
	// Must not leak filesystem paths or exec error shape.
	if strings.Contains(body["error"], "fork/exec") ||
		strings.Contains(body["error"], "no-such-kiro-cli") ||
		strings.Contains(body["error"], "not found") {
		t.Errorf("error = %q, leaks binary path / exec details", body["error"])
	}
}

func TestHandleLogin_BodyTooLargeReturns413(t *testing.T) {
	h := NewHandler(fixedPath("/does-not-exist"))
	big := strings.Repeat("a", int(httpreply.MaxJSONBody)+1024)
	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"provider":"`+big+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("handleLogin oversize status = %d, want 413; body=%s",
			rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "request too large" {
		t.Errorf("error = %q, want %q", body["error"], "request too large")
	}
}

func TestHandleLogin_ConcurrentAttemptReturns409(t *testing.T) {
	skipIfNotUnix(t)
	// Fake CLI that hangs so the first request stays in flight
	// while we fire the second.
	path := writeFakeCLIScript(t, "sleep 5\n")
	// Shrink URL timeout so the first handler returns fast; we
	// care about the 409 on the second attempt, not the timeout
	// details of the first.
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: 100 * time.Millisecond,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   DefaultConfig.LogoutTimeout,
		WhoamiTimeout:   DefaultConfig.WhoamiTimeout,
	}))

	// Seat the semaphore directly to simulate an in-flight login
	// without racing the first handler.
	h.loginSem <- struct{}{}
	t.Cleanup(func() { <-h.loginSem })

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("concurrent login status = %d, want 409; body=%s",
			rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] != "login in progress" {
		t.Errorf("error = %q, want %q", body["error"], "login in progress")
	}
}

// TestHandleLogin_SecondAttemptAfterURLEmittedReturns409 exercises the
// real-world race the cycle-2 semaphore fix addresses: the first
// handler emits a URL and returns, but the kiro-cli subprocess is
// still alive pinning a device code. A second POST arriving during
// that window must still get 409 — otherwise a LAN probe or a user
// retry spawns a second subprocess and pins a second device code.
// Before the fix, the semaphore was released by a defer on handler
// return, so the second POST succeeded. Now it's released by the reap
// goroutine when cmd.Wait returns.
func TestHandleLogin_SecondAttemptAfterURLEmittedReturns409(t *testing.T) {
	skipIfNotUnix(t)
	// Fake CLI that emits a URL then sleeps. The first handler
	// returns 200 on the URL; the subprocess stays alive. We keep
	// LoginURLTimeout at its default (10s) so the first request
	// returns via the URL-found path, not the timeout path.
	path := writeFakeCLIScript(t,
		"echo 'Open this URL: https://example.com/auth'\n"+
			"sleep 30\n")
	// Shrink LoginTimeout so the test doesn't hold the
	// subprocess for 16 minutes. The sleep is 30s; a 500ms hard
	// cap forces the reap goroutine to SIGKILL long before.
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    500 * time.Millisecond,
		LogoutTimeout:   DefaultConfig.LogoutTimeout,
		WhoamiTimeout:   DefaultConfig.WhoamiTimeout,
	}))

	// First request: should succeed with a URL.
	req1 := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{}`))
	req1.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	h.handleLogin(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first login status = %d, want 200; body=%s",
			rr1.Code, rr1.Body.String())
	}
	var body1 map[string]string
	if err := json.Unmarshal(rr1.Body.Bytes(), &body1); err != nil {
		t.Fatalf("first response not JSON: %v", err)
	}
	if body1["url"] != "https://example.com/auth" {
		t.Fatalf("first url = %q, want https://example.com/auth", body1["url"])
	}

	// Second request, arriving after the first handler returned
	// but while the subprocess is still alive. Before the fix,
	// this would succeed and spawn a second subprocess. After the
	// fix, the reap goroutine still holds the semaphore.
	req2 := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	h.handleLogin(rr2, req2)

	if rr2.Code != http.StatusConflict {
		t.Fatalf("second login status = %d, want 409 (sem held by reap goroutine); body=%s",
			rr2.Code, rr2.Body.String())
	}
	var body2 map[string]string
	if err := json.Unmarshal(rr2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("second response not JSON: %v", err)
	}
	if body2["error"] != "login in progress" {
		t.Errorf("second error = %q, want %q", body2["error"], "login in progress")
	}

	// Wait for the reap goroutine to release the sem (hard cap
	// fires at 500ms, SIGKILL, cmd.Wait returns, sem released).
	// Bounded acquire+release select — fails fast on regression
	// rather than polling at a 50ms quantum.
	select {
	case h.loginSem <- struct{}{}:
		<-h.loginSem
	case <-time.After(5 * time.Second):
		t.Fatal("semaphore not released within 5s of hard-cap expiry")
	}
}

// ---------------------------------------------------------------------------
// scanLoginOutput already-logged-in fast path (cycle 3 t-5)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// whoamiInfo capital-Email fallback (cycle 3 t-3)
// ---------------------------------------------------------------------------

func TestWhoamiInfo_CapitalEmailFallback(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantEmail string
	}{
		{
			name:      "capital Email without lowercase falls back",
			in:        `{"account_type":"BuilderId","Email":"fallback@example.com"}`,
			wantEmail: "fallback@example.com",
		},
		{
			name:      "lowercase email wins over capital Email",
			in:        `{"email":"lower@example.com","Email":"upper@example.com"}`,
			wantEmail: "lower@example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := whoamiInfo([]byte(tc.in))
			if err != nil {
				t.Fatalf("whoamiInfo: %v", err)
			}
			if got.Email != tc.wantEmail {
				t.Errorf("Email = %q, want %q", got.Email, tc.wantEmail)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// loginKill nil-process early return (cycle 3 t-6)
// ---------------------------------------------------------------------------

func TestLoginKill_NilProcessReturnsESRCH(t *testing.T) {
	skipIfNotUnix(t)
	cmd := exec.Command("/bin/true")
	// Never call Start; Process is nil.
	err := loginKill(cmd)
	if !errors.Is(err, syscall.ESRCH) {
		t.Errorf("loginKill(unstarted) = %v, want syscall.ESRCH", err)
	}
}

// ---------------------------------------------------------------------------
// extractAuthURL unit tests (cycle 3 Q3)
// ---------------------------------------------------------------------------

func TestExtractAuthURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"explicit prefix", "Open this URL: https://idp.example.com/a", "https://idp.example.com/a"},
		{"bare URL token", "visit https://example.com/b then", "https://example.com/b"},
		{"explicit wins over bare token on same line", "Open this URL: https://primary.example.com/ also https://secondary.example.com/", "https://primary.example.com/"},
		{"no URL", "nothing to see", ""},
		{"empty", "", ""},
		{"http only is not extracted as bare", "visit http://insecure.example.com/", ""},
		{"explicit prefix rejects javascript scheme", "Open this URL: javascript:alert(1)", ""},
		{"explicit prefix skips http and picks https", "Open this URL: http://evil.com/ and https://legit.example.com/", "https://legit.example.com/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAuthURL(tc.in)
			if got != tc.want {
				t.Errorf("extractAuthURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildLoginArgs unit tests (cycle 3 Q4)
// ---------------------------------------------------------------------------

func TestBuildLoginArgs(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		region   string
		want     []string
	}{
		{
			name: "both empty",
			want: []string{"login", "--use-device-flow"},
		},
		{
			name:     "provider only",
			provider: "https://view.awsapps.com/start",
			want: []string{
				"login", "--use-device-flow",
				"--identity-provider", "https://view.awsapps.com/start",
			},
		},
		{
			name:   "region only",
			region: "us-east-1",
			want: []string{
				"login", "--use-device-flow",
				"--region", "us-east-1",
			},
		},
		{
			name:     "both set",
			provider: "https://view.awsapps.com/start",
			region:   "us-east-1",
			want: []string{
				"login", "--use-device-flow",
				"--identity-provider", "https://view.awsapps.com/start",
				"--region", "us-east-1",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildLoginArgs(tc.provider, tc.region)
			if len(got) != len(tc.want) {
				t.Fatalf("buildLoginArgs = %v (len %d), want %v (len %d)",
					got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// classifyLoginStartErr unit tests (cycle 1 test-u5c1-f1)
// ---------------------------------------------------------------------------

// TestClassifyLoginStartErr pins the ErrNotFound-vs-generic mapping.
// Current integration coverage only hits the ErrNotFound branch via
// TestHandleLogin_BinaryMissingReturns503; the generic default branch
// (500 on non-ENOENT) is uncovered. Without this test, a refactor
// that extended the ENOENT branch to also swallow EPERM/EACCES would
// silently downgrade 500 to 503 and pass CI — breaking the
// "503 = binary missing (redeploy), 500 = transient (retry)"
// contract Grafana alert rules depend on.
func TestClassifyLoginStartErr(t *testing.T) {
	tests := []struct {
		err  error
		name string
		want int
	}{
		{
			name: "exec.ErrNotFound maps to 503 (binary missing)",
			err:  exec.ErrNotFound,
			want: http.StatusServiceUnavailable,
		},
		{
			name: "fs.ErrNotExist maps to 503 (fork/exec ENOENT)",
			err:  fs.ErrNotExist,
			want: http.StatusServiceUnavailable,
		},
		{
			name: "wrapped ExecError(ErrNotFound) maps to 503",
			err:  &exec.Error{Name: "kiro-cli", Err: exec.ErrNotFound},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "generic error maps to 500 (transient fork failure)",
			err:  errors.New("resource temporarily unavailable"),
			want: http.StatusInternalServerError,
		},
		{
			name: "EACCES-like error maps to 500 (not downgraded to 503)",
			err:  errors.New("permission denied"),
			want: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLoginStartErr(tc.err, "/fake/cli")
			if got != tc.want {
				t.Errorf("classifyLoginStartErr(%v) = %d, want %d",
					tc.err, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// slog-capture helpers + log/branch assertions
// (folded from the retired gremlins_kill_vibekit_u28 micro-file;
//  invariants live in the test names below)
// ---------------------------------------------------------------------------

// captureSlogJSON swaps the default slog logger for a JSON handler writing
// to an in-memory buffer at the given level, runs fn, restores the previous
// default, and returns the parsed log records. fn must be synchronous (no
// background goroutines) so every record is flushed before parsing.
func captureSlogJSON(t *testing.T, level slog.Level, fn func()) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
	defer slog.SetDefault(prev)
	fn()
	var recs []map[string]any
	for line := range bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("captureSlogJSON: bad json log line %q: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

// findLogRec returns the first captured record whose "msg" equals msg, or
// nil if none.
func findLogRec(recs []map[string]any, msg string) map[string]any {
	for _, r := range recs {
		if m, _ := r["msg"].(string); m == msg {
			return r
		}
	}
	return nil
}

// drainErrReader yields its canned data on the first reads, then returns a
// non-EOF error once the data is exhausted. Forces io.Copy in
// scanLoginOutputWithDrain to return a non-nil error after the URL line has
// been consumed by scanLoginOutput.
type drainErrReader struct {
	err  error
	data []byte
	pos  int
}

func (r *drainErrReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, r.err
}

func (r *drainErrReader) Close() error { return nil }

// stderrAttr returns nil for empty stderr and a ["stderr", text] attr pair
// for non-empty stderr, so a failed CLI's diagnostics reach the structured
// log without emitting an empty attribute on success.
func TestStderrAttr_EmptyVsNonEmpty(t *testing.T) {
	var empty bytes.Buffer
	if got := stderrAttr(&empty); got != nil {
		t.Errorf("stderrAttr(empty) = %v, want nil", got)
	}

	var nonEmpty bytes.Buffer
	nonEmpty.WriteString("boom")
	got := stderrAttr(&nonEmpty)
	if len(got) != 2 {
		t.Fatalf("stderrAttr(non-empty) len = %d, want 2; got=%v", len(got), got)
	}
	if got[0] != "stderr" {
		t.Errorf("stderrAttr(non-empty)[0] = %v, want %q", got[0], "stderr")
	}
	if got[1] != "boom" {
		t.Errorf("stderrAttr(non-empty)[1] = %v, want %q", got[1], "boom")
	}
}

// scanLoginOutputWithDrain logs "login: stdout drain stopped" at Debug when
// draining the post-URL stdout returns a non-EOF error, while still emitting
// the extracted URL.
func TestScanLoginOutputWithDrain_LogsOnDrainError(t *testing.T) {
	r := &drainErrReader{
		data: []byte("Open this URL: https://example.com/auth\n"),
		err:  errors.New("drain boom"),
	}
	ch := make(chan map[string]string, 1)
	recs := captureSlogJSON(t, slog.LevelDebug, func() {
		scanLoginOutputWithDrain(r, ch)
	})

	got := <-ch
	if got["url"] != "https://example.com/auth" {
		t.Fatalf("url = %q, want https://example.com/auth", got["url"])
	}
	if findLogRec(recs, "login: stdout drain stopped") == nil {
		t.Errorf("expected debug log %q on drain error; logs=%v",
			"login: stdout drain stopped", recs)
	}
}

// scanLoginOutput records has_code = (a Code: line was seen) on its
// "auth URL extracted" log line: true when a Code: line precedes the URL,
// false otherwise.
func TestScanLoginOutput_LogsHasCodeAttribute(t *testing.T) {
	withCode := captureSlogJSON(t, slog.LevelInfo, func() {
		ch := make(chan map[string]string, 1)
		scanLoginOutput(strings.NewReader(
			"Code: ABCD-1234\nOpen this URL: https://idp.example.com/\n"), ch)
	})
	rc := findLogRec(withCode, "login: auth URL extracted")
	if rc == nil {
		t.Fatalf("no 'auth URL extracted' log (with code); logs=%v", withCode)
	}
	if hc, _ := rc["has_code"].(bool); !hc {
		t.Errorf("has_code = %v, want true (Code: line present)", rc["has_code"])
	}

	noCode := captureSlogJSON(t, slog.LevelInfo, func() {
		ch := make(chan map[string]string, 1)
		scanLoginOutput(strings.NewReader(
			"Open this URL: https://idp.example.com/\n"), ch)
	})
	rn := findLogRec(noCode, "login: auth URL extracted")
	if rn == nil {
		t.Fatalf("no 'auth URL extracted' log (no code); logs=%v", noCode)
	}
	if hc, _ := rn["has_code"].(bool); hc {
		t.Errorf("has_code = %v, want false (no Code: line)", rn["has_code"])
	}
}

// killLoginProcess logs "login: kill group no-op (already reaped)" at Debug
// when the subprocess has already exited (loginKill returns ESRCH).
func TestKillLoginProcess_ReapedLogsNoOp(t *testing.T) {
	skipIfNotUnix(t)
	cmd := exec.Command("/bin/true")
	setLoginProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	recs := captureSlogJSON(t, slog.LevelDebug, func() {
		killLoginProcess(cmd)
	})
	if findLogRec(recs, "login: kill group no-op (already reaped)") == nil {
		t.Errorf("expected debug log %q for reaped process; logs=%v",
			"login: kill group no-op (already reaped)", recs)
	}
}

// fixedPath adapts a static path to the resolver NewHandler takes. Production
// passes the install manager's CLIPath, which changes when the active version
// does; a test wants one fixed binary for the whole case.
func fixedPath(p string) func() string {
	return func() string { return p }
}
