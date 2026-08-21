package translate

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/cplieger/runesafe"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// A DECISION SURFACE is a payload a human reads to make an approval choice:
// the permission card, the agent's question card, an MCP elicitation form. The
// threat these tests pin is decision integrity rather than display fidelity —
// an agent that reaches a poisoned file can compose a tool-call title, and a
// Bidi override in it renders one command while a different one is what
// pressing Allow approves.
//
// deceptiveTitle is that attack, written the way it would arrive. U+202E
// RIGHT-TO-LEFT OVERRIDE forces the following text to render right-to-left, so
// the reversed span below reads, on screen, as
//
//	Run rm -rf /workspace -Name-found
//
// backwards — i.e. an innocuous `find` — while the bytes an approving user
// authorizes are the rm. U+202C POP DIRECTIONAL FORMATTING closes the span so
// the deception is invisible at the edges.
const (
	rlo             = "\u202e"
	pdf             = "\u202c"
	deceptiveTitle  = "Run " + rlo + "dnuof-emaN- ecapskrow/ fr- mr" + pdf
	deceptiveOption = "Reject" + rlo + "wollA" + pdf
)

// assertNeutralizedOnTheWire is the assertion that matters: not "the Go string
// changed" but "the JSON the browser receives carries no direction override".
// It marshals the payload exactly as the SSE writer does, so a field that was
// sanitized into a copy the payload does not hold would still fail here.
func assertNeutralizedOnTheWire(t *testing.T, what string, payload any) {
	t.Helper()
	wire, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("%s: marshal payload: %v", what, err)
	}
	// json.Marshal emits the Bidi controls as \u202e escapes rather than raw
	// bytes, so decode back to text before counting runes — an escape is still
	// an override once the browser parses it.
	var decoded any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("%s: unmarshal payload: %v", what, err)
	}
	for _, r := range flatten(decoded) {
		if runesafe.IsBidiControl(r) {
			t.Errorf("%s: U+%04X survived to the wire; payload=%s", what, r, wire)
		}
		if unicode.IsControl(r) {
			t.Errorf("%s: control U+%04X survived to the wire; payload=%s", what, r, wire)
		}
	}
}

// flatten walks a decoded JSON value and returns every rune in every string it
// contains, keys included. Recursive rather than a regexp over the raw bytes
// because a nested option or sub-option is exactly where a partial fix hides.
func flatten(v any) []rune {
	var out []rune
	switch t := v.(type) {
	case string:
		out = append(out, []rune(t)...)
	case []any:
		for _, e := range t {
			out = append(out, flatten(e)...)
		}
	case map[string]any:
		for k, e := range t {
			out = append(out, []rune(k)...)
			out = append(out, flatten(e)...)
		}
	}
	return out
}

// TestPermissionCard_NeutralizesADeceptiveTitleOnTheWire is the primary proof.
// The title reaching the client must no longer be able to reorder itself, and
// the reversed span must still be PRESENT — the fix replaces each control with a
// space rather than deleting it, so the deception becomes visible instead of
// vanishing along with the evidence of it.
func TestPermissionCard_NeutralizesADeceptiveTitleOnTheWire(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(9001)
	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-1",
				"title":      deceptiveTitle,
				"kind":       "execute",
			},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "deny", "name": deceptiveOption, "kind": "reject_once"},
			},
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	assertNeutralizedOnTheWire(t, "permission_needed", got)

	// The reversed text is still there, now readable as the nonsense it is.
	if !strings.Contains(got.Title, "mr") || !strings.Contains(got.Title, "ecapskrow/") {
		t.Errorf("Title = %q: the reversed span was deleted rather than exposed", got.Title)
	}
	if strings.Contains(got.Title, rlo) {
		t.Errorf("Title = %q still carries U+202E", got.Title)
	}
	// The button label is a decision surface too.
	if len(got.Options) != 2 {
		t.Fatalf("len(Options) = %d, want 2", len(got.Options))
	}
	if strings.Contains(got.Options[1].Name, rlo) {
		t.Errorf("Options[1].Name = %q still carries U+202E", got.Options[1].Name)
	}
	// The identifiers the answer is keyed on are NOT display text and must
	// arrive byte-identical, or the approval means something else.
	if got.Options[1].OptionID != "deny" || got.Options[1].Kind != "reject_once" {
		t.Errorf("Options[1] identifiers changed: %+v", got.Options[1])
	}
}

// TestPermissionCard_LeavesLegitimateTitlesByteIdentical is the other half of
// the ruling: the cost is paid only by text that could also be the attack.
// Measured on runesafe v1.4.2 — pure RTL survives because the Unicode bidi
// algorithm derives direction from strong characters alone.
func TestPermissionCard_LeavesLegitimateTitlesByteIdentical(t *testing.T) {
	for _, title := range []string{
		"Write config.tf",
		"Créer le répertoire naïve",
		"מחק את כל הקבצים",  // Hebrew, pure RTL
		"احذف جميع الملفات", // Arabic, pure RTL
		"設定ファイルを書き込む",
		"설정 파일 쓰기",
		"เขียนไฟล์การตั้งค่า",
		"फ़ाइल लिखें",
		"Deploy 🚀 to prod",
		"Write שלום now", // mixed scripts, no explicit marks
	} {
		deps, events := newEventCaptureDeps()
		tr := New(rolesOf(deps))
		id := int64(1)
		tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
			ID: &id,
			Params: mustJSON(t, map[string]any{
				"sessionId": "s",
				"toolCall":  map[string]any{"toolCallId": "tc", "title": title, "kind": "edit"},
				"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
			}),
		})
		got, ok := findPermissionNeeded(t, events)
		if !ok {
			t.Fatalf("%q: no permission_needed event", title)
		}
		if got.Title != title {
			t.Errorf("Title = %q, want byte-identical %q", got.Title, title)
		}
	}
}

// TestPermissionCard_MixedScriptWithExplicitMarksIsTheWholeCost states the one
// loss, so it is a recorded decision rather than a surprise: a label that MIXES
// scripts AND relies on explicit direction marks loses those marks to spaces.
// A tool-call title cannot need them without also being the shape the attack
// takes, which is why the trade is accepted.
func TestPermissionCard_MixedScriptWithExplicitMarksIsTheWholeCost(t *testing.T) {
	const lrm = "\u200e"
	title := "Write " + lrm + "שלום" + lrm + " now"

	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(1)
	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc", "title": title, "kind": "edit"},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		}),
	})
	got, _ := findPermissionNeeded(t, events)
	if want := "Write  שלום  now"; got.Title != want {
		t.Errorf("Title = %q, want %q (the two LRMs become spaces)", got.Title, want)
	}
}

// TestUserInputCard_NeutralizesEveryLabelOnTheWire covers the second decision
// surface at both nesting levels. The question, each option's title and
// description, the sub-options label, and each sub-option's title and
// description are all agent-composed and all rendered.
func TestUserInputCard_NeutralizesEveryLabelOnTheWire(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &pendingCaptureDeps{baseDeps: base}
	tr := New(rolesOf(deps))
	reqID := int64(42)

	tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, &reqID, map[string]any{
		"sessionId": "s",
		"question":  "Apply " + rlo + "?sehctap 41 lla" + pdf,
		"options": []map[string]any{{
			"title":           "Apply" + rlo + "enon" + pdf,
			"description":     "Writes" + rlo + "gnihton" + pdf,
			"subOptionsLabel": "Include" + rlo + ":" + pdf,
			"subOptions": []map[string]any{
				{"title": "Tests" + rlo + "yortsed" + pdf, "description": "Also" + rlo + "setirwrevo" + pdf},
			},
		}},
	}))

	var got *vibekit.UserInputNeededPayload
	for _, e := range *events {
		if e.Type == vibekit.EventUserInputNeeded {
			p := e.Payload.(vibekit.UserInputNeededPayload)
			got = &p
		}
	}
	if got == nil {
		t.Fatal("no user_input_needed event broadcast")
	}
	assertNeutralizedOnTheWire(t, "user_input_needed", *got)

	if len(got.Options) != 1 || len(got.Options[0].SubOptions) != 1 {
		t.Fatalf("shape = %d options / %d sub-options, want 1/1", len(got.Options), len(got.Options[0].SubOptions))
	}
}

// TestUserInputCard_TitleIsSanitizedBeforeTheDropAndDedupRules pins the ORDER,
// which is load-bearing twice.
//
// A title of nothing but a direction override sanitizes to a single space, so
// trimming AFTER empties it and the drop rule removes it. Trimming first would
// leave " " — a card rendering as blank space whose answer, since the reply
// carries the title, is the raw override. And two titles differing only in
// invisible controls collapse to one only when the dedup keys on the sanitized
// form; keyed on the raw text both survive as visually identical cards, which is
// the ambiguity the dedup exists to prevent.
func TestUserInputCard_TitleIsSanitizedBeforeTheDropAndDedupRules(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &pendingCaptureDeps{baseDeps: base}
	tr := New(rolesOf(deps))
	reqID := int64(43)

	tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, &reqID, map[string]any{
		"sessionId": "s",
		"question":  "Pick one",
		"options": []map[string]any{
			{"title": rlo},               // invisible-only: must be DROPPED
			{"title": "Proceed"},         //
			{"title": "Pro\u200eceed"},   // same rendered text: must be DEDUPED away
			{"title": "\u202dProceed 2"}, // distinct once sanitized: kept
		},
	}))

	var got *vibekit.UserInputNeededPayload
	for _, e := range *events {
		if e.Type == vibekit.EventUserInputNeeded {
			p := e.Payload.(vibekit.UserInputNeededPayload)
			got = &p
		}
	}
	if got == nil {
		t.Fatal("no user_input_needed event broadcast")
	}
	titles := make([]string, len(got.Options))
	for i, o := range got.Options {
		titles[i] = o.Title
	}
	// "Pro ceed" is what "Pro<LRM>ceed" sanitizes to, so it is NOT a duplicate
	// of "Proceed" — the dedup catches identical rendered text, not similar.
	want := []string{"Proceed", "Pro ceed", "Proceed 2"}
	if len(titles) != len(want) {
		t.Fatalf("titles = %q, want %q", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("titles = %q, want %q", titles, want)
			break
		}
	}
	for _, ti := range titles {
		if strings.TrimSpace(ti) == "" {
			t.Errorf("an invisible-only title survived as %q instead of being dropped", ti)
		}
	}
}

// TestElicitationForm_NeutralizesItsMessageOnTheWire covers the third surface.
// An MCP server is further from vibekit's trust than the agent is, and accept /
// decline is an approval choice like any other.
func TestElicitationForm_NeutralizesItsMessageOnTheWire(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &pendingCaptureDeps{baseDeps: base}
	tr := New(rolesOf(deps))
	id := int64(77)

	tr.HandleElicitationCreate(t.Context(), "c1", &vibekit.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId":  "s",
			"toolCallId": "tc",
			"elicitation": map[string]any{
				"mode":    "form",
				"message": "Share " + rlo + "gnihton" + pdf,
			},
		}),
	})

	var got *vibekit.ElicitationNeededPayload
	for _, e := range *events {
		if e.Type == vibekit.EventElicitationNeeded {
			p := e.Payload.(vibekit.ElicitationNeededPayload)
			got = &p
		}
	}
	if got == nil {
		t.Fatal("no elicitation_needed event broadcast")
	}
	assertNeutralizedOnTheWire(t, "elicitation_needed", *got)
}

// TestDisplayText_BoundsAnUnboundedUpstreamString pins the second half of the
// treatment. Nothing on the wire bounds a title, a question or a provider
// message, and each lands in an SSE payload the server also logs.
func TestDisplayText_BoundsAnUnboundedUpstreamString(t *testing.T) {
	// A 3-byte rune repeated, so a naive byte cut would split one.
	long := strings.Repeat("設", 400)
	got := displayText(long)
	if len(got) > maxDisplayTextBytes+len("...") {
		t.Errorf("len = %d bytes, want <= %d", len(got), maxDisplayTextBytes+3)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a truncated result must end in the cut marker; got %q", got[max(0, len(got)-12):])
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Error("the cut split a rune")
	}
	// Within the cap, byte-identical: no marker on a value that was not cut.
	if short := "Write config.tf"; displayText(short) != short {
		t.Errorf("displayText(%q) = %q, want byte-identical", short, displayText(short))
	}
}
