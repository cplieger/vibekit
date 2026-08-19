package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

type fakeAcctUsage struct {
	ret   *vibekit.AccountUsage
	err   error
	calls int
}

func (f *fakeAcctUsage) AccountUsage(_ context.Context) (*vibekit.AccountUsage, error) {
	f.calls++
	return f.ret, f.err
}

func getUsage(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/account/usage", http.NoBody)
	rec := httptest.NewRecorder()
	s.handleAccountUsage(rec, req)
	return rec
}

func sampleUsage() *vibekit.AccountUsage {
	return &vibekit.AccountUsage{
		PlanName:   "KIRO POWER",
		Breakdowns: []vibekit.AccountUsageBreakdown{{ResourceType: "CREDIT", Used: 10, Limit: 100, Percentage: 10, HasLimit: true}},
	}
}

func TestHandleAccountUsageNilProvider(t *testing.T) {
	s := &Server{}
	rec := getUsage(t, s)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleAccountUsageSuccess(t *testing.T) {
	f := &fakeAcctUsage{ret: sampleUsage()}
	s := &Server{accountUsage: f}
	rec := getUsage(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got vibekit.AccountUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PlanName != "KIRO POWER" || got.Stale {
		t.Errorf("body = %+v", got)
	}
	if f.calls != 1 {
		t.Errorf("provider calls = %d, want 1", f.calls)
	}
}

func TestHandleAccountUsageCacheHit(t *testing.T) {
	// Pre-seed a fresh cache entry; the provider must NOT be called.
	f := &fakeAcctUsage{err: context.DeadlineExceeded}
	s := &Server{accountUsage: f}
	s.acctUsage.data = sampleUsage()
	s.acctUsage.atNanos = time.Now().UnixNano()

	rec := getUsage(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.calls != 0 {
		t.Errorf("provider called %d times on a fresh cache hit", f.calls)
	}
}

func TestHandleAccountUsageStaleFallback(t *testing.T) {
	// Cache is older than the TTL and the fetch fails: serve last-known
	// marked stale rather than erroring.
	f := &fakeAcctUsage{err: context.DeadlineExceeded}
	s := &Server{accountUsage: f}
	s.acctUsage.data = sampleUsage()
	s.acctUsage.atNanos = time.Now().Add(-2 * accountUsageTTL).UnixNano()

	rec := getUsage(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got vibekit.AccountUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Stale {
		t.Error("stale fallback should set stale=true")
	}
	if f.calls != 1 {
		t.Errorf("provider calls = %d, want 1", f.calls)
	}
}

func TestHandleAccountUsageErrorNoCache(t *testing.T) {
	f := &fakeAcctUsage{err: context.DeadlineExceeded}
	s := &Server{accountUsage: f}
	rec := getUsage(t, s)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
