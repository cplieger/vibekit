// File-system read request handlers for kiro-cli ACP bridges.
//
// Spec: https://agentclientprotocol.com/protocol/file-system

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/workspace"
)

// handleFSRequest dispatches fs/* incoming requests. Returns true if msg
// was an fs method (dispatched async); false if not, so the caller can
// try other dispatch paths. The dispatch is async to keep the forward
// goroutine unblocked for concurrent session/update chunks — fs reads
// can take real time on slow disks or large files (up to fsReadCap),
// and blocking the forward loop would stall assistant streaming. A
// dedicated goroutine per request is fine; requests are bounded by
// kiro-cli's own in-flight count (it won't fire hundreds in parallel).
//
// Every dispatch is wrapped with a deferred recover so that a panic in
// path handling, JSON unmarshal, or the checkpoint snapshot path is
// turned into a logged warn + JSON-RPC error back to kiro-cli rather
// than a process-killing crash. Panics in fs handlers were reachable
// via integer overflow on attacker-influenced line/limit params; the
// immediate bug is fixed in sliceByLines but this wrapper forecloses
// the whole class.
func (h *Hub) handleFSRequest(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) bool {
	var handler func(context.Context, api.ChatID, *api.RPCResponse)
	switch msg.Method {
	case api.MethodFSRead:
		handler = h.respondFSRead
	case api.MethodFSWrite:
		handler = h.respondFSWrite
	default:
		return false
	}
	h.lifecycle.inflight.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("fs handler panic",
					"chat_id", chatID, "method", msg.Method, "panic", r)
				h.respondBridge(ctx, chatID, msg, nil, errors.New("internal error"))
			}
		}()
		handler(ctx, chatID, msg)
	})
	return true
}

// respondFSRead handles fs/read_text_file. Request params:
//
//	{ sessionId, path, line?: int, limit?: int }
//
// Response: { content: "..." }. Per ACP, line/limit are 1-indexed +
// inclusive; we slice the read content to that window.
func (h *Hub) respondFSRead(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Line  *int   `json:"line,omitempty"`
		Limit *int   `json:"limit,omitempty"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		h.respondFSError(ctx, chatID, msg, fmt.Errorf("parse params: %w", err))
		return
	}
	if p.Path == "" {
		h.respondFSError(ctx, chatID, msg, errors.New("path is required"))
		return
	}
	abs, err := h.resolveInsideWorkDir(p.Path)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	// Single stat for both the ignore-check (isDir) and the size guard.
	// Eliminates a redundant syscall per read request.
	info, statErr := os.Stat(abs)
	// Agent-ignore-files filter: if the user listed ignore files in
	// Settings → Permissions and this path matches, refuse the read.
	// Writes are deliberately not blocked (same semantics as git —
	// ignored files stay writable). The ignore matcher re-parses on
	// its own if the files change, so toggles take effect without a
	// bridge restart.
	if h.perm.ignore != nil {
		rel, relErr := workspace.RelPath(h.lifecycle.workDir, abs)
		if relErr != nil {
			// Rel should never fail after resolveInsideWorkDir
			// anchored abs under workDir, but if it does we fail
			// open (ignore filter skipped) to preserve reads.
			// Log so operators see the 3am case.
			slog.Warn("ignore check skipped: filepath.Rel failed",
				"chat_id", chatID, "path", p.Path, "abs", abs, "error", relErr)
		} else {
			isDir := statErr == nil && info.IsDir()
			if h.perm.ignore.Matches(ctx, rel, isDir) {
				h.respondFSError(ctx, chatID, msg, errIgnored)
				return
			}
		}
	}
	// Size-check before loading so a multi-gigabyte file doesn't pin
	// the process at peak memory while we decide to reject it. Stat
	// can lie about sparse/remote files, but the post-read guard
	// below catches the residual case.
	if statErr == nil && info.Size() > fsReadCap {
		h.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsReadCap))
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	if len(data) > fsReadCap {
		h.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsReadCap))
		return
	}
	content := sliceByLines(string(data), p.Line, p.Limit)
	h.respondBridge(ctx, chatID, msg, map[string]any{"content": content}, nil)
}

// sliceByLines returns content[line-1 : line-1+limit] (1-indexed,
// inclusive). Nil pointers mean "from the start" / "to the end".
func sliceByLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	start := 0
	if line != nil && *line > 0 {
		start = *line - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	// Saturating add: *limit can be attacker-controlled (JSON numbers
	// deserialise into *int with arbitrary-precision values), and a
	// naive `start + *limit < end` check overflows to a negative int
	// which then passes as < end and turns `lines[start:negative]`
	// into a panic. Rewrite the comparison so it can't overflow:
	// ask whether *limit is smaller than the remaining window.
	if limit != nil && *limit > 0 && *limit < end-start {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "")
}
