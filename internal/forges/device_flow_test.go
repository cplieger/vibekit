package forges

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// interpretPollResponse maps each GitHub OAuth device-flow poll outcome to
// the right deviceTokenResult. The empty-access_token case is the security-critical
// one: it must be reported as an error, never as a completed login.
func TestInterpretPollResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    deviceTokenResult
		wantErr bool
	}{
		{name: "authorization_pending", body: `{"error":"authorization_pending"}`, want: deviceTokenResult{Status: "pending"}},
		{name: "slow_down", body: `{"error":"slow_down"}`, want: deviceTokenResult{Status: "pending"}},
		{name: "expired_token", body: `{"error":"expired_token"}`, want: deviceTokenResult{Status: "expired", Error: "device code expired"}},
		{name: "access_denied", body: `{"error":"access_denied"}`, want: deviceTokenResult{Status: "error", Error: "access denied"}},
		{name: "other_error_uses_description", body: `{"error":"unsupported_grant_type","error_description":"bad grant"}`, want: deviceTokenResult{Status: "error", Error: "bad grant"}},
		{name: "success_empty_token_is_error", body: `{"access_token":""}`, want: deviceTokenResult{Status: "error", Error: "empty access_token"}},
		{name: "no_error_no_token_is_error", body: `{}`, want: deviceTokenResult{Status: "error", Error: "empty access_token"}},
		{name: "success_with_token", body: `{"access_token":"gho_secret"}`, want: deviceTokenResult{Status: "complete", Token: "gho_secret"}},
		{name: "malformed_json", body: `not json`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := interpretPollResponse([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("interpretPollResponse(%q) err = nil, want error", tt.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("interpretPollResponse(%q) err = %v, want nil", tt.body, err)
			}
			if got != tt.want {
				t.Errorf("interpretPollResponse(%q) = %+v, want %+v", tt.body, got, tt.want)
			}
		})
	}
}

func TestParseDeviceFlowResponse(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		body := `{"user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","device_code":"dev123","interval":5,"expires_in":900}`
		got, err := parseDeviceFlowResponse([]byte(body))
		if err != nil {
			t.Fatalf("parseDeviceFlowResponse err = %v, want nil", err)
		}
		want := DeviceFlowResponse{
			UserCode:        "WDJB-MJHT",
			VerificationURI: "https://github.com/login/device",
			DeviceCode:      "dev123",
			Interval:        5,
			ExpiresIn:       900,
		}
		if *got != want {
			t.Errorf("parseDeviceFlowResponse = %+v, want %+v", *got, want)
		}
	})

	t.Run("embedded_error", func(t *testing.T) {
		got, err := parseDeviceFlowResponse([]byte(`{"error":"slow_down"}`))
		if err == nil {
			t.Fatalf("parseDeviceFlowResponse = %+v, want error", got)
		}
		if got != nil {
			t.Errorf("parseDeviceFlowResponse returned %+v alongside error, want nil", got)
		}
	})

	t.Run("malformed_json", func(t *testing.T) {
		if _, err := parseDeviceFlowResponse([]byte(`{bad`)); err == nil {
			t.Fatal("parseDeviceFlowResponse(malformed) err = nil, want error")
		}
	})
}

// TestValidScope pins the fail-safe: one unrecognized token would make
// GitHub reject the whole device-code request, so anything not
// scope-shaped is dropped rather than forwarded.
func TestValidScope(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		want  bool
	}{
		{name: "plain", scope: "repo", want: true},
		{name: "colon", scope: "read:org", want: true},
		{name: "underscore", scope: "security_events", want: true},
		{name: "hyphen", scope: "some-scope", want: true},
		{name: "digits", scope: "oauth2", want: true},
		{name: "empty", scope: "", want: false},
		{name: "uppercase", scope: "Repo", want: false},
		{name: "space_inside", scope: "read org", want: false},
		{name: "comma_inside", scope: "repo,gist", want: false},
		{name: "newline", scope: "repo\n", want: false},
		{name: "over_length", scope: strings.Repeat("a", maxScopeLen+1), want: false},
		{name: "at_length", scope: strings.Repeat("a", maxScopeLen), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validScope(tt.scope); got != tt.want {
				t.Errorf("validScope(%q) = %v, want %v", tt.scope, got, tt.want)
			}
		})
	}
}

// TestParseScopeList decodes gh's comma-and-space separated form and
// drops what validScope refuses.
func TestParseScopeList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "gh_status_form", in: "gist, read:org, repo, workflow", want: []string{"gist", "read:org", "repo", "workflow"}},
		{name: "no_spaces", in: "repo,read:org", want: []string{"repo", "read:org"}},
		{name: "empty", in: "", want: nil},
		{name: "blanks_dropped", in: "repo, ,,gist", want: []string{"repo", "gist"}},
		{name: "malformed_dropped", in: "repo, NOT A SCOPE, gist", want: []string{"repo", "gist"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseScopeList(tt.in)
			if !slices.Equal(got, tt.want) {
				t.Errorf("parseScopeList(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestScopeRequest is the defect's own test: a reconnect must ask for
// every scope the stored token already carries, because the login
// REPLACES that token. Losing the union here is what silently narrows
// a credential granted out of band.
func TestScopeRequest(t *testing.T) {
	tests := []struct {
		name    string
		granted []string
		want    string
	}{
		{
			name:    "nothing_granted_is_the_baseline",
			granted: nil,
			want:    "repo,read:org,workflow",
		},
		{
			// gist is the measured case the union exists for: a
			// `gh auth refresh -s gist` grant the next reconnect would
			// otherwise take away.
			name:    "out_of_band_scope_is_preserved",
			granted: []string{"gist", "read:org", "repo", "workflow"},
			want:    "repo,read:org,workflow,gist",
		},
		{
			name:    "baseline_order_kept_extras_sorted",
			granted: []string{"gist", "security_events", "codespace"},
			want:    "repo,read:org,workflow,codespace,gist,security_events",
		},
		{
			name:    "granted_subset_adds_nothing",
			granted: []string{"repo"},
			want:    "repo,read:org,workflow",
		},
		{
			name:    "duplicates_collapse",
			granted: []string{"codespace", "codespace", "codespace"},
			want:    "repo,read:org,workflow,codespace",
		},
		{
			name:    "malformed_granted_scope_is_dropped",
			granted: []string{"codespace", "NOT A SCOPE"},
			want:    "repo,read:org,workflow,codespace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeRequest(tt.granted); got != tt.want {
				t.Errorf("scopeRequest(%v) = %q, want %q", tt.granted, got, tt.want)
			}
		})
	}
}

// deviceCodeReply is one canned response for scopeRecorder.
type deviceCodeReply struct {
	body   string
	status int
}

// scopeRecorder records the scope parameter of every device-code
// request and answers from a queue, so a test can assert what vibekit
// ASKED GitHub for rather than what it did with the answer.
type scopeRecorder struct {
	replies []deviceCodeReply
	scopes  []string
	calls   int
}

func (r *scopeRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, err
	}
	r.scopes = append(r.scopes, form.Get("scope"))
	reply := r.replies[min(r.calls, len(r.replies)-1)]
	r.calls++
	return &http.Response{
		StatusCode: reply.status,
		Body:       io.NopCloser(strings.NewReader(reply.body)),
		Header:     make(http.Header),
	}, nil
}

// recordDeviceCodeRequests points the OAuth client at rec for one test.
func recordDeviceCodeRequests(t *testing.T, rec *scopeRecorder) {
	t.Helper()
	previous := oauthHTTPClient
	t.Cleanup(func() { oauthHTTPClient = previous })
	oauthHTTPClient = &http.Client{Transport: rec}
}

// stubGHScopes makes `gh auth status --json hosts` report scopes for
// github.com.
func stubGHScopes(t *testing.T, scopes string) {
	t.Helper()
	dir := stubPath(t)
	status := `{"hosts":{"github.com":[{"active":true,"login":"alice","scopes":"` + scopes + `"}]}}`
	stubCLI(t, dir, "gh", "echo '"+status+"'")
}

const deviceCodeOK = `{"user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","device_code":"dev123","interval":5,"expires_in":900}`

// TestStartGitHubDeviceFlow_AsksForTheScopesAlreadyGranted is the
// end-to-end half of the defect: the scope parameter that reaches
// GitHub must carry the out-of-band scope, or the reconnect issues a
// narrower token than the one it replaces.
func TestStartGitHubDeviceFlow_AsksForTheScopesAlreadyGranted(t *testing.T) {
	stubGHScopes(t, "codespace, gist, read:org, repo, workflow")
	rec := &scopeRecorder{replies: []deviceCodeReply{{status: http.StatusOK, body: deviceCodeOK}}}
	recordDeviceCodeRequests(t, rec)

	if _, err := StartGitHubDeviceFlow(t.Context()); err != nil {
		t.Fatalf("StartGitHubDeviceFlow() err = %v, want nil", err)
	}
	if len(rec.scopes) != 1 {
		t.Fatalf("device-code requests = %d, want 1: %v", len(rec.scopes), rec.scopes)
	}
	if got, want := rec.scopes[0], "repo,read:org,workflow,codespace,gist"; got != want {
		t.Errorf("requested scope = %q, want %q", got, want)
	}
}

// TestStartGitHubDeviceFlow_NoRetryWhenNothingWasPreserved: with the
// union equal to the floor there is nothing to fall back to, so a
// refusal must not cost a second round trip.
func TestStartGitHubDeviceFlow_NoRetryWhenNothingWasPreserved(t *testing.T) {
	stubGHScopes(t, "repo, workflow")
	rec := &scopeRecorder{replies: []deviceCodeReply{{status: http.StatusBadRequest, body: `{"error":"bad_verification_code"}`}}}
	recordDeviceCodeRequests(t, rec)

	if _, err := StartGitHubDeviceFlow(t.Context()); err == nil {
		t.Fatal("StartGitHubDeviceFlow() err = nil, want the refusal reported")
	}
	if len(rec.scopes) != 1 {
		t.Errorf("device-code requests = %d, want 1 (no fallback to retry): %v", len(rec.scopes), rec.scopes)
	}
}

// TestStartGitHubDeviceFlow_FallsBackToTheBaseline: a scope GitHub has
// since retired must cost the union, never the login itself.
func TestStartGitHubDeviceFlow_FallsBackToTheBaseline(t *testing.T) {
	stubGHScopes(t, "repo, workflow, retired-scope")
	rec := &scopeRecorder{replies: []deviceCodeReply{
		{status: http.StatusBadRequest, body: `{"error":"invalid_scope"}`},
		{status: http.StatusOK, body: deviceCodeOK},
	}}
	recordDeviceCodeRequests(t, rec)

	resp, err := StartGitHubDeviceFlow(t.Context())
	if err != nil {
		t.Fatalf("StartGitHubDeviceFlow() err = %v, want the baseline retry to succeed", err)
	}
	if resp.DeviceCode != "dev123" {
		t.Errorf("device code = %q, want dev123", resp.DeviceCode)
	}
	if len(rec.scopes) != 2 {
		t.Fatalf("device-code requests = %d, want 2 (union then baseline): %v", len(rec.scopes), rec.scopes)
	}
	if got, want := rec.scopes[0], "repo,read:org,workflow,retired-scope"; got != want {
		t.Errorf("first request scope = %q, want the union %q", got, want)
	}
	if got, want := rec.scopes[1], "repo,read:org,workflow"; got != want {
		t.Errorf("retry scope = %q, want the baseline %q", got, want)
	}
}

// TestStartGitHubDeviceFlow_WithoutGHAsksForTheBaseline: no gh means no
// stored token and nothing to preserve, so an unreadable scope list
// must not fail the login.
func TestStartGitHubDeviceFlow_WithoutGHAsksForTheBaseline(t *testing.T) {
	stubPath(t) // empty PATH: gh is not installed
	rec := &scopeRecorder{replies: []deviceCodeReply{{status: http.StatusOK, body: deviceCodeOK}}}
	recordDeviceCodeRequests(t, rec)

	if _, err := StartGitHubDeviceFlow(t.Context()); err != nil {
		t.Fatalf("StartGitHubDeviceFlow() err = %v, want nil", err)
	}
	if got, want := rec.scopes[0], "repo,read:org,workflow"; got != want {
		t.Errorf("requested scope = %q, want the baseline %q", got, want)
	}
}

// TestDroppedScopes: the fallback's log line is the only report a
// capability loss ever gets, so the diff it names has to be the real
// one. A wrong answer here misdirects the one person looking.
func TestDroppedScopes(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		baseline  string
		want      []string
	}{
		{
			name:      "nothing_preserved_drops_nothing",
			requested: "repo,read:org,workflow",
			baseline:  "repo,read:org,workflow",
			want:      nil,
		},
		{
			name:      "the_out_of_band_scope_is_named",
			requested: "repo,read:org,workflow,gist",
			baseline:  "repo,read:org,workflow",
			want:      []string{"gist"},
		},
		{
			name:      "several_dropped_keep_requested_order",
			requested: "repo,read:org,workflow,codespace,gist",
			baseline:  "repo,read:org,workflow",
			want:      []string{"codespace", "gist"},
		},
		{
			// The caller passes strings built by scopeRequest, but a
			// malformed entry must not surface as a scope name a reader
			// would then try to re-add.
			name:      "malformed_entries_are_not_reported_as_dropped",
			requested: "repo,NOT A SCOPE,gist",
			baseline:  "repo",
			want:      []string{"gist"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := droppedScopes(tt.requested, tt.baseline)
			if !slices.Equal(got, tt.want) {
				t.Errorf("droppedScopes(%q, %q) = %v, want %v", tt.requested, tt.baseline, got, tt.want)
			}
		})
	}
}
