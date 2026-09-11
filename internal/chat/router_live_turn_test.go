package chat

// The in-flight reply reaches the chat file only at turn end, so until it does the
// transcript GET's `messages` has no carrier for it and `turn_open` states a fact whose
// content is nowhere in the response. `live_turn` is that content, and these are the
// store's own rules about it: it rides the newest page only, and it is absent when nobody
// injected a reader.
//
// The response carries TWO INDEPENDENT BOUNDS rather than one. `?max_bytes=` bounds the
// WINDOW; the live turn is bounded by internal/agent's liveTurnGETCaps, which are sized
// above the measured maximum so the ordinary turn is not cut. The live turn is NOT charged
// against the caller's budget: the newest turn is served whole unconditionally, and
// charging it would let the in-flight reply's size decide how much history a reader gets.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// liveTurnPage is the response as the CLIENT decodes it. Spelled by hand rather than
// taken from the production struct, so a renamed json tag fails here instead of passing
// an assertion against itself.
type liveTurnPage struct {
	LiveTurn *struct {
		Message   vibekit.Message `json:"message"`
		ChunkSeq  int64           `json:"chunk_seq"`
		Truncated bool            `json:"truncated"`
	} `json:"live_turn"`
	Messages []json.RawMessage `json:"messages"`
}

// liveTurnFixture is one in-flight turn of textBytes, in BOTH carriers the way the
// buffer's own snapshot produces it: the flat field the export path reads and the block
// the renderer draws from.
func liveTurnFixture(textBytes int) vibekit.LiveTurn {
	text := strings.Repeat("y", textBytes)
	return vibekit.LiveTurn{
		Message: vibekit.Message{
			ID:      "m-live",
			Role:    vibekit.RoleAssistant,
			Ts:      200,
			Content: text,
			Blocks:  []vibekit.Block{{Type: vibekit.BlockText, Text: text}},
		},
		ChunkSeq:  7,
		Truncated: true,
	}
}

// seedTranscript writes n persisted turns, each a prompt and the reply it opened, so the
// window has a turn boundary to be cut short AT: the page's turn floor admits past every
// ceiling until the window opens on a prompt, so a single-turn chat is never cut.
func seedTranscript(t *testing.T, s *Store, id vibekit.ChatID, n, bytesEach int) {
	t.Helper()
	var msgs []vibekit.Message
	for i := range n {
		msgs = append(msgs,
			vibekit.Message{
				ID: "u" + strconv.Itoa(i), Role: vibekit.RoleUser,
				Content: "do the thing", Ts: 1,
			},
			fatMessage("a"+strconv.Itoa(i), bytesEach))
	}
	if err := s.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Name = string(id)
		c.Messages = msgs
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// getLiveTurnPage drives the real route and decodes the page.
func getLiveTurnPage(t *testing.T, s *Store, id vibekit.ChatID, query string) liveTurnPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+query, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/%s%s = %d, want 200; body = %s", id, query, rec.Code, rec.Body.String())
	}
	var page liveTurnPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return page
}

func storeWithLiveTurn(t *testing.T, live vibekit.LiveTurn) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(),
		WithLiveTurn(func(vibekit.ChatID) (vibekit.LiveTurn, bool) { return live, true }),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// TestChatGet_CarriesTheInjectedLiveTurn pins every field the client reads, in the
// spelling it reads them under: without the watermark the client cannot drop the chunks
// already folded in, and without the truncation marker it reads a capped tail as the
// whole reply.
func TestChatGet_CarriesTheInjectedLiveTurn(t *testing.T) {
	s := storeWithLiveTurn(t, liveTurnFixture(64))
	seedTranscript(t, s, "c1", 1, 8)

	page := getLiveTurnPage(t, s, "c1", "")

	if page.LiveTurn == nil {
		t.Fatalf("live_turn absent while a reader is injected: the transcript GET is the one "+
			"channel a client that declared no chat at connect has, so the reply already on "+
			"screen would be unreachable. Body: %+v", page)
	}
	if got, want := page.LiveTurn.Message.ID, "m-live"; got != want {
		t.Errorf("live_turn.message.id = %q, want %q", got, want)
	}
	if got, want := page.LiveTurn.ChunkSeq, int64(7); got != want {
		t.Errorf("live_turn.chunk_seq = %d, want %d: it is the client's dedup watermark, so a "+
			"lost one double-appends every chunk the snapshot already carries", got, want)
	}
	if !page.LiveTurn.Truncated {
		t.Errorf("live_turn.truncated = false, want true: the cap withheld part of the message, " +
			"and a reader shown the tail with nothing saying so reads it as the whole reply")
	}
	// The live turn is a SIBLING of the window, never spliced into it: `messages` means
	// what the file holds, which is what keeps has_more, turn_offset, turn_segment_closed
	// and message_count meaning what they say.
	if got, want := len(page.Messages), 2; got != want {
		t.Errorf("messages carries %d rows, want %d (the persisted prompt and reply)", got, want)
	}
}

// TestChatGet_OmitsTheLiveTurnOnAnOlderPage: a scroll-up asserts nothing about the live
// edge, which is the rule `turn_open` and `draft` already follow. Without it every page a
// reader scrolls back through re-delivers the in-flight turn.
func TestChatGet_OmitsTheLiveTurnOnAnOlderPage(t *testing.T) {
	s := storeWithLiveTurn(t, liveTurnFixture(64))
	seedTranscript(t, s, "c1", 2, 8)

	if page := getLiveTurnPage(t, s, "c1", ""); page.LiveTurn == nil {
		t.Fatalf("the newest page carries no live_turn, so the assertion below is vacuous")
	}
	if page := getLiveTurnPage(t, s, "c1", "?before_id=a1"); page.LiveTurn != nil {
		t.Errorf("an older page carries live_turn = %+v, want it omitted", page.LiveTurn)
	}
}

// TestChatGet_OmitsTheLiveTurnWithNoReaderInjected keeps an unwired store byte-identical
// to what it served before the field existed. Composition supplies the reader, so a
// wiring mistake must cost a missing carrier rather than a nil dereference.
func TestChatGet_OmitsTheLiveTurnWithNoReaderInjected(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedTranscript(t, s, "c1", 1, 8)

	if page := getLiveTurnPage(t, s, "c1", ""); page.LiveTurn != nil {
		t.Errorf("an unwired store carries live_turn = %+v, want it omitted", page.LiveTurn)
	}
}

// TestChatGet_LeavesTheWindowBudgetToTheWindow is the boundary the field's own cap cannot
// answer for, and it pins the OPPOSITE of what this test asserted before the "one whole
// turn" guarantee: the live turn's presence shortens the window by nothing.
//
// Asserted as the same message IDS either way rather than as a byte total, because a total
// assertion would pin the envelope's own size and break on any unrelated field. Ids rather
// than a count, so a window that kept its LENGTH while sliding to different history would
// still fail.
func TestChatGet_LeavesTheWindowBudgetToTheWindow(t *testing.T) {
	const (
		perMessage = 4 << 10
		budget     = 32 << 10
		liveBytes  = 16 << 10
	)
	// turns=1 leaves the byte ceiling as the thing that cuts: the floor overrides every
	// ceiling until the window opens on a prompt, so the default floor of 3 would carry
	// three whole turns past this budget and neither page would be cut at all.
	query := "?max_bytes=" + strconv.Itoa(budget) + "&turns=1"

	bare, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedTranscript(t, bare, "c1", 12, perMessage)
	idle := getLiveTurnPage(t, bare, "c1", query)

	busy := storeWithLiveTurn(t, liveTurnFixture(liveBytes))
	seedTranscript(t, busy, "c1", 12, perMessage)
	live := getLiveTurnPage(t, busy, "c1", query)

	if len(idle.Messages) < 3 {
		t.Fatalf("the idle page carries %d messages at max_bytes=%d, want at least 3: the "+
			"fixture cannot show a window that was cut at all, so an unchanged window proves "+
			"nothing", len(idle.Messages), budget)
	}
	if live.LiveTurn == nil {
		t.Fatal("the busy page carries no live_turn, so the comparison below is vacuous")
	}
	if got, want := decodedIDs(t, live.Messages), decodedIDs(t, idle.Messages); !slices.Equal(got, want) {
		t.Errorf("at max_bytes=%d the window carries %v with a %d-byte live turn and %v without: "+
			"the caller's budget bounds the WINDOW, so the in-flight reply's size may not decide "+
			"how much history the reader gets", budget, got, liveBytes, want)
	}
}

// TestChatGet_ServesBothBoundsAtOnce pins the two bounds holding independently in ONE
// response: the window IS cut by the caller's own budget while the live turn arrives whole
// beside it. Without this a page that ignored `?max_bytes=` entirely would satisfy the test
// above, since an uncut window is also an unchanged one.
func TestChatGet_ServesBothBoundsAtOnce(t *testing.T) {
	const (
		perMessage = 4 << 10
		liveBytes  = 256 << 10
	)
	s := storeWithLiveTurn(t, liveTurnFixture(liveBytes))
	seedTranscript(t, s, "c1", 12, perMessage)

	// A budget far under one turn's bytes, with turns=1 so the floor admits a cut as soon
	// as the window opens on a prompt.
	page := getLiveTurnPage(t, s, "c1", "?max_bytes=1024&turns=1")

	if page.LiveTurn == nil {
		t.Fatal("live_turn absent under a 1 KiB window budget: the live turn is bounded by its " +
			"OWN caps, so a caller's window budget may not withhold it")
	}
	if got := len(page.LiveTurn.Message.Content); got != liveBytes {
		t.Errorf("live turn content = %d bytes, want the whole %d: the window budget bounds the "+
			"window, and nothing here truncates the live turn", got, liveBytes)
	}
	// The window is genuinely cut: 12 turns of 4 KiB each cannot fit a 1 KiB budget, so a
	// full window would mean the budget was ignored rather than that the floor carried it.
	if got, want := len(page.Messages), 24; got >= want {
		t.Errorf("the window carries %d of %d messages at max_bytes=1024, want fewer: the caller's "+
			"budget still bounds the window", got, want)
	}
	if len(page.Messages) == 0 {
		t.Error("the window is empty: the turn floor guarantees the newest turn whole, so a 1 KiB " +
			"budget must still serve it")
	}
}

// decodedIDs reads the message ids out of a window, so a comparison is over WHICH messages
// were served rather than over how many.
func decodedIDs(t *testing.T, window []json.RawMessage) []string {
	t.Helper()
	ids := make([]string, 0, len(window))
	for _, raw := range window {
		var m struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode window row: %v", err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}
