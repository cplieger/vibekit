package translate

// The runtime wire census.
//
// Serial, never parallel: these tests swap slog's process-global default and
// reset the package-global ledger. That is the same constraint captureSlog
// already carries, stated again because the ledger adds a second reason.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// resetCensus empties the ledger and un-latches it for one test, restoring
// nothing afterwards — a fresh map per test is the isolation, and a leftover
// ledger from an earlier test is exactly what would make one of these pass
// vacuously.
func resetCensus(t *testing.T) {
	t.Helper()
	census.mu.Lock()
	census.reported = make(map[string]struct{}, maxCensusKeys)
	census.off = false
	census.mu.Unlock()
	t.Cleanup(func() {
		census.mu.Lock()
		census.reported = make(map[string]struct{}, maxCensusKeys)
		census.off = false
		census.mu.Unlock()
	})
}

// TestCensusMeta_ReportsAnUnknownFieldOnce is the mechanism: a member KAS sends
// that the struct does not read reaches the log, by name and JSON type, exactly
// once however many frames carry it.
func TestCensusMeta_ReportsAnUnknownFieldOnce(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	raw := json.RawMessage(`{"kind":"agent-subtask","cacheReadTokens":41}`)
	for range 3 {
		censusMeta("_meta.kiro", raw, reflect.TypeFor[acpKiroBlockShadow]())
	}

	out := logbuf.String()
	if n := strings.Count(out, "UNKNOWN _meta.kiro field"); n != 1 {
		t.Errorf("reported %d times for 3 identical frames, want 1: %s", n, out)
	}
	for _, want := range []string{"cachereadtokens", "type=number"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("report does not carry %q: %s", want, out)
		}
	}
	// A known field must not be reported, or the probe is noise on every frame.
	if strings.Contains(out, `field=kind`) {
		t.Errorf("reported a field the struct reads: %s", out)
	}
}

// TestCensusMeta_NeverLogsAValue is the safety property, and the reason the probe
// reads one byte of each value rather than decoding it. A field's contents cannot
// leak from code that never materializes them.
func TestCensusMeta_NeverLogsAValue(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	const secret = "ya29.A0ARrdaM-NOT-A-REAL-TOKEN"
	censusMeta("_meta.kiro", json.RawMessage(`{"someNewAuthField":"`+secret+`"}`),
		reflect.TypeFor[acpKiroBlockShadow]())

	out := logbuf.String()
	if !strings.Contains(out, "UNKNOWN") {
		t.Fatalf("the field was not reported at all: %s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the census logged a field VALUE: %s", out)
	}
	if !strings.Contains(out, "type=string") {
		t.Errorf("the census dropped the type, which is the whole payload: %s", out)
	}
}

// TestCensusMeta_FoldsCase is the subtle one, and it is why knownKeysOf
// lowercases.
//
// encoding/json matches object members case-insensitively, so a frame sending
// `MessageId` IS consumed by the `messageId` field. Comparing case-sensitively
// would report a field that was read — a false finding, which is worse than none
// because it teaches the reader to ignore the probe.
func TestCensusMeta_FoldsCase(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	censusMeta("_meta.kiro", json.RawMessage(`{"MessageId":"m-1","AGENTSUBTASKID":"s-1"}`),
		reflect.TypeFor[acpKiroBlockShadow]())

	if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
		t.Errorf("reported a field encoding/json consumed by case-insensitive match: %s", out)
	}
}

// TestCensusMeta_DeclinedFieldsAreQuiet: `preview` is skipped on purpose, so
// reporting it would fire on the first frame of every file write and get the
// probe muted before it could say anything real. The declined list is not
// optional decoration.
func TestCensusMeta_DeclinedFieldsAreQuiet(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	censusMeta("_meta.kiro", json.RawMessage(`{"preview":{"originalContent":"..."}}`),
		reflect.TypeFor[acpKiroBlockShadow](), "preview")

	if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
		t.Errorf("reported a deliberately-declined field: %s", out)
	}
}

// TestCensusMeta_IsBounded covers both bounds. The per-frame one stops a hostile
// block from turning one frame into a hundred thousand map inserts; the
// per-process one latches the probe off rather than growing an unbounded map
// keyed on backend-controlled text.
func TestCensusMeta_IsBounded(t *testing.T) {
	t.Run("an oversized object is skipped", func(t *testing.T) {
		resetCensus(t)
		var logbuf bytes.Buffer
		defer captureSlog(&logbuf)()

		big := `{"novelField":"` + strings.Repeat("x", maxCensusObjectBytes) + `"}`
		censusMeta("_meta.kiro", json.RawMessage(big), reflect.TypeFor[acpKiroBlockShadow]())

		if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
			t.Errorf("scanned an object past the per-frame bound: %s", out)
		}
	})

	t.Run("the key budget latches the probe off", func(t *testing.T) {
		resetCensus(t)
		var logbuf bytes.Buffer
		defer captureSlog(&logbuf)()

		// One novel field per frame, well past the cap.
		for i := range maxCensusKeys * 2 {
			censusMeta("_meta.kiro",
				json.RawMessage(`{"f`+string(rune('a'+i%26))+string(rune('a'+i/26))+`":1}`),
				reflect.TypeFor[acpKiroBlockShadow]())
		}

		out := logbuf.String()
		if !strings.Contains(out, "field budget spent") {
			t.Errorf("the budget was never reported as spent: %s", out)
		}
		if !census.disabled() {
			t.Error("the ledger did not latch off at its cap; the map grows unbounded on backend-controlled keys")
		}
		if got := strings.Count(out, "UNKNOWN _meta.kiro field"); got > maxCensusKeys {
			t.Errorf("reported %d fields, want at most the %d-key cap", got, maxCensusKeys)
		}
	})
}

// TestCensusMeta_SanitizesTheFieldName: a field name is backend-controlled text
// on a logfmt line, so a raw newline in it forges a log record and an ANSI
// sequence repaints the terminal of whoever tails it.
//
// The BOUND is what this asserts on, plus the absence of an escaped newline.
// Asserting only "no raw newline in the output" would be vacuous: slog's
// TextHandler quotes and escapes a value containing one, so that assertion passes
// with the sanitizer removed — measured, not assumed. The length cap is entirely
// this package's, so it distinguishes.
func TestCensusMeta_SanitizesTheFieldName(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	// A name carrying a newline, a forged record after it, and far more bytes
	// than the cap allows.
	name := "evil\nlevel=ERROR msg=\"forged\" " + strings.Repeat("x", 4096)
	raw, err := json.Marshal(map[string]int{name: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	censusMeta("_meta.kiro", raw, reflect.TypeFor[acpKiroBlockShadow]())

	out := logbuf.String()
	if !strings.Contains(out, "UNKNOWN") {
		t.Fatalf("the field was not reported at all: %s", out)
	}
	// The escaped form slog would render an unsanitized newline as. runesafe
	// replaces the newline with a space, so neither spelling should survive.
	if strings.Contains(out, `\n`) {
		t.Errorf("an unsanitized newline reached the log line: %q", out)
	}
	// The cap is this package's own, so it is the assertion that fails when the
	// sanitize-and-bound call is removed.
	if len(out) > len(name) {
		t.Errorf("the log line is %d bytes for a %d-byte name; the field name was not bounded",
			len(out), maxCensusNameBytes)
	}
}

// TestCensusMeta_NeverBreaksADecode is the hard rule for a diagnostic, asserted
// through the real decode path rather than on the helper: every call site drops
// the frame when its decode errors, so a probe able to contribute an error would
// stop tool cards from rendering.
func TestCensusMeta_NeverBreaksADecode(t *testing.T) {
	resetCensus(t)
	for name, frame := range map[string]string{
		"unknown field":     `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":{"brandNew":1}}}`,
		"null meta":         `{"content":{"type":"text","text":"hi"},"_meta":null}`,
		"null kiro":         `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":null}}`,
		"kiro is a string":  `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":"nope"}}`,
		"kiro is an array":  `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":[1,2]}}`,
		"empty kiro object": `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var chunk ACPChunkWire
			err := json.Unmarshal([]byte(frame), &chunk)
			// A wrongly-typed kiro block is the caller's decode error to report,
			// not the census's to invent — what matters is that the probe never
			// ADDS one, which the unknown-field and empty rows pin.
			if err != nil && !strings.Contains(name, "string") && !strings.Contains(name, "array") {
				t.Fatalf("decode failed: %v", err)
			}
			if err == nil && chunk.Content.Text != "hi" {
				t.Errorf("text = %q, want the frame decoded normally", chunk.Content.Text)
			}
		})
	}
}

// TestCensusMeteringUnit_ReportsAnUnsummedUnit is the one place a VALUE is
// reported, because there the label IS the discovery: `unit` is a field vibekit
// reads, so no field-name probe can see that KAS started billing in a new
// dimension. An unrecognised unit is silently dropped from the spend total today.
func TestCensusMeteringUnit_ReportsAnUnsummedUnit(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	censusMeteringUnit("cacheRead")
	censusMeteringUnit("cacheRead")
	censusMeteringUnit(meteringUnitCredit)
	censusMeteringUnit("")

	out := logbuf.String()
	if n := strings.Count(out, "UNKNOWN metering unit"); n != 1 {
		t.Errorf("reported %d times, want 1 (once per process per label): %s", n, out)
	}
	if !strings.Contains(out, "cacheRead") {
		t.Errorf("the report does not name the unit, which is the whole finding: %s", out)
	}
	if strings.Contains(out, "unit=credit") {
		t.Errorf("reported the unit that IS summed: %s", out)
	}
}

// TestKnownKeysOf_CoversTheWholeWireBlock pins the derived set against the two
// carriers it describes.
//
// Derived from the struct tags rather than hand-listed, so the set cannot drift
// from the parser — a hand-written list goes stale silently, and in the direction
// that produces a false report. These assertions exist to catch the two ways the
// walk itself can be wrong: missing a field, and failing to lowercase.
func TestKnownKeysOf_CoversTheWholeWireBlock(t *testing.T) {
	cases := map[string]struct {
		typ  reflect.Type
		want []string
	}{
		"_meta.kiro": {
			typ: reflect.TypeFor[acpKiroBlockShadow](),
			want: []string{
				"refusal", "checkpoint", "disclosedcontext", "policydenial", "kind",
				"agentsubtaskid", "messageid", "timestamp", "notification", "workflow", "hookask",
			},
		},
		"session_info_update._meta.kiro": {
			typ: reflect.TypeFor[sessionInfoKiroShadow](),
			want: []string{
				"summarization", "usagepercentage", "workflow", "contextusage", "focus",
				"messageids", "messageid", "content", "notificationseverity", "kind",
				"promptturnsummaries", "elapsedtime",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := knownKeysOf(tc.typ)
			for _, key := range tc.want {
				if _, ok := got[key]; !ok {
					// Report the whole set once rather than per missing key.
					t.Errorf("derived key set is missing %q; keys=%v", key, sortedKeys(got))
				}
			}
			for key := range got {
				if key != strings.ToLower(key) {
					t.Errorf("derived key %q is not lowercased; encoding/json matches members case-insensitively", key)
				}
			}
		})
	}
}

// sortedKeys renders a set for a failure message.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestJSONKindOf names every shape a member can take, because the type is the
// entire payload of a report.
func TestJSONKindOf(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:  "object",
		`[1,2]`:    "array",
		`"text"`:   "string",
		`true`:     "bool",
		`false`:    "bool",
		`null`:     "null",
		`42`:       "number",
		`-1.5e3`:   "number",
		"  \n\t{}": "object",
		``:         "empty",
	}
	for raw, want := range cases {
		if got := jsonKindOf(json.RawMessage(raw)); got != want {
			t.Errorf("jsonKindOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestSessionInfoUpdate_CensusRunsOnTheRealFrame closes the loop: the probe has to
// fire through the ordinary handler, not only when called directly. A census wired
// into a type nothing decodes reports nothing forever.
func TestSessionInfoUpdate_CensusRunsOnTheRealFrame(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	tr.HandleSessionInfoUpdate(t.Context(), vibekit.ChatID("c1"), mustJSON(t, map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{
			"kind": "turn_end", "brandNewBlock": map[string]any{"x": 1},
		}},
	}), FrameAttribution{})

	out := logbuf.String()
	if !strings.Contains(out, "UNKNOWN _meta.kiro field") {
		t.Errorf("the census did not run on a real session_info_update: %s", out)
	}
	if !strings.Contains(out, "type=object") {
		t.Errorf("the report does not name the new member's type: %s", out)
	}
}

// TestKnownKeys_IsCachedPerType: censusMeta runs on every frame, and
// agent_message_chunk arrives per token, so a reflection walk plus a map
// allocation per frame is the one way this diagnostic could reach a profile.
// Asserted by identity — the cache must return the SAME map, not an equal one.
func TestKnownKeys_IsCachedPerType(t *testing.T) {
	first := knownKeys(reflect.TypeFor[acpKiroBlockShadow]())
	second := knownKeys(reflect.TypeFor[acpKiroBlockShadow]())
	if len(first) == 0 {
		t.Fatal("derived no keys at all")
	}
	// Two calls must not produce two maps. Compared with reflect.ValueOf pointers
	// because Go forbids comparing maps directly.
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Error("knownKeys rebuilt the set; the walk and its allocation run per frame")
	}
}

// TestCensusMeta_DoesNotMutateTheCachedSet is the hazard the caching introduces
// and the reason declined names are scanned rather than merged: the cached map is
// shared by every caller, so folding one caller's declined list into it would
// silence that name for all of them.
func TestCensusMeta_DoesNotMutateTheCachedSet(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	typ := reflect.TypeFor[acpKiroBlockShadow]()
	before := len(knownKeys(typ))

	censusMeta("_meta.kiro", json.RawMessage(`{"preview":1}`), typ, "preview")

	if after := len(knownKeys(typ)); after != before {
		t.Errorf("the shared key set grew from %d to %d; a declined name leaked into it",
			before, after)
	}
	// And the decline still has to work, or the test above passes for the wrong
	// reason.
	if strings.Contains(logbuf.String(), "UNKNOWN") {
		t.Errorf("the declined name was reported: %s", logbuf.String())
	}
}

// BenchmarkCensusMeta prices the probe on the streaming hot path. Run with
// -benchmem; the interesting number is allocations per frame, which the key-set
// cache is what keeps down.
func BenchmarkCensusMeta(b *testing.B) {
	raw := json.RawMessage(`{"kind":"agent-subtask","agentSubtaskId":"s-1",` +
		`"messageId":"m-1-say","timestamp":"2026-08-21T10:00:00.000Z"}`)
	typ := reflect.TypeFor[acpKiroBlockShadow]()
	b.ReportAllocs()
	for b.Loop() {
		censusMeta("_meta.kiro", raw, typ, "preview")
	}
}
