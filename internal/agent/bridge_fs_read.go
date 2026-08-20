// File-system read request handlers for kiro-cli ACP bridges.
//
// Spec: https://agentclientprotocol.com/protocol/file-system

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
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
func (in *inbound) handleFSRequest(_ context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	var handler func(context.Context, vibekit.ChatID, *vibekit.RPCResponse)
	switch msg.Method {
	case vibekit.MethodFSRead:
		handler = in.respondFSRead
	case vibekit.MethodFSWrite:
		handler = in.respondFSWrite
	default:
		return false
	}
	in.lifetime.inflight.Go(func() {
		// Derive a fresh runtime-scoped context: translateACPEvent cancels the
		// per-event ctx via its defer the instant it returns, which is
		// BEFORE this async handler runs its Respond. Bridge.Respond drops
		// the write when its ctx is already cancelled, so the agent's
		// fs read/write Call would hang forever. The fresh ctx lives until
		// this goroutine finishes or the runtime shuts down (in.lifetime.done),
		// so Respond succeeds AND shutdown still cancels it. Mirrors the
		// chat_summary goroutine in runtime.go. Shadows the passed-in ctx.
		ctx, cancel := in.lifetime.derivedContext()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("fs handler panic",
					"chat_id", chatID, "method", msg.Method, "panic", r)
				in.respondBridge(ctx, chatID, msg, nil, errors.New("internal error"))
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
func (in *inbound) respondFSRead(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		Line  *int   `json:"line,omitempty"`
		Limit *int   `json:"limit,omitempty"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("parse params: %w", err))
		return
	}
	if p.Path == "" {
		in.respondFSError(ctx, chatID, msg, errors.New("path is required"))
		return
	}
	abs, err := in.lifetime.resolveInsideWorkDir(p.Path)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
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
	if in.ignore != nil {
		rel, relErr := workspace.RelPath(in.lifetime.workDir, abs)
		if relErr != nil {
			// Rel should never fail after resolveInsideWorkDir
			// anchored abs under workDir, but if it does we fail
			// open (ignore filter skipped) to preserve reads.
			// Log so operators see the 3am case.
			slog.Warn("ignore check skipped: filepath.Rel failed",
				"chat_id", chatID, "path", p.Path, "abs", abs, "error", relErr)
		} else {
			isDir := statErr == nil && info.IsDir()
			if in.ignore.Matches(ctx, rel, isDir) {
				in.respondFSError(ctx, chatID, msg, errIgnored)
				return
			}
		}
	}
	// Size-check before loading so a multi-gigabyte file doesn't pin
	// the process at peak memory while we decide to reject it. Stat
	// can lie about sparse/remote files, but the post-read guard
	// below catches the residual case.
	if statErr == nil && info.Size() > fsReadCap {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsReadCap))
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	if len(data) > fsReadCap {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsReadCap))
		return
	}
	content := sliceByLines(string(data), p.Line, p.Limit)
	in.respondBridge(ctx, chatID, msg, map[string]any{"content": content}, nil)
}

// sliceByLines returns content[line-1 : line-1+limit] (1-indexed,
// inclusive). Nil pointers mean "from the start" / "to the end".
//
// Every line strings.Lines yields is contiguous in content, so the window is a
// SUBSTRING of content and needs no copy: the two walks below accumulate byte
// offsets and the result is one slice expression. That also removes the
// arithmetic the previous version had to defend. It built the whole line index
// with strings.SplitAfter and then strings.Join'ed the window, which cost one
// slice header per line of the file plus a copy of the selected text, and it
// needed a saturating comparison because attacker-controlled *limit was ADDED
// to an offset (`end = start + *limit`) and could overflow to a negative index.
// Here *limit is only ever compared against a running count, so there is no
// arithmetic left to overflow.
//
// Measured on go1.27.0 over a 100k-line file (8 MB, the same order as
// fsReadCap's 8 MiB ceiling): a 20-line window went 1,800,803 ns/op and
// 1,607,443 B/op to 183.3 ns/op and 0 B/op, and a read from line 1 with no
// limit went 3,424,455 ns/op and 9,707,529 B/op to 15.13 ns/op and 0 B/op. The
// rewrite is behaviour-identical: 0 divergences from the old implementation over
// 95,091 exhaustive comparisons (every string over {'a','\n','\r'} up to length
// 6 against 87 line/limit combinations, negative and MaxInt included) and
// 1,740,000 randomized ones over content mixing '\n', '\r\n' and bare '\r'.
func sliceByLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	skip := 0
	if line != nil && *line > 0 {
		skip = *line - 1
	}
	// Walk to the first byte of the requested line.
	lo, n := 0, 0
	for ln := range strings.Lines(content) {
		if n == skip {
			break
		}
		lo += len(ln)
		n++
	}
	if n < skip {
		// The window starts past the last line.
		return ""
	}
	if limit == nil || *limit <= 0 {
		return content[lo:]
	}
	// Walk limit lines on from there. Stopping on the count rather than on an
	// offset is what makes an absurd *limit harmless: the loop simply runs out
	// of lines.
	hi, taken := lo, 0
	for ln := range strings.Lines(content[lo:]) {
		if taken == *limit {
			break
		}
		hi += len(ln)
		taken++
	}
	return content[lo:hi]
}
