package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestIdentity(retire func(), probe func() (string, error)) (*Identity, *time.Time) {
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	id := &Identity{
		retire: retire,
		probe: func(context.Context) (string, error) {
			return probe()
		},
		now: func() time.Time { return now },
	}
	return id, &now
}

func TestIdentity_ObserveRetiresOncePerChange(t *testing.T) {
	retired := 0
	id, _ := newTestIdentity(func() { retired++ }, func() (string, error) { return "", nil })

	id.Observe("account-a")
	id.Observe("account-a")
	id.Observe("account-b")
	id.Observe("account-b")

	if retired != 1 {
		t.Errorf("Observe identity changes retired %d times, want 1", retired)
	}
}

func TestIdentity_AbsentRetiresOnceAndNeverBecomesBaseline(t *testing.T) {
	retired := 0
	id, _ := newTestIdentity(func() { retired++ }, func() (string, error) { return "", nil })

	id.Observe("account-a")
	id.Observe("")
	id.Observe("")
	id.Observe("account-a")

	if retired != 1 {
		t.Errorf("Observe absent then the original identity retired %d times, want 1", retired)
	}

	id.Observe("account-b")
	if retired != 2 {
		t.Errorf("Observe identity after absent retired %d times, want 2; absent must not replace the baseline", retired)
	}
}

func TestIdentity_EnsureCurrentHonorsProbeTTL(t *testing.T) {
	retired := 0
	probes := 0
	fingerprint := "account-a"
	id, now := newTestIdentity(func() { retired++ }, func() (string, error) {
		probes++
		return fingerprint, nil
	})

	id.EnsureCurrent(t.Context())
	fingerprint = "account-b"
	id.EnsureCurrent(t.Context())
	if probes != 1 {
		t.Fatalf("EnsureCurrent within the TTL probed %d times, want 1", probes)
	}
	if retired != 0 {
		t.Errorf("EnsureCurrent within the TTL retired %d times, want 0", retired)
	}

	*now = now.Add(identityProbeTTL + time.Second)
	id.EnsureCurrent(t.Context())
	if probes != 2 {
		t.Errorf("EnsureCurrent after the TTL probed %d times, want 2", probes)
	}
	if retired != 1 {
		t.Errorf("EnsureCurrent after the identity changed retired %d times, want 1", retired)
	}
}

func TestIdentity_ProbeFailureKeepsBaseline(t *testing.T) {
	retired := 0
	id, _ := newTestIdentity(func() { retired++ }, func() (string, error) {
		return "", errors.New("whoami unavailable")
	})
	id.Observe("account-a")

	id.EnsureCurrent(t.Context())
	if retired != 0 {
		t.Fatalf("a failed probe retired %d times, want 0", retired)
	}
	id.Observe("account-b")
	if retired != 1 {
		t.Errorf("Observe after a failed probe retired %d times, want 1; the failed probe must keep the baseline", retired)
	}
}

func TestHandleLogout_ObservesAbsentIdentity(t *testing.T) {
	skipIfNotUnix(t)
	retired := 0
	id, _ := newTestIdentity(func() { retired++ }, func() (string, error) { return "", nil })
	id.Observe("account-a")
	cli := writeFakeCLI(t, "Logged out\n", 0)
	h := NewHandler(fixedPath(cli), WithIdentity(id))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rr := httptest.NewRecorder()
	h.handleLogout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handleLogout status = %d, want 200", rr.Code)
	}
	if retired != 1 {
		t.Errorf("handleLogout retired %d times, want 1", retired)
	}
}

func TestIdentityFingerprint_UsesOnlyNamedStableFields(t *testing.T) {
	base := WhoamiResponse{Email: "a", AccountType: "bc", StartURL: "https://example.com/start", Region: "us-east-1", Auth: "display one"}
	sameIdentity := base
	sameIdentity.Auth = "display two"
	if identityFingerprint(base) != identityFingerprint(sameIdentity) {
		t.Error("identityFingerprint changed when only the derived display label changed")
	}

	ambiguousWithoutNames := WhoamiResponse{Email: "ab", AccountType: "c", StartURL: base.StartURL, Region: base.Region}
	if identityFingerprint(base) == identityFingerprint(ambiguousWithoutNames) {
		t.Error("identityFingerprint did not preserve stable field boundaries")
	}
}

// readIdentity is the ONE seam that feeds the registrar, because handleWhoami answers
// from identityCache and forks nothing. So the two tests below are what pin the
// asymmetry the two mechanisms exist under: an identity that PARSED is observed, and
// one that could not be READ is withheld — a kiro-cli that timed out must not read as
// an account change and retire every live bridge.
func TestReadIdentity_ObservesAParsedIdentity(t *testing.T) {
	skipIfNotUnix(t)
	retired := 0
	id, _ := newTestIdentity(func() { retired++ }, func() (string, error) { return "", nil })
	id.Observe(identityFingerprint(WhoamiResponse{Email: "first@example.com", AccountType: "BuilderId"}))
	cli := writeFakeCLI(t, `{"email":"second@example.com","account_type":"BuilderId"}`, 0)
	h := NewHandler(fixedPath(cli), WithIdentity(id))

	if got := h.readIdentity(t.Context()); got.Email != "second@example.com" {
		t.Fatalf("readIdentity email = %q, want second@example.com", got.Email)
	}
	if retired != 1 {
		t.Errorf("readIdentity retired %d times, want 1", retired)
	}
}

func TestReadIdentity_WithholdsAnUnreadableIdentity(t *testing.T) {
	skipIfNotUnix(t)
	retired := 0
	id, _ := newTestIdentity(func() { retired++ }, func() (string, error) { return "", nil })
	id.Observe(identityFingerprint(WhoamiResponse{Email: "first@example.com", AccountType: "BuilderId"}))
	cli := writeFakeCLI(t, "not json", 0)
	h := NewHandler(fixedPath(cli), WithIdentity(id))

	if got := h.readIdentity(t.Context()); got.Email != "" {
		t.Fatalf("readIdentity email = %q, want empty for an unreadable answer", got.Email)
	}
	if retired != 0 {
		t.Errorf("readIdentity retired %d times on an unreadable answer, want 0", retired)
	}
}
