package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseAccountUsage(t *testing.T) {
	// Real KAS getUsage shape captured from a live v3 probe.
	success := `{"success":true,"message":"Plan: KIRO POWER | 1 usage breakdowns",` +
		`"data":{"planName":"KIRO POWER","billingCycleReset":"2026-08-01",` +
		`"overagesEnabled":true,"isEnterprise":false,"usageBreakdowns":[` +
		`{"resourceType":"CREDIT","displayName":"Credits","used":133705.77,` +
		`"limit":10000,"percentage":1337,"currentOverages":123705.77,` +
		`"overageRate":0.04,"overageCharges":4948.23,"currency":"USD","hasLimit":true}]}}`

	t.Run("Success", func(t *testing.T) {
		u, err := parseAccountUsage(json.RawMessage(success))
		if err != nil {
			t.Fatalf("parseAccountUsage: %v", err)
		}
		if u.PlanName != "KIRO POWER" {
			t.Errorf("PlanName = %q", u.PlanName)
		}
		if u.BillingCycleReset != "2026-08-01" || !u.OveragesEnabled || u.IsEnterprise {
			t.Errorf("meta = %+v", u)
		}
		if len(u.Breakdowns) != 1 {
			t.Fatalf("breakdowns = %d, want 1", len(u.Breakdowns))
		}
		b := u.Breakdowns[0]
		if b.ResourceType != "CREDIT" || b.Used != 133705.77 || b.Limit != 10000 ||
			b.Percentage != 1337 || b.Currency != "USD" || !b.HasLimit {
			t.Errorf("breakdown = %+v", b)
		}
		if u.FetchedAt == "" {
			t.Error("FetchedAt should be set")
		}
	})

	t.Run("ManagedByAdmin", func(t *testing.T) {
		// success:true with no data object (admin-managed plan).
		u, err := parseAccountUsage(json.RawMessage(`{"success":true,"message":"Your plan is managed by admin"}`))
		if err != nil {
			t.Fatalf("parseAccountUsage: %v", err)
		}
		if u.Note != "Your plan is managed by admin" {
			t.Errorf("Note = %q", u.Note)
		}
		if len(u.Breakdowns) != 0 {
			t.Errorf("breakdowns = %v, want empty", u.Breakdowns)
		}
	})

	t.Run("InvalidProfileArn", func(t *testing.T) {
		// success:false — the exact error the wire returns without profileArn.
		_, err := parseAccountUsage(json.RawMessage(
			`{"success":false,"message":"Failed to retrieve usage information: Invalid profileArn."}`,
		))
		if err == nil {
			t.Fatal("expected error for success:false")
		}
		if err.Error() != "Failed to retrieve usage information: Invalid profileArn." {
			t.Errorf("err = %q", err.Error())
		}
	})

	t.Run("FailureNoMessage", func(t *testing.T) {
		_, err := parseAccountUsage(json.RawMessage(`{"success":false}`))
		if err == nil || err.Error() != "account usage unavailable" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("Empty", func(t *testing.T) {
		if _, err := parseAccountUsage(nil); err == nil {
			t.Fatal("expected error for empty result")
		}
	})

	t.Run("Malformed", func(t *testing.T) {
		if _, err := parseAccountUsage(json.RawMessage(`{not json`)); err == nil {
			t.Fatal("expected error for malformed JSON")
		}
	})
}

// TestAccountUsage_HandsBackWhatTheBridgeReported closes the gap between the
// parser's own table and the fetch: TestParseAccountUsage proves the shape is read
// correctly, and TestAccountUsage_CallHasTimeout proves the call is bounded, but
// nothing asserted the fetch's two outcomes actually reach the caller.
//
// It matters because the footer renders whatever comes back: a nil snapshot with a
// nil error reads as "no usage to show" rather than as a failure, so the plan and
// the credit balance quietly vanish from the UI with nothing logged anywhere.
func TestAccountUsage_HandsBackWhatTheBridgeReported(t *testing.T) {
	t.Run("a plan reply becomes the snapshot", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"Plan: KIRO POWER",` +
				`"data":{"planName":"KIRO POWER","billingCycleReset":"2026-09-01",` +
				`"usageBreakdowns":[{"resourceType":"CREDIT","displayName":"Credits",` +
				`"used":10,"limit":100,"percentage":10,"hasLimit":true}]}}`),
		}

		got, err := h.AccountUsage(t.Context())
		if err != nil {
			t.Fatalf("AccountUsage: %v", err)
		}
		if got == nil {
			t.Fatal("AccountUsage returned no snapshot and no error, so the footer shows nothing " +
				"and no failure is recorded anywhere")
		}
		if got.PlanName != "KIRO POWER" {
			t.Errorf("plan = %q, want %q", got.PlanName, "KIRO POWER")
		}
		if len(got.Breakdowns) != 1 || got.Breakdowns[0].ResourceType != "CREDIT" {
			t.Errorf("breakdowns = %+v, want the one CREDIT entry", got.Breakdowns)
		}
	})

	t.Run("an unreachable bridge is an error, not an empty snapshot", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroGetUsage: errors.New("kas gone")}

		got, err := h.AccountUsage(t.Context())
		if err == nil {
			t.Fatalf("AccountUsage reported success with %+v; the caller cannot tell a fetch that "+
				"failed from an account with nothing to report", got)
		}
		if got != nil {
			t.Errorf("AccountUsage returned %+v alongside an error, want no snapshot", got)
		}
	})
}
