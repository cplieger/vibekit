package chat

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/api"
)

// outcomeFixture mirrors testdata/turn_outcomes.json. See that file's _comment
// for why the table is shared with the TypeScript side rather than duplicated.
type outcomeFixture struct {
	Cases []struct {
		Name string `json:"name"`
		Body []struct {
			Refusal bool   `json:"refusal"`
			Event   string `json:"event"`
		} `json:"body"`
		Want   string `json:"want"`
		IsLive bool   `json:"is_live"`
	} `json:"cases"`
}

// TestTurnOutcomeContract is one half of a cross-language pin: turns.test.ts
// runs the same table against the TypeScript implementation. A rule changed in
// only one language fails in the other.
func TestTurnOutcomeContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/turn_outcomes.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx outcomeFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture carries no cases; a silently-empty table would pass forever")
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			body := make([]api.Message, 0, len(tc.Body))
			for _, b := range tc.Body {
				m := api.Message{Role: api.RoleAssistant}
				if b.Refusal {
					m.Refusal = &api.RefusalInfo{}
				}
				if b.Event != "" {
					m.Role = api.RoleEvent
					m.EventKind = api.EventKind(b.Event)
				}
				body = append(body, m)
			}
			if got := deriveTurnOutcome(body, tc.IsLive); string(got) != tc.Want {
				t.Errorf("deriveTurnOutcome = %q, want %q", got, tc.Want)
			}
		})
	}
}

func user(id, content string, ts int64) api.Message {
	return api.Message{ID: id, Role: api.RoleUser, Content: content, Ts: ts}
}

func assistant(id string, ts int64) api.Message {
	return api.Message{ID: id, Role: api.RoleAssistant, Content: "ok", Ts: ts}
}

func TestProjectTurnSummaries_PromotesTheUserMessageAndNumbersFromOne(t *testing.T) {
	got := projectTurnSummaries([]api.Message{
		user("u1", "first thing", 100),
		assistant("a1", 200),
		user("u2", "second thing", 300),
		assistant("a2", 400),
	}, false)

	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2", len(got))
	}
	if got[0].ID != "u1" || got[0].N != 1 || got[0].Ts != 100 {
		t.Errorf("turn 1 = %+v", got[0])
	}
	if got[0].FirstLine != "first thing" {
		t.Errorf("turn 1 first line = %q", got[0].FirstLine)
	}
	if got[1].ID != "u2" || got[1].N != 2 || got[1].Ts != 300 {
		t.Errorf("turn 2 = %+v", got[1])
	}
	for _, s := range got {
		if s.AgentInitiated {
			t.Errorf("turn %d marked agent-initiated", s.N)
		}
	}
}

// A turn with no user row is both the agent-initiated case and the case where a
// transcript legitimately begins mid-turn. Marking it lets the rail render it as
// a non-user marker instead of implying the user asked for it.
func TestProjectTurnSummaries_MarksAHeaderlessTurn(t *testing.T) {
	got := projectTurnSummaries([]api.Message{
		assistant("a1", 100),
		user("u1", "then this", 200),
		assistant("a2", 300),
	}, false)

	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2", len(got))
	}
	if !got[0].AgentInitiated {
		t.Error("leading assistant turn not marked agent-initiated")
	}
	if got[0].ID != "a1" || got[0].Ts != 100 {
		t.Errorf("turn 1 = %+v", got[0])
	}
	if got[0].FirstLine != "" {
		t.Errorf("turn 1 invented a first line: %q", got[0].FirstLine)
	}
	if got[1].AgentInitiated {
		t.Error("turn 2 wrongly marked agent-initiated")
	}
}

func TestProjectTurnSummaries_MarksOnlyTheLastTurnRunning(t *testing.T) {
	got := projectTurnSummaries([]api.Message{
		user("u1", "a", 100),
		assistant("a1", 200),
		user("u2", "b", 300),
	}, true)

	if got[0].Outcome != api.TurnOutcomeCompleted {
		t.Errorf("turn 1 outcome = %q, want completed", got[0].Outcome)
	}
	if got[1].Outcome != api.TurnOutcomeRunning {
		t.Errorf("turn 2 outcome = %q, want running", got[1].Outcome)
	}
}

// An empty result must marshal as [] rather than null: the client iterates it,
// and a null would be a second empty case for every caller to remember.
func TestProjectTurnSummaries_EmptyMarshalsAsArray(t *testing.T) {
	got := projectTurnSummaries(nil, false)
	if got == nil {
		t.Fatal("got nil slice")
	}
	b, err := json.Marshal(map[string]any{"turns": got})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"turns":[]}` {
		t.Errorf("marshalled %s", b)
	}
}

func TestProjectTurnSummaries_FirstLineCollapsesWhitespace(t *testing.T) {
	got := projectTurnSummaries([]api.Message{
		user("u1", "  line one\n\n\tline two   ", 100),
	}, false)
	if got[0].FirstLine != "line one line two" {
		t.Errorf("first line = %q", got[0].FirstLine)
	}
}

func TestProjectTurnSummaries_FirstLineTruncatesOnRuneBoundary(t *testing.T) {
	// Multi-byte runes: a byte-wise cut would split one and produce mojibake.
	long := strings.Repeat("\u00e9", 200)
	got := projectTurnSummaries([]api.Message{user("u1", long, 100)}, false)
	line := got[0].FirstLine
	if !strings.HasSuffix(line, "\u2026") {
		t.Fatalf("no ellipsis on a truncated line: %q", line)
	}
	body := strings.TrimSuffix(line, "\u2026")
	if n := len([]rune(body)); n != turnFirstLineMax {
		t.Errorf("kept %d runes, want %d", n, turnFirstLineMax)
	}
	if !strings.HasPrefix(long, body) {
		t.Error("truncation corrupted the text")
	}
}

func TestProjectTurnSummaries_FirstLineKeepsAShortRequestWhole(t *testing.T) {
	got := projectTurnSummaries([]api.Message{user("u1", "short", 100)}, false)
	if got[0].FirstLine != "short" {
		t.Errorf("first line = %q", got[0].FirstLine)
	}
}

// FuzzFirstLineBoundedAndWellFormed guards the one untrusted-input boundary in
// this file: a turn's first line is derived from user prompt CONTENT, which is
// arbitrary text, and it is truncated by rune count. A byte-wise cut would emit
// a split rune, and an unbounded result would put a pasted file into every rail
// marker's hover label.
func FuzzFirstLineBoundedAndWellFormed(f *testing.F) {
	f.Add("")
	f.Add("plain request")
	f.Add("  leading and trailing  ")
	f.Add("tabs\tand\nnewlines\r\nmixed")
	f.Add(strings.Repeat("a", 500))
	f.Add(strings.Repeat("\u00e9", 500))
	f.Add(strings.Repeat("\U0001f600", 200))
	f.Add("\u0000\u0001 control chars")
	f.Add(strings.Repeat(" ", 400))
	f.Add("\u00e9" + strings.Repeat("x", 119) + "cut here")

	f.Fuzz(func(t *testing.T, in string) {
		got := firstLine(in, turnFirstLineMax)

		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 out of valid-or-not input %q: %q", in, got)
		}
		// Bounded: at most the cap, plus the one ellipsis rune that reports the cut.
		if n := len([]rune(got)); n > turnFirstLineMax+1 {
			t.Errorf("got %d runes, cap is %d(+1 ellipsis)", n, turnFirstLineMax)
		}
		// Single-line by construction: the hover label is one line, so no
		// whitespace other than the single spaces that replaced runs may survive.
		for _, r := range got {
			if unicode.IsSpace(r) && r != ' ' {
				t.Errorf("whitespace rune %q survived in %q", r, got)
			}
		}
		if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
			t.Errorf("un-trimmed edge in %q", got)
		}
		if strings.Contains(got, "  ") {
			t.Errorf("uncollapsed whitespace run in %q", got)
		}
		// Lossless when nothing needed doing: a request that is already one short
		// line must arrive unchanged, or the label misquotes the user.
		//
		// The oracle normalises invalid UTF-8 the same way the implementation
		// does, because ranging a string decodes an invalid byte to U+FFFD. That
		// is not a lossy accident to fix: this value is marshalled to JSON, and
		// encoding/json performs the identical substitution, so normalising here
		// makes the server's own value equal to what the client receives instead
		// of differing from it.
		if !strings.HasSuffix(got, "\u2026") {
			want := strings.Join(strings.Fields(string([]rune(in))), " ")
			if got != want {
				t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

// Found by FuzzFirstLineBoundedAndWellFormed and kept as a named case, because
// the reason it is correct is not obvious from the code: this value is
// marshalled to JSON, and encoding/json substitutes U+FFFD for invalid UTF-8
// too, so normalising at derivation makes the stored value equal the delivered
// one rather than diverging from it.
func TestFirstLineNormalisesInvalidUTF8LikeJSONWould(t *testing.T) {
	got := firstLine("\xff", turnFirstLineMax)
	if got != "\uFFFD" {
		t.Errorf("firstLine(invalid byte) = %q, want the replacement rune", got)
	}
	if !utf8.ValidString(got) {
		t.Error("result is not valid UTF-8")
	}
}
