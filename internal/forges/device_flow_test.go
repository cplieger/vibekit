package forges

import "testing"

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
