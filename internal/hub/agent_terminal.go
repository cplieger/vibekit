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

	"github.com/cplieger/vibekit/internal/api"
)

// keySignal is the wire key for a terminating signal in an ACP terminal
// exit status (KAS zTerminalExitStatus {exitCode?, signal?}).
const keySignal = "signal"

// agentTerminal is one headless subprocess spawned by kiro-cli.
type agentTerminal struct {
	exitErr  error
	cmd      *exec.Cmd
	done     chan struct{}
	output   *byteRing
	chatID   api.ChatID
	signal   string
	exitCode int
	mu       sync.Mutex
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

// agentTerminals holds all active terminals keyed by terminal ID.
type agentTerminals struct {
	terms    map[string]*agentTerminal
	byChatID map[api.ChatID][]string // chatID → []terminalID
	mu       sync.Mutex
}

func newAgentTerminals() *agentTerminals {
	return &agentTerminals{
		terms:    make(map[string]*agentTerminal),
		byChatID: make(map[api.ChatID][]string),
	}
}

// KillForChat kills all terminals belonging to chatID and removes them
// from both maps. Called from cleanupChatState to prevent orphaned
// subprocesses when a chat is deleted.
func (at *agentTerminals) KillForChat(chatID api.ChatID) {
	at.mu.Lock()
	ids := at.byChatID[chatID]
	delete(at.byChatID, chatID)
	for _, id := range ids {
		term, ok := at.terms[id]
		if ok {
			delete(at.terms, id)
			if term.cmd.Process != nil {
				if err := term.cmd.Process.Kill(); err != nil {
					slog.Debug("hub: agent terminal kill failed", "id", id, "error", err)
				}
			}
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
	for id := range terms {
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
	}
}

func (h *Hub) termCreate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var params struct {
		Command string   `json:"command"`
		Cwd     string   `json:"cwd"`
		Args    []string `json:"args"`
		// Env is the ACP terminal/create env array (KAS zEnvVariable
		// {name,value}); populated into cmd.Env so env-dependent agent
		// commands run with the requested variables.
		Env             []termEnvVar `json:"env"`
		OutputByteLimit int          `json:"outputByteLimit"`
	}
	if parseRequest(msg, &params) != nil {
		respondErr(ctx, h, chatID, msg, "invalid params")
		return
	}
	if params.Command == "" {
		respondErr(ctx, h, chatID, msg, "command is required")
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
	cmd := exec.CommandContext(cmdCtx, params.Command, params.Args...) // #nosec G204 -- agent-controlled
	// Graceful shutdown: SIGTERM on context cancel, escalate to SIGKILL
	// after 2s. Matches the cplieger Go apps' consistent subprocess
	// management pattern (fclones, bridge, subflux/ffmpeg).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if env := termEnv(params.Env); env != nil {
		cmd.Env = env
	}
	if params.Cwd != "" {
		abs, err := h.resolveInsideWorkDir(params.Cwd)
		if err != nil {
			stop()
			cmdCancel()
			respondErr(ctx, h, chatID, msg, "cwd escapes workspace: "+err.Error())
			return
		}
		cmd.Dir = abs
	} else {
		cmd.Dir = h.lifecycle.workDir
	}

	term := &agentTerminal{
		cmd:    cmd,
		done:   make(chan struct{}),
		output: newByteRing(limit),
		chatID: chatID,
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		cmdCancel()
		respondErr(ctx, h, chatID, msg, err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stop()
		cmdCancel()
		respondErr(ctx, h, chatID, msg, err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		stop()
		cmdCancel()
		respondErr(ctx, h, chatID, msg, err.Error())
		return
	}

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
	h.agentTerms.terms[termID] = term
	h.agentTerms.byChatID[chatID] = append(h.agentTerms.byChatID[chatID], termID)
	h.agentTerms.mu.Unlock()

	slog.Info("agent terminal created", "chat_id", chatID, "term_id", termID, "cmd", params.Command)
	h.Broadcast(ctx, api.NewEvent(api.EventTerminalCreated, chatID, api.TerminalCreatedPayload{
		TerminalID: termID,
		Command:    params.Command,
		Args:       params.Args,
	}))
	respondOK(ctx, h, chatID, msg, map[string]string{"terminalId": termID})

	// Stream stdout + stderr into the ring buffer. Started only after the
	// terminal is registered and terminal_created is broadcast (see above).
	h.lifecycle.inflight.Go(func() {
		h.pumpTerminalOutput(term, termID, chatID, io.MultiReader(stdout, stderr))
	})

	// Wait for exit in background.
	h.lifecycle.inflight.Go(func() {
		h.awaitTerminalExit(term, termID, chatID, cmd, stop, cmdCancel)
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
	broadcast := func(data string) {
		h.Broadcast(ctx, api.NewEvent(api.EventTerminalOutput, chatID, api.TerminalOutputPayload{
			TerminalID: termID,
			Data:       data,
		}))
	}
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
			if emit := chunk[:len(chunk)-hold]; len(emit) > 0 {
				broadcast(string(emit))
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
		broadcast(string(pending))
	}
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
func (h *Hub) awaitTerminalExit(term *agentTerminal, termID string, chatID api.ChatID, cmd *exec.Cmd, stop func() bool, cmdCancel context.CancelFunc) {
	// Hub-scoped context for the broadcast (outlives the per-event ctx).
	ctx, cancel := h.hubContext()
	defer cancel()
	err := cmd.Wait()
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
		if err := term.cmd.Process.Kill(); err != nil {
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
		if err := term.cmd.Process.Kill(); err != nil {
			slog.Warn("terminal kill failed", "term_id", params.TerminalID, "error", err)
		}
	}
	// KAS zKillTerminalResponse is an empty object.
	respondOK(ctx, h, chatID, msg, map[string]any{})
}
