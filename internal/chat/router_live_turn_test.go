package chat

// The in-flight reply reaches the chat file only at turn end, so until it does the
// transcript GET's `messages` has no carrier for it and `turn_open` states a fact whose
// content is nowhere in the response. `live_turn` is that content, and these are the
// store's own rules about it: it rides the newest page only, it is absent when nobody
// injected a reader, and it is CHARGED against the page's byte budget rather than added
// on top of it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// seedTranscript writes n persisted assistant rows plus the prompt that opened them, so
// the window has something to be cut short OF.
func seedTranscript(t *testing.T, s *Store, id vibekit.ChatID, n, bytesEach int) {
	t.Helper()
	msgs := []vibekit.Message{{ID: "u1", Role: vibekit.RoleUser, Content: "do the thing", Ts: 1}}
	for i := range n {
		msgs = append(msgs, fatMessage("a"+strconv.Itoa(i), bytesEach))
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

// TestChatGet_ChargesTheLiveTurnAgainstThePageBudget is the boundary the field's own cap
// cannot answer for: both halves ride ONE response, so a caller's ?max_bytes= has to
// bound their sum. Asserted as the window being SHORTER with the live turn present rather
// than as a byte total, because a total assertion would pin the envelope's own size and
// break on any unrelated field.
func TestChatGet_ChargesTheLiveTurnAgainstThePageBudget(t *testing.T) {
	const (
		perMessage = 4 << 10
		budget     = 32 << 10
		liveBytes  = 16 << 10
	)
	query := "?max_bytes=" + strconv.Itoa(budget)

	bare, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedTranscript(t, bare, "c1", 12, perMessage)
	without := len(getLiveTurnPage(t, bare, "c1", query).Messages)

	busy := storeWithLiveTurn(t, liveTurnFixture(liveBytes))
	seedTranscript(t, busy, "c1", 12, perMessage)
	with := len(getLiveTurnPage(t, busy, "c1", query).Messages)

	if without < 3 {
		t.Fatalf("the unbusy page carries %d messages at max_bytes=%d, want at least 3: the "+
			"fixture cannot show a window being cut short", without, budget)
	}
	if with >= without {
		t.Errorf("at max_bytes=%d the window carries %d messages with a %d-byte live turn and "+
			"%d without: the live turn rides the same response, so its bytes must come OUT of "+
			"the caller's budget rather than be added on top of it",
			budget, with, liveBytes, without)
	}
}
