// Agent terminal handler for kiro-cli's terminal/* ACP requests.
//
// When the bridge declares terminal: true, kiro-cli sends terminal/create,
// terminal/output, terminal/release, terminal/wait_for_exit, and terminal/kill
// requests. Each terminal is a headless subprocess (os/exec.Command) with
// piped stdout/stderr and a byte-limited ring buffer for output.
//
// This matches ASAI's TerminalManager: plain child_process.spawn, NOT a PTY.
// No resize handling needed. The ring buffer is UTF-8 aware (advances past
// partial multi-byte characters at the cut point).

package hub

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/ansitext"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/procgroup"
	"github.com/cplieger/vibekit/internal/redact"
)

// keySignal is the wire key for a terminating signal in an ACP terminal
// exit status (KAS zTerminalExitStatus {exitCode?, signal?}).
const keySignal = "signal"

// terminalDrainGrace bounds how long awaitTerminalExit waits for the output
// pipe to reach EOF AFTER the process has exited, before it force-closes the
// read end. Only a grandchild still holding the write end can reach it. Matches
// cmd.WaitDelay, which bounds the mirror case inside Wait.
const terminalDrainGrace = 2 * time.Second

// agentTerminal is one headless subprocess spawned by kiro-cli.
type agentTerminal struct {
	exitErr error
	cmd     *exec.Cmd
	done    chan struct{}
	output  *byteRing
	// ansi carries SGR state and any incomplete escape across read boundaries,
	// so a colour opened in one 4 KB chunk still applies in the next and a
	// sequence split by the boundary never leaks bytes into the text. Used only
	// for the LIVE stream; the durable copy is re-parsed from the ring.
	ansi   *ansitext.Parser
	chatID api.ChatID
	signal string
	// sentUTF16 is how many UTF-16 code units of plain text have been broadcast,
	// which is what TerminalOutputPayload.Offset reports. It is NOT a byte
	// length: span offsets are UTF-16 units because the client indexes with
	// JavaScript string offsets, so a byte base would rebase every live span
	// onto the wrong character the moment output contained anything non-ASCII.
	sentUTF16 int
	// turnSeq is the chat's turn sequence at creation — which TURN spawned
	// this terminal. What lets an interrupt kill the turn's own processes
	// without touching a background command an earlier turn left running.
	turnSeq  uint64
	exitCode int
	mu       sync.Mutex
}

// newAgentTerminal builds one terminal with every field a running terminal
// needs. A constructor rather than a struct literal because two of those fields
// are easy to forget and the consequence is not a compile error: without `ansi`
// the pump nil-panics on the first byte, and without `output` the agent's own
// terminal/output pull returns nothing.
func newAgentTerminal(cmd *exec.Cmd, chatID api.ChatID, limit int) *agentTerminal {
	return &agentTerminal{
		cmd:    cmd,
		done:   make(chan struct{}),
		output: newByteRing(limit),
		ansi:   ansitext.NewParser(),
		chatID: chatID,
	}
}

// wireSpans converts the parser's spans to the wire shape. The two structs are
// deliberately separate: internal/ansitext stays a stdlib-only leaf that owns
// the parse, and internal/api owns every shape codegen projects into
// TypeScript. Same boundary as the ACP wire types in internal/translate, which
// convert into api types rather than being them.
func wireSpans(in []ansitext.Span) []api.TextSpan {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.TextSpan, len(in))
	for i, s := range in {
		out[i] = api.TextSpan{Start: s.Start, End: s.End, FG: s.FG, BG: s.BG, Attrs: s.Attrs}
	}
	return out
}

// termEnvVar is one entry of the ACP terminal/create `env` array (KAS
// zEnvVariable: {name, value}). Decoded into cmd.Env.
type termEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// termEnv layers the requested env vars on top of the current process
// environment. Returns nil when none are requested so cmd.Env stays nil
// and the child inherits os.Environ() unchanged (identical to the
// pre-env behaviour).
func termEnv(vars []termEnvVar) []string {
	if len(vars) == 0 {
		return nil
	}
	env := os.Environ()
	for _, v := range vars {
		env = append(env, v.Name+"="+v.Value)
	}
	return env
}

// exitStatusObject returns the ACP exit-status object for an exited
// terminal, matching KAS's zTerminalExitStatus ({exitCode?, signal?}). A
// signal-killed process reports {signal} with exitCode omitted (KAS
// requires exitCode>=0); a normal exit reports {exitCode}. Takes term.mu;
// call only after term.done is closed.
func (t *agentTerminal) exitStatusObject() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.signal != "" {
		return map[string]any{keySignal: t.signal}
	}
	return map[string]any{keyExitCode: t.exitCode}
}

// exitStatusFromState maps a finished process's state to (exitCode, signal).
// ProcessState.ExitCode() is -1 on signal death, which KAS rejects
// (zTerminalExitStatus requires exitCode>=0), so a signal death returns
// (0, "<signal>") and callers omit exitCode in favour of the signal
// string. A normal exit returns (code>=0, "").
func exitStatusFromState(st *os.ProcessState) (exitCode int, signal string) {
	if st == nil {
		return 0, ""
	}
	if code := st.ExitCode(); code >= 0 {
		return code, ""
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 0, ws.Signal().String()
	}
	return 0, "unknown"
}

// agentTerminals holds all active terminals keyed by terminal ID, plus the
// output records of terminals that have since gone away.
type agentTerminals struct {
	terms    map[string]*agentTerminal
	byChatID map[api.ChatID][]string // chatID → []terminalID
	// retired holds the RAW output of terminals no longer in terms, keyed by
	// the same id, because a terminal's output outlives the terminal.
	//
	// KAS releases a terminal as soon as it has read the output, and only THEN
	// reports the tool call's result: measured on a live run, release landed 3ms
	// after create (23:42:13.344 → .347) while the `completed` tool_call_update
	// came after the terminal_output frame. So a design that looks the terminal
	// up at completion is racing a lifetime it does not control, and loses every
	// time — the tool call persisted an empty output, which is the defect the
	// adoption path was added to fix.
	//
	// Keyed by terminal id rather than tool-call id on purpose: the tool call
	// learns its terminal id LATER still, so the id the pump has is the only one
	// available when the bytes arrive.
	retired map[string]retiredOutput
	// turnSeq is each chat's CURRENT turn ordinal, incremented at every
	// turn end. A terminal stamps the value at creation, so "this turn's
	// terminals" is a comparison rather than bookkeeping the create path
	// has to remember to do.
	turnSeq map[api.ChatID]uint64
	mu      sync.Mutex
}

// retiredOutput is what survives a terminal: its raw bytes and enough identity
// to evict the record at the right moment.
//
// Raw rather than rendered, because the rendered form is DERIVABLE and the ring
// already bounds it. Keeping a second accumulator would mean a second buffer, a
// second cap, and span offsets to rebase whenever that cap dropped bytes; the
// ring already keeps a bounded tail, so adoption parses it on demand instead.
type retiredOutput struct {
	raw     string
	chatID  api.ChatID
	turnSeq uint64
}

func newAgentTerminals() *agentTerminals {
	return &agentTerminals{
		terms:    make(map[string]*agentTerminal),
		byChatID: make(map[api.ChatID][]string),
		retired:  make(map[string]retiredOutput),
		turnSeq:  make(map[api.ChatID]uint64),
	}
}

// retire records a departing terminal's output so a later tool-call completion
// can still adopt it. Callers hold at.mu.
//
// Every path that removes a terminal from `terms` goes through here, so no path
// can forget: forgetting is silent (adoption simply finds nothing) and costs the
// stored output of that command.
func (at *agentTerminals) retire(id string, term *agentTerminal) {
	if term == nil || term.output == nil {
		return
	}
	raw := term.output.String()
	if raw == "" {
		return
	}
	at.retired[id] = retiredOutput{raw: raw, chatID: term.chatID, turnSeq: term.turnSeq}
}

// takeRetired returns and consumes a retired terminal's raw output.
func (at *agentTerminals) takeRetired(id string) (string, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	rec, ok := at.retired[id]
	if !ok {
		return "", false
	}
	delete(at.retired, id)
	return rec.raw, true
}

// AdvanceTurn marks a chat's turn boundary. Called at turn end, so every
// terminal created since the LAST call belongs to the turn now closing —
// nothing creates agent terminals outside a turn.
//
// It is also when retired output records are evicted. A turn ends only after
// every tool call in it has settled, so any record still here has had its chance
// to be adopted; holding it longer would grow with the session.
func (at *agentTerminals) AdvanceTurn(chatID api.ChatID) {
	at.mu.Lock()
	closing := at.turnSeq[chatID]
	at.turnSeq[chatID]++
	for id, rec := range at.retired {
		if rec.chatID == chatID && rec.turnSeq <= closing {
			delete(at.retired, id)
		}
	}
	at.mu.Unlock()
}

// currentTurn reads a chat's turn ordinal. Callers hold at.mu.
func (at *agentTerminals) currentTurn(chatID api.ChatID) uint64 {
	return at.turnSeq[chatID]
}

// KillForTurn kills the terminals the CURRENT (still-open) turn created and
// leaves every other chat process alone. The interrupt gate (§5.6 R3): turn
// cancel used to tear down nothing — `KillForChat`'s only callers are the
// delete/close paths — so cancelling mid-`npm test` left the command running,
// owned by nobody, streaming into a turn that no longer existed. Scoped to
// the turn rather than the chat deliberately: KillForChat here would also
// kill a background command an EARLIER turn started on purpose.
func (at *agentTerminals) KillForTurn(chatID api.ChatID) {
	at.mu.Lock()
	cur := at.currentTurn(chatID)
	ids := at.byChatID[chatID]
	var doomed []*agentTerminal
	kept := ids[:0]
	for _, id := range ids {
		term, ok := at.terms[id]
		if !ok {
			continue
		}
		if term.turnSeq != cur {
			kept = append(kept, id)
			continue
		}
		at.retire(id, term)
		delete(at.terms, id)
		doomed = append(doomed, term)
	}
	at.byChatID[chatID] = kept
	at.mu.Unlock()
	for _, term := range doomed {
		if term.cmd.Process != nil {
			if err := procgroup.Kill(term.cmd.Process, syscall.SIGKILL); err != nil {
				slog.Debug("hub: turn terminal kill failed", "chat_id", chatID, "error", err)
			}
		}
	}
	if len(doomed) > 0 {
		slog.Info("interrupt: killed the turn's terminals", "chat_id", chatID, "count", len(doomed))
	}
}

// KillForChat kills all terminals belonging to chatID and removes them
// from both maps. Called from cleanupChatState to prevent orphaned
// subprocesses when a chat is deleted.
//
// This is the one removal path that does NOT retire the output: the chat is
// being deleted, so there is no transcript left to adopt into. It drops any
// record the chat already had for the same reason.
func (at *agentTerminals) KillForChat(chatID api.ChatID) {
	at.mu.Lock()
	ids := at.byChatID[chatID]
	delete(at.byChatID, chatID)
	for _, id := range ids {
		term, ok := at.terms[id]
		if ok {
			delete(at.terms, id)
			if term.cmd.Process != nil {
				if err := procgroup.Kill(term.cmd.Process, syscall.SIGKILL); err != nil {
					slog.Debug("hub: agent terminal kill failed", "id", id, "error", err)
				}
			}
		}
	}
	for id, rec := range at.retired {
		if rec.chatID == chatID {
			delete(at.retired, id)
		}
	}
	at.mu.Unlock()
}

// drainAll waits for all terminals to exit. Each terminal's context is
// derived from shutdownCtx; when that cancels, cmd.Cancel sends SIGTERM
// and cmd.WaitDelay escalates to SIGKILL after 2s. This function just
// waits for the done channels to close, then clears the maps.
func (at *agentTerminals) drainAll() {
	at.mu.Lock()
	terms := make(map[string]*agentTerminal, len(at.terms))
	maps.Copy(terms, at.terms)
	at.mu.Unlock()

	if len(terms) == 0 {
		return
	}

	for _, term := range terms {
		<-term.done
	}

	// Clear maps.
	at.mu.Lock()
	for id, term := range terms {
		at.retire(id, term)
		delete(at.terms, id)
	}
	at.byChatID = make(map[api.ChatID][]string)
	at.mu.Unlock()
}

// release removes terminalID from both maps and returns the removed
// terminal (nil, false if it wasn't present). It does not kill the
// process — callers do that outside the lock.
func (at *agentTerminals) release(terminalID string) (*agentTerminal, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	term, ok := at.terms[terminalID]
	if !ok {
		return nil, false
	}
	at.retire(terminalID, term)
	delete(at.terms, terminalID)
	if term.chatID != "" {
		ids := at.byChatID[term.chatID]
		for i, id := range ids {
			if id == terminalID {
				at.byChatID[term.chatID] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
	}
	return term, true
}

// handleTerminalRequest dispatches terminal/* ACP requests.
func (h *Hub) handleTerminalRequest(ctx context.Context, chatID api.ChatID, method string, msg *api.RPCResponse) {
	switch method {
	case methodTermCreate:
		h.termCreate(ctx, chatID, msg)
	case methodTermOutput:
		h.termOutput(ctx, chatID, msg)
	case methodTermRelease:
		h.termRelease(ctx, chatID, msg)
	case methodTermWaitForExit:
		h.termWaitForExit(ctx, chatID, msg)
	case methodTermKill:
		h.termKill(ctx, chatID, msg)
	default:
		// The caller routes here on a `terminal/` PREFIX match, so a verb KAS
		// adds later reaches this switch and must still be answered. Falling
		// off the end left the request pending forever, and because
		// Bridge.Call has no client-side deadline that wedges the turn rather
		// than failing it (see translateACPEvent's refusal branch for the full
		// mechanism). Refusing is the honest answer: vibekit does not implement
		// the verb, and KAS can surface that to the model.
		if msg.ID == nil {
			return
		}
		slog.Warn("chat bridge: refusing an unimplemented terminal verb",
			"method", method, "chat_id", chatID, "id", *msg.ID)
		if err := h.BridgeRespond(ctx, chatID, *msg.ID, nil,
			&api.RPCError{
				Code:    api.RPCCodeMethodNotFound,
				Message: "unimplemented terminal method: " + method,
			}); err != nil {
			slog.Error("chat bridge: terminal refusal could not be delivered; the turn may be wedged",
				"method", method, "chat_id", chatID, "error", err)
		}
	}
}

// agentShell resolves the shell that runs an agent command LINE, once per
// process. bash first (KAS names the tool `execute_bash`, and agents write
// bash-isms: `[[ ]]`, process substitution, `${var/x/y}`), POSIX sh as the
// fallback for an image without bash.
var agentShell = sync.OnceValue(func() string {
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/sh"
})

// agentCommand builds the process for one terminal/create.
//
// ACP's CreateTerminalRequest is `{command, args?}` and KAS leaves `args`
// UNSET, putting the whole command line in `command` — measured 2026-08-17
// against kiro-cli 2.18.0 (the KAS bundle's request schema marks args
// optional, and every observed frame carried `"args":null` with the full line
// in `command`). Handing that straight to exec.Command makes the ENTIRE string
// the executable path, so every agent command containing a space died with
// `exec: "echo \"hello\"": executable file not found in $PATH`. Only a bare
// binary name or a bare path worked, which made the agent's job close to
// impossible: no flags, no arguments, no pipelines, no redirection.
//
// So an ABSENT `args` means `command` is a command line and runs through a
// shell. A PRESENT `args` — including an empty one — means the sender already
// split the argv and is exec'd directly: the schema permits it, this client
// honours it, and neither reading has to guess which one it got.
//
// Presence, not length. `{"command":"prog","args":[]}` says "exec prog with no
// arguments", which is a different statement from omitting the field, and a
// length test collapses them: a program whose name contains a space or a shell
// metacharacter would be handed to bash despite the sender having said it was a
// bare program name. Hence the pointer at the decode site.
func agentCommand(ctx context.Context, command string, args *[]string) *exec.Cmd {
	if args != nil {
		return exec.CommandContext(ctx, command, *args...) // #nosec G204 -- agent-controlled
	}
	return exec.CommandContext(ctx, agentShell(), "-c", command) // #nosec G204 -- agent-controlled
}

// derefArgs flattens the presence-preserving pointer for the wire.
func derefArgs(args *[]string) []string {
	if args == nil {
		return nil
	}
	return *args
}

// failTermCreate answers a terminal/create that could not start, and logs it.
// Every failure path goes through this one door, because before it six
// respondErr returns logged nothing at all: a command that failed to exec left
// NO server-side trace, broadcast no event and produced no tab, so the only
// party that ever learned was the agent, in its own tool result. That is how
// the exec-without-a-shell bug above stayed invisible for as long as it did.
func (h *Hub) failTermCreate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, command, reason string) {
	slog.Warn("agent terminal create failed", "chat_id", chatID, "cmd", command, "reason", reason)
	respondErr(ctx, h, chatID, msg, reason)
}

func (h *Hub) termCreate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		// Args is a POINTER so an explicitly empty array stays distinguishable
		// from an omitted field. `{"command":"prog","args":[]}` is a request to
		// exec `prog` with no arguments, which is not the same statement as
		// omitting args, and treating the two alike would hand a command line to
		// a shell that the sender had already decided was a bare program name.
		Args *[]string `json:"args"`
		// Env is the ACP terminal/create env array (KAS zEnvVariable
		// {name,value}); populated into cmd.Env so env-dependent agent
		// commands run with the requested variables.
		Env             []termEnvVar `json:"env"`
		OutputByteLimit int          `json:"outputByteLimit"`
	}
	if parseRequest(msg, &params) != nil {
		h.failTermCreate(ctx, chatID, msg, params.Command, "invalid params")
		return
	}
	if params.Command == "" {
		h.failTermCreate(ctx, chatID, msg, params.Command, "command is required")
		return
	}

	limit := outputBufferLimit
	if params.OutputByteLimit > 0 && params.OutputByteLimit < limit {
		limit = params.OutputByteLimit
	}

	// The command must outlive the per-event ctx: translateACPEvent cancels
	// that ctx the moment it returns, and a child of it would SIGTERM the
	// just-spawned process before it does any work (C2). WithoutCancel keeps
	// the ctx values but strips its cancellation; the AfterFunc re-attaches
	// shutdown-scoped teardown so the command still dies on hub shutdown
	// (and on terminal_kill / terminal_release / normal exit via cmdCancel).
	cmdCtx, cmdCancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(h.lifecycle.shutdownCtx, cmdCancel)
	cmd := agentCommand(cmdCtx, params.Command, params.Args)
	// Own process group, so every teardown path can signal the whole tree
	// rather than just the head. See procgroup.Kill for why the head alone is
	// not enough — for an agent terminal or, since kiro-cli 2.18.0, for the
	// bridge either.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Graceful shutdown: SIGTERM on context cancel, escalate to SIGKILL
	// after 2s. Matches the cplieger Go apps' consistent subprocess
	// management pattern (fclones, bridge, subflux/ffmpeg).
	cmd.Cancel = func() error { return procgroup.Kill(cmd.Process, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if env := termEnv(params.Env); env != nil {
		cmd.Env = env
	}
	if params.Cwd != "" {
		abs, err := h.resolveInsideWorkDir(params.Cwd)
		if err != nil {
			stop()
			cmdCancel()
			h.failTermCreate(ctx, chatID, msg, params.Command, "cwd escapes workspace: "+err.Error())
			return
		}
		cmd.Dir = abs
	} else {
		cmd.Dir = h.lifecycle.workDir
	}

	term := newAgentTerminal(cmd, chatID, limit)

	// One CALLER-OWNED pipe for both streams, not Cmd.StdoutPipe/StderrPipe.
	//
	// os/exec closes the pipes it hands out inside Wait, which is the hazard
	// Cmd.StdoutPipe documents ("it is incorrect to call Wait before all reads
	// from the pipe have completed"). Owning the read end means Wait cannot
	// truncate the pump, so the ordering constraint disappears instead of being
	// managed: Wait observes exit, and the drain is then bounded from THAT
	// moment. The earlier shape waited for the drain BEFORE Wait, which put the
	// grace clock on the wrong side of the process — any command running longer
	// than the grace exhausted it and fell straight back into the race.
	//
	// Merging both streams into one pipe also removes the interleaving question:
	// stdout and stderr arrive in the order the process wrote them, which is
	// what a terminal shows.
	pr, pw, err := os.Pipe()
	if err != nil {
		stop()
		cmdCancel()
		h.failTermCreate(ctx, chatID, msg, params.Command, "pipe: "+err.Error())
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		stop()
		cmdCancel()
		h.failTermCreate(ctx, chatID, msg, params.Command, err.Error())
		return
	}
	// The child holds its own duplicate of the write end, so closing ours is
	// what makes the reader see EOF when the last writer exits. Without it the
	// pump would block forever on a process that has already gone.
	_ = pw.Close()

	termID := newMessageID() // reuse the ID generator for unique terminal IDs

	// Register the terminal in the maps and broadcast terminal_created
	// BEFORE starting the pump/exit goroutines. emit() assigns monotonic
	// event ids in call order, and those goroutines emit terminal_output /
	// terminal_exited; if they ran first, a fast write-and-exit command
	// could emit output/exited with a lower event id than terminal_created.
	// The client would then drop those events (unknown terminal id) and
	// leave the tab stuck "running". Registering + broadcasting first
	// guarantees terminal_created is ordered ahead of any output/exit event.
	h.agentTerms.mu.Lock()
	term.turnSeq = h.agentTerms.currentTurn(chatID)
	h.agentTerms.terms[termID] = term
	h.agentTerms.byChatID[chatID] = append(h.agentTerms.byChatID[chatID], termID)
	h.agentTerms.mu.Unlock()

	slog.Info("agent terminal created", "chat_id", chatID, "term_id", termID, "cmd", params.Command)
	h.Broadcast(ctx, api.NewEvent(api.EventTerminalCreated, chatID, api.TerminalCreatedPayload{
		TerminalID: termID,
		Command:    params.Command,
		// The wire field stays a plain slice: the client only labels the tab
		// with it, so presence-versus-empty carries no meaning there.
		Args: derefArgs(params.Args),
	}))
	respondOK(ctx, h, chatID, msg, map[string]string{"terminalId": termID})

	// Stream the merged output into the ring buffer. Started only after the
	// terminal is registered and terminal_created is broadcast (see above).
	// drained closes at EOF, which awaitTerminalExit waits for after Wait.
	drained := make(chan struct{})
	h.lifecycle.inflight.Go(func() {
		defer close(drained)
		defer func() { _ = pr.Close() }()
		h.pumpTerminalOutput(term, termID, chatID, pr)
	})

	// Wait for exit in background.
	h.lifecycle.inflight.Go(func() {
		h.awaitTerminalExit(term, termID, chatID, cmd, stop, cmdCancel, drained, pr)
	})
}

// pumpTerminalOutput streams a terminal's combined stdout/stderr into
// its ring buffer and broadcasts each chunk to SSE clients until the
// reader hits EOF or an error.
func (h *Hub) pumpTerminalOutput(term *agentTerminal, termID string, chatID api.ChatID, r io.Reader) {
	// Hub-scoped context: this goroutine outlives the per-event ctx that
	// spawned it (translateACPEvent cancels that on return), so derive a
	// fresh one that lives until the reader hits EOF or the hub shuts down.
	ctx, cancel := h.hubContext()
	defer cancel()
	buf := getPumpBuf()
	defer pumpBufPool.Put(buf) //nolint:staticcheck // returned after loop exits

	// pending carries a short (≤3-byte) trailing remainder from the previous
	// read that formed an incomplete multi-byte UTF-8 rune split across the
	// 4 KB read boundary. It is prepended to the next chunk before
	// broadcasting so the live SSE terminal_output stream is always valid
	// UTF-8 — without it string()/JSON coerces the split rune to U+FFFD in the
	// browser's live view. The ring buffer still receives every raw byte
	// exactly once (the agent pull path via byteRing.String() runs its own
	// ToValidUTF8), so only the SSE broadcast needs the carry.
	var pending []byte
	emit := h.terminalEmitter(ctx, term, termID, chatID)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			term.mu.Lock()
			term.output.Write(buf[:n]) // ring gets every raw byte, unchanged
			term.mu.Unlock()

			// Assemble the broadcast chunk from any carried remainder plus the
			// new bytes, then split off a fresh incomplete-rune tail.
			chunk := buf[:n]
			if len(pending) > 0 {
				chunk = append(pending, chunk...)
			}
			hold := incompleteTailLen(chunk)
			if out := chunk[:len(chunk)-hold]; len(out) > 0 {
				emit(string(out))
			}
			// Carry the incomplete tail (copied — buf is reused next Read).
			pending = append(pending[:0:0], chunk[len(chunk)-hold:]...)
		}
		if readErr != nil {
			break
		}
	}
	// Flush any leftover incomplete bytes at EOF so no output is lost; they
	// render as U+FFFD, matching the ring's ToValidUTF8 behaviour.
	if len(pending) > 0 {
		emit(string(pending))
	}
	// Release any escape sequence the stream ended mid-way through, so a
	// truncated sequence shows its bytes rather than vanishing.
	if text, parsed := term.ansi.Flush(); text != "" {
		h.publishTerminalText(ctx, term, termID, chatID, text, wireSpans(parsed))
	}
}

// terminalEmitter returns the function that turns one raw chunk into what the
// TRANSCRIPT renders: secrets masked, escape sequences parsed off into style
// spans, plain text out.
//
// Both halves matter. Redaction was previously absent on this surface, which was
// survivable only while the output went to a panel nobody opened; now that it is
// the tool card's content it gets the same treatment as any other agent output.
// And parsing server-side is what lets the client paint spans with textContent
// instead of building HTML from these bytes.
func (h *Hub) terminalEmitter(
	ctx context.Context, term *agentTerminal, termID string, chatID api.ChatID,
) func(string) {
	return func(raw string) {
		// SanitizeUnicode, NOT SanitizeOutput. SanitizeOutput is
		// SanitizeUnicode(StripANSI(s)) iterated to a fixed point, so calling it
		// here deleted every escape sequence BEFORE the parser could see one:
		// spans came out empty on every chunk and agent output rendered entirely
		// unstyled, which is worse than the library this replaced. Measured
		// through this exact path — `ESC[90m1:47AM…` produced spans=0 with
		// SanitizeOutput and spans=2 without. TestTerminalEmitter_* pins it.
		//
		// The security property SanitizeOutput's iteration provided is preserved
		// by the ORDER rather than by stripping. Hidden Unicode goes first, so
		// nothing can hide a sequence behind a zero-width character; the parser
		// then consumes SGR and drops every other escape family StripANSI
		// matched; and ansitext guarantees an escape-free output text (its
		// release paths neutralize a stray ESC, asserted by FuzzParse). So the
		// text reaching the chat file carries no residual escape, which is what
		// the strip was for.
		text, parsed := term.ansi.Write(redact.Output(api.SanitizeUnicode(raw)))
		if text == "" && len(parsed) == 0 {
			return
		}
		h.publishTerminalText(ctx, term, termID, chatID, text, wireSpans(parsed))
	}
}

// publishTerminalText broadcasts one rendered chunk. Offset is the count of
// UTF-16 code units already sent, read and advanced under the same lock, so a
// client can tell a contiguous chunk from one that followed a drop.
func (h *Hub) publishTerminalText(
	ctx context.Context, term *agentTerminal, termID string, chatID api.ChatID,
	text string, spans []api.TextSpan,
) {
	term.mu.Lock()
	offset := term.sentUTF16
	term.sentUTF16 += utf16Len(text)
	term.mu.Unlock()
	h.Broadcast(ctx, api.NewEvent(api.EventTerminalOutput, chatID, api.TerminalOutputPayload{
		TerminalID: termID,
		Data:       text,
		Spans:      spans,
		Offset:     offset,
	}))
}

// utf16Len counts the UTF-16 code units a string occupies: one per rune below
// U+10000, two above (a surrogate pair). Mirrors internal/ansitext's own count,
// which is what produced the span offsets this offset has to agree with.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

// incompleteTailLen returns the number of trailing bytes of b that form an
// incomplete (truncated) leading UTF-8 sequence — the start of a multi-byte
// rune whose continuation bytes have not arrived yet. It returns 0 when b
// ends on a complete rune, is empty, or ends in a standalone invalid byte
// that will never complete. At most utf8.UTFMax-1 (3) bytes are ever held.
// Used by pumpTerminalOutput to avoid splitting a rune across the read
// boundary in the live SSE stream.
func incompleteTailLen(b []byte) int {
	if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
		// Ends on a complete rune (a real rune, or a genuine U+FFFD, which
		// encodes as 3 bytes → size > 1). Nothing to hold.
		return 0
	}
	// The final byte(s) don't decode as a complete rune. Walk back to the
	// lead byte of the trailing sequence (bounded to UTFMax) and hold it only
	// if it is a valid-but-not-yet-complete rune prefix.
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if lead := b[len(b)-i]; utf8.RuneStart(lead) {
			if utf8.FullRune(b[len(b)-i:]) {
				return 0 // a complete (though possibly invalid) rune — don't hold
			}
			return i
		}
	}
	return 0
}

// awaitTerminalExit blocks on cmd.Wait, records the exit status on the
// terminal, releases the per-terminal context resources (stop
// unregisters the shutdown AfterFunc, cmdCancel releases the command
// context), closes term.done, and broadcasts terminal_exited. A
// signal-killed process records a signal string (ProcessState.ExitCode()
// is -1) rather than exitCode -1, so the exit reports signal:"..." and
// omits exit_code (KAS's zTerminalExitStatus requires exitCode>=0).
func (h *Hub) awaitTerminalExit(
	term *agentTerminal, termID string, chatID api.ChatID, cmd *exec.Cmd,
	stop func() bool, cmdCancel context.CancelFunc,
	drained <-chan struct{}, pr *os.File,
) {
	// Hub-scoped context for the broadcast (outlives the per-event ctx).
	ctx, cancel := h.hubContext()
	defer cancel()

	// Wait FIRST, then drain. Safe in that order only because the pipe is
	// caller-owned (see termCreate): os/exec closes the pipes IT hands out
	// inside Wait, so with Cmd.StdoutPipe this order would truncate the pump.
	err := cmd.Wait()

	// Now bound the drain from the moment the process actually exited. The bound
	// is needed because EOF is not guaranteed: a command that leaves a
	// grandchild holding the write end (`some-daemon &`) keeps the pipe open
	// after the head exits. Closing our read end is what releases the pump in
	// that case — a plain timeout would leave the goroutine blocked on Read for
	// as long as the grandchild lived.
	//
	// terminal_exited must not be observable before the output it describes, so
	// the broadcast happens after this: measured 2026-08-16, a `whoami` sent its
	// exit as event 365 and its own "root\n" as 368, and the client painted the
	// exit footer above the line that produced it.
	select {
	case <-drained:
	case <-time.After(terminalDrainGrace):
		slog.Warn("agent terminal: output still open after exit; releasing the reader",
			"chat_id", chatID, "term_id", termID, "grace", terminalDrainGrace)
		_ = pr.Close()
		<-drained
	}

	stop()      // unregister the AfterFunc
	cmdCancel() // release the command context
	term.mu.Lock()
	if err != nil {
		term.exitErr = err
		term.exitCode, term.signal = exitStatusFromState(cmd.ProcessState)
	}
	sig := term.signal
	code := term.exitCode
	term.mu.Unlock()
	close(term.done)
	payload := api.TerminalExitedPayload{TerminalID: termID}
	if sig != "" {
		payload.Signal = sig
	} else {
		payload.ExitCode = &code
	}
	h.Broadcast(ctx, api.NewEvent(api.EventTerminalExited, chatID, payload))
}

func (h *Hub) termOutput(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if parseRequest(msg, &params) != nil {
		return
	}
	h.agentTerms.mu.Lock()
	term, ok := h.agentTerms.terms[params.TerminalID]
	h.agentTerms.mu.Unlock()
	if !ok {
		// Thread the real chatID (from the request) so the error response
		// resolves a bridge; an empty chatID makes respondErr's bridge
		// lookup miss and the error is silently dropped, hanging the agent.
		respondErr(ctx, h, chatID, msg, "terminal not found")
		return
	}
	term.mu.Lock()
	output := term.output.String()
	truncated := term.output.Truncated()
	term.mu.Unlock()

	result := map[string]any{"output": output, "truncated": truncated}
	// Check if the process has exited; v3/KAS zTerminalOutputResponse
	// requires exitStatus to be an object {exitCode?, signal?} (or null).
	select {
	case <-term.done:
		result["exitStatus"] = term.exitStatusObject()
	default:
	}
	respondOK(ctx, h, chatID, msg, result)
}

func (h *Hub) termRelease(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if parseRequest(msg, &params) != nil {
		return
	}
	term, ok := h.agentTerms.release(params.TerminalID)
	if ok && term.cmd.Process != nil {
		if err := procgroup.Kill(term.cmd.Process, syscall.SIGKILL); err != nil {
			slog.Warn("terminal release: kill failed", "term_id", params.TerminalID, "error", err)
		}
	}
	slog.Info("agent terminal released", "term_id", params.TerminalID)
	// Respond with the request's chatID (not the possibly-nil term.chatID)
	// so the ack resolves a bridge even for an unknown terminal id. KAS
	// zReleaseTerminalResponse is an empty object.
	respondOK(ctx, h, chatID, msg, map[string]any{})
}

func (h *Hub) termWaitForExit(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if parseRequest(msg, &params) != nil {
		return
	}
	h.agentTerms.mu.Lock()
	term, ok := h.agentTerms.terms[params.TerminalID]
	h.agentTerms.mu.Unlock()
	if !ok {
		// Real chatID so the not-found error resolves a bridge (see termOutput).
		respondErr(ctx, h, chatID, msg, "terminal not found")
		return
	}
	// Block until the process exits or the hub shuts down. Derive a fresh
	// hub-scoped context inside the goroutine: the per-event ctx is cancelled
	// the moment translateACPEvent returns (before this async responder runs),
	// and Bridge.Respond drops a write on a cancelled ctx — which would hang
	// the agent's wait_for_exit Call.
	h.lifecycle.inflight.Go(func() {
		fctx, cancel := h.hubContext()
		defer cancel()
		select {
		case <-term.done:
			respondOK(fctx, h, chatID, msg, term.exitStatusObject())
		case <-h.lifecycle.done:
			// Shutdown in progress; bridge is dead, response is moot.
			return
		}
	})
}

func (h *Hub) termKill(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if parseRequest(msg, &params) != nil {
		return
	}
	h.agentTerms.mu.Lock()
	term, ok := h.agentTerms.terms[params.TerminalID]
	h.agentTerms.mu.Unlock()
	if !ok {
		// Real chatID so the not-found error resolves a bridge (see termOutput).
		respondErr(ctx, h, chatID, msg, "terminal not found")
		return
	}
	if term.cmd.Process != nil {
		if err := procgroup.Kill(term.cmd.Process, syscall.SIGKILL); err != nil {
			slog.Warn("terminal kill failed", "term_id", params.TerminalID, "error", err)
		}
	}
	// KAS zKillTerminalResponse is an empty object.
	respondOK(ctx, h, chatID, msg, map[string]any{})
}
