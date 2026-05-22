// Shell subsystem — single global PTY session.
//
// The shell runs in a real pseudo-terminal (creack/pty) so interactive
// programs (vim, htop, less, tab completion) work correctly. I/O flows
// over a WebSocket at /api/shell/ws; the PTY stays alive across WS
// reconnects (iOS sleep, network blips) and replays the last 64 KB of
// output on reconnect so xterm.js can reconstruct terminal state.
//
// Wire protocol (binary WebSocket frames):
//   client → server: raw terminal input bytes
//   server → client: raw PTY output bytes
//   client → server: JSON control messages prefixed with 0x00:
//     {"type":"resize","cols":N,"rows":N}
//     {"type":"signal","name":"SIGINT"}
//
// The 0x00 prefix byte distinguishes control messages from raw input;
// no valid terminal input starts with NUL.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"golang.org/x/sync/singleflight"
)

const (
	// wsReadLimit is the maximum size of a single WS message from
	// the client. Terminal input is tiny; 64 KB is generous.
	wsReadLimit = 64 * 1024
)

// pumpBufPool is a pool of 4 KB byte slices reused by PTY/terminal
// output pump goroutines to avoid per-goroutine allocations.
var pumpBufPool = sync.Pool{
	New: func() any { return make([]byte, 4096) },
}

// getPumpBuf fetches a pooled 4 KB buffer and performs the type
// assertion once. On the impossible path where the pool yields an
// unexpected type (only possible if pumpBufPool.New is ever changed
// incorrectly), it falls back to a fresh slice so callers never get nil.
func getPumpBuf() []byte {
	buf, ok := pumpBufPool.Get().([]byte)
	if !ok {
		return make([]byte, 4096)
	}
	return buf
}

// ShellManager owns the single PTY session and its persistent working
// directory. It has its own mutex so shell operations don't contend
// with Hub.mu (SSE fan-out, bridge lifecycle, etc.).
type ShellManager struct {
	ctx     context.Context
	startSF singleflight.Group
	session *shellSession
	cwd     string
	workDir string
	mu      sync.Mutex
}

// NewShellManager creates a ShellManager with the given workspace root
// as the default working directory. The ctx should be the hub's shutdown
// context so PTY processes are cancelled on shutdown.
func NewShellManager(ctx context.Context, workDir string) *ShellManager {
	return &ShellManager{ctx: ctx, workDir: workDir}
}

// shellSession is the single running PTY session shared by the whole app.
type shellSession struct {
	subscribers map[*wsConn]struct{}
	cancel      context.CancelFunc
	ptmx        *os.File
	cmd         *exec.Cmd
	done        chan struct{}
	scrollback  *byteRing
	subSnap     []*wsConn // reusable slice for subscriber snapshot in pump loop
	mu          sync.Mutex
	subMu       sync.Mutex
}

// wsConn wraps a single WebSocket connection to the shell.
type wsConn struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

// controlMsg is a JSON control message from the client, distinguished
// from raw input by a leading 0x00 byte.
type controlMsg struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// handleShellWS is the WebSocket endpoint for the PTY. It upgrades the
// HTTP connection, attaches to the current shell session (starting one
// if needed), replays scrollback, and bridges I/O until the WS or PTY
// closes.
func (h *Hub) handleShellWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Same-origin only; the security middleware already checks Origin
		// for POST but WS needs its own check. InsecureSkipVerify=false
		// is the default; we rely on the browser's same-origin policy.
	})
	if err != nil {
		slog.Error("shell ws accept", "error", err)
		return
	}
	ws.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	wc := &wsConn{conn: ws, cancel: cancel}

	defer func() {
		cancel()
		ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Ensure a shell session exists.
	sess := h.shellMgr.getOrStart()
	if sess == nil {
		ws.Close(websocket.StatusInternalError, "shell unavailable")
		return
	}

	// Subscribe this WS to PTY output.
	sess.subMu.Lock()
	sess.subscribers[wc] = struct{}{}
	sess.subMu.Unlock()

	defer func() {
		sess.subMu.Lock()
		delete(sess.subscribers, wc)
		sess.subMu.Unlock()
	}()

	// Replay scrollback so the client can reconstruct terminal state.
	replay := sess.getScrollback()
	if len(replay) > 0 {
		if wErr := ws.Write(ctx, websocket.MessageBinary, replay); wErr != nil {
			return
		}
	}

	// Ping loop to keep the connection alive through iOS background.
	// Gate on shutdownCtx so the ping loop exits promptly during Shutdown
	// without needing explicit inflight tracking.
	stopShutdown := context.AfterFunc(h.lifecycle.shutdownCtx, cancel)
	defer stopShutdown()
	go func() {
		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sess.done:
				return
			case <-ticker.C:
				if pErr := ws.Ping(ctx); pErr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Notify client that the shell is alive (or send exit if already dead).
	select {
	case <-sess.done:
		// PTY already exited; the client will see the scrollback and
		// then the close frame.
		return
	default:
	}

	// Read loop: client → PTY.
	for {
		_, msg, rErr := ws.Read(ctx)
		if rErr != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}

		// 0x00 prefix = JSON control message.
		if msg[0] == 0x00 {
			var ctrl controlMsg
			if jErr := json.Unmarshal(msg[1:], &ctrl); jErr != nil {
				continue
			}
			handleShellControl(h.shellMgr, sess, &ctrl)
			continue
		}

		// Raw terminal input → PTY stdin.
		if _, wErr := sess.ptmx.Write(msg); wErr != nil {
			return
		}
	}
}

func handleShellControl(sm *ShellManager, sess *shellSession, ctrl *controlMsg) {
	switch ctrl.Type {
	case "resize":
		// Clamp to uint16 bounds. xterm.js doesn't send values anywhere
		// near 65535 cols/rows in practice, but an unclamped conversion
		// could silently overflow on a malformed payload.
		if ctrl.Cols > 0 && ctrl.Rows > 0 && ctrl.Cols <= 0xFFFF && ctrl.Rows <= 0xFFFF {
			if err := pty.Setsize(sess.ptmx, &pty.Winsize{
				Cols: uint16(ctrl.Cols),
				Rows: uint16(ctrl.Rows),
			}); err != nil {
				slog.Debug("shell resize", "error", err)
			}
		}
	case "signal":
		sig, ok := parseSignal(ctrl.Name)
		if !ok {
			return
		}
		if err := signalShellGroup(sess.cmd, sig); err != nil {
			slog.Error("shell signal", "error", err, "signal", ctrl.Name)
		}
	case "kill":
		sm.kill()
	}
}

// SetCwd updates the persistent working directory for new shell sessions.
func (sm *ShellManager) SetCwd(cwd string) {
	sm.mu.Lock()
	sm.cwd = cwd
	sm.mu.Unlock()
}

// getOrStart returns the current shell session, starting a new one
// if none exists. Concurrent callers coalesce via singleflight so only
// one start() runs at a time, eliminating the race-and-kill pattern.
func (sm *ShellManager) getOrStart() *shellSession {
	sm.mu.Lock()
	if sm.session != nil {
		sess := sm.session
		sm.mu.Unlock()
		return sess
	}
	sm.mu.Unlock()

	// Coalesce concurrent start attempts into a single pty.Start.
	// The closure always returns (*shellSession, nil); err is surfaced
	// at Warn only so a future closure change that adds a real error
	// path isn't silently dropped.
	v, err, _ := sm.startSF.Do("start", func() (any, error) {
		// Double-check after winning the singleflight race.
		sm.mu.Lock()
		if sm.session != nil {
			sess := sm.session
			sm.mu.Unlock()
			return sess, nil
		}
		sm.mu.Unlock()
		return sm.start(), nil
	})
	if err != nil {
		slog.Warn("shell: startSF.Do returned error", "error", err)
		return nil
	}
	if v == nil {
		return nil
	}
	sess, ok := v.(*shellSession)
	if !ok {
		slog.Error("shell: startSF.Do returned unexpected type",
			"type", fmt.Sprintf("%T", v))
		return nil
	}
	return sess
}

// start launches a new PTY session. Any existing session is killed first.
func (sm *ShellManager) start() *shellSession {
	sm.kill()

	ctx, cancel := context.WithCancel(sm.ctx)

	sm.mu.Lock()
	cwd := sm.cwd
	if cwd == "" {
		cwd = sm.workDir
	}
	sm.mu.Unlock()

	const shellBin = "bash"
	c := exec.CommandContext(ctx, shellBin, "--login")
	c.Dir = cwd
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	setShellProcAttr(c)

	ptmx, err := pty.StartWithSize(c, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		cancel()
		slog.Error("shell start", "error", err)
		return nil
	}

	sess := &shellSession{
		ptmx:        ptmx,
		cmd:         c,
		cancel:      cancel,
		scrollback:  newByteRing(outputBufferLimit),
		subscribers: make(map[*wsConn]struct{}),
		done:        make(chan struct{}),
	}

	sm.mu.Lock()
	sm.session = sess
	sm.mu.Unlock()

	slog.Info("shell started", "shell", shellBin, "pid", c.Process.Pid)

	// PTY output pump: reads from the PTY master and fans out to all
	// subscribed WebSocket connections + the scrollback buffer.
	go pumpShellPTY(ctx, sess)

	// Wait for the process to exit and clean up.
	go func() {
		if err := c.Wait(); err != nil {
			slog.Debug("shell process", "error", err)
		}
		close(sess.done)
		ptmx.Close()

		sm.mu.Lock()
		if sm.session == sess {
			sm.session = nil
		}
		sm.mu.Unlock()

		slog.Info("shell exited", "pid", c.Process.Pid)

		// Close all subscriber WebSockets so clients know the shell died.
		sess.subMu.Lock()
		for wc := range sess.subscribers {
			wc.conn.Close(websocket.StatusNormalClosure, "shell exited")
			wc.cancel()
		}
		sess.subMu.Unlock()
	}()

	return sess
}

// pumpShellPTY reads PTY output and fans it out to subscribers + scrollback.
func pumpShellPTY(ctx context.Context, sess *shellSession) {
	buf := getPumpBuf()
	defer pumpBufPool.Put(buf) //nolint:staticcheck // buf escapes to pool only after loop exits
	for {
		n, err := sess.ptmx.Read(buf)
		if n > 0 {
			data := buf[:n]

			// Append to scrollback.
			sess.appendScrollback(data)

			// Reuse subscriber snapshot slice to avoid per-read allocation.
			sess.subMu.Lock()
			sess.subSnap = sess.subSnap[:0]
			for wc := range sess.subscribers {
				sess.subSnap = append(sess.subSnap, wc)
			}
			subs := sess.subSnap
			sess.subMu.Unlock()

			for _, wc := range subs {
				wCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				if wErr := wc.conn.Write(wCtx, websocket.MessageBinary, data); wErr != nil {
					slog.Debug("shell ws write", "error", wErr)
				}
				cancel()
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				slog.Debug("shell pty read", "error", err)
			}
			return
		}
	}
}

// appendScrollback writes data to the circular scrollback buffer.
func (sess *shellSession) appendScrollback(data []byte) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.scrollback.Write(data)
}

// getScrollback returns the current scrollback contents in order.
func (sess *shellSession) getScrollback() []byte {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.scrollback.Bytes()
}

// kill stops the shell process if one is running.
func (sm *ShellManager) kill() {
	sm.mu.Lock()
	sess := sm.session
	sm.session = nil
	sm.mu.Unlock()

	if sess != nil {
		sess.cancel()
		sess.ptmx.Close()
	}
}
