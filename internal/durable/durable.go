// Package durable supplies the context a durable effect runs under.
//
// A leaf package because both sides of the agent/translate seam need the one
// symbol and agent imports translate, so an exported helper in either would be
// an import cycle.
package durable

import "context"

// Context returns the context a durable effect runs under: the caller's values,
// detached from its cancellation.
//
// chat.Store.Mutate's entry guard is the whole context surface of the write
// path — the I/O below it already runs on context.Background() — so an attached
// context there refuses a message already assembled in memory. No deadline, for
// the same reason: it could only make that guard refuse the write the detach
// exists to permit. Not a completion guarantee.
func Context(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
