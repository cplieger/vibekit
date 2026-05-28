// Package testsupport provides shared test fakes for interfaces defined in
// the api package. These are intended for use across multiple test packages
// to avoid duplicating interface implementations.
package testsupport

import (
	"context"
	"net/http"

	"vibekit/internal/api"
)

// NopChatStore is a no-op api.ChatStore implementation for benchmarks.
// Every method returns zero/nil.
type NopChatStore struct{}

func (NopChatStore) RegisterRoutes(*http.ServeMux)                                            {}
func (NopChatStore) SetBroadcaster(api.Broadcaster)                                           {}
func (NopChatStore) Get(context.Context, api.ChatID) (*api.Chat, bool)                        { return nil, false }
func (NopChatStore) List(context.Context) []api.ChatHeader                                    { return nil }
func (NopChatStore) BuildHistory(context.Context, api.ChatID) string                          { return "" }
func (NopChatStore) Mutate(context.Context, api.ChatID, func(*api.Chat, bool) bool) error     { return nil }
func (NopChatStore) Delete(context.Context, api.ChatID) error                                 { return nil }
func (NopChatStore) Archive(context.Context, api.ChatID) error                                { return nil }
func (NopChatStore) ListArchived(context.Context) []api.ChatHeader                            { return nil }
func (NopChatStore) RestoreArchived(context.Context, api.ChatID) error                        { return nil }
func (NopChatStore) UpdateArchivedSummary(context.Context, api.ChatID, string) error          { return nil }
func (NopChatStore) LoadArchived(context.Context, api.ChatID) (*api.Chat, error)              { return nil, nil }
func (NopChatStore) DeleteArchived(context.Context, api.ChatID) error                         { return nil }
func (NopChatStore) AppendMessage(context.Context, api.ChatID, *api.Message) error            { return nil }
func (NopChatStore) UpdateMessage(context.Context, api.ChatID, string, func(*api.Message)) error { return nil }

// Compile-time assertion.
var _ api.ChatStore = NopChatStore{}
