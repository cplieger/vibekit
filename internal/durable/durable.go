// Package durable supplies the context a durable effect runs under.
//
// A leaf package because both sides of the agent/translate seam need the one
// symbol and agent imports translate.
package durable

import "context"

// Context returns the context a durable effect runs under: the caller's values,
// detached from its cancellation. Not a completion guarantee.
//
// No deadline either: it could only make chat.Store.Mutate's entry guard refuse
// the write the detach exists to permit.
func Context(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
