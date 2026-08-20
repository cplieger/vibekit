package hub

// Tests for the four agent-terminal defects fixed together with the move of the
// ANSI parse to the server: the command line handed to exec without a shell, the
// silent create-failure paths, the exit event racing the output it describes, and
// the output nothing persisted.

import (
	"cmp"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// termCreateMsgArgs builds a terminal/create request with EXPLICIT control over
// whether `args` is present, which the shared termCreateMsg cannot express (it
// omits the key for an empty slice — and the presence of that key is the whole
// decision under test).
func termCreateMsgArgs(t *testing.T, id int64, command string, args *[]string) *vibekit.RPCResponse {
	t.Helper()
	params := map[string]any{"command": command}
	if args != nil {
		params["args"] = *args
	}
	return &vibekit.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t, params)}
}

// waitForTermExit drives one terminal/create to completion and returns the
// terminal's raw ring output.
func waitForTermExit(t *testing.T, h *Hub, msg *vibekit.RPCResponse) string {
	t.Helper()
	h.translateACPEvent("c1", msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	return term.rawOutput()
}

// BF1. ACP's CreateTerminalRequest is {command, args?} and KAS leaves args
// UNSET, putting the whole command LINE in command. exec.Command then treats
// that entire string as the executable path, so every agent command containing a
// space failed with `executable file not found in $PATH` — no flags, no
// arguments, no pipelines, no redirection. An absent args therefore runs through
// a shell.
func TestTermCreate_AbsentArgsRunsTheCommandLineThroughAShell(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
		reason  string
	}{{
		name: "argument", command: `echo hello world`, want: "hello world\n",
		reason: "the single most common shape: a command with arguments",
	}, {
		name: "quoting", command: `echo "spaced arg"`, want: "spaced arg\n",
		reason: "quotes are the shell's, and exec.Command never removes them",
	}, {
		name: "pipeline", command: `printf 'a\nb\n' | grep b`, want: "b\n",
		reason: "a pipeline has no meaning without a shell",
	}, {
		name: "redirection", command: `echo out 1>&2`, want: "out\n",
		reason: "stderr redirection, which also proves both streams share one pipe",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
			got := waitForTermExit(t, h, termCreateMsgArgs(t, 1, tc.command, nil))
			if got != tc.want {
				t.Errorf("output = %q, want %q (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

// BF1, the other half. The gate is PRESENCE, not length: {"command":"prog",
// "args":[]} says "exec prog with no arguments", which is a different statement
// from omitting the field. A length test collapses the two and would hand a
// program name carrying a space or a metacharacter to bash.
func TestTermCreate_PresentArgsExecsDirectly(t *testing.T) {
	t.Run("EmptyArgsIsStillDirectExec", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
		// A shell would print an empty line for this; a direct exec cannot find
		// a program by this name and produces nothing at all.
		empty := []string{}
		h.translateACPEvent("c1", termCreateMsgArgs(t, 1, "echo hi", &empty))
		h.agentTerms.mu.Lock()
		n := len(h.agentTerms.terms)
		h.agentTerms.mu.Unlock()
		if n != 0 {
			t.Errorf("registered %d terminals, want 0: an empty args must exec"+
				" %q as a program name, which does not exist", n, "echo hi")
		}
	})
	t.Run("NonEmptyArgsAreNotReSplitByAShell", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
		args := []string{"a b", "c"}
		got := waitForTermExit(t, h, termCreateMsgArgs(t, 1, "echo", &args))
		if got != "a b c\n" {
			t.Errorf("output = %q, want %q: a pre-split argv is passed through"+
				" verbatim, not re-parsed", got, "a b c\n")
		}
	})
}

// BF2. Six create-failure paths logged nothing, so a command that could not
// spawn left NO server-side trace: no line, no event, no tab. The only party
// that ever learned was the agent, in its own tool result — which is how BF1
// stayed invisible for a month.
func TestTermCreate_EveryFailurePathLogsAndAnswers(t *testing.T) {
	cases := []struct {
		name   string
		msg    func(t *testing.T) *vibekit.RPCResponse
		reason string
	}{{
		name: "InvalidParams",
		msg: func(t *testing.T) *vibekit.RPCResponse {
			t.Helper()
			id := int64(1)
			return &vibekit.RPCResponse{ID: &id, Method: methodTermCreate, Params: []byte(`{"command":5}`)}
		},
		reason: "a malformed frame",
	}, {
		name: "EmptyCommand",
		msg: func(t *testing.T) *vibekit.RPCResponse {
			t.Helper()
			return termCreateMsgArgs(t, 1, "", nil)
		},
		reason: "no command at all",
	}, {
		name: "CwdEscapesWorkspace",
		msg: func(t *testing.T) *vibekit.RPCResponse {
			t.Helper()
			id := int64(1)
			return &vibekit.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t,
				map[string]any{"command": "true", "cwd": "/etc"})}
		},
		reason: "a cwd outside the workspace",
	}, {
		name: "ExecFails",
		msg: func(t *testing.T) *vibekit.RPCResponse {
			t.Helper()
			nope := []string{}
			return termCreateMsgArgs(t, 1, "definitely-not-a-real-binary-xyz", &nope)
		},
		reason: "a program that does not exist — BF1's own symptom",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t) // not parallel: swaps the slog default
			br := newRecordingTermBridge()
			h := hubWithBridge(t, t.TempDir(), br)

			h.translateACPEvent("c1", tc.msg(t))

			if !strings.Contains(logs.String(), "agent terminal create failed") {
				t.Errorf("no server-side line for %s\nlogs: %s", tc.reason, logs.String())
			}
			resp, ok := br.lastResponse()
			if !ok || resp.err == nil {
				t.Errorf("the agent got no error response for %s (resp=%+v ok=%v)", tc.reason, resp, ok)
			}
		})
	}
}

// BF3. terminal_exited could be broadcast before the output it describes:
// measured, a `whoami` sent its exit as event 365 and its own "root\n" as 368,
// so the client painted the exit footer above the line that produced it. The
// pipe is caller-owned, so Wait runs FIRST and the drain is then bounded from
// the moment the process actually exited.
func TestTerminalExited_IsOrderedAfterEveryOutputEvent(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// A write immediately before exit is the shape that raced: the pump has to
	// be given no time at all, and the exit must still sort last.
	got := waitForTermExit(t, h, termCreateMsgArgs(t, 1, `printf 'root\n'`, nil))
	if got != "root\n" {
		t.Fatalf("ring = %q, want %q", got, "root\n")
	}

	var evs []ringEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs = captureTerminalEvents(t, h)
		if hasType(evs, vibekit.EventTerminalExited) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	exitedID, ok := firstEventID(evs, vibekit.EventTerminalExited)
	if !ok {
		t.Fatal("terminal_exited was never broadcast")
	}
	sawOutput := false
	for _, e := range evs {
		if e.typ != string(vibekit.EventTerminalOutput) {
			continue
		}
		sawOutput = true
		if e.eventID > exitedID {
			t.Errorf("terminal_output event %d came AFTER terminal_exited %d:"+
				" the client paints the exit footer above the line that produced it",
				e.eventID, exitedID)
		}
	}
	if !sawOutput {
		t.Error("no terminal_output event: the drain lost the command's only line")
	}
}

// The live stream carries PLAIN text plus spans, and the order of the two
// sanitizers is what makes that possible.
//
// sanitize.Output is SanitizeUnicode(StripANSI(s)) iterated to a fixed point,
// so calling it here would delete every escape BEFORE the parser saw one and
// every chunk would arrive unstyled — worse than the client library this
// replaced. SanitizeUnicode alone keeps the hidden-Unicode defence (nothing can
// hide a sequence behind a zero-width character) and leaves the escapes for the
// parser, whose own guarantee is that no ESC survives into the text.
func TestTerminalEmitter_ParsesStylingAndStillStripsHiddenUnicode(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	term := newAgentTerminal(nil, "c1", 4096)
	emit := h.agentTerms.terminalEmitter(t.Context(), term, "t1", "c1")

	// The measured real shape: gitleaks' zerolog console writer, with a
	// zero-width space smuggled into the middle of it.
	emit("\x1b[90m1:47AM\x1b[0m \u200b\x1b[32mINF\x1b[0m ok\n")

	got := terminalOutputPayloads(t, h)
	if len(got) != 1 {
		t.Fatalf("got %d terminal_output events, want 1", len(got))
	}
	p := got[0]
	if p.Data != "1:47AM INF ok\n" {
		t.Errorf("data = %q, want %q: escapes parsed off, zero-width space stripped",
			p.Data, "1:47AM INF ok\n")
	}
	if strings.ContainsRune(p.Data, 0x1b) || strings.ContainsRune(p.Data, 0x200b) {
		t.Errorf("data still carries an escape or a hidden character: %q", p.Data)
	}
	if len(p.Spans) != 2 {
		t.Errorf("spans = %d, want 2 (the timestamp's grey and the level's green)."+
			" 0 means SanitizeOutput stripped the escapes before the parser saw them", len(p.Spans))
	}
	if p.Offset != 0 {
		t.Errorf("offset = %d, want 0 for the first chunk", p.Offset)
	}
}

// terminalOutputPayloads decodes every terminal_output payload the hub has
// broadcast, in event-id order.
func terminalOutputPayloads(t *testing.T, h *Hub) []vibekit.TerminalOutputPayload {
	t.Helper()
	type idPayload struct {
		p  vibekit.TerminalOutputPayload
		id uint64
	}
	var found []idPayload
	for _, e := range h.sse.hub.Buffered() {
		var env struct {
			Type    string                        `json:"type"`
			Payload vibekit.TerminalOutputPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		if env.Type == string(vibekit.EventTerminalOutput) {
			found = append(found, idPayload{p: env.Payload, id: e.ID})
		}
	}
	slices.SortFunc(found, func(a, b idPayload) int { return cmp.Compare(a.id, b.id) })
	out := make([]vibekit.TerminalOutputPayload, len(found))
	for i, f := range found {
		out[i] = f.p
	}
	return out
}

// The wire's Offset is the base a client subtracts from the ABSOLUTE span
// offsets it receives, so it must be the parser's own UTF-16 count and not a
// byte length kept beside it. A byte base rebases every live span onto the wrong
// character the moment output contains anything non-ASCII.
func TestTerminalEmitter_OffsetIsTheUTF16BaseOfEachChunk(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	term := newAgentTerminal(nil, "c1", 4096)
	emit := h.agentTerms.terminalEmitter(t.Context(), term, "t1", "c1")

	emit("\U0001F600ok")       // 4 UTF-16 units (2 for the pair, 2 for "ok"), 6 bytes
	emit("\x1b[31mred\x1b[0m") // styled, must start at unit 4

	got := terminalOutputPayloads(t, h)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Offset != 0 {
		t.Errorf("first chunk offset = %d, want 0", got[0].Offset)
	}
	if got[1].Offset != 4 {
		t.Errorf("second chunk offset = %d, want 4 UTF-16 units"+
			" (a byte count would say 6)", got[1].Offset)
	}
	if len(got[1].Spans) != 1 {
		t.Fatalf("second chunk spans = %+v, want one", got[1].Spans)
	}
	// The span is absolute, so subtracting the reported base indexes the chunk
	// the client was handed. That is the whole contract.
	if s := got[1].Spans[0]; s.Start-got[1].Offset != 0 || s.End-got[1].Offset != 3 {
		t.Errorf("span %+v rebased by offset %d = [%d,%d), want [0,3) over %q",
			s, got[1].Offset, s.Start-got[1].Offset, s.End-got[1].Offset, got[1].Data)
	}
}

// BF4. Terminal output was never persisted, so a reload showed a command with
// no result. KAS puts no output on a successful terminal-backed
// tool_call_update AND releases the terminal about 3ms after creating it, before
// reporting the result — so any design that looks the terminal up at completion
// loses every time. A retired record, evicted at the turn boundary, is what
// makes the later adoption possible.
func TestTerminalOutput_SurvivesReleaseAndIsEvictedAtTheTurnBoundary(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	h.translateACPEvent("c1", termCreateMsgArgs(t, 1, `printf '\033[31mfail\033[0m\n'`, nil))
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	termID := onlyTermID(t, h)

	// KAS's release, which lands long before the completion that needs the bytes.
	if _, ok := h.agentTerms.release(termID); !ok {
		t.Fatalf("release(%q) found no terminal", termID)
	}
	text, spans, ok := h.TerminalOutput(termID)
	if !ok {
		t.Fatal("TerminalOutput found nothing after release: the record was not retired")
	}
	if text != "fail\n" {
		t.Errorf("text = %q, want %q (escapes parsed off, same as the live path)", text, "fail\n")
	}
	if len(spans) != 1 || spans[0].FG != 1 {
		t.Errorf("spans = %+v, want one red span", spans)
	}

	// A second read must answer the same. KAS can send more than one terminal
	// status frame for a tool call, and adoption runs on each; a consuming read
	// makes the second one report the output as missing.
	if _, _, ok := h.TerminalOutput(termID); !ok {
		t.Error("the second read found nothing: adoption is not idempotent," +
			" so a duplicate completed frame logs a false 'output missing'")
	}

	// The turn boundary is the eviction point: every tool call in the turn has
	// settled by then, so a record still here has had its chance.
	h.agentTerms.AdvanceTurn("c1")
	if _, _, ok := h.TerminalOutput(termID); ok {
		t.Error("the record survived the turn boundary, so it grows with the session")
	}
}

// A command that printed nothing is a different fact from a lost record, and
// only the second is worth a warning. Both must be distinguishable HERE, because
// the translate layer's diagnostic keys on exactly this boolean — and if a
// silent `mkdir -p` reported as missing, every turn would file a false alarm and
// the signal would be worth nothing.
func TestTerminalOutput_KnownButSilentIsNotMissing(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	h.translateACPEvent("c1", termCreateMsgArgs(t, 1, "true", nil))
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	termID := onlyTermID(t, h)

	t.Run("Live", func(t *testing.T) {
		text, spans, ok := h.TerminalOutput(termID)
		if !ok || text != "" || spans != nil {
			t.Errorf("TerminalOutput = (%q, %+v, %v), want (\"\", nil, true):"+
				" a registered terminal that printed nothing is not missing", text, spans, ok)
		}
	})
	t.Run("Retired", func(t *testing.T) {
		if _, ok := h.agentTerms.release(termID); !ok {
			t.Fatal("release found no terminal")
		}
		text, _, ok := h.TerminalOutput(termID)
		if !ok || text != "" {
			t.Errorf("TerminalOutput = (%q, _, %v), want (\"\", _, true):"+
				" a silent command's record must be retired like any other", text, ok)
		}
	})
	t.Run("Unknown", func(t *testing.T) {
		if _, _, ok := h.TerminalOutput("no-such-terminal"); ok {
			t.Error("an unknown id reported as known, so a genuine miss cannot be diagnosed")
		}
	})
}

// Deleting a chat is the one removal path that must NOT retire: there is no
// transcript left to adopt into, and holding the bytes would keep a deleted
// chat's command output in memory until the next turn boundary that never comes.
func TestKillForChat_DropsRetiredOutput(t *testing.T) {
	at := bareTerminals()
	term := newAgentTerminal(&exec.Cmd{}, "c1", 64)
	term.output.Write([]byte("secret\n"))
	at.terms["t1"] = term
	at.byChatID["c1"] = []string{"t1"}
	// A record from an earlier command in the same chat.
	at.retire("t0", term)

	at.KillForChat("c1")

	at.mu.Lock()
	n := len(at.retired)
	at.mu.Unlock()
	if n != 0 {
		t.Errorf("KillForChat left %d retired records for a deleted chat, want 0", n)
	}
}

// onlyTermID returns the sole registered terminal's id.
func onlyTermID(t *testing.T, h *Hub) string {
	t.Helper()
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	ids := make([]string, 0, len(h.agentTerms.terms))
	for id := range h.agentTerms.terms {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, cmp.Compare)
	if len(ids) != 1 {
		t.Fatalf("got %d registered terminals, want 1", len(ids))
	}
	return ids[0]
}

// The ring is written by the pump goroutine and read by three callers on other
// goroutines, and two of those run WHILE the process is still producing bytes —
// KAS releases a terminal a few milliseconds after creating it, well inside a
// command's lifetime. So the retire path and the adoption path both have to take
// the terminal's own lock, and this is the test that says so: it releases and
// adopts against a terminal that is still writing, which is a data race under
// -race if either read is unlocked.
func TestTerminalOutput_ReleaseAndAdoptWhileTheProcessIsStillWriting(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// A steady writer, so the pump is certainly mid-flight when the release lands.
	h.translateACPEvent("c1", termCreateMsgArgs(t, 1,
		`i=0; while [ $i -lt 400 ]; do printf 'line %s\n' "$i"; i=$((i+1)); done; sleep 2`, nil))
	term := singleTerm(t, h)
	termID := onlyTermID(t, h)

	// Wait for the first bytes so the pump is demonstrably running, then race it.
	deadline := time.Now().Add(5 * time.Second)
	for term.rawOutput() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the pump produced nothing in 5s")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := h.agentTerms.release(termID); !ok {
		t.Fatalf("release(%q) found no terminal", termID)
	}
	if _, _, ok := h.TerminalOutput(termID); !ok {
		t.Error("TerminalOutput found nothing for a terminal released mid-write")
	}
	waitClosed(t, term.done, "terminal")
}

// BF3's bound. EOF is not guaranteed after the process exits: a command that
// leaves a grandchild holding the write end (`some-daemon &`) keeps the pipe open
// after the head is gone. Closing the READ end is what releases the pump there —
// a plain timeout would leave the goroutine blocked on Read for as long as the
// grandchild lived, and the exit event would never be broadcast at all.
func TestAwaitTerminalExit_ForceClosesTheReaderWhenAGrandchildHoldsThePipe(t *testing.T) {
	logs := captureLogs(t) // not parallel: swaps the slog default
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// The head prints and exits immediately; `sleep` inherits the write end and
	// holds it far past the grace.
	h.translateACPEvent("c1", termCreateMsgArgs(t, 1,
		`sleep 30 & printf 'head done\n'`, nil))
	term := singleTerm(t, h)

	start := time.Now()
	waitClosed(t, term.done, "terminal exit")
	elapsed := time.Since(start)

	// Bounded by the grace, not by the grandchild's lifetime.
	if elapsed > terminalDrainGrace+3*time.Second {
		t.Errorf("exit took %v, want it bounded near the %v grace:"+
			" the reader was never force-closed", elapsed, terminalDrainGrace)
	}
	if !strings.Contains(logs.String(), "output still open after exit") {
		t.Errorf("no line about the forced release\nlogs: %s", logs.String())
	}
	// The head's own line still had to reach the wire before the exit.
	if got := term.rawOutput(); !strings.Contains(got, "head done") {
		t.Errorf("ring = %q, want the head's line: the drain dropped it", got)
	}
}
