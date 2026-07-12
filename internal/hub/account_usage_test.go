package hub

import (
	"encoding/json"
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
			`{"success":false,"message":"Failed to retrieve usage information: Invalid profileArn."}`))
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
