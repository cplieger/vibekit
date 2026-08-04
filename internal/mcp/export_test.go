package mcp

// Tests for export.go: ACP wire-shape conversion + secret masking +
// secret-preserving merge.

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

func TestMaskedCopy_ReplacesEnvAndHeaderValues(t *testing.T) {
	s := &Server{
		Name:      "s1",
		Transport: TransportStdio,
		Env:       []KeyPair{{Name: "TOKEN", Value: "secret"}},
		Headers:   []KeyPair{{Name: "Auth", Value: "bearer"}},
		Args:      []string{"--flag", "value"},
	}
	m := maskedCopy(s)
	for _, kv := range m.Env {
		if kv.Value != SecretMask {
			t.Errorf("env %q unmasked: %q", kv.Name, kv.Value)
		}
	}
	for _, kv := range m.Headers {
		if kv.Value != SecretMask {
			t.Errorf("header %q unmasked: %q", kv.Name, kv.Value)
		}
	}
	// Original must not be mutated.
	if s.Env[0].Value != "secret" {
		t.Errorf("original env mutated: %q", s.Env[0].Value)
	}
}

func TestMaskedCopy_NilSafe(t *testing.T) {
	if got := maskedCopy(nil); got != nil {
		t.Errorf("maskedCopy(nil) = %+v, want nil", got)
	}
}

func TestRawCopy_NilSafe(t *testing.T) {
	if got := rawCopy(nil); got != nil {
		t.Errorf("rawCopy(nil) = %+v, want nil", got)
	}
}

func TestRawCopy_PreservesSecrets(t *testing.T) {
	s := &Server{
		Name: "s1",
		Env:  []KeyPair{{Name: "TOKEN", Value: "secret"}},
	}
	c := rawCopy(s)
	if c.Env[0].Value != "secret" {
		t.Errorf("rawCopy masked: %q", c.Env[0].Value)
	}
	// Mutating the copy must not affect the original.
	c.Env[0].Value = "changed"
	if s.Env[0].Value != "secret" {
		t.Errorf("rawCopy is not deep: original mutated")
	}
}

func TestMergeSecrets_PreservesMaskedValues(t *testing.T) {
	existing := []KeyPair{
		{Name: "TOKEN", Value: "original"},
		{Name: "URL", Value: "https://old"},
	}
	patch := []KeyPair{
		{Name: "TOKEN", Value: SecretMask},  // keep
		{Name: "URL", Value: "https://new"}, // replace
		{Name: "NEW", Value: "hello"},       // new field
	}
	merged := mergeSecrets(patch, existing)
	wantValues := map[string]string{"TOKEN": "original", "URL": "https://new", "NEW": "hello"}
	for _, kv := range merged {
		if wantValues[kv.Name] != kv.Value {
			t.Errorf("merged %q = %q, want %q", kv.Name, kv.Value, wantValues[kv.Name])
		}
	}
	if len(merged) != 3 {
		t.Errorf("merged has %d entries, want 3", len(merged))
	}
}

func TestMergeSecrets_MaskedWithNoPriorValueBecomesEmpty(t *testing.T) {
	// User wrote a mask for a key that doesn't exist in existing.
	// Should resolve to empty string, not the literal mask.
	merged := mergeSecrets(
		[]KeyPair{{Name: "ORPHAN", Value: SecretMask}},
		nil,
	)
	if merged[0].Value != "" {
		t.Errorf("orphaned mask = %q, want empty", merged[0].Value)
	}
}

func TestEnabledNames_ReturnsOnlyEnabled(t *testing.T) {
	tmp := t.TempDir()
	s, err := New(context.Background(), tmp, nil, WithKASConfigPath(filepath.Join(tmp, "kas-mcp.json")))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Create(context.Background(), &Server{Name: "alpha", Transport: TransportStdio, Command: "a", Enabled: true})
	_, _ = s.Create(context.Background(), &Server{Name: "beta", Transport: TransportStdio, Command: "b", Enabled: false})

	names := s.EnabledNames(context.Background())
	if _, ok := names["alpha"]; !ok {
		t.Errorf("alpha missing from EnabledNames: %+v", names)
	}
	if _, ok := names["beta"]; ok {
		t.Errorf("beta should be excluded: %+v", names)
	}
}

// F6 (partial, no rapid dep): mergeSecrets round-trip + idempotency as
// table tests. Property-based variant deferred to TODO (needs pgregory.net/rapid).
func TestMergeSecrets_IdempotentAndNoMutation(t *testing.T) {
	existing := []KeyPair{
		{Name: "TOKEN", Value: "secret"},
		{Name: "URL", Value: "https://old"},
	}
	cases := [][]KeyPair{
		{}, // empty patch
		{{Name: "TOKEN", Value: SecretMask}},
		{{Name: "TOKEN", Value: "new"}},
		{{Name: "TOKEN", Value: SecretMask}, {Name: "URL", Value: "https://new"}},
		{{Name: "ORPHAN", Value: SecretMask}},                                  // resolves to empty
		{{Name: "URL", Value: SecretMask}, {Name: "TOKEN", Value: SecretMask}}, // reordered
	}
	for _, patch := range cases {
		once := mergeSecrets(patch, existing)
		twice := mergeSecrets(once, existing)
		if !reflect.DeepEqual(once, twice) {
			t.Errorf("not idempotent:\n patch=%+v\n once=%+v\n twice=%+v", patch, once, twice)
		}
		if existing[0].Value != "secret" || existing[1].Value != "https://old" {
			t.Errorf("mergeSecrets mutated existing: %+v", existing)
		}
	}
}

// FuzzMergeSecrets verifies idempotency and no-mutation invariants for
// the secret-preserving merge under arbitrary inputs.
func FuzzMergeSecrets(f *testing.F) {
	// Seed corpus: representative shapes.
	f.Add("TOKEN", "secret", "TOKEN", SecretMask)
	f.Add("URL", "https://old", "URL", "https://new")
	f.Add("A", "val", "B", SecretMask)
	f.Add("", "", "", "")

	f.Fuzz(func(t *testing.T, eName, eVal, pName, pVal string) {
		existing := []KeyPair{{Name: eName, Value: eVal}}
		patch := []KeyPair{{Name: pName, Value: pVal}}

		// Snapshot existing to detect mutation.
		origEVal := existing[0].Value

		once := mergeSecrets(patch, existing)
		twice := mergeSecrets(once, existing)

		// Idempotency: merging the result again yields the same output.
		if !reflect.DeepEqual(once, twice) {
			t.Errorf("not idempotent:\n patch=%+v\n once=%+v\n twice=%+v", patch, once, twice)
		}
		// No mutation of existing slice.
		if existing[0].Value != origEVal {
			t.Errorf("existing mutated: got %q, want %q", existing[0].Value, origEVal)
		}
		// Output length equals patch length.
		if len(once) != len(patch) {
			t.Errorf("output len=%d, want %d (patch len)", len(once), len(patch))
		}
	})
}

// TestMergeSecrets_RapidRoundTrip is a property-based test verifying the
// mergeSecrets invariants across arbitrary key sets:
//   - len(output) == len(patch)
//   - if patch[i].Value == SecretMask AND name exists in existing → output uses existing's value
//   - if patch[i].Value != SecretMask → output uses patch's value verbatim
//   - if patch[i].Value == SecretMask AND name NOT in existing → output is ""
//   - existing slice is never mutated
func TestMergeSecrets_RapidRoundTrip(t *testing.T) {
	genKeyPair := rapid.Custom(func(t *rapid.T) KeyPair {
		return KeyPair{
			Name:  rapid.StringMatching(`[a-zA-Z_][a-zA-Z0-9_]{0,15}`).Draw(t, "name"),
			Value: rapid.StringN(0, 64, -1).Draw(t, "value"),
		}
	})

	rapid.Check(t, func(t *rapid.T) {
		existing := rapid.SliceOfN(genKeyPair, 0, 10).Draw(t, "existing")
		patchLen := rapid.IntRange(0, 10).Draw(t, "patchLen")

		// Build patch: randomly choose SecretMask or a real value for each entry.
		patch := make([]KeyPair, patchLen)
		for i := range patch {
			patch[i].Name = rapid.StringMatching(`[a-zA-Z_][a-zA-Z0-9_]{0,15}`).Draw(t, "patchName")
			if rapid.Bool().Draw(t, "useMask") {
				patch[i].Value = SecretMask
			} else {
				patch[i].Value = rapid.StringN(0, 64, -1).Draw(t, "patchValue")
			}
		}

		// Snapshot existing to detect mutation.
		existingSnapshot := make([]KeyPair, len(existing))
		copy(existingSnapshot, existing)

		output := mergeSecrets(patch, existing)

		// Invariant 1: output length equals patch length.
		if len(output) != len(patch) {
			t.Fatalf("len(output)=%d, want len(patch)=%d", len(output), len(patch))
		}

		// Build lookup for existing values.
		existingIndex := make(map[string]string, len(existing))
		for _, kv := range existing {
			existingIndex[kv.Name] = kv.Value
		}

		// Invariant 2 & 3: per-element value correctness.
		for i, kv := range patch {
			if kv.Value == SecretMask {
				if prev, ok := existingIndex[kv.Name]; ok {
					// Masked + exists → preserve existing value.
					if output[i].Value != prev {
						t.Fatalf("output[%d].Value=%q, want existing value %q (masked, name=%q)",
							i, output[i].Value, prev, kv.Name)
					}
				} else {
					// Masked + not in existing → empty string.
					if output[i].Value != "" {
						t.Fatalf("output[%d].Value=%q, want \"\" (masked orphan, name=%q)",
							i, output[i].Value, kv.Name)
					}
				}
			} else {
				// Not masked → verbatim.
				if output[i].Value != kv.Value {
					t.Fatalf("output[%d].Value=%q, want patch value %q (name=%q)",
						i, output[i].Value, kv.Value, kv.Name)
				}
			}
			// Name is always preserved from patch.
			if output[i].Name != kv.Name {
				t.Fatalf("output[%d].Name=%q, want %q", i, output[i].Name, kv.Name)
			}
		}

		// Invariant 4: existing slice not mutated.
		if !reflect.DeepEqual(existing, existingSnapshot) {
			t.Fatalf("existing was mutated: got %+v, want %+v", existing, existingSnapshot)
		}
	})
}
