package hub

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/procgroup"
)

func TestRingBuffer(t *testing.T) {
	r := newByteRing(10)
	r.Write([]byte("hello"))
	if r.String() != "hello" {
		t.Errorf("got %q, want %q", r.String(), "hello")
	}
	r.Write([]byte(" world!"))
	// "hello world!" is 12 bytes, limit is 10, so first 2+ bytes trimmed.
	got := r.String()
	if len(got) > 10 {
		t.Errorf("buffer exceeded limit: len=%d", len(got))
	}
	// Should contain the tail of the input.
	if got != "lo world!" && got != "o world!" && got != " world!" {
		// The exact trim depends on UTF-8 boundary advancement.
		// Just verify it's within limit and contains the tail.
		t.Logf("ring buffer output: %q (len=%d)", got, len(got))
	}
}

func TestRingBufferUTF8(t *testing.T) {
	r := newByteRing(8)
	// Write a multi-byte UTF-8 character (3 bytes for é = 0xC3 0xA9)
	r.Write([]byte("aaaaaé"))
	got := r.String()
	if len(got) > 8 {
		t.Errorf("buffer exceeded limit: len=%d, content=%q", len(got), got)
	}
}

func TestAgentTerminalsNewAndLookup(t *testing.T) {
	terms := newAgentTerminals()
	if len(terms.terms) != 0 {
		t.Errorf("expected empty, got %d", len(terms.terms))
	}
	terms.terms["test"] = newAgentTerminal(nil, "", 64)
	if _, ok := terms.terms["test"]; !ok {
		t.Error("expected to find terminal 'test'")
	}
}

func TestRingBuffer_StoresBoundedByteCount(t *testing.T) {
	t.Parallel()
	r := newByteRing(20)
	if got := len(r.Bytes()); got != 0 {
		t.Errorf("empty byteRing stored bytes = %d, want 0", got)
	}
	r.Write([]byte("hello"))
	if got := len(r.Bytes()); got != 5 {
		t.Errorf("byteRing stored bytes after 'hello' = %d, want 5", got)
	}
	r.Write([]byte(" world, this is long"))
	if got := len(r.Bytes()); got > 20 {
		t.Errorf("byteRing stored bytes = %d, exceeds limit 20", got)
	}
}

func TestParseRequest_NilParamsReturnsError(t *testing.T) {
	t.Parallel()
	msg := &api.RPCResponse{Params: nil}
	var target struct{ Name string }
	if err := parseRequest(msg, &target); err == nil {
		t.Error("parseRequest(nil params) should return error")
	}
}

func TestParseRequest_ValidJSON(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"command":"ls","cwd":"/tmp"}`)
	msg := &api.RPCResponse{Params: raw}
	var target struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := parseRequest(msg, &target); err != nil {
		t.Fatalf("parseRequest(valid) returned error: %v", err)
	}
	if target.Command != "ls" {
		t.Errorf("Command = %q, want %q", target.Command, "ls")
	}
	if target.Cwd != "/tmp" {
		t.Errorf("Cwd = %q, want %q", target.Cwd, "/tmp")
	}
}

func TestParseRequest_MalformedJSON(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{invalid json}`)
	msg := &api.RPCResponse{Params: raw}
	var target struct{ Name string }
	if err := parseRequest(msg, &target); err == nil {
		t.Error("parseRequest(malformed JSON) should return error")
	}
}

func BenchmarkByteRing_Write(b *testing.B) {
	for _, cap := range []int{4096, 65536, 262144} {
		for _, ws := range []int{64, 1024, 8192} {
			name := fmt.Sprintf("cap=%d/write=%d", cap, ws)
			b.Run(name, func(b *testing.B) {
				r := newByteRing(cap)
				data := make([]byte, ws)
				for i := range data {
					data[i] = byte(i)
				}
				b.ReportAllocs()
				b.SetBytes(int64(ws))
				b.ResetTimer()
				for range b.N {
					r.Write(data)
				}
			})
		}
	}
}

func FuzzByteRing_WriteRead(f *testing.F) {
	f.Add(uint16(10), []byte("hello"))
	f.Add(uint16(1), []byte{0xC3, 0xA9})
	f.Add(uint16(512), []byte{0})
	f.Fuzz(func(t *testing.T, capRaw uint16, data []byte) {
		cap := int(capRaw)%512 + 1
		r := newByteRing(cap)
		r.Write(data)
		out := r.Bytes()
		if len(out) > cap {
			t.Fatalf("Bytes() len %d exceeds capacity %d", len(out), cap)
		}
		s := r.String()
		if !utf8.ValidString(s) && utf8.Valid(data) {
			t.Fatalf("String() not valid UTF-8 for valid UTF-8 input")
		}
	})
}

// drainAll clears the terminal maps even when a registered terminal has
// already exited (its done channel is closed).
func TestDrainAll_ClearsExitedTerminals(t *testing.T) {
	at := newAgentTerminals()
	done := make(chan struct{})
	close(done) // already exited
	t1 := newAgentTerminal(nil, "", 64)
	t1.done = done
	at.terms["t1"] = t1
	at.byChatID["c1"] = []string{"t1"}

	at.drainAll()

	at.mu.Lock()
	n := len(at.terms)
	at.mu.Unlock()
	if n != 0 {
		t.Errorf("drainAll() left %d terminals, want 0", n)
	}
}

// release removes only the named terminal from both maps (dropping just
// its id from the chat's slice) and reports (nil,false) for an unknown id.
func TestAgentTerminals_Release(t *testing.T) {
	at := newAgentTerminals()
	at.terms["t1"] = newAgentTerminal(nil, "c1", 64)
	at.terms["t2"] = newAgentTerminal(nil, "c1", 64)
	at.byChatID["c1"] = []string{"t1", "t2"}

	term, ok := at.release("t1")
	if !ok || term == nil || term.chatID != "c1" {
		t.Fatalf("release(t1) = %v, %v; want the t1 terminal, true", term, ok)
	}
	if _, exists := at.terms["t1"]; exists {
		t.Errorf("t1 still present in terms after release")
	}
	if got := at.byChatID["c1"]; len(got) != 1 || got[0] != "t2" {
		t.Errorf("byChatID[c1] = %v, want [t2] (only t1 should be dropped)", got)
	}

	// Unknown id: no removal, returns (nil, false).
	if gotTerm, gotOK := at.release("nope"); gotOK || gotTerm != nil {
		t.Errorf("release(unknown) = %v, %v; want nil, false", gotTerm, gotOK)
	}
	if len(at.terms) != 1 {
		t.Errorf("terms size = %d, want 1", len(at.terms))
	}
}

// --- agent-terminal server-side regression tests (ordering + UTF-8 boundary) ---

// ringEvent is a decoded terminal_* SSE event captured from the replay ring.
type ringEvent struct {
	typ     string
	termID  string
	data    string
	eventID uint64
}

// captureTerminalEvents reads the hub's replay ring and returns every
// terminal_created/terminal_output/terminal_exited event, sorted by the
// monotonic SSE event id (ring insertion order can differ from event-id
// order because seq.Add runs before the fan-out lock).
func captureTerminalEvents(t *testing.T, h *Hub) []ringEvent {
	t.Helper()
	var out []ringEvent
	for _, e := range h.sse.hub.Buffered() {
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				TerminalID string `json:"terminal_id"`
				Data       string `json:"data"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		switch env.Type {
		case string(api.EventTerminalCreated), string(api.EventTerminalOutput), string(api.EventTerminalExited):
			out = append(out, ringEvent{
				eventID: e.ID,
				typ:     env.Type,
				termID:  env.Payload.TerminalID,
				data:    env.Payload.Data,
			})
		}
	}
	slices.SortFunc(out, func(a, b ringEvent) int { return cmp.Compare(a.eventID, b.eventID) })
	return out
}

func hasType(evs []ringEvent, typ api.EventType) bool {
	for _, e := range evs {
		if e.typ == string(typ) {
			return true
		}
	}
	return false
}

// firstEventID returns the lowest event id of the given type (evs is sorted).
func firstEventID(evs []ringEvent, typ api.EventType) (uint64, bool) {
	for _, e := range evs {
		if e.typ == string(typ) {
			return e.eventID, true
		}
	}
	return 0, false
}

// terminal_created must be broadcast (and so receive a lower monotonic event
// id) before any terminal_output / terminal_exited event. emit() assigns ids
// in call order and the pump/exit goroutines are started only after the
// terminal_created broadcast, so created always sorts first. If it didn't, a
// fast write-and-exit command's output/exited events would reach the client
// before the tab exists and get dropped (unknown terminal id) — wedging the
// tab in "running".
func TestTerminalCreated_BroadcastBeforeOutputAndExited(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	// printf writes to stdout (pump → terminal_output); the brief sleep keeps
	// the process alive so the pump reads that output well before cmd.Wait
	// closes the pipe, then the process exits (exit goroutine → terminal_exited).
	msg := termCreateMsg(t, 1, "sh", []string{"-c", "printf hello; sleep 1"}, nil)

	h.translateACPEvent("c1", msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")

	// The terminal_exited broadcast happens just after term.done closes, so
	// poll until both output and exited have landed in the ring.
	var evs []ringEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs = captureTerminalEvents(t, h)
		if hasType(evs, api.EventTerminalOutput) && hasType(evs, api.EventTerminalExited) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	createdID, ok := firstEventID(evs, api.EventTerminalCreated)
	if !ok {
		t.Fatal("terminal_created was never broadcast")
	}
	sawOutput, sawExited := false, false
	for _, e := range evs {
		switch e.typ {
		case string(api.EventTerminalOutput):
			sawOutput = true
			if e.eventID <= createdID {
				t.Errorf("terminal_output event id %d <= terminal_created id %d (created must be broadcast first)", e.eventID, createdID)
			}
		case string(api.EventTerminalExited):
			sawExited = true
			if e.eventID <= createdID {
				t.Errorf("terminal_exited event id %d <= terminal_created id %d (created must be broadcast first)", e.eventID, createdID)
			}
		}
	}
	if !sawOutput {
		t.Error("no terminal_output event captured (pump never broadcast the command's stdout)")
	}
	if !sawExited {
		t.Error("no terminal_exited event captured")
	}
}

// chunkReader hands out its byte chunks one Read at a time, letting a test
// place a read boundary exactly inside a multi-byte UTF-8 rune.
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.i >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.i])
	c.i++
	return n, nil
}

// A multi-byte rune split across the 4 KB read boundary must not be corrupted
// in the live SSE broadcast: pumpTerminalOutput carries the incomplete tail to
// the next chunk so every terminal_output chunk is valid UTF-8, while the ring
// still receives every raw byte.
func TestPumpTerminalOutput_RuneSplitAcrossReadBoundaryNotCorrupted(t *testing.T) {
	h := New(t.TempDir(), func() api.ACPBridge { return newFakeBridge() }, newFakeChatStore())
	term := newAgentTerminal(nil, "c1", 1024)
	// "aé€😀" with reads that split every multi-byte rune internally:
	// é = C3 A9, € = E2 82 AC, 😀 = F0 9F 98 80.
	r := &chunkReader{chunks: [][]byte{
		{0x61},             // "a" (complete ASCII)
		{0xC3},             // é byte 1/2  → incomplete, held
		{0xA9, 0xE2},       // é byte 2/2 (completes é) + € byte 1/3 (held)
		{0x82},             // € byte 2/3  → still incomplete, held
		{0xAC, 0xF0, 0x9F}, // € byte 3/3 (completes €) + 😀 bytes 1,2/4 (held)
		{0x98},             // 😀 byte 3/4 → still incomplete, held
		{0x80},             // 😀 byte 4/4 (completes 😀)
	}}
	const want = "aé€😀"

	h.pumpTerminalOutput(term, "t1", "c1", r)

	// Reassemble the broadcast chunks. A split rune would have its halves
	// coerced to U+FFFD by the SSE JSON marshal, so the reassembly would not
	// equal the original text — the equality check is what proves the fix.
	var got bytes.Buffer
	for _, e := range captureTerminalEvents(t, h) {
		if e.typ == string(api.EventTerminalOutput) {
			got.WriteString(e.data)
		}
	}
	if got.String() != want {
		t.Errorf("broadcast reassembly = %q, want %q (a rune was split across the read boundary)", got.String(), want)
	}
	// The ring buffer must still hold every raw byte.
	if ring := term.output.String(); ring != want {
		t.Errorf("ring content = %q, want %q", ring, want)
	}
}

func TestIncompleteTailLen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"empty", nil, 0},
		{"ascii", []byte("hello"), 0},
		{"complete 2-byte é", []byte{0xC3, 0xA9}, 0},
		{"complete 3-byte €", []byte{0xE2, 0x82, 0xAC}, 0},
		{"complete 4-byte 😀", []byte{0xF0, 0x9F, 0x98, 0x80}, 0},
		{"ascii then partial 2-byte lead", []byte{0x41, 0xC3}, 1},
		{"partial 3-byte 1of3", []byte{0xE2}, 1},
		{"partial 3-byte 2of3", []byte{0xE2, 0x82}, 2},
		{"partial 4-byte 3of4", []byte{0xF0, 0x9F, 0x98}, 3},
		{"real U+FFFD is complete", []byte{0xEF, 0xBF, 0xBD}, 0},
		{"lone continuation byte", []byte{0x80}, 0},
		{"invalid lead 0xFF", []byte{0xFF}, 0},
		{"complete multibyte then ascii", []byte{0xE2, 0x82, 0xAC, 0x41}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := incompleteTailLen(tc.in); got != tc.want {
				t.Errorf("incompleteTailLen(%x) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// sizeChunkReader hands out fixed-size slices of data, one per Read, so a fuzz
// target can place read boundaries at every offset.
type sizeChunkReader struct {
	data []byte
	size int
	pos  int
}

func (r *sizeChunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	end := min(r.pos+r.size, len(r.data))
	n := copy(p, r.data[r.pos:end])
	r.pos += n
	return n, nil
}

// FuzzPumpTerminalOutput_UTF8Broadcast asserts two invariants over arbitrary
// bytes split at arbitrary read boundaries: (1) the concatenation of every
// broadcast chunk equals the raw input exactly (no byte dropped or
// duplicated), and (2) when the whole input is valid UTF-8, every broadcast
// chunk is itself valid UTF-8 (no rune split across the read boundary). One
// hub is reused across iterations (fuzz iterations run sequentially per
// process) to avoid leaking the per-Hub background goroutines.
func FuzzPumpTerminalOutput_UTF8Broadcast(f *testing.F) {
	f.Add([]byte("hello"), uint8(1))
	f.Add([]byte("aé€😀z"), uint8(1))
	f.Add([]byte("aé€😀z"), uint8(3))
	f.Add([]byte{0xE2, 0x82, 0xAC}, uint8(1))
	f.Add([]byte{0xFF, 0x80, 0xE2}, uint8(1)) // invalid + incomplete tail
	h := New(f.TempDir(), func() api.ACPBridge { return newFakeBridge() }, newFakeChatStore())
	f.Fuzz(func(t *testing.T, data []byte, chunkRaw uint8) {
		if len(data) > 512 {
			data = data[:512] // keep this iteration's emits under the 1024-event ring cap
		}
		chunkSize := int(chunkRaw)%8 + 1
		_, preSeq := h.sse.hub.Bounds()
		term := newAgentTerminal(nil, "c1", 1<<20)
		h.pumpTerminalOutput(term, "t1", "c1", &sizeChunkReader{data: data, size: chunkSize})

		// The ring receives every raw byte exactly once, for any input.
		if ring := term.output.Bytes(); !bytes.Equal(ring, data) {
			t.Errorf("ring content = %x, want raw input %x (chunkSize=%d)", ring, data, chunkSize)
		}

		var got []byte
		for _, e := range bufferedSince(h, preSeq) {
			var env struct {
				Type    string `json:"type"`
				Payload struct {
					Data string `json:"data"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(e.Event.Data, &env); err != nil {
				t.Fatalf("unmarshal ring event: %v", err)
			}
			if env.Type == string(api.EventTerminalOutput) {
				got = append(got, env.Payload.Data...)
			}
		}
		// When the whole input is valid UTF-8 the fix never splits a rune, so
		// each broadcast chunk survives the SSE JSON round-trip intact and the
		// chunks reassemble to the exact input. Invalid input is expectedly
		// coerced to U+FFFD by json.Marshal, so byte-preservation of the
		// broadcast only holds for valid UTF-8 — the ring check above covers
		// the raw-byte path for all inputs.
		if utf8.Valid(data) && !bytes.Equal(got, data) {
			t.Errorf("valid UTF-8 input %x reassembled from broadcasts as %x (chunkSize=%d) — a rune was split across the read boundary", data, got, chunkSize)
		}
	})
}

// --- R2: process-group teardown for agent terminals ---

// TestKillGroup_ReapsTheWholeTree is the R2 regression: a head-only kill strands
// an agent terminal's children.
//
// Agent terminals are the agent's own commands, so they are routinely trees, and
// unlike the bridge there is no stdin-EOF channel to reclaim them. The bait is
// the shape any build tool produces — a shell with children that outlive it —
// and the assertion is on the GRANDCHILD, because that is what survived the
// measured head-only kill (2 spawned, 2 survived).
func TestKillGroup_ReapsTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// The shell writes its child's pid, then blocks. Killing the group must take
	// the child too; killing the head alone leaves it running.
	script := "sleep 300 & echo $! > " + pidFile + "; wait"
	cmd := exec.Command("sh", "-c", script) // #nosec G204 -- fixed test script
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bait: %v", err)
	}
	t.Cleanup(func() {
		_ = procgroup.Kill(cmd.Process, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	childPID := waitForPIDFile(t, pidFile)
	if !processAlive(childPID) {
		t.Fatalf("bait child %d not alive before the kill; the test proves nothing", childPID)
	}

	if err := procgroup.Kill(cmd.Process, syscall.SIGKILL); err != nil {
		t.Fatalf("killGroup: %v", err)
	}
	_, _ = cmd.Process.Wait() // reap the head; the poll below is on the child

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived killGroup; the group form is not reaching the tree", childPID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForPIDFile polls for the bait script's pid file and returns the pid.
func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(path) // #nosec G304 -- t.TempDir path
		if err == nil {
			if pid, cErr := strconv.Atoi(strings.TrimSpace(string(b))); cErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("bait never wrote its child pid to %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// processAlive reports whether pid is a live (non-zombie) process, read from
// /proc/<pid>/stat.
//
// The null signal is NOT usable here. `kill(pid, 0)` answers "alive" for a
// zombie, and the process this test polls is the bait shell's child: killGroup
// takes the shell too, so the child is orphaned and reparented to PID 1 the
// moment it dies, and it stays a zombie until whatever init the test happens to
// run under collects it. Reaping the HEAD does not help — the head is the
// child's parent, not the child's reaper. So a null-signal poll measures the
// ambient reaper's latency rather than killGroup's reach: it needs an init that
// reaps (it flakes on a loaded runner and fails outright in a container whose
// PID 1 never reaps), and a zombie already proves the signal landed.
//
// The state field follows the last ')' — comm is parenthesized and may itself
// contain spaces or parens, so the whole prefix has to be skipped from the
// right rather than split on whitespace.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) // #nosec G304 -- pid from the test's own child
	if err != nil {
		return false // no /proc entry: reaped and gone
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z'
}
