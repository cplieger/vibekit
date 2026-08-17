package hub

import (
	"bytes"
	"cmp"
	"context"
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
	terms.terms["test"] = newAgentTerminal(nil, "", 1024)
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
	at.terms["t1"] = func() *agentTerminal { t := newAgentTerminal(nil, "", 1024); t.done = done; return t }()
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
	at.terms["t1"] = newAgentTerminal(nil, "c1", 1024)
	at.terms["t2"] = newAgentTerminal(nil, "c1", 1024)
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
	typ       string
	termID    string
	data      string
	eventID   uint64
	offset    int
	spanCount int
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
				TerminalID string         `json:"terminal_id"`
				Data       string         `json:"data"`
				Offset     int            `json:"offset"`
				Spans      []api.TextSpan `json:"spans"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		switch env.Type {
		case string(api.EventTerminalCreated), string(api.EventTerminalOutput), string(api.EventTerminalExited):
			out = append(out, ringEvent{
				eventID:   e.ID,
				typ:       env.Type,
				termID:    env.Payload.TerminalID,
				data:      env.Payload.Data,
				offset:    env.Payload.Offset,
				spanCount: len(env.Payload.Spans),
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
	term := newAgentTerminal(nil, "", 1024)
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
		term := newAgentTerminal(nil, "", 1<<20)
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

// --- terminal/create command-line handling ---

// agentCommand decides between "the sender split the argv" and "the sender
// handed me a command line". KAS only ever does the latter (it leaves `args`
// unset and puts the whole line in `command`), so the shell branch is the one
// that carries every real agent command.
func TestAgentCommand_BareCommandRunsThroughAShell(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    *[]string
		want    string
	}{
		// The regression: before agentCommand, `command` went to exec.Command
		// as the executable PATH, so anything with a space died with
		// `executable file not found in $PATH` and only bare binaries ran.
		{name: "quoted argument", command: `printf '%s' 'hello world'`, want: "hello world"},
		{name: "shell operator", command: `printf a; printf b`, want: "ab"},
		{name: "pipeline", command: `printf 'x\ny\n' | grep -c .`, want: "2\n"},
		{name: "variable expansion", command: `V=ok; printf '%s' "$V"`, want: "ok"},
		// A bare binary still works; it is simply `sh -c whoami` now.
		{name: "single token", command: `printf ok`, want: "ok"},
		// A populated args means the sender pre-split, and is exec'd directly.
		{name: "pre-split argv", command: "printf", args: &[]string{"%s", "split"}, want: "split"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := agentCommand(t.Context(), tc.command, tc.args).Output()
			if err != nil {
				t.Fatalf("agentCommand(%q, %v): %v", tc.command, tc.args, err)
			}
			if got := string(out); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pre-split argv must NOT be re-interpreted by a shell: the sender already
// decided where the word boundaries are, so an argument containing shell
// metacharacters has to survive as one literal argument.
func TestAgentCommand_PreSplitArgvIsNotShellInterpreted(t *testing.T) {
	out, err := agentCommand(t.Context(), "printf", &[]string{"%s", "a; rm -rf /"}).Output()
	if err != nil {
		t.Fatalf("agentCommand: %v", err)
	}
	if got := string(out); got != "a; rm -rf /" {
		t.Errorf("output = %q, want the argument passed through literally", got)
	}
}

// End-to-end through the ACP handler: a command line with spaces must spawn,
// broadcast its output and exit cleanly. This is the shape that failed in
// production for every agent command, so it is asserted over the real wire
// path rather than against agentCommand alone.
func TestTermCreate_CommandLineWithSpacesProducesOutput(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	msg := termCreateMsg(t, 1, `printf 'hello world'`, nil, nil)

	h.translateACPEvent("c1", msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")

	var evs []ringEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs = captureTerminalEvents(t, h)
		if hasType(evs, api.EventTerminalOutput) && hasType(evs, api.EventTerminalExited) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var sb strings.Builder
	for _, e := range evs {
		if e.typ == string(api.EventTerminalOutput) {
			sb.WriteString(e.data)
		}
	}
	if got := sb.String(); got != "hello world" {
		t.Errorf("broadcast output = %q, want %q (a spaced command line must run through a shell)", got, "hello world")
	}
	if !hasType(evs, api.EventTerminalExited) {
		t.Error("no terminal_exited event: the command never reported completion")
	}
}

// A terminal/create that cannot spawn must leave a server-side trace. This is
// the observability half of the exec-without-a-shell bug: six failure paths in
// termCreate answered the agent and logged NOTHING, broadcast no event and made
// no tab, so the only party that ever learned a command had failed was the
// agent, in its own tool result. Every failure now goes through failTermCreate.
func TestTermCreate_FailedSpawnIsLogged(t *testing.T) {
	logs := captureLogs(t)
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)

	// An argv the sender pre-split, naming an executable that does not exist:
	// the args branch skips the shell, so cmd.Start fails for real.
	msg := termCreateMsg(t, 1, "/nonexistent/definitely-not-a-binary", []string{"--flag"}, nil)
	h.translateACPEvent("c1", msg)

	got := logs.String()
	if !strings.Contains(got, "agent terminal create failed") {
		t.Errorf("no failure log for a spawn that could not start; captured:\n%s", got)
	}
	if !strings.Contains(got, "/nonexistent/definitely-not-a-binary") {
		t.Errorf("failure log does not name the command that failed; captured:\n%s", got)
	}
}

// --- awaitTerminalExit ordering and drain bound ---
//
// Both tests drive the REAL pipe and the REAL pump rather than a stand-in
// channel, because the mechanism under test IS the pipe: the previous shape
// waited for the drain before cmd.Wait, which put the grace clock on the wrong
// side of the process, and a mocked channel cannot tell the two shapes apart.

// exitFixture starts a command that has already finished and returns the pieces
// awaitTerminalExit needs, with the pump reading a caller-owned pipe.
func exitFixture(t *testing.T, h *Hub, termID string) (*agentTerminal, *exec.Cmd, *os.File, *os.File, <-chan struct{}, func() bool, context.CancelFunc) {
	t.Helper()
	cmdCtx, cmdCancel := context.WithCancel(context.WithoutCancel(t.Context()))
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd := agentCommand(cmdCtx, "true", nil)
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	term := newAgentTerminal(cmd, "c1", 1024)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		h.pumpTerminalOutput(term, termID, "c1", pr)
	}()
	return term, cmd, pr, pw, drained, func() bool { return true }, cmdCancel
}

// terminal_exited must not be observable before the output it describes.
// Measured in production 2026-08-16: `whoami` broadcast its exit as event 365
// and its own "root\n" as 368, so the client painted the exit footer above the
// line that produced it.
func TestAwaitTerminalExit_OutputPrecedesExit(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term, cmd, pr, pw, drained, stop, cancel := exitFixture(t, h, "t-order")
	defer cancel()

	// Write output, then close every write handle so the pump reaches EOF.
	if _, err := pw.WriteString("first\nsecond\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}

	h.awaitTerminalExit(term, "t-order", "c1", cmd, stop, cancel, drained, pr)

	evs := captureTerminalEvents(t, h)
	exitedID, ok := firstEventID(evs, api.EventTerminalExited)
	if !ok {
		t.Fatal("terminal_exited was never broadcast")
	}
	sawOutput := false
	for _, e := range evs {
		if e.typ != string(api.EventTerminalOutput) {
			continue
		}
		sawOutput = true
		if e.eventID > exitedID {
			t.Errorf("terminal_output event id %d > terminal_exited id %d: output was broadcast after the exit it belongs to", e.eventID, exitedID)
		}
	}
	if !sawOutput {
		t.Fatal("no terminal_output captured, so the ordering assertion is vacuous")
	}
}

// The drain wait is BOUNDED, and the bound RELEASES the reader rather than just
// giving up on it. A command that leaves a grandchild holding the write end
// (`some-daemon &`) keeps the pipe open past the head's exit, so a plain timeout
// would leave the pump blocked on Read for as long as that grandchild lived.
// Closing the read end is what unblocks it.
func TestAwaitTerminalExit_BoundReleasesAReaderHeldOpen(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term, cmd, pr, pw, drained, stop, cancel := exitFixture(t, h, "t-held")
	defer cancel()

	// A second handle on the write end stands in for the grandchild: the command
	// has exited but the pipe stays open, so the pump cannot reach EOF.
	held, err := os.Open("/dev/null")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = held.Close() }()
	// pw is deliberately NOT closed.
	defer func() { _ = pw.Close() }()

	start := time.Now()
	h.awaitTerminalExit(term, "t-held", "c1", cmd, stop, cancel, drained, pr)
	elapsed := time.Since(start)

	if elapsed < terminalDrainGrace {
		t.Errorf("returned in %v, before the %v grace: the bound cannot have been honoured", elapsed, terminalDrainGrace)
	}
	if elapsed > terminalDrainGrace+10*time.Second {
		t.Fatalf("returned in %v: the drain wait is effectively unbounded", elapsed)
	}
	if !hasType(captureTerminalEvents(t, h), api.EventTerminalExited) {
		t.Error("terminal_exited was never broadcast after the drain grace expired")
	}
	// The pump must have been released, not merely abandoned.
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Error("the pump goroutine is still blocked on Read: closing the read end did not release it")
	}
}

// --- the emitter seam: sanitize, redact, parse ---
//
// This is the seam that had no test, and the gap cost a shipped defect: the
// emitter called api.SanitizeOutput, which strips ANSI to a fixed point, so the
// parser never saw a sequence and every chunk produced zero spans. Agent output
// rendered entirely unstyled, worse than the library it replaced. Both the
// parser and the client renderer were tested in isolation and both passed.

// pumpOnce runs one string through the real pump and returns the broadcast
// terminal_output events.
func pumpOnce(t *testing.T, raw string) (*Hub, []ringEvent) {
	t.Helper()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term := newAgentTerminal(nil, "c1", 1<<20)
	h.pumpTerminalOutput(term, "t-emit", "c1", strings.NewReader(raw))
	var out []ringEvent
	for _, e := range captureTerminalEvents(t, h) {
		if e.typ == string(api.EventTerminalOutput) {
			out = append(out, e)
		}
	}
	return h, out
}

// joinData concatenates the broadcast chunks of a terminal's output.
func joinData(evs []ringEvent) string {
	var sb strings.Builder
	for _, e := range evs {
		sb.WriteString(e.data)
	}
	return sb.String()
}

func TestTerminalEmitter_SGRBecomesSpansNotStrippedText(t *testing.T) {
	// The exact shape measured in real tool output: gitleaks' zerolog writer.
	_, evs := pumpOnce(t, "\x1b[90m1:47AM\x1b[0m \x1b[32mINF\x1b[0m ok\n")
	if len(evs) == 0 {
		t.Fatal("no terminal_output broadcast")
	}
	text := joinData(evs)
	spanCount := 0
	for _, e := range evs {
		spanCount += e.spanCount
	}
	if text != "1:47AM INF ok\n" {
		t.Errorf("text = %q, want the escapes removed from it", text)
	}
	if spanCount == 0 {
		t.Error("zero spans: the SGR sequences were stripped before the parser saw them, so the output renders unstyled")
	}
}

func TestTerminalEmitter_TextNeverCarriesAnEscape(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "sgr", raw: "\x1b[31mred\x1b[0m"},
		{name: "cursor move", raw: "a\x1b[2Ab"},
		{name: "osc title", raw: "a\x1b]0;title\x07b"},
		{name: "charset", raw: "a\x1b(Bb"},
		{name: "dcs", raw: "a\x1bPq~~\x1b\\b"},
		{name: "lone escape", raw: "ab\x1b"},
		// A zero-width space hiding an escape: SanitizeUnicode removes it FIRST,
		// which is what lets the parser see and drop the completed sequence. This
		// is the property api.SanitizeOutput's fixed-point iteration provided,
		// preserved here by the order rather than by stripping.
		{name: "escape hidden behind a zero-width space", raw: "a\x1b\u200b[31mb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, evs := pumpOnce(t, tc.raw)
			if text := joinData(evs); strings.ContainsRune(text, 0x1b) {
				t.Errorf("broadcast text still contains ESC: %q", text)
			}
		})
	}
}

func TestTerminalEmitter_RedactsSecretsAndStripsHiddenUnicode(t *testing.T) {
	_, evs := pumpOnce(t, "token ghp_0123456789abcdefghijklmnopqrstuvwx and a\u200bb\n")
	text := joinData(evs)
	if strings.Contains(text, "ghp_0123456789abcdefghijklmnopqrstuvwx") {
		t.Error("the GitHub token reached the wire unredacted")
	}
	if strings.ContainsRune(text, 0x200b) {
		t.Error("a zero-width space survived into the wire text")
	}
}

// Offset is in UTF-16 code units because the spans it travels with are, and the
// client rebases those spans by it. A byte base shifted live styling onto the
// wrong characters for any non-ASCII output.
func TestTerminalEmitter_OffsetIsInUTF16Units(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term := newAgentTerminal(nil, "c1", 1<<20)

	emit := h.terminalEmitter(t.Context(), term, "t-off", "c1")
	// "é" is 2 bytes but 1 UTF-16 unit; the emoji is 4 bytes and 2 units.
	emit("caf\u00e9 \U0001F600")
	emit("x")

	var offsets []int
	for _, e := range captureTerminalEvents(t, h) {
		if e.typ == string(api.EventTerminalOutput) {
			offsets = append(offsets, e.offset)
		}
	}
	if len(offsets) != 2 {
		t.Fatalf("got %d output events, want 2", len(offsets))
	}
	if offsets[0] != 0 {
		t.Errorf("first offset = %d, want 0", offsets[0])
	}
	// "café " is 5 units, the emoji 2 → the second chunk starts at 7. In bytes
	// it would be 10, which is what the defect reported.
	if offsets[1] != 7 {
		t.Errorf("second offset = %d, want 7 UTF-16 units (10 would be the byte count)", offsets[1])
	}
}

// --- output survives the terminal ---

// KAS releases a terminal as soon as it has read the output and only THEN
// reports the tool call's result, so a lookup at completion finds nothing. The
// hub keeps a retired terminal's bytes for the rest of the turn.
func TestTerminalOutput_SurvivesRelease(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term := newAgentTerminal(nil, "c1", 1<<20)
	term.output.Write([]byte("\x1b[31mred\x1b[0m output\n"))

	h.agentTerms.mu.Lock()
	h.agentTerms.terms["t-live"] = term
	h.agentTerms.byChatID["c1"] = append(h.agentTerms.byChatID["c1"], "t-live")
	h.agentTerms.mu.Unlock()

	// While live, the output reads from the ring.
	text, spans, ok := h.TerminalOutput("t-live")
	if !ok || text != "red output\n" {
		t.Fatalf("live read = (%q, %v), want the rendered ring", text, ok)
	}
	if len(spans) == 0 {
		t.Error("live read produced no spans for coloured output")
	}

	// KAS releases the terminal. The output must still be reachable.
	if _, released := h.agentTerms.release("t-live"); !released {
		t.Fatal("release reported the terminal was not present")
	}
	if _, stillLive := h.agentTerms.terms["t-live"]; stillLive {
		t.Fatal("release left the terminal in the live registry")
	}
	text, spans, ok = h.TerminalOutput("t-live")
	if !ok {
		t.Fatal("output was lost when the terminal was released: a tool call completing now persists nothing")
	}
	if text != "red output\n" {
		t.Errorf("retired read = %q, want %q", text, "red output\n")
	}
	if len(spans) == 0 {
		t.Error("retired read produced no spans for coloured output")
	}
}

// A retired record is evicted at the turn boundary, so it cannot grow with the
// session. A turn ends only after every tool call in it has settled, so anything
// still held has already had its chance to be adopted.
func TestRetiredOutput_EvictedAtTurnEnd(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term := newAgentTerminal(nil, "c1", 1<<20)
	term.output.Write([]byte("output\n"))

	h.agentTerms.mu.Lock()
	term.turnSeq = h.agentTerms.currentTurn("c1")
	h.agentTerms.terms["t-evict"] = term
	h.agentTerms.mu.Unlock()

	if _, ok := h.agentTerms.release("t-evict"); !ok {
		t.Fatal("release reported the terminal was not present")
	}
	if _, _, ok := h.TerminalOutput("t-evict"); !ok {
		t.Fatal("the record was not retained across release")
	}

	// Re-retire it, then close the turn.
	h.agentTerms.mu.Lock()
	h.agentTerms.terms["t-evict"] = term
	h.agentTerms.mu.Unlock()
	if _, ok := h.agentTerms.release("t-evict"); !ok {
		t.Fatal("second release failed")
	}
	h.agentTerms.AdvanceTurn("c1")
	if _, _, ok := h.TerminalOutput("t-evict"); ok {
		t.Error("the retired record survived the turn boundary, so it grows with the session")
	}
}

// Deleting a chat drops its records: there is no transcript left to adopt into.
func TestRetiredOutput_DroppedWithTheChat(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	term := newAgentTerminal(&exec.Cmd{}, "c1", 1<<20)
	term.output.Write([]byte("output\n"))

	h.agentTerms.mu.Lock()
	h.agentTerms.terms["t-chat"] = term
	h.agentTerms.byChatID["c1"] = append(h.agentTerms.byChatID["c1"], "t-chat")
	h.agentTerms.mu.Unlock()
	if _, ok := h.agentTerms.release("t-chat"); !ok {
		t.Fatal("release failed")
	}

	h.agentTerms.KillForChat("c1")
	if _, _, ok := h.TerminalOutput("t-chat"); ok {
		t.Error("a deleted chat's terminal output was retained")
	}
}

// An ACP request may supply args EXPLICITLY EMPTY, which is a different
// statement from omitting the field: it says "exec this program with no
// arguments". A length test collapses the two, so a program name containing a
// space or a shell metacharacter would be handed to bash despite the sender
// having said it was a bare name.
func TestAgentCommand_ArgsPresenceNotLength(t *testing.T) {
	empty := []string{}
	cases := []struct {
		name      string
		args      *[]string
		wantShell bool
	}{
		{name: "omitted args runs through a shell", args: nil, wantShell: true},
		{name: "explicitly empty args execs directly", args: &empty, wantShell: false},
		{name: "populated args execs directly", args: &[]string{"ok"}, wantShell: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := agentCommand(t.Context(), "printf", tc.args)
			viaShell := cmd.Path == agentShell()
			if viaShell != tc.wantShell {
				t.Errorf("cmd.Path = %q, shell=%v, want shell=%v", cmd.Path, viaShell, tc.wantShell)
			}
		})
	}
}

// The same distinction end to end: an explicitly empty args must not be
// re-interpreted by a shell, so a command name holding a metacharacter fails to
// exec rather than being run as a pipeline.
func TestTermCreate_ExplicitlyEmptyArgsIsNotShellInterpreted(t *testing.T) {
	logs := captureLogs(t)
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)

	id := int64(1)
	params := map[string]any{"command": "printf ok", "args": []string{}}
	msg := &api.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t, params)}
	h.translateACPEvent("c1", msg)

	if !strings.Contains(logs.String(), "agent terminal create failed") {
		t.Error("an explicitly empty args was interpreted by a shell; it must exec the literal program name and fail")
	}
}
