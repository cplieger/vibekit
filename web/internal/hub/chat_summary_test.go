package hub

import (
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"vibekit/internal/api"
)

func TestPostprocessSummary_tableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"trim_whitespace", "   hello world   ", "hello world"},
		{"strip_double_quotes", `"hello"`, "hello"},
		{"strip_single_quotes", `'hello'`, "hello"},
		{"mixed_quotes_not_stripped", `"hello'`, `"hello'`},
		{"one_char_quote_not_stripped", `"`, `"`},
		{"truncates_at_newline", "first line\nsecond line", "first line"},
		{"truncates_at_carriage_return", "first\rsecond", "first"},
		{"pass_through_short", "A short summary.", "A short summary."},
		{
			"truncates_long_to_cap_with_ellipsis",
			strings.Repeat("a", summaryMaxChars+10),
			strings.Repeat("a", summaryMaxChars-1) + "…",
		},
		{"strip_then_trim_inner_whitespace", `"  padded  "`, "padded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := postprocessSummary(tc.in)
			if got != tc.want {
				t.Errorf("postprocessSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPostprocessSummary_invariants(t *testing.T) {
	cases := []string{
		"",
		"short",
		"   padded   ",
		`"quoted"`,
		"has\nnewline",
		strings.Repeat("x", 500),
		strings.Repeat("éléphant ", 100),
	}
	for _, in := range cases {
		out := postprocessSummary(in)
		if utf8.RuneCountInString(out) > summaryMaxChars {
			t.Errorf("postprocessSummary(%q) rune-length %d exceeds cap %d",
				in, utf8.RuneCountInString(out), summaryMaxChars)
		}
		if strings.ContainsAny(out, "\r\n") {
			t.Errorf("postprocessSummary(%q) = %q contains newline", in, out)
		}
		if got := postprocessSummary(out); got != out {
			t.Errorf("postprocessSummary not idempotent on %q: first=%q second=%q", in, out, got)
		}
	}
}

func FuzzPostprocessSummary(f *testing.F) {
	f.Add("")
	f.Add("short")
	f.Add("   padded   ")
	f.Add(`"quoted"`)
	f.Add("has\nnewline")
	f.Add(strings.Repeat("x", 500))
	f.Add(strings.Repeat("éléphant ", 100))
	f.Add("line1\r\nline2\nline3")
	f.Add(`'single quoted'`)

	f.Fuzz(func(t *testing.T, input string) {
		out := postprocessSummary(input)

		if utf8.RuneCountInString(out) > summaryMaxChars {
			t.Errorf("rune count %d exceeds cap %d for input %q",
				utf8.RuneCountInString(out), summaryMaxChars, input)
		}
		if strings.ContainsAny(out, "\r\n") {
			t.Errorf("output %q contains newline for input %q", out, input)
		}
		if got := postprocessSummary(out); got != out {
			t.Errorf("not idempotent: first=%q second=%q for input %q", out, got, input)
		}
	})
}

func TestTruncateAt_tableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		n    int
	}{
		{"zero_returns_empty", "hello", "", 0},
		{"negative_returns_empty", "hello", "", -5},
		{"n_equals_length", "hello", "hello", 5},
		{"n_greater_than_length", "hi", "hi", 10},
		{"ascii_cut", "hello world", "hello", 5},
		{"multibyte_safe_cut", "héllo", "hél", 3},
		{"emoji_rune_boundary", "a🙂b🙂c", "a🙂b", 3},
		{"empty_input", "", "", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestTruncateAt_neverExceedsNRunes(t *testing.T) {
	cases := []struct {
		in string
		n  int
	}{
		{"", 3},
		{"hello", 3},
		{"héllo", 3},
		{"a🙂b🙂c", 2},
		{strings.Repeat("z", 100), 30},
	}
	for _, tc := range cases {
		got := truncateRunes(tc.in, tc.n)
		if tc.n <= 0 {
			if got != "" {
				t.Errorf("truncateRunes(%q, %d) = %q, want \"\"", tc.in, tc.n, got)
			}
			continue
		}
		if utf8.RuneCountInString(got) > tc.n {
			t.Errorf("truncateRunes(%q, %d) = %q (%d runes) exceeds n",
				tc.in, tc.n, got, utf8.RuneCountInString(got))
		}
		if !strings.HasPrefix(tc.in, got) {
			t.Errorf("truncateRunes(%q, %d) = %q is not a prefix of input", tc.in, tc.n, got)
		}
	}
}

func TestCountAssistantMessages(t *testing.T) {
	cases := []struct {
		name string
		msgs []api.Message
		want int
	}{
		{"empty", nil, 0},
		{"only_user", []api.Message{{Role: api.RoleUser}}, 0},
		{"mixed", []api.Message{
			{Role: api.RoleUser},
			{Role: api.RoleAssistant},
			{Role: api.RoleUser},
			{Role: api.RoleAssistant},
			{Role: api.RoleEvent},
		}, 2},
		{"only_events_and_user", []api.Message{
			{Role: api.RoleUser},
			{Role: api.RoleEvent},
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &api.Chat{Messages: tc.msgs}
			if got := countAssistantMessages(c); got != tc.want {
				t.Errorf("countAssistantMessages = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildSummaryPrompt_emptyWhenNoAssistant(t *testing.T) {
	c := &api.Chat{Name: "Chat", Messages: []api.Message{
		{Role: api.RoleUser, Content: "hello"},
	}}
	if got := buildSummaryPrompt(c); got != "" {
		t.Errorf("buildSummaryPrompt(no assistant) = %q, want \"\"", got)
	}
}

func TestBuildSummaryPrompt_includesTitleAndAssistantContent(t *testing.T) {
	c := &api.Chat{
		Name: "Refactor HTTP layer",
		Messages: []api.Message{
			{Role: api.RoleUser, Content: "hi"},
			{Role: api.RoleAssistant, Content: "one"},
		},
	}
	got := buildSummaryPrompt(c)
	if !strings.Contains(got, "Refactor HTTP layer") {
		t.Errorf("prompt missing title: %q", got)
	}
	if !strings.Contains(got, "one") {
		t.Errorf("prompt missing assistant body: %q", got)
	}
}

func TestBuildSummaryPrompt_keepsOnlyLastN(t *testing.T) {
	msgs := make([]api.Message, 0, 2*10)
	for i := range 10 {
		msgs = append(msgs,
			api.Message{Role: api.RoleUser, Content: "q"},
			api.Message{Role: api.RoleAssistant, Content: "a" + string(rune('0'+i))},
		)
	}
	c := &api.Chat{Name: "t", Messages: msgs}
	got := buildSummaryPrompt(c)
	keptStart := 10 - summaryContextTurns
	for i := range keptStart {
		if strings.Contains(got, "a"+string(rune('0'+i))) {
			t.Errorf("prompt should not contain early msg a%d: %q", i, got)
		}
	}
	for i := keptStart; i < 10; i++ {
		if !strings.Contains(got, "a"+string(rune('0'+i))) {
			t.Errorf("prompt missing late msg a%d: %q", i, got)
		}
	}
}

func TestBuildSummaryPrompt_truncatesLongMessagesTo800Runes(t *testing.T) {
	long := strings.Repeat("x", 5000)
	c := &api.Chat{Name: "t", Messages: []api.Message{
		{Role: api.RoleAssistant, Content: long},
	}}
	got := buildSummaryPrompt(c)
	if strings.Contains(got, strings.Repeat("x", 801)) {
		t.Error("prompt not truncated to 800 runes: found >800 consecutive xs")
	}
	if !strings.Contains(got, strings.Repeat("x", 800)) {
		t.Error("prompt missing 800-x run (expected exact truncation to 800)")
	}
}

func TestBuildSummaryPrompt_skipsBlankAssistant(t *testing.T) {
	c := &api.Chat{Name: "t", Messages: []api.Message{
		{Role: api.RoleAssistant, Content: "   "},
	}}
	if got := buildSummaryPrompt(c); got != "" {
		t.Errorf("blank assistant content should yield empty prompt, got %q", got)
	}
}

// memArchiveStore extends fakeChatStore to exercise the LoadArchived
// plumbing without needing a full chat.Store under t.TempDir. Used by
// loadArchived-related tests below.
type memArchiveStore struct {
	*fakeChatStore

	archived map[string]*api.Chat
	loadErr  error
}

func newMemArchiveStore() *memArchiveStore {
	return &memArchiveStore{fakeChatStore: newFakeChatStore(), archived: map[string]*api.Chat{}}
}

func (s *memArchiveStore) LoadArchived(id string) (*api.Chat, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	c, ok := s.archived[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	clone := *c
	return &clone, nil
}

func TestLoadArchived_interfaceRoundTrip(t *testing.T) {
	s := newMemArchiveStore()
	want := &api.Chat{ID: "c1", Name: "hello", Messages: []api.Message{
		{ID: "m1", Role: api.RoleUser, Content: "hi"},
	}}
	s.archived["c1"] = want

	got, err := s.LoadArchived("c1")
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if got.ID != "c1" || got.Name != "hello" || len(got.Messages) != 1 {
		t.Errorf("got %+v, want id=c1 name=hello 1 msg", got)
	}
}

func TestLoadArchived_missingReturnsErrNotExist(t *testing.T) {
	s := newMemArchiveStore()
	if _, err := s.LoadArchived("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}
