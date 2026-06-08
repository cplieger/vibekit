// Agent terminal handler for kiro-cli's terminal/* ACP requests.
//
// When the bridge declares terminal: true, kiro-cli sends terminal/create,
// terminal/output, terminal/release, terminal/waitForExit, and terminal/kill
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
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// agentTerminal is one headless subprocess spawned by kiro-cli.
type agentTerminal struct {
	exitErr  error
	cmd      *exec.Cmd
	done     chan struct{}
	output   *byteRing
	chatID   api.ChatID
	exitCode int
	mu       sync.Mutex
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
		Command         string   `json:"command"`
		Cwd             string   `json:"cwd"`
		Args            []string `json:"args"`
		OutputByteLimit int      `json:"outputByteLimit"`
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

	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.lifecycle.shutdownCtx, cancel)
	cmd := exec.CommandContext(ctx, params.Command, params.Args...) // #nosec G204 -- agent-controlled
	// Graceful shutdown: SIGTERM on context cancel, escalate to SIGKILL
	// after 2s. Matches the homelab Go apps' consistent subprocess
	// management pattern (fclones, bridge, subflux/ffmpeg).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if params.Cwd != "" {
		abs, err := h.resolveInsideWorkDir(params.Cwd)
		if err != nil {
			stop()
			cancel()
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
		cancel()
		respondErr(ctx, h, chatID, msg, err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stop()
		cancel()
		respondErr(ctx, h, chatID, msg, err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		stop()
		cancel()
		respondErr(ctx, h, chatID, msg, err.Error())
		return
	}

	termID := newMessageID() // reuse the ID generator for unique terminal IDs

	// Stream stdout + stderr into the ring buffer.
	h.lifecycle.inflight.Go(func() {
		multi := io.MultiReader(stdout, stderr)
		buf := getPumpBuf()
		defer pumpBufPool.Put(buf) //nolint:staticcheck // returned after loop exits
		for {
			n, readErr := multi.Read(buf)
			if n > 0 {
				term.mu.Lock()
				term.output.Write(buf[:n])
				term.mu.Unlock()
				// Broadcast output to SSE clients for the agent terminal tab.
				h.Broadcast(context.Background(), api.NewEvent(api.EventTerminalOutput, chatID, api.TerminalOutputPayload{
					TerminalID: termID,
					Data:       string(buf[:n]),
				}))
			}
			if readErr != nil {
				break
			}
		}
	})

	// Wait for exit in background.
	h.lifecycle.inflight.Go(func() {
		err := cmd.Wait()
		stop()   // unregister the AfterFunc
		cancel() // release context resources
		term.mu.Lock()
		if err != nil {
			term.exitErr = err
			term.exitCode = cmd.ProcessState.ExitCode()
		}
		term.mu.Unlock()
		close(term.done)
		h.Broadcast(context.Background(), api.NewEvent(api.EventTerminalExited, chatID, api.TerminalExitedPayload{
			TerminalID: termID,
			ExitCode:   term.exitCode,
		}))
	})

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
}

func (h *Hub) termOutput(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
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
		respondErr(ctx, h, "", msg, "terminal not found")
		return
	}
	term.mu.Lock()
	output := term.output.String()
	truncated := term.output.Truncated()
	chatID := term.chatID
	term.mu.Unlock()

	// Check if process has exited.
	var exitStatus *int
	select {
	case <-term.done:
		term.mu.Lock()
		code := term.exitCode
		term.mu.Unlock()
		exitStatus = &code
	default:
	}

	result := map[string]any{"output": output, "truncated": truncated}
	if exitStatus != nil {
		result["exitStatus"] = *exitStatus
	}
	respondOK(ctx, h, chatID, msg, result)
}

func (h *Hub) termRelease(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if parseRequest(msg, &params) != nil {
		return
	}
	h.agentTerms.mu.Lock()
	term, ok := h.agentTerms.terms[params.TerminalID]
	if ok {
		delete(h.agentTerms.terms, params.TerminalID)
		if term.chatID != "" {
			ids := h.agentTerms.byChatID[term.chatID]
			for i, id := range ids {
				if id == params.TerminalID {
					h.agentTerms.byChatID[term.chatID] = append(ids[:i], ids[i+1:]...)
					break
				}
			}
		}
	}
	h.agentTerms.mu.Unlock()
	if ok && term.cmd.Process != nil {
		if err := term.cmd.Process.Kill(); err != nil {
			slog.Warn("terminal release: kill failed", "term_id", params.TerminalID, "error", err)
		}
	}
	var chatID api.ChatID
	if term != nil {
		chatID = term.chatID
	}
	slog.Info("agent terminal released", "term_id", params.TerminalID)
	respondOK(ctx, h, chatID, msg, map[string]bool{"ok": true})
}

func (h *Hub) termWaitForExit(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
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
		respondErr(ctx, h, "", msg, "terminal not found")
		return
	}
	chatID := term.chatID
	// Block until the process exits or hub shuts down.
	h.lifecycle.inflight.Go(func() {
		select {
		case <-term.done:
			term.mu.Lock()
			code := term.exitCode
			term.mu.Unlock()
			respondOK(ctx, h, chatID, msg, map[string]int{"exitCode": code})
		case <-h.lifecycle.done:
			// Shutdown in progress; bridge is dead, response is moot.
			return
		}
	})
}

func (h *Hub) termKill(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
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
		respondErr(ctx, h, "", msg, "terminal not found")
		return
	}
	if term.cmd.Process != nil {
		if err := term.cmd.Process.Kill(); err != nil {
			slog.Warn("terminal kill failed", "term_id", params.TerminalID, "error", err)
		}
	}
	respondOK(ctx, h, term.chatID, msg, map[string]bool{"ok": true})
}
