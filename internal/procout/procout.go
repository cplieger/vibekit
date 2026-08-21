// Package procout captures a spawned process's stdout or stderr under a byte
// cap, so a runaway or hostile subprocess cannot exhaust the container's memory
// through a pipe vibekit is draining.
//
// It is its own package because both consumers need it and neither should own
// it: internal/auth shells out to kiro-cli for the identity endpoints and
// internal/server shells out to it for version, diagnostics and settings.
// Sibling of internal/procgroup, which owns the other half of spawning a
// command safely — that one bounds a command's process TREE, this one bounds
// its OUTPUT.
//
// The capture is deliberately NOT an HTTP concern and used to live in the HTTP
// package because a refactor moved two files together. Nothing here touches
// net/http, and no consumer of it serves the bytes to a client unread: both run
// them through sanitize.Output first.
//
// # Write always reports a full write, and that is the whole point
//
// os/exec drains a non-*os.File Stdout/Stderr with io.Copy on its own
// goroutine, and io.Copy converts a short write into io.ErrShortWrite, which
// Cmd.Wait then returns AS THE COMMAND'S ERROR when the process itself exited
// 0. A capping writer that reports how many bytes it KEPT therefore reports a
// successful command as a failed one on exactly the write that crosses the cap.
//
// This is not theoretical. It was the behaviour of the LimitedWriter this type
// replaces, and it was measured against that type directly, at a 10-byte cap on
// a child that writes 100 bytes to stderr and exits 0. The failure has TWO
// shapes and which one appears is a race, so neither is the whole story:
//
//   - The child has already exited when the copier reports. io.Copy's
//     io.ErrShortWrite reaches Cmd.Wait as the copy error, the process itself
//     succeeded, so cmd.Run() returns "short write" (errors.Is(err,
//     io.ErrShortWrite) is true).
//   - The child is still writing. The copier has stopped, so its end of the
//     pipe closes, the next write takes SIGPIPE, and cmd.Run() returns "signal:
//     broken pipe" — the process is KILLED, and the exit error hides the copy
//     error that caused it. This is the shape a chatty long-running child hits,
//     and the one a search for "short write" does not find.
//
// Either way a command that exited 0 is reported as failed, and every auth
// handler treats a non-nil Run error as "kiro-cli failed" — so a successful
// whoami or logout with chatty stderr reported failure to the client, and in
// the second shape the subprocess was terminated part-way as well.
//
// Its sibling capped writer in internal/server got this right and said so in a
// comment; the two lived four lines apart in one file, and that sibling's Write
// is the body this one adopted. Keeping one bounded-capture type is what stops
// the choice from being a per-site coin flip.
//
// So Write returns len(p) with a nil error unconditionally, and the bytes it
// declined to keep are reported through Truncated instead. A caller that needs
// to know whether output was lost asks; a caller that does not, cannot be
// broken by not asking.
package procout

// Buffer is an io.Writer that keeps at most Limit bytes of what is written to
// it and drops the rest, recording whether anything was dropped.
//
// The zero value keeps nothing. A non-positive limit is a valid "capture
// nothing but tell me it happened" configuration rather than an error.
//
// Concurrency: a Buffer is safe for the two ways os/exec uses one, and unsafe
// for anything else. Assigning the SAME *Buffer to both Cmd.Stdout and
// Cmd.Stderr is the documented way to merge the two streams — os/exec compares
// the two writers and guarantees at most one goroutine calls Write at a time —
// and reading Bytes/String/Len/Truncated after Cmd.Wait returns is ordered by
// Wait's own wait on the copying goroutines. Do not share one Buffer between
// two commands, and do not read it while a command is still running.
type Buffer struct {
	data      []byte
	limit     int
	truncated bool
}

// NewBuffer returns a Buffer that keeps at most limit bytes.
func NewBuffer(limit int) *Buffer {
	return &Buffer{limit: limit}
}

// Write keeps as much of p as the remaining budget allows and reports a full
// write regardless, so os/exec's copier never turns a reached cap into
// io.ErrShortWrite on a command that succeeded. It never returns an error.
func (b *Buffer) Write(p []byte) (int, error) {
	n := len(p)
	switch room := b.limit - len(b.data); {
	case room <= 0:
		if n > 0 {
			b.truncated = true
		}
	case n > room:
		b.truncated = true
		b.data = append(b.data, p[:room]...)
	default:
		b.data = append(b.data, p...)
	}
	return n, nil
}

// Bytes returns the captured prefix. The slice aliases the Buffer's storage, so
// a caller that retains it past the next Write must copy.
func (b *Buffer) Bytes() []byte { return b.data }

// String returns the captured prefix as a string.
func (b *Buffer) String() string { return string(b.data) }

// Len returns how many bytes were captured, which is at most the limit.
func (b *Buffer) Len() int { return len(b.data) }

// Truncated reports whether any byte written to the Buffer was dropped, so a
// caller can label the output it captured as partial. It is the only channel
// for that fact: Write cannot signal it without breaking the os/exec contract
// documented on this package.
func (b *Buffer) Truncated() bool { return b.truncated }
