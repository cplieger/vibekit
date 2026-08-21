package chat

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// withinBudget runs fn on its own goroutine and fails the test if it has not
// returned inside budget.
//
// A DEADLINE rather than a plain call, because the defect these tests pin does
// not fail — it HANGS. os.Open on a FIFO blocks in open(2) until a writer
// appears and no context deadline can rescue it, so reverting the OpenRegular
// adoption in readCappedFile makes each of these run to the go-test timeout
// instead of reporting a failure. That is the strongest evidence available for
// this class, and it is why the assertion is on elapsed time and not only on
// the error.
//
// The goroutine is deliberately abandoned on expiry: it is parked in the kernel
// and nothing can reclaim it. One leaked goroutine in a failing test run is the
// price of reporting the failure at all.
func withinBudget(t *testing.T, budget time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(budget):
		t.Fatalf("still blocked after %v: the read followed a non-regular file into open(2)", budget)
		return nil
	}
}

// mkfifoChat plants a FIFO at a chat id's file name and returns the id.
func mkfifoChat(t *testing.T, dir string) vibekit.ChatID {
	t.Helper()
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no unix device nodes")
	}
	id := vibekit.ChatID("m-fifo0000-aaaa")
	if !chatIDPattern(id) {
		t.Fatalf("fixture id %q is not a valid chat id", id)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, string(id)+chatFileSuffix), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	return id
}

// TestGet_RefusesAFifoInsteadOfBlockingForever is the load-bearing one: a FIFO
// at <chats>/<valid-chat-id>.json is a one-command permanent wedge of every chat
// read, and the /config volume is reachable both by the operator (invariant 6
// invites reshaping it) and by the agent's own shell.
func TestGet_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	s, _ := newTestStore(t)
	id := mkfifoChat(t, s.dir)

	err := withinBudget(t, 3*time.Second, func() error {
		_, err := s.load(id)
		return err
	})
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("load over a FIFO = %v, want atomicfile.ErrNotRegular", err)
	}
	// And the public read reports absence rather than propagating the wedge.
	if _, ok := withinBudgetGet(t, s, id); ok {
		t.Error("Get returned ok for a FIFO planted at a chat file name")
	}
}

func withinBudgetGet(t *testing.T, s *Store, id vibekit.ChatID) (*vibekit.Chat, bool) {
	t.Helper()
	type res struct {
		c  *vibekit.Chat
		ok bool
	}
	out := make(chan res, 1)
	go func() {
		c, ok := s.Get(t.Context(), id)
		out <- res{c, ok}
	}()
	select {
	case r := <-out:
		return r.c, r.ok
	case <-time.After(3 * time.Second):
		t.Fatal("Get still blocked after 3s over a FIFO")
		return nil, false
	}
}

// TestList_SurvivesAFifoAndReportsTheScanIncomplete pins the fan-out half. List
// reads with 8 workers inside one singleflight slot, so a blocking open wedged
// every concurrent GET /api/chats behind it — and the completeness flag is what
// makes the session sweep fail closed over the file it could not read.
func TestList_SurvivesAFifoAndReportsTheScanIncomplete(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Mutate(t.Context(), "good", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "readable"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	mkfifoChat(t, s.dir)

	type out struct {
		headers  []vibekit.ChatHeader
		complete bool
	}
	ch := make(chan out, 1)
	go func() {
		h, c := s.listWithCompleteness(t.Context())
		ch <- out{h, c}
	}()
	select {
	case got := <-ch:
		if len(got.headers) != 1 || got.headers[0].ID != "good" {
			t.Errorf("headers = %+v, want just the readable chat", got.headers)
		}
		if got.complete {
			t.Error("complete = true, want false: a chat that exists was not read, " +
				"so the session keep-list derived from this must not be trusted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List still blocked after 5s: one FIFO wedged the whole scan")
	}
}

// TestReadCappedFile_RefusesASymlink closes the smaller hole in the same open: a
// link at <chats>/<id>.json made another file's bytes reachable through the chat
// read, the header projection and both search paths.
func TestReadCappedFile_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"id":"x","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "m-link0000-bbbb.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if _, err := readCappedFile(link, "chat link"); err == nil {
		t.Error("readCappedFile followed a symlink at a chat file name")
	}
}
