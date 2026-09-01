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
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// handleFSRequest dispatches fs/* incoming requests. Returns true if msg
// was an fs method (dispatched async); false if not, so the caller can
// try other dispatch paths. Async so fs reads (up to fsReadCap, on slow
// disks) don't block concurrent session/update streaming.
//
// Every dispatch is wrapped with a deferred recover so a panic in path
// handling, JSON unmarshal, or the checkpoint snapshot path becomes a logged
// warn + JSON-RPC error rather than a process-killing crash. Panics were
// reachable via integer overflow on attacker-influenced line/limit params;
// the bug is fixed in sliceByLines but this wrapper forecloses the class.
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
		// per-event ctx via its defer the instant it returns, which is BEFORE
		// this async handler runs its Respond, and Bridge.Respond drops a write
		// on an already-cancelled ctx. The fresh ctx lives until this goroutine
		// finishes or the runtime shuts down (in.lifetime.done). Shadows the
		// passed-in ctx.
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
	root, rel, err := in.lifetime.confineInWorkDir(p.Path)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	// Agent-ignore-files filter: refuse the read when the path matches a user
	// ignore rule (Settings → Permissions). Writes are NOT blocked (git
	// semantics: an ignored file stays writable). rel comes from the
	// confinement, so the filter judges the same string the read is named by.
	// The stat is confined too and exists only for the isDir hint.
	if in.ignore != nil {
		isDir := false
		if info, statErr := root.Stat(rel); statErr == nil {
			isDir = info.IsDir()
		}
		if in.ignore.Matches(ctx, rel, isDir) {
			in.respondFSError(ctx, chatID, msg, errIgnored)
			return
		}
	}
	// A confined, bounded read: the size bound is taken from the open
	// DESCRIPTOR rather than a stat-then-open pathname (which could describe a
	// different file by the time it opens), a named pipe at the name is
	// refused rather than blocking open(2) forever, and every path component
	// is re-resolved inside the root.
	data, err := atomicfile.ReadBoundedInRoot(ctx, root, rel, fsReadCap)
	if err != nil {
		if errors.Is(err, atomicfile.ErrFileTooLarge) {
			err = fmt.Errorf("%w: %d", errCapExceeded, fsReadCap)
		}
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	content := sliceByLines(string(data), p.Line, p.Limit)
	in.respondBridge(ctx, chatID, msg, map[string]any{"content": content}, nil)
}

// sliceByLines returns content[line-1 : line-1+limit] (1-indexed,
// inclusive). Nil pointers mean "from the start" / "to the end".
//
// The window is a SUBSTRING of content (every line strings.Lines yields is
// contiguous), so the two walks below accumulate byte offsets and the result
// needs no copy. *limit is only ever compared against a running count, never
// added to an offset, so an attacker-controlled *limit cannot overflow an
// index the way `end = start + *limit` could.
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
