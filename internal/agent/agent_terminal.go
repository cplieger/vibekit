// Agent terminal handler for kiro-cli's terminal/* ACP requests.
//
// Each terminal is a headless subprocess (os/exec.Command), NOT a PTY: piped
// stdout/stderr into a byte-limited, UTF-8-aware ring buffer, and no resize.

package agent

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/ansitext"
	"github.com/cplieger/vibekit/internal/procgroup"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// keySignal is the wire key for a terminating signal in an ACP terminal
// exit status (KAS zTerminalExitStatus {exitCode?, signal?}).
const keySignal = "signal"

// terminalDrainGrace bounds how long awaitExit waits for the output pipe to reach
// EOF AFTER the process has exited. Only a grandchild still holding the write end
// can reach it. Matches cmd.WaitDelay, which bounds the mirror case inside Wait.
const terminalDrainGrace = 2 * time.Second

// terminalGroupGrace bounds how long a teardown waits for the command's process
// GROUP to empty after the kill. Same 2 seconds as the drain, because it bounds
// the same population: a grandchild the head left behind.
const terminalGroupGrace = 2 * time.Second

// killTerminalGroup signals term's whole process group and then waits, bounded,
// for it to empty — so a caller that returns holds the FACT that the command is
// gone. Without the wait a grandchild outlives the reaped head, reparents to PID 1,
// and becomes exactly the orphan the group kill exists to prevent.
//
// Returns Kill's error for the caller's already-gone/failed split; the group not
// emptying is logged here, because that line is the same at every site.
func killTerminalGroup(term *agentTerminal, termID string) error {
	pgid, owns := procgroup.GroupOf(term.cmd.Process)
	err := procgroup.Kill(term.cmd.Process, syscall.SIGKILL)
	// Nothing to wait on when the command is not its own group leader: Kill fell back
	// to the head alone.
	if owns && !procgroup.WaitGone(pgid, terminalGroupGrace) {
		slog.Warn("agent terminal: process group still had a live member after the kill",
			"term_id", termID, "pgid", pgid, "grace", terminalGroupGrace)
	}
	return err
}

// agentTerminal is one headless subprocess spawned by kiro-cli.
type agentTerminal struct {
	exitErr error
	cmd     *exec.Cmd
	done    chan struct{}
	output  *byteRing
	// ansi carries SGR state and any incomplete escape across read boundaries, and
	// owns the running UTF-16 offset the wire reports as a chunk's base. LIVE stream
	// only. Owned by the pump goroutine alone, or two streams' styles bleed together.
	ansi   *ansitext.Parser
	chatID vibekit.ChatID
	signal string
	// epoch is the turn that spawned this terminal, so an interrupt can kill the
	// turn's own processes without touching an earlier turn's background command.
	epoch    vibekit.TurnEpoch
	exitCode int
	mu       sync.Mutex
}

// newAgentTerminal builds one terminal with every field a running terminal needs.
// A constructor rather than a literal because two of them fail silently: without
// `ansi` the pump nil-panics, and without `output` terminal/output returns nothing.
func newAgentTerminal(cmd *exec.Cmd, chatID vibekit.ChatID, limit int) *agentTerminal {
	return &agentTerminal{
		cmd:    cmd,
		done:   make(chan struct{}),
		output: newByteRing(limit),
		ansi:   ansitext.NewParser(),
		chatID: chatID,
	}
}

// rawOutput returns a snapshot of the terminal's raw ring under the same lock the
// pump writes it with.
//
// The lock is the point: the ring is written by the pump goroutine and read by
// three callers on other goroutines, two of them while the process is still
// producing bytes — KAS releases a terminal milliseconds after creating it.
func (t *agentTerminal) rawOutput() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output.String()
}

// wireSpans converts the parser's spans to the wire shape. The two structs are
// deliberately separate: internal/ansitext stays a stdlib-only leaf that owns the
// parse, and internal/vibekit owns every shape codegen projects into TypeScript.
func wireSpans(in []ansitext.Span) []vibekit.TextSpan {
	if len(in) == 0 {
		return nil
	}
	out := make([]vibekit.TextSpan, len(in))
	for i, s := range in {
		out[i] = vibekit.TextSpan{Start: s.Start, End: s.End, FG: s.FG, BG: s.BG, Attrs: s.Attrs}
	}
	return out
}

// termEnvVar is one entry of the ACP terminal/create `env` array (KAS
// zEnvVariable: {name, value}). Decoded into cmd.Env.
type termEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// termLocaleEnvVar is the locale variable an agent terminal's command inherits.
// TWIN of internal/bridge's localeEnvVar: internal/agent declares the subprocess
// contract at its consumer and never imports internal/bridge. Change one, change
// the other.
const termLocaleEnvVar = "LANG"

// termLocaleEnv pins the text encoding of an agent terminal's command. The runtime
// image ships no `locales` package, so an unset LANG leaves glibc's C locale and
// the command octal-escapes every non-ASCII path. C.UTF-8 needs no locale files.
func termLocaleEnv() []string {
	return []string{termLocaleEnvVar + "=C.UTF-8"}
}

// termEnv layers the requested env vars on top of the current process environment,
// then the locale pin. Returns nil when none are requested, so cmd.Env stays nil
// and the child inherits os.Environ() with the image's own LANG already in it.
//
// The pin lands LAST because os/exec keeps the last value for a repeated key. It
// covers LANG only: glibc gives LC_ALL and LC_CTYPE precedence whatever the order.
func termEnv(vars []termEnvVar) []string {
	if len(vars) == 0 {
		return nil
	}
	env := os.Environ()
	for _, v := range vars {
		env = append(env, v.Name+"="+v.Value)
	}
	return append(env, termLocaleEnv()...)
}

// exitStatusObject returns the ACP exit-status object for an exited terminal
// (KAS's zTerminalExitStatus). A signal-killed process reports {signal} with
// exitCode omitted, since KAS requires exitCode>=0. Takes term.mu; call only after
// term.done is closed.
func (t *agentTerminal) exitStatusObject() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.signal != "" {
		return map[string]any{keySignal: t.signal}
	}
	return map[string]any{keyExitCode: t.exitCode}
}

// exitStatusFromState maps a finished process's state to (exitCode, signal).
// ProcessState.ExitCode() is -1 on signal death, which KAS rejects, so a signal
// death returns (0, "<signal>") and callers omit exitCode in favour of the signal.
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
	byChatID map[vibekit.ChatID][]string // chatID → []terminalID
	// retired holds the RAW output of terminals no longer in terms: a terminal's
	// output outlives the terminal, since KAS releases it within milliseconds of
	// creating it. Bounded by the turn, each record holding at most one 64 KiB ring.
	retired map[string]retiredOutput
	// currentEpoch reads which turn a chat's activity belongs to right now, so a
	// terminal is stamped with the lifecycle's identity, not a parallel count.
	currentEpoch func(vibekit.ChatID) (vibekit.TurnEpoch, bool)
	// broadcast publishes a terminal's lifecycle and output frames.
	broadcast func(context.Context, vibekit.ServerEvent)
	// bridges answers the ACP request a terminal operation arrived on.
	bridges *bridgeManager
	// lifecycle supplies the process lifetime, the in-flight counter a pump must
	// register on, and the workspace dir a terminal's cwd is resolved inside.
	lifecycle *lifetime
	mu        sync.Mutex
}

// retiredOutput is what survives a terminal: its raw bytes and enough identity to
// evict the record at the right moment. Raw rather than rendered, because the ring
// already bounds it. Kept even when raw is EMPTY: existence answers "did this
// terminal ever run", so dropping empty records would make a silent command
// indistinguishable from a lost one.
type retiredOutput struct {
	raw    string
	chatID vibekit.ChatID
	// epoch is the turn that spawned the terminal, or zero when the chat had no
	// turn open at the time. A zero is evicted by the chat's NEXT turn close.
	epoch vibekit.TurnEpoch
}

func newAgentTerminals(bridges *bridgeManager, lc *lifetime,
	broadcast func(context.Context, vibekit.ServerEvent),
	currentEpoch func(vibekit.ChatID) (vibekit.TurnEpoch, bool),
) *agentTerminals {
	return &agentTerminals{
		terms:        make(map[string]*agentTerminal),
		byChatID:     make(map[vibekit.ChatID][]string),
		retired:      make(map[string]retiredOutput),
		bridges:      bridges,
		lifecycle:    lc,
		broadcast:    broadcast,
		currentEpoch: currentEpoch,
	}
}

// retire records a departing terminal's output so a later tool-call completion can
// still adopt it. Callers hold at.mu. Every path that removes a terminal goes
// through here, because forgetting is silent: adoption simply finds nothing.
func (at *agentTerminals) retire(id string, term *agentTerminal) {
	if term == nil || term.output == nil {
		return
	}
	at.retired[id] = retiredOutput{raw: term.rawOutput(), chatID: term.chatID, epoch: term.epoch}
}

// peekRetired returns a retired terminal's raw output WITHOUT consuming the record.
//
// Non-destructive on purpose: KAS can send more than one terminal status frame for
// the same tool call and adoption runs on each, so a consuming read would make the
// second log "terminal output missing" about output adopted a moment earlier. The
// turn boundary evicts it either way.
func (at *agentTerminals) peekRetired(id string) (string, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	rec, ok := at.retired[id]
	if !ok {
		return "", false
	}
	return rec.raw, true
}

// CloseTurn evicts the output records of the turn that just closed.
//
// Called by the winning closer, so a turn the wire started is attributed too —
// otherwise its terminals stay attached to a turn that already ended, letting a
// later cancel kill them. A turn ends only after every tool call has settled, so a
// record still here has had its chance. Epochs are monotonic per chat, so `<=` also
// collects one left behind by an earlier turn that never closed.
func (at *agentTerminals) CloseTurn(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	at.mu.Lock()
	for id, rec := range at.retired {
		if rec.chatID == chatID && rec.epoch <= epoch {
			delete(at.retired, id)
		}
	}
	at.mu.Unlock()
}

// turnEpochOf reads which turn a chat's activity belongs to right now, or zero
// when the chat is idle. Nil-safe: a registry built without the reader attributes
// nothing, the honest answer for a runtime with no turn lifecycle either.
func (at *agentTerminals) turnEpochOf(chatID vibekit.ChatID) vibekit.TurnEpoch {
	if at.currentEpoch == nil {
		return 0
	}
	epoch, _ := at.currentEpoch(chatID)
	return epoch
}

// KillForTurn kills the terminals the chat's CURRENT turn created and leaves every
// other chat process alone, so it does not also kill a background command an
// earlier turn started. An idle chat kills nothing, and epoch zero is never this
// turn's to kill.
func (at *agentTerminals) KillForTurn(chatID vibekit.ChatID) {
	cur := at.turnEpochOf(chatID)
	if cur == 0 {
		return
	}
	at.mu.Lock()
	ids := at.byChatID[chatID]
	var doomed []doomedTerminal
	kept := ids[:0]
	for _, id := range ids {
		term, ok := at.terms[id]
		if !ok {
			continue
		}
		if term.epoch != cur {
			kept = append(kept, id)
			continue
		}
		at.retire(id, term)
		delete(at.terms, id)
		doomed = append(doomed, doomedTerminal{id: id, term: term})
	}
	at.byChatID[chatID] = kept
	at.mu.Unlock()
	for _, d := range doomed {
		if d.term.cmd.Process != nil {
			if err := killTerminalGroup(d.term, d.id); err != nil {
				slog.Debug("agent: turn terminal kill failed", "chat_id", chatID, "error", err)
			}
		}
	}
	if len(doomed) > 0 {
		slog.Info("interrupt: killed the turn's terminals", "chat_id", chatID, "count", len(doomed))
	}
}

// KillForChat kills all terminals belonging to chatID and removes them from both
// maps. Called from cleanupChatState to prevent orphaned subprocesses.
//
// The one removal path that does NOT retire the output: the chat is being deleted,
// so there is no transcript left to adopt into. It drops any record it already had.
func (at *agentTerminals) KillForChat(chatID vibekit.ChatID) {
	at.mu.Lock()
	ids := at.byChatID[chatID]
	delete(at.byChatID, chatID)
	var doomed []doomedTerminal
	for _, id := range ids {
		term, ok := at.terms[id]
		if ok {
			delete(at.terms, id)
			doomed = append(doomed, doomedTerminal{id: id, term: term})
		}
	}
	for id, rec := range at.retired {
		if rec.chatID == chatID {
			delete(at.retired, id)
		}
	}
	at.mu.Unlock()
	// OUTSIDE the lock: the kill waits for the group to empty, and holding the
	// registry mutex across N bounded waits would shut every other operation.
	for _, d := range doomed {
		if d.term.cmd.Process != nil {
			if err := killTerminalGroup(d.term, d.id); err != nil {
				slog.Debug("agent: agent terminal kill failed", "id", d.id, "error", err)
			}
		}
	}
}

// doomedTerminal is one terminal a teardown removed from the registry, carried out
// of the locked section so the kill and its group wait run unlocked.
type doomedTerminal struct {
	term *agentTerminal
	id   string
}

// drainAll waits for all terminals to exit, then clears the maps. Each terminal's
// context is derived from shutdownCtx; when that cancels, cmd.Cancel sends SIGTERM
// and cmd.WaitDelay escalates to SIGKILL after 2s.
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

	at.mu.Lock()
	for id, term := range terms {
		at.retire(id, term)
		delete(at.terms, id)
	}
	at.byChatID = make(map[vibekit.ChatID][]string)
	at.mu.Unlock()
}

// release removes terminalID from both maps and returns the removed terminal. It
// does not kill the process — callers do that outside the lock.
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
func (rt *Runtime) handleTerminalRequest(ctx context.Context, chatID vibekit.ChatID, method string, msg *vibekit.RPCResponse) {
	switch method {
	case methodTermCreate:
		rt.agentTerms.respondCreate(ctx, chatID, msg)
	case methodTermOutput:
		rt.agentTerms.respondOutput(ctx, chatID, msg)
	case methodTermRelease:
		rt.agentTerms.respondRelease(ctx, chatID, msg)
	case methodTermWaitForExit:
		rt.agentTerms.respondWaitForExit(ctx, chatID, msg)
	case methodTermKill:
		rt.agentTerms.respondKill(ctx, chatID, msg)
	default:
		// The caller routes here on a `terminal/` PREFIX match, so a verb KAS adds
		// later reaches this switch and must still be answered: Bridge.Call has no
		// client-side deadline, so falling off the end wedges the turn rather than
		// failing it.
		if msg.ID == nil {
			return
		}
		slog.Warn("chat bridge: refusing an unimplemented terminal verb",
			"method", method, "chat_id", chatID, "id", *msg.ID)
		if err := rt.BridgeRespond(ctx, chatID, *msg.ID, nil,
			&vibekit.RPCError{
				Code:    vibekit.RPCCodeMethodNotFound,
				Message: "unimplemented terminal method: " + method,
			}); err != nil {
			slog.Error("chat bridge: terminal refusal could not be delivered; the turn may be wedged",
				"method", method, "chat_id", chatID, "error", err)
		}
	}
}

// agentShell resolves the shell that runs an agent command LINE, once per process.
// bash first (KAS names the tool `execute_bash`, and agents write bash-isms: `[[ ]]`,
// process substitution), POSIX sh as the fallback for an image without bash.
var agentShell = sync.OnceValue(func() string {
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/sh"
})

// agentCommand builds the process for one terminal/create.
//
// ACP's CreateTerminalRequest is `{command, args?}`, and KAS leaves `args` UNSET,
// putting the whole command line in `command` (measured against kiro-cli 2.18.0).
// So an ABSENT `args` means `command` is a command line and runs through a shell; a
// PRESENT `args`, empty included, means the sender already split the argv.
// Presence, not length, decides: a length test would shell a bare program name
// containing a space. The shell branch widens nothing — Cedar owns the LINE.
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

// failCreate answers a terminal/create that could not start, and logs it. Every
// failure path goes through this one door, because a command that failed to exec
// otherwise leaves NO server-side trace: only the agent's tool result learns.
func (at *agentTerminals) failCreate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse, command, reason string) {
	slog.Warn("agent terminal create failed", "chat_id", chatID, "cmd", command, "reason", reason)
	respondErr(ctx, at.bridges, chatID, msg, reason)
}

// failParse answers a terminal request whose params did not decode, and logs it.
//
// The id is already verified non-nil by the router and KAS awaits these with no
// timeout, so a bare return strands the promise. The request's OWN chatID is
// load-bearing: respondErr resolves the reply bridge by chat id.
func (at *agentTerminals) failParse(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse, method string, err error) {
	slog.Warn("agent terminal request had undecodable params",
		"method", method, "chat_id", chatID, "error", err)
	respondErr(ctx, at.bridges, chatID, msg, "invalid params")
}

func (at *agentTerminals) respondCreate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		// Args is a POINTER so an explicitly empty array stays distinguishable from an
		// omitted field: `{"command":"prog","args":[]}` asks to exec `prog` with no
		// arguments, which is not the same statement as omitting args.
		Args *[]string `json:"args"`
		// Env is the ACP terminal/create env array (KAS zEnvVariable {name,value}),
		// populated into cmd.Env.
		Env             []termEnvVar `json:"env"`
		OutputByteLimit int          `json:"outputByteLimit"`
	}
	if parseRequest(msg, &params) != nil {
		at.failCreate(ctx, chatID, msg, params.Command, "invalid params")
		return
	}
	if params.Command == "" {
		at.failCreate(ctx, chatID, msg, params.Command, "command is required")
		return
	}
	// Screen the requested environment BEFORE anything is created, so the refusal
	// needs no teardown. See agent_terminal_env.go: an agent-supplied variable wins
	// over the process environment, and a few names redirect execution.
	if blocked := screenAgentEnv(params.Env, operatorAllowedEnv()); len(blocked) > 0 {
		slog.Warn("refused an agent terminal that redirects execution through the environment",
			"chat_id", chatID, "command", params.Command, "variables", strings.Join(blocked, ","))
		respondErr(ctx, at.bridges, chatID, msg, "refusing to set "+strings.Join(blocked, ", ")+
			": these variables change what a program executes, so they are not accepted from the agent."+
			" Pass the setting on the command itself, or have the operator allow the name via "+envAllowVar)
		return
	}

	limit := outputBufferLimit
	if params.OutputByteLimit > 0 && params.OutputByteLimit < limit {
		limit = params.OutputByteLimit
	}

	// The command must outlive the per-event ctx: translateACPEvent cancels it the
	// moment it returns, and a child of it would SIGTERM the just-spawned process.
	// The AfterFunc re-attaches shutdown-scoped teardown.
	cmdCtx, cmdCancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(at.lifecycle.shutdownCtx, cmdCancel)
	cmd := agentCommand(cmdCtx, params.Command, params.Args)
	// Own process group, so every teardown path can signal the whole tree rather
	// than just the head. See procgroup.Kill for why the head alone is not enough.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Graceful shutdown: SIGTERM on context cancel, SIGKILL after 2s.
	cmd.Cancel = func() error { return procgroup.Kill(cmd.Process, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if env := termEnv(params.Env); env != nil {
		cmd.Env = env
	}
	if params.Cwd != "" {
		abs, err := at.lifecycle.resolveInsideWorkDir(params.Cwd)
		if err != nil {
			stop()
			cmdCancel()
			at.failCreate(ctx, chatID, msg, params.Command, "cwd escapes workspace: "+err.Error())
			return
		}
		cmd.Dir = abs
	} else {
		cmd.Dir = at.lifecycle.workDir
	}

	term := newAgentTerminal(cmd, chatID, limit)

	// One CALLER-OWNED pipe for both streams, not Cmd.StdoutPipe/StderrPipe: os/exec
	// closes the pipes it hands out inside Wait, so owning the read end lets Wait
	// observe exit while the drain is bounded from that moment. Merging both streams
	// also preserves write order between them, which io.MultiReader does not.
	pr, pw, err := os.Pipe()
	if err != nil {
		stop()
		cmdCancel()
		at.failCreate(ctx, chatID, msg, params.Command, "pipe: "+err.Error())
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		stop()
		cmdCancel()
		at.failCreate(ctx, chatID, msg, params.Command, err.Error())
		return
	}
	// The child holds its own duplicate of the write end, so closing ours is what
	// makes the reader see EOF when the last writer exits.
	_ = pw.Close()

	termID := newMessageID() // reuse the ID generator for unique terminal IDs

	// Which turn owns this terminal, read BEFORE at.mu is taken: the reader reaches
	// the turn lifecycle's own mutex, and taking that under at.mu would give this
	// type two lock orders.
	epoch := at.turnEpochOf(chatID)

	// Register the terminal in the maps and broadcast terminal_created
	// BEFORE starting the pump/exit goroutines. emit() assigns monotonic
	// event ids in call order, and those goroutines emit terminal_output /
	// terminal_exited; if they ran first, a fast write-and-exit command
	// could emit output/exited with a lower event id than terminal_created.
	// The client would then drop those events (unknown terminal id) and
	// leave the tab stuck "running". Registering + broadcasting first
	// guarantees terminal_created is ordered ahead of any output/exit event.
	at.mu.Lock()
	term.epoch = epoch
	at.terms[termID] = term
	at.byChatID[chatID] = append(at.byChatID[chatID], termID)
	at.mu.Unlock()

	slog.Info("agent terminal created", "chat_id", chatID, "term_id", termID, "cmd", params.Command)
	at.broadcast(ctx, vibekit.NewEvent(vibekit.EventTerminalCreated, chatID, vibekit.TerminalCreatedPayload{
		TerminalID: termID,
		Command:    params.Command,
		// A plain slice on the wire: the client only labels the tab with it.
		Args: derefArgs(params.Args),
	}))
	respondOK(ctx, at.bridges, chatID, msg, map[string]string{"terminalId": termID})

	// Stream the merged output into the ring. Started only after the terminal is
	// registered and terminal_created is broadcast. drained closes at EOF.
	drained := make(chan struct{})
	at.lifecycle.inflight.Go(func() {
		defer close(drained)
		defer func() { _ = pr.Close() }()
		at.pumpOutput(term, termID, chatID, pr)
	})

	at.lifecycle.inflight.Go(func() {
		at.awaitExit(term, termID, chatID, cmd, stop, cmdCancel, drained, pr)
	})
}

// pumpOutput streams a terminal's combined stdout/stderr into its ring buffer and
// broadcasts each chunk to SSE clients until the reader hits EOF or an error.
func (at *agentTerminals) pumpOutput(term *agentTerminal, termID string, chatID vibekit.ChatID, r io.Reader) {
	// Runtime-scoped context: this goroutine outlives the per-event ctx that spawned
	// it, so derive one that lives until EOF or shutdown.
	ctx, cancel := at.lifecycle.derivedContext()
	defer cancel()
	buf := getPumpBuf()
	defer pumpBufPool.Put(buf) //nolint:staticcheck // returned after loop exits

	// pending carries a short (≤3-byte) trailing remainder from the previous read
	// that formed a multi-byte rune split across the read boundary, prepended to the
	// next chunk so the live SSE stream is always valid UTF-8. The ring still gets
	// every raw byte exactly once, so only the broadcast needs the carry.
	var pending []byte
	emit := at.emitter(ctx, term, termID, chatID)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			term.mu.Lock()
			term.output.Write(buf[:n]) // ring gets every raw byte, unchanged
			term.mu.Unlock()

			// Assemble the chunk from any carried remainder, then split off a fresh tail.
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
	// Flush leftover incomplete bytes at EOF; they render as U+FFFD, like the ring.
	if len(pending) > 0 {
		emit(string(pending))
	}
	// Release an escape sequence the stream ended mid-way through, rather than lose it.
	base := term.ansi.Offset()
	if text, parsed := term.ansi.Flush(); text != "" {
		at.publishText(ctx, termID, chatID, text, wireSpans(parsed), base)
	}
}

// emitter returns the function that turns one raw chunk into what the TRANSCRIPT
// renders: hidden Unicode stripped, escape sequences parsed off into style spans,
// plain text out. Parsing server-side is what lets the client paint spans with
// textContent instead of building HTML from bytes.
//
// SanitizeUnicode, NOT SanitizeOutput: SanitizeOutput strips ANSI BEFORE the parser
// sees it, so spans come out empty and output renders unstyled (TestTerminalEmitter_*
// pins it). Hidden Unicode goes first, so nothing can hide a sequence behind it.
func (at *agentTerminals) emitter(
	ctx context.Context, term *agentTerminal, termID string, chatID vibekit.ChatID,
) func(string) {
	return func(raw string) {
		base := term.ansi.Offset()
		text, parsed := term.ansi.Write(sanitize.Unicode(raw))
		if text == "" && len(parsed) == 0 {
			return
		}
		at.publishText(ctx, termID, chatID, text, wireSpans(parsed), base)
	}
}

// publishText broadcasts one rendered chunk.
//
// base is where this chunk starts in the terminal's accumulated plain text, counted
// in UTF-16 code units, read off the parser's own counter before the write that
// produced the text. NOT a byte length and NOT a second tally: span offsets are
// absolute in UTF-16 units, so the base must be the same quantity from the same
// source, or every live span rebases onto the wrong character.
func (at *agentTerminals) publishText(
	ctx context.Context, termID string, chatID vibekit.ChatID,
	text string, spans []vibekit.TextSpan, base int,
) {
	at.broadcast(ctx, vibekit.NewEvent(vibekit.EventTerminalOutput, chatID, vibekit.TerminalOutputPayload{
		TerminalID: termID,
		Data:       text,
		Spans:      spans,
		Offset:     base,
	}))
}

// incompleteTailLen returns the number of trailing bytes of b that form an
// incomplete leading UTF-8 sequence. Zero when b ends on a complete rune, is empty,
// or ends in a standalone invalid byte that will never complete. At most
// utf8.UTFMax-1 (3) bytes are ever held.
func incompleteTailLen(b []byte) int {
	if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
		// A real rune, or a genuine U+FFFD (3 bytes → size > 1). Nothing to hold.
		return 0
	}
	// Walk back to the lead byte of the trailing sequence (bounded to UTFMax) and
	// hold it only if it is a valid-but-not-yet-complete rune prefix.
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

// awaitExit blocks on cmd.Wait, records the exit status, releases the per-terminal
// context resources, closes term.done, and broadcasts terminal_exited. A
// signal-killed process records a signal string rather than exitCode -1, because
// KAS's zTerminalExitStatus requires exitCode>=0.
func (at *agentTerminals) awaitExit(
	term *agentTerminal, termID string, chatID vibekit.ChatID, cmd *exec.Cmd,
	stop func() bool, cmdCancel context.CancelFunc,
	drained <-chan struct{}, pr *os.File,
) {
	// Runtime-scoped context for the broadcast (outlives the per-event ctx).
	ctx, cancel := at.lifecycle.derivedContext()
	defer cancel()

	// Read the process group BEFORE Wait reaps the head, which is the only moment
	// getpgid can answer (procgroup.GroupOf says why).
	pgid, owns := procgroup.GroupOf(cmd.Process)

	// Wait FIRST, then drain. Safe in that order only because the pipe is
	// caller-owned: os/exec closes the pipes IT hands out inside Wait.
	err := cmd.Wait()

	// The head is reaped; the GROUP may not be empty. The load-bearing wait of the
	// four: everything below tells the agent the command finished, and the agent then
	// reads a file the command's backgrounded grandchild is still writing. Not a kill
	// — the point is to observe when the tree stopped, and to SAY SO when it has not.
	// Started here and JOINED after the drain so the two overlap: sequenced, they
	// would charge `some-daemon &` both graces before the agent hears anything.
	groupEmptied := make(chan bool, 1)
	go func() { groupEmptied <- !owns || procgroup.WaitGone(pgid, terminalGroupGrace) }()

	// Bound the drain from the moment the process exited: EOF is not guaranteed,
	// since a command that leaves a grandchild holding the write end keeps the pipe
	// open, and closing our read end is what releases the pump then. terminal_exited
	// must not be observable before the output it describes — measured, a `whoami`
	// sent its exit as event 365 and its own "root\n" as 368.
	select {
	case <-drained:
	case <-time.After(terminalDrainGrace):
		slog.Warn("agent terminal: output still open after exit; releasing the reader",
			"chat_id", chatID, "term_id", termID, "grace", terminalDrainGrace)
		_ = pr.Close()
		<-drained
	}

	// Debug, not Warn: a command that backgrounds a long-lived process ON PURPOSE
	// reaches this on every exit and nobody can act on it. killTerminalGroup's line
	// stays a Warn, because there a SIGKILL was sent and the group outlived it.
	if !<-groupEmptied {
		slog.Debug("agent terminal: the command exited but its process group did not empty",
			"chat_id", chatID, "term_id", termID, "pgid", pgid, "grace", terminalGroupGrace)
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
	payload := vibekit.TerminalExitedPayload{TerminalID: termID}
	if sig != "" {
		payload.Signal = sig
	} else {
		payload.ExitCode = &code
	}
	at.broadcast(ctx, vibekit.NewEvent(vibekit.EventTerminalExited, chatID, payload))
}

func (at *agentTerminals) respondOutput(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermOutput, err)
		return
	}
	at.mu.Lock()
	term, ok := at.terms[params.TerminalID]
	at.mu.Unlock()
	if !ok {
		// Thread the real chatID so the error resolves a bridge; an empty one makes
		// respondErr's lookup miss and the error is dropped, hanging the agent.
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
		return
	}
	term.mu.Lock()
	output := term.output.String()
	truncated := term.output.Truncated()
	term.mu.Unlock()

	result := map[string]any{"output": output, "truncated": truncated}
	// v3/KAS zTerminalOutputResponse requires exitStatus to be an object or null.
	select {
	case <-term.done:
		result["exitStatus"] = term.exitStatusObject()
	default:
	}
	respondOK(ctx, at.bridges, chatID, msg, result)
}

func (at *agentTerminals) respondRelease(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermRelease, err)
		return
	}
	term, ok := at.release(params.TerminalID)
	if ok && term.cmd.Process != nil {
		// An already-reaped process is the EXPECTED answer here: KAS drives
		// terminal/release after wait_for_exit returned, and that responder unblocks on
		// term.done, which awaitExit closes only after cmd.Wait reaped the process.
		// Debug rather than silence still says whether the kill was a no-op.
		if err := killTerminalGroup(term, params.TerminalID); err != nil {
			if procgroup.AlreadyGone(err) {
				slog.Debug("terminal release: kill was a no-op, the process was already reaped",
					"term_id", params.TerminalID, "error", err)
			} else {
				slog.Warn("terminal release: kill failed", "term_id", params.TerminalID, "error", err)
			}
		}
	}
	slog.Info("agent terminal released", "term_id", params.TerminalID)
	// The request's chatID, not the possibly-nil term.chatID, so the ack resolves a
	// bridge even for an unknown terminal id. KAS's response is an empty object.
	respondOK(ctx, at.bridges, chatID, msg, map[string]any{})
}

func (at *agentTerminals) respondWaitForExit(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermWaitForExit, err)
		return
	}
	at.mu.Lock()
	term, ok := at.terms[params.TerminalID]
	at.mu.Unlock()
	if !ok {
		// Real chatID so the not-found error resolves a bridge (see output).
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
		return
	}
	// Block until the process exits or the runtime shuts down. A fresh runtime-scoped
	// context inside the goroutine: the per-event ctx is cancelled before this async
	// responder runs, and Bridge.Respond drops a write on a cancelled ctx.
	at.lifecycle.inflight.Go(func() {
		fctx, cancel := at.lifecycle.derivedContext()
		defer cancel()
		select {
		case <-term.done:
			respondOK(fctx, at.bridges, chatID, msg, term.exitStatusObject())
		case <-at.lifecycle.done:
			// Shutdown in progress; bridge is dead, response is moot.
			return
		}
	})
}

func (at *agentTerminals) respondKill(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermKill, err)
		return
	}
	at.mu.Lock()
	term, ok := at.terms[params.TerminalID]
	at.mu.Unlock()
	if !ok {
		// Real chatID so the not-found error resolves a bridge (see output).
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
		return
	}
	if term.cmd.Process != nil {
		// Same split as release, for a rarer reason: KAS sends terminal/kill on the
		// timeout and cancel paths, where the command is normally still running, but it
		// races the command's own exit and losing that race is not a failure.
		if err := killTerminalGroup(term, params.TerminalID); err != nil {
			if procgroup.AlreadyGone(err) {
				slog.Debug("terminal kill was a no-op, the process was already reaped",
					"term_id", params.TerminalID, "error", err)
			} else {
				slog.Warn("terminal kill failed", "term_id", params.TerminalID, "error", err)
			}
		}
	}
	// KAS zKillTerminalResponse is an empty object.
	respondOK(ctx, at.bridges, chatID, msg, map[string]any{})
}

// Output returns an agent terminal's output for the translate layer to persist onto
// the owning tool call. See translate.TerminalReader.
//
// It reads the RAW ring and renders on demand: the rendering is derivable, so there
// is no second buffer to keep in step; it works for an already-released terminal,
// because `retire` kept those bytes under the same id; and the sanitize-then-parse
// order matches the live pump, so persisted and streamed text cannot disagree. ok
// reports whether the terminal is KNOWN, so a silent command answers ("", nil, true).
func (at *agentTerminals) Output(terminalID string) (string, []vibekit.TextSpan, bool) {
	at.mu.Lock()
	term, live := at.terms[terminalID]
	at.mu.Unlock()

	var raw string
	if live {
		raw = term.rawOutput()
	} else {
		var known bool
		raw, known = at.peekRetired(terminalID)
		if !known {
			return "", nil, false
		}
	}
	if raw == "" {
		return "", nil, true
	}
	text, spans := ansitext.Parse(sanitize.Unicode(raw))
	return text, wireSpans(spans), true
}
