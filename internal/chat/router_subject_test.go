package chat

// The two chat GET envelopes carry the digest stamp the client's version map
// observes on commit: the store's own counter, plus the hub epoch that lets the map
// refuse a response issued under a previous hub.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func decodeStamp(t *testing.T, raw any) vibekit.SubjectStamp {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-encode subject: %v", err)
	}
	var stamp vibekit.SubjectStamp
	if err := json.Unmarshal(b, &stamp); err != nil {
		t.Fatalf("decode subject %s: %v", b, err)
	}
	return stamp
}

func getChats(t *testing.T, s *Store) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return envelope
}

func TestGetChat_SubjectIsTheStoresChatVersionWithTheEpoch(t *testing.T) {
	s, _, v := newVersionedTestStore(t)
	WithEpoch(func() string { return "epoch-7" })(s)
	seedMidTurn(t, s, "c1")
	seedMidTurn(t, s, "c1") // a second save, so the version is not trivially "1"

	got := decodeStamp(t, getChat(t, s, "c1")["subject"])
	current, _ := v.Current(subject.KindChat, "c1")
	want := vibekit.SubjectStamp{Kind: "chat", Ref: "c1", Version: current, Epoch: "epoch-7"}
	if got != want {
		t.Errorf("GET /api/chats/c1 subject = %+v, want %+v", got, want)
	}
	if current == subject.Unminted {
		t.Errorf("registry reports %q after two saves; the assertion above is vacuous", current)
	}
}

// A chat file that exists but was never mutated this process (a restart) stamps
// Unminted, which is also what the resolver answers, so the pair compares equal.
func TestGetChat_NeverMutatedThisProcessStampsUnminted(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedMidTurn(t, first, "c1")
	restarted, err := NewStore(dir, WithEpoch(func() string { return "e2" }))
	if err != nil {
		t.Fatalf("NewStore after restart: %v", err)
	}

	got := decodeStamp(t, getChat(t, restarted, "c1")["subject"])
	want := vibekit.SubjectStamp{Kind: "chat", Ref: "c1", Version: subject.Unminted, Epoch: "e2"}
	if got != want {
		t.Errorf("subject = %+v, want %+v", got, want)
	}
}

func TestGetChats_SubjectIsTheChatsVersionWithTheEpoch(t *testing.T) {
	s, _, v := newVersionedTestStore(t)
	WithEpoch(func() string { return "epoch-7" })(s)
	seedMidTurn(t, s, "c1")
	seedMidTurn(t, s, "c2")

	envelope := getChats(t, s)
	got := decodeStamp(t, envelope["subject"])
	current, _ := v.Current(subject.KindChats, "")
	want := vibekit.SubjectStamp{Kind: "chats", Ref: "", Version: current, Epoch: "epoch-7"}
	if got != want {
		t.Errorf("GET /api/chats subject = %+v, want %+v", got, want)
	}
	if current != "2" {
		t.Errorf("chats version after two creates = %q, want 2", current)
	}
	if chats, ok := envelope["chats"].([]any); !ok || len(chats) != 2 {
		t.Errorf("chats = %v, want the two headers beside the stamp", envelope["chats"])
	}
}
