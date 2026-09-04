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

// terminalDrainGrace bounds how long awaitExit waits for the output
// pipe to reach EOF AFTER the process has exited, before it force-closes the
// read end. Only a grandchild still holding the write end can reach it. Matches
// cmd.WaitDelay, which bounds the mirror case inside Wait.
const terminalDrainGrace = 2 * time.Second

// terminalGroupGrace bounds how long a teardown waits for the command's process
// GROUP to empty after the kill. Same 2 seconds as the drain and as
// cmd.WaitDelay, because it bounds the same population: a grandchild the head left
// behind.
const terminalGroupGrace = 2 * time.Second

// killTerminalGroup signals term's whole process group and then waits, bounded, for
// it to empty — so a caller that returns holds the FACT that the command is gone
// rather than the knowledge that a signal was sent. Without the wait a grandchild
// outlives the reaped head, reparents to PID 1, and becomes exactly the orphan the
// group kill exists to prevent.
//
// Returns Kill's error for the caller's own already-gone/failed split; the group not
// emptying is logged here, because that line is the same at every site.
func killTerminalGroup(term *agentTerminal, termID string) error {
	pgid, owns := procgroup.GroupOf(term.cmd.Process)
	err := procgroup.Kill(term.cmd.Process, syscall.SIGKILL)
	// Nothing to wait on when the command is not its own group leader: the group
	// is vibekit's, and Kill fell back to the head alone.
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
	// ansi carries SGR state and any incomplete escape across read boundaries,
	// so a colour opened in one chunk still applies in the next. It also owns
	// the running UTF-16 offset the wire reports as a chunk's base. Used only
	// for the LIVE stream; the durable copy is re-parsed from the ring.
	//
	// Owned by the pump goroutine alone; touching it elsewhere lets two
	// streams' styles bleed together.
	ansi   *ansitext.Parser
	chatID vibekit.ChatID
	signal string
	// epoch is the turn that spawned this terminal (the chat's open turn at
	// create time), so an interrupt can kill the turn's own processes without
	// touching a background command an earlier turn left running.
	epoch    vibekit.TurnEpoch
	exitCode int
	mu       sync.Mutex
}

// newAgentTerminal builds one terminal with every field a running terminal
// needs. A constructor rather than a struct literal because two of those fields
// are easy to forget and the consequence is not a compile error: without `ansi`
// the pump nil-panics on the first byte, and without `output` the agent's own
// terminal/output pull returns nothing.
func newAgentTerminal(cmd *exec.Cmd, chatID vibekit.ChatID, limit int) *agentTerminal {
	return &agentTerminal{
		cmd:    cmd,
		done:   make(chan struct{}),
		output: newByteRing(limit),
		ansi:   ansitext.NewParser(),
		chatID: chatID,
	}
}

// rawOutput returns a snapshot of the terminal's raw ring under the same lock
// the pump writes it with.
//
// The lock is the point. The ring is written by the pump goroutine and read by
// three callers on other goroutines (the agent's terminal/output pull, the
// retire path, and the translate layer's adoption), and two of those run while
// the process is still producing bytes — KAS releases a terminal a few
// milliseconds after creating it, which is well inside a command's lifetime.
func (t *agentTerminal) rawOutput() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output.String()
}

// wireSpans converts the parser's spans to the wire shape. The two structs are
// deliberately separate: internal/ansitext stays a stdlib-only leaf that owns
// the parse, and internal/vibekit owns every shape codegen projects into
// TypeScript. Same boundary as the ACP wire types in internal/translate, which
// convert into api types rather than being them.
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
// contract at its consumer and never imports internal/bridge, so the value is
// stated twice on purpose. Change one, change the other.
const termLocaleEnvVar = "LANG"

// termLocaleEnv pins the text encoding of an agent terminal's command. The
// runtime image ships no `locales` package, so an unset LANG leaves glibc's C
// locale and the command octal-escapes every non-ASCII path the model reads
// back. C.UTF-8 is a glibc built-in needing no generated locale files.
func termLocaleEnv() []string {
	return []string{termLocaleEnvVar + "=C.UTF-8"}
}

// termEnv layers the requested env vars on top of the current process
// environment, then the locale pin. Returns nil when none are requested so
// cmd.Env stays nil and the child inherits os.Environ() unchanged — which
// already carries the image's own LANG, so there is nothing to pin there.
//
// The pin lands LAST because os/exec keeps the last value for a repeated key, so
// appending it first would be a silent no-op against an agent-supplied one. It
// covers LANG only: glibc gives LC_ALL and LC_CTYPE precedence over LANG whatever
// the env ordering, so an agent setting either still picks its own encoding.
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
	byChatID map[vibekit.ChatID][]string // chatID → []terminalID
	// retired holds the RAW output of terminals no longer in terms, keyed by
	// the same id: a terminal's output outlives the terminal, since KAS
	// releases it (and reports the tool call's result) within milliseconds,
	// well before the command finishes. Keyed by terminal id rather than
	// tool-call id because the tool call learns its terminal id LATER still.
	// Bounded by the turn: evicted at the turn boundary, each holding at
	// most one 64 KiB ring.
	retired map[string]retiredOutput
	// currentEpoch reads which turn a chat's activity belongs to right now,
	// so a terminal is stamped with the lifecycle's identity rather than a
	// count kept in parallel.
	currentEpoch func(vibekit.ChatID) (vibekit.TurnEpoch, bool)
	// broadcast publishes a terminal's lifecycle and output frames.
	broadcast func(context.Context, vibekit.ServerEvent)
	// bridges is the per-chat bridge registry, for answering the ACP request a
	// terminal operation arrived on.
	bridges *bridgeManager
	// lifecycle supplies the process lifetime, the in-flight counter a pump must
	// register on, and the workspace dir a terminal's cwd is resolved inside.
	lifecycle *lifetime
	mu        sync.Mutex
}

// retiredOutput is what survives a terminal: its raw bytes and enough
// identity to evict the record at the right moment.
//
// Raw rather than rendered: the ring already bounds it, so a second
// accumulator would mean a second buffer and cap to keep in step.
//
// Kept even when raw is EMPTY: existence answers "did this terminal ever
// run", a different question from "what did it print" — dropping empty
// records would make a silent command indistinguishable from a lost one.
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
	at.retired[id] = retiredOutput{raw: term.rawOutput(), chatID: term.chatID, epoch: term.epoch}
}

// peekRetired returns a retired terminal's raw output WITHOUT consuming the
// record.
//
// Non-destructive on purpose. KAS can send more than one terminal status frame
// for the same tool call, and adoption runs on each: a consuming read would make
// the second one find nothing and log "terminal output missing at completion"
// about output that was adopted successfully a moment earlier. Leaving the record
// in place makes adoption idempotent instead, and costs nothing — the turn
// boundary evicts it either way.
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
// Called by the winning closer, so a turn the wire started (an auto-wake, an
// agent-initiated turn, a bridge death, a model switch) is attributed too —
// otherwise their terminals would stay attached to a turn that had already
// ended, letting a later cancel kill them.
//
// A turn ends only after every tool call in it has settled, so any record
// still here has had its chance to be adopted. Epochs are monotonic per
// chat, so `<=` also collects a record left behind by an earlier turn that
// never closed, and a ZERO epoch (created while idle) is collected by
// whichever turn closes next.
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
// when the chat is idle. Nil-safe: a terminals registry built without the reader
// attributes nothing, which is the honest answer for a runtime that has no turn
// lifecycle either.
func (at *agentTerminals) turnEpochOf(chatID vibekit.ChatID) vibekit.TurnEpoch {
	if at.currentEpoch == nil {
		return 0
	}
	epoch, _ := at.currentEpoch(chatID)
	return epoch
}

// KillForTurn kills the terminals the chat's CURRENT turn created and leaves
// every other chat process alone. Scoped to the turn rather than the whole
// chat, so it does not also kill a background command an earlier turn
// started on purpose. An idle chat kills nothing, and a terminal created
// while the chat was idle (epoch zero) is never this turn's to kill.
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

// KillForChat kills all terminals belonging to chatID and removes them
// from both maps. Called from cleanupChatState to prevent orphaned
// subprocesses when a chat is deleted.
//
// This is the one removal path that does NOT retire the output: the chat is
// being deleted, so there is no transcript left to adopt into. It drops any
// record the chat already had for the same reason.
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
	// OUTSIDE the lock, which KillForTurn already did and this did not: the kill
	// now waits for the group to empty, and holding the registry mutex across N
	// bounded waits would shut every other terminal operation for that long.
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
	at.byChatID = make(map[vibekit.ChatID][]string)
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
// ACP's CreateTerminalRequest is `{command, args?}`, and KAS leaves `args`
// UNSET, putting the whole command line in `command` (measured against
// kiro-cli 2.18.0). So an ABSENT `args` means `command` is a command line
// and runs through a shell; a PRESENT `args` — including an empty one —
// means the sender already split the argv and is exec'd directly. Presence,
// not length, decides: a length test would shell a bare program name whose
// name happens to contain a space.
//
// The shell branch does not widen what the agent may run: authorization is
// kiro-cli's Cedar policy over the command LINE, and the environment is
// screened separately in create.
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

// failCreate answers a terminal/create that could not start, and logs it.
// Every failure path goes through this one door, because before it six
// respondErr returns logged nothing at all: a command that failed to exec left
// NO server-side trace, broadcast no event and produced no tab, so the only
// party that ever learned was the agent, in its own tool result. That is how
// the exec-without-a-shell bug above stayed invisible for as long as it did.
func (at *agentTerminals) failCreate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse, command, reason string) {
	slog.Warn("agent terminal create failed", "chat_id", chatID, "cmd", command, "reason", reason)
	respondErr(ctx, at.bridges, chatID, msg, reason)
}

// failParse answers a terminal request whose params did not decode, and logs it.
//
// The id is already verified non-nil by the router and KAS awaits these with no
// timeout, so a bare return strands the promise rather than failing the call. The
// request's OWN chatID is load-bearing: respondErr resolves the reply bridge by
// chat id, so an empty one misses the lookup and the answer is dropped.
func (at *agentTerminals) failParse(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse, method string, err error) {
	slog.Warn("agent terminal request had undecodable params",
		"method", method, "chat_id", chatID, "error", err)
	respondErr(ctx, at.bridges, chatID, msg, "invalid params")
}

func (at *agentTerminals) respondCreate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
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
		at.failCreate(ctx, chatID, msg, params.Command, "invalid params")
		return
	}
	if params.Command == "" {
		at.failCreate(ctx, chatID, msg, params.Command, "command is required")
		return
	}
	// Screen the requested environment BEFORE anything is created, so the refusal
	// needs no teardown (unlike the cwd check below, which already holds a ctx).
	// See agent_terminal_env.go: an agent-supplied variable wins over the process
	// environment, and a few names redirect execution rather than carry data, so
	// this is what stops an approved command running something else.
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

	// The command must outlive the per-event ctx: translateACPEvent cancels
	// that ctx the moment it returns, and a child of it would SIGTERM the
	// just-spawned process before it does any work (C2). WithoutCancel keeps
	// the ctx values but strips its cancellation; the AfterFunc re-attaches
	// shutdown-scoped teardown so the command still dies on runtime shutdown
	// (and on terminal_kill / terminal_release / normal exit via cmdCancel).
	cmdCtx, cmdCancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(at.lifecycle.shutdownCtx, cmdCancel)
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

	// One CALLER-OWNED pipe for both streams, not Cmd.StdoutPipe/StderrPipe.
	// os/exec closes the pipes it hands out inside Wait ("it is incorrect to
	// call Wait before all reads from the pipe have completed"), so owning the
	// read end lets Wait observe exit while the drain is bounded from that
	// moment rather than racing it. Merging both streams into one pipe also
	// preserves write order between stdout and stderr — io.MultiReader does
	// not, since it drains stdout to EOF first.
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
	// The child holds its own duplicate of the write end, so closing ours is
	// what makes the reader see EOF when the last writer exits. Without it the
	// pump would block forever on a process that has already gone.
	_ = pw.Close()

	termID := newMessageID() // reuse the ID generator for unique terminal IDs

	// Which turn owns this terminal, read BEFORE at.mu is taken: the reader
	// reaches the turn lifecycle's own mutex, and taking that under at.mu would
	// give this type two lock orders (the finalizer calls CloseTurn with the
	// lifecycle mutex already released).
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
		// The wire field stays a plain slice: the client only labels the tab
		// with it, so presence-versus-empty carries no meaning there.
		Args: derefArgs(params.Args),
	}))
	respondOK(ctx, at.bridges, chatID, msg, map[string]string{"terminalId": termID})

	// Stream the merged output into the ring buffer. Started only after the
	// terminal is registered and terminal_created is broadcast (see above).
	// drained closes at EOF, which awaitExit waits for after Wait.
	drained := make(chan struct{})
	at.lifecycle.inflight.Go(func() {
		defer close(drained)
		defer func() { _ = pr.Close() }()
		at.pumpOutput(term, termID, chatID, pr)
	})

	// Wait for exit in background.
	at.lifecycle.inflight.Go(func() {
		at.awaitExit(term, termID, chatID, cmd, stop, cmdCancel, drained, pr)
	})
}

// pumpOutput streams a terminal's combined stdout/stderr into
// its ring buffer and broadcasts each chunk to SSE clients until the
// reader hits EOF or an error.
func (at *agentTerminals) pumpOutput(term *agentTerminal, termID string, chatID vibekit.ChatID, r io.Reader) {
	// Runtime-scoped context: this goroutine outlives the per-event ctx that
	// spawned it (translateACPEvent cancels that on return), so derive a
	// fresh one that lives until the reader hits EOF or the runtime shuts down.
	ctx, cancel := at.lifecycle.derivedContext()
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
	emit := at.emitter(ctx, term, termID, chatID)
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
	base := term.ansi.Offset()
	if text, parsed := term.ansi.Flush(); text != "" {
		at.publishText(ctx, termID, chatID, text, wireSpans(parsed), base)
	}
}

// emitter returns the function that turns one raw chunk into what the
// TRANSCRIPT renders: hidden Unicode stripped, escape sequences parsed off
// into style spans, plain text out. Parsing server-side is what lets the
// client paint spans with textContent instead of building HTML from bytes.
//
// SanitizeUnicode, NOT SanitizeOutput. SanitizeOutput strips ANSI BEFORE the
// parser can see it, so spans come out empty and output renders unstyled
// (measured: `ESC[90m1:47AM…` produces spans=0 with SanitizeOutput, spans=2
// without; TestTerminalEmitter_* pins it). The escape-stripping property is
// preserved by ORDER instead: hidden Unicode goes first so nothing can hide
// a sequence behind a zero-width character, the parser consumes SGR, and
// ansitext guarantees escape-free output text on its own.
//
// No secret-masking step here: this app deleted its redaction layer on
// purpose (logs are served as kiro-cli produced them), and adding one back
// on this one surface would make the transcript disagree with the tool
// card's own content about what the command printed.
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
// base is where this chunk starts in the terminal's accumulated plain text,
// counted in UTF-16 code units, read off the parser's own counter before the
// write that produced the text. It is NOT a byte length, and it is NOT a second
// tally: span offsets are absolute across the stream in UTF-16 units, so the
// base has to be the same quantity from the same source, or every live span
// rebases onto the wrong character the moment output contains anything
// non-ASCII.
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
// incomplete (truncated) leading UTF-8 sequence — the start of a multi-byte
// rune whose continuation bytes have not arrived yet. It returns 0 when b
// ends on a complete rune, is empty, or ends in a standalone invalid byte
// that will never complete. At most utf8.UTFMax-1 (3) bytes are ever held.
// Used by pumpOutput to avoid splitting a rune across the read
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

// awaitExit blocks on cmd.Wait, records the exit status on the
// terminal, releases the per-terminal context resources (stop
// unregisters the shutdown AfterFunc, cmdCancel releases the command
// context), closes term.done, and broadcasts terminal_exited. A
// signal-killed process records a signal string (ProcessState.ExitCode()
// is -1) rather than exitCode -1, so the exit reports signal:"..." and
// omits exit_code (KAS's zTerminalExitStatus requires exitCode>=0).
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
	// caller-owned (see create): os/exec closes the pipes IT hands out
	// inside Wait, so with Cmd.StdoutPipe this order would truncate the pump.
	err := cmd.Wait()

	// The head is reaped; the GROUP may not be empty. The load-bearing wait of the
	// four: everything below tells the agent the command finished (terminal_exited,
	// term.done, the pending wait_for_exit), and the agent then reads a file the
	// command's backgrounded grandchild is still writing. Not a kill — the point is
	// to observe when the tree has stopped, and to SAY SO when it has not.
	//
	// Started here and JOINED after the drain, so the two overlap. They bound the
	// same population — a grandchild the head left behind — and sequenced they would
	// charge `some-daemon &` both graces before the agent hears the command
	// finished. Neither observation touches the other's subject.
	groupEmptied := make(chan bool, 1)
	go func() { groupEmptied <- !owns || procgroup.WaitGone(pgid, terminalGroupGrace) }()

	// Bound the drain from the moment the process actually exited: EOF is not
	// guaranteed, since a command that leaves a grandchild holding the write
	// end (`some-daemon &`) keeps the pipe open after the head exits, and
	// closing our read end is what releases the pump then.
	//
	// terminal_exited must not be observable before the output it describes:
	// measured, a `whoami` sent its exit as event 365 and its own "root\n" as
	// 368, and the client painted the exit footer above the line that
	// produced it.
	select {
	case <-drained:
	case <-time.After(terminalDrainGrace):
		slog.Warn("agent terminal: output still open after exit; releasing the reader",
			"chat_id", chatID, "term_id", termID, "grace", terminalDrainGrace)
		_ = pr.Close()
		<-drained
	}

	// Debug, not Warn: a command that backgrounds a long-lived process ON PURPOSE
	// (a dev server, a watcher) reaches this on every exit and there is nothing for
	// anyone to do about it. killTerminalGroup's line stays a Warn because there a
	// SIGKILL was sent and the group outlived it, which is not an ordinary outcome.
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
		// Thread the real chatID (from the request) so the error response
		// resolves a bridge; an empty chatID makes respondErr's bridge
		// lookup miss and the error is silently dropped, hanging the agent.
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
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
		// An already-reaped process is the EXPECTED answer here, not a failure:
		// KAS drives terminal/release after wait_for_exit has returned, and that
		// responder unblocks on term.done, which awaitExit closes only after
		// cmd.Wait has reaped the process. So every ordinary agent-terminal
		// release used to log a warning, which is how a warning stops meaning
		// anything. Debug rather than silence keeps the one thing worth knowing
		// when someone is debugging teardown: whether the kill was a no-op or
		// really signalled a live tree. Anything else is a genuine failure.
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
	// Respond with the request's chatID (not the possibly-nil term.chatID)
	// so the ack resolves a bridge even for an unknown terminal id. KAS
	// zReleaseTerminalResponse is an empty object.
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
	// Block until the process exits or the runtime shuts down. Derive a fresh
	// runtime-scoped context inside the goroutine: the per-event ctx is cancelled
	// the moment translateACPEvent returns (before this async responder runs),
	// and Bridge.Respond drops a write on a cancelled ctx — which would hang
	// the agent's wait_for_exit Call.
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
		// Same split as release, for a rarer reason. KAS sends terminal/kill on
		// the timeout and cancel paths, where the command is normally still
		// running and the kill succeeds — but it races the command's own exit,
		// and losing that race is not a failure to warn about. See respondRelease
		// for why an already-gone target lands at Debug.
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

// Output returns an agent terminal's output for the translate layer to
// persist onto the owning tool call. See translate.TerminalReader.
//
// It reads the RAW ring and renders on demand: the rendering is derivable so
// there is no second buffer to keep in step; it works for an already-released
// terminal because `retire` kept those bytes under the same id (KAS releases
// before it reports the result, so the live registry is empty by then); and
// the sanitize-then-parse order matches the live pump, so persisted and
// streamed text cannot disagree about what an escape meant.
//
// ok reports whether the terminal is KNOWN, not whether it printed anything:
// a silent command answers ("", nil, true), a different fact from a lost
// record.
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
