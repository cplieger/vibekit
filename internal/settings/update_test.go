package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(body), 0o600); err != nil {
		t.Fatalf("seed config.json: %v", err)
	}
}

func readConfig(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse config.json (%s): %v", data, err)
	}
	return doc
}

func setKey(key, value string) func(map[string]json.RawMessage) error {
	return func(doc map[string]json.RawMessage) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		doc[key] = raw
		return nil
	}
}

// TestUpdate_ConcurrentWritersLoseNoKey is the whole reason the lock lives in this
// package: an HTTP-shaped write and a command-shaped one reach one file from two
// packages, and a read-modify-write with no lock across it drops whichever key was
// read before the other landed. Remove the lock from Update and this fails under
// -race, usually with one of the two keys missing.
//
// Every writer names its OWN key, so the merged document is only complete when
// every read saw every earlier write.
func TestUpdate_ConcurrentWritersLoseNoKey(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"theme":"dark"}`)

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			key := "k" + strconv.Itoa(i)
			if _, err := Update(t.Context(), dir, setKey(key, key)); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Update: %v", err)
	}

	doc := readConfig(t, dir)
	for i := range writers {
		key := "k" + strconv.Itoa(i)
		if got := string(doc[key]); got != `"`+key+`"` {
			t.Errorf("%s = %s, want %q; a concurrent writer's key was lost", key, got, key)
		}
	}
	if got := string(doc["theme"]); got != `"dark"` {
		t.Errorf("theme = %s, want \"dark\"; a writer replaced the document", got)
	}
}

// TestUpdate_RefusesAnUnreadableDocument is the refuse-on-unreadable contract every
// caller depends on: the write replaces the whole file, so merging over the empty
// map a failed read would hand back replaces config.json with the caller's keys
// alone and durably destroys the rest.
func TestUpdate_RefusesAnUnreadableDocument(t *testing.T) {
	tests := []struct {
		desc string
		body string
	}{
		// A trailing comma is the shape a hand-edit produces on this volume.
		{desc: "a trailing comma", body: `{"theme":"dark",}`},
		{desc: "a top-level null", body: `null`},
		{desc: "a top-level array", body: `[]`},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.body)

			_, err := Update(t.Context(), dir, setKey(KeyTheme, "light"))

			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("Update over %s = %v, want ErrUnreadable", tc.desc, err)
			}
			data, rErr := os.ReadFile(filepath.Join(dir, Filename))
			if rErr != nil {
				t.Fatalf("read back: %v", rErr)
			}
			if string(data) != tc.body {
				t.Errorf("config.json =\n%s\nwant it untouched:\n%s", data, tc.body)
			}
		})
	}
}

// TestUpdate_AbsentFileIsNotAFailure is the half the refusals must not swallow: a
// fresh volume has no config.json, and its first write is ordinary.
func TestUpdate_AbsentFileIsNotAFailure(t *testing.T) {
	dir := t.TempDir()

	merged, err := Update(t.Context(), dir, setKey(KeyTheme, "light"))
	if err != nil {
		t.Fatalf("Update with no file: %v", err)
	}
	if got := string(merged[KeyTheme]); got != `"light"` {
		t.Errorf("returned theme = %s, want \"light\"", got)
	}
	if got := string(readConfig(t, dir)[KeyTheme]); got != `"light"` {
		t.Errorf("stored theme = %s, want \"light\"", got)
	}
}

// TestUpdate_ReturnsTheMergedDocument pins the reason Update answers with one: a
// caller broadcasts the change and syncs preferences off this value rather than
// reading the file it just wrote.
func TestUpdate_ReturnsTheMergedDocument(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"theme":"dark"}`)

	merged, err := Update(t.Context(), dir, setKey(KeyLastModel, "opus"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := string(merged[KeyTheme]); got != `"dark"` {
		t.Errorf("merged theme = %s, want the stored value \"dark\"", got)
	}
	if got := string(merged[KeyLastModel]); got != `"opus"` {
		t.Errorf("merged last_model = %s, want \"opus\"", got)
	}
}

// TestUpdate_ANewValueIsVisibleToTheNextRead pins the cache invalidation. The read
// cache is process-wide and keyed on the file's identity, so a write that does not
// drop it can leave Field answering the previous value.
func TestUpdate_ANewValueIsVisibleToTheNextRead(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"debug_logs":false}`)
	if on, _ := Field[bool](t.Context(), dir, KeyDebugLogs); on {
		t.Fatalf("seeded debug_logs = true, want false")
	}

	if _, err := Update(t.Context(), dir, func(doc map[string]json.RawMessage) error {
		doc[KeyDebugLogs] = json.RawMessage(`true`)
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if on, ok := Field[bool](t.Context(), dir, KeyDebugLogs); !ok || !on {
		t.Errorf("debug_logs after the write = (%v, %v), want (true, true); the read cache was not invalidated", on, ok)
	}
}

// TestUpdate_RefusesAFifoInsteadOfBlockingForever is the FIFO-wedge property, and
// it moved here with the lock. Update holds the per-configDir lock across its read,
// and os.Open on a FIFO blocks in open(2) with no context deadline to rescue it —
// so one FIFO planted at config.json would hold that lock for the life of the
// process and wedge every later settings write. /config is a granted browse mount
// and the agent has a shell there, so one mkfifo is the whole attack.
//
// Bounded rather than direct, because reverting the OpenRegular read does not make
// this FAIL, it makes it HANG. The timer is what turns that into a report; the
// goroutine is left blocked, which is acceptable in a binary about to exit.
func TestUpdate_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, Filename), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Update(t.Context(), dir, setKey(KeyTheme, "light"))
		done <- err
	}()

	select {
	case err := <-done:
		// Named rather than any error: the refusal has to come from the file being
		// the wrong KIND, not from a read that happened to fail some other way.
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Errorf("Update over a FIFO = %v, want atomicfile.ErrNotRegular", err)
		}
		if !errors.Is(err, ErrUnreadable) {
			t.Errorf("Update over a FIFO = %v, want it to match ErrUnreadable too", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Update blocked on a FIFO at config.json; it holds the write lock, so every later settings write is wedged")
	}
}

// TestUpdate_SizeCapIsInclusive keeps the cap where the HTTP path had it: a
// document AT the cap merges, one byte past refuses. Trailing whitespace is legal
// JSON, so the padding leaves it parseable at any size.
func TestUpdate_SizeCapIsInclusive(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "at_the_cap", size: MaxBytes},
		{name: "one_past_the_cap", size: MaxBytes + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			const doc = `{"theme":"dark"}`
			writeConfig(t, dir, doc+spaces(tt.size-len(doc)))

			_, err := Update(t.Context(), dir, setKey(KeyLastModel, "opus"))

			if tt.wantErr {
				if !errors.Is(err, ErrUnreadable) {
					t.Fatalf("Update over a %d-byte document = %v, want ErrUnreadable", tt.size, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update over a %d-byte document: %v", tt.size, err)
			}
		})
	}
}

func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

// TestUpdate_AFailingMergeWritesNothing pins that fn owns the abort: a merge that
// cannot build the value it wants leaves the stored document exactly as it was.
func TestUpdate_AFailingMergeWritesNothing(t *testing.T) {
	dir := t.TempDir()
	const stored = `{"theme":"dark"}`
	writeConfig(t, dir, stored)
	sentinel := errors.New("nope")

	_, err := Update(t.Context(), dir, func(map[string]json.RawMessage) error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("Update = %v, want the merge's own error", err)
	}
	data, rErr := os.ReadFile(filepath.Join(dir, Filename))
	if rErr != nil {
		t.Fatalf("read back: %v", rErr)
	}
	if string(data) != stored {
		t.Errorf("config.json =\n%s\nwant it untouched:\n%s", data, stored)
	}
}
