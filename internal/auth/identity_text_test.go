package auth

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/runesafe/v2"
)

// The identity row is not a decision surface, but the five strings on it are
// upstream text this handler used to pass to the browser and the log verbatim
// while its sibling handleLogout ran the same subprocess's output through
// sanitize.Output. These pin the closure of that asymmetry.
//
// The values originate at the user's identity provider (email, startUrl,
// region) or are echoed by humanizeAccountType's generic arm, and their only
// bound was the 64 KiB whole-output cap.
func TestWhoamiInfo_SanitizesAndBoundsEveryIdentityString(t *testing.T) {
	const rlo = "\u202e" // RIGHT-TO-LEFT OVERRIDE

	// A 60 KiB "email" and a direction override in every field, which is what
	// the 64 KiB output cap alone permitted.
	huge := strings.Repeat("a", 60*1024) + "@example.com"
	raw, err := json.Marshal(map[string]any{
		"email":        huge,
		"account_type": "SomethingNew" + rlo + "detsurt",
		"startUrl":     "https://" + rlo + "moc.live" + "/start",
		"region":       "us-east-1" + rlo,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := whoamiInfo(raw)
	if err != nil {
		t.Fatalf("whoamiInfo: %v", err)
	}

	for name, v := range map[string]string{
		"Email":       got.Email,
		"Auth":        got.Auth,
		"AccountType": got.AccountType,
		"StartURL":    got.StartURL,
		"Region":      got.Region,
	} {
		if len(v) > maxIdentityFieldBytes+len("...") {
			t.Errorf("%s = %d bytes, want <= %d", name, len(v), maxIdentityFieldBytes+3)
		}
		for _, r := range v {
			if runesafe.IsBidiControl(r) {
				t.Errorf("%s = %q still carries U+%04X", name, v, r)
			}
		}
	}
	// Bounded, not emptied: the field still identifies the account.
	if !strings.HasPrefix(got.Email, "aaaa") || !strings.HasSuffix(got.Email, "...") {
		t.Errorf("Email = %q..., want a truncated prefix ending in the cut marker", got.Email[:min(20, len(got.Email))])
	}
	// The generic arm echoes the upstream account type into the label, so the
	// override has to be gone from Auth too, not only from AccountType.
	if !strings.HasPrefix(got.Auth, authPrefixGeneric) {
		t.Errorf("Auth = %q, want the generic %q arm", got.Auth, authPrefixGeneric)
	}
}

// TestWhoamiInfo_LeavesOrdinaryIdentityFieldsByteIdentical states the cost: an
// email address, an AWS region id and an SSO start URL cannot need explicit
// direction marks, so nothing a real identity provider returns is touched.
func TestWhoamiInfo_LeavesOrdinaryIdentityFieldsByteIdentical(t *testing.T) {
	const in = `{"email":"jean-rené@société.example","account_type":"BuilderId",` +
		`"startUrl":"https://d-9067f19e2b.awsapps.com/start","region":"eu-west-3"}`

	got, err := whoamiInfo([]byte(in))
	if err != nil {
		t.Fatalf("whoamiInfo: %v", err)
	}
	for name, pair := range map[string][2]string{
		"Email":       {got.Email, "jean-rené@société.example"},
		"AccountType": {got.AccountType, "BuilderId"},
		"StartURL":    {got.StartURL, "https://d-9067f19e2b.awsapps.com/start"},
		"Region":      {got.Region, "eu-west-3"},
		"Auth":        {got.Auth, authBuilderID},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want byte-identical %q", name, pair[0], pair[1])
		}
	}
}
