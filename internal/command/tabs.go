package command

// The four tab commands. Each validates its payload, hands the operation to
// the membership coordinator, and returns what the coordinator committed.
// Nothing here touches either store directly: the ordering, capacity
// reservation and event all live in one type.
//
// Every payload carries a client-minted op_id, echoed back on the event so
// the caller can correlate the frame with its own dispatch. It is not an
// idempotency key — that is the Idempotency-Key header's job.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// keyVersion is the response field carrying the collection version a
// mutation committed — one spelling across every tab response, since a
// client keys its gap detection on this field.
const keyVersion = "version"

// errTooManyOrderIDs is reorder_tabs' 413: a list longer than the decode
// bound is refused before it reaches the store.
var errTooManyOrderIDs = errors.New("order names more ids than the store can hold")

// CmdOpenTab opens a tab for something that already exists; it never
// mints a chat.
//
// The response's `created` flag is load-bearing: an already-open (kind,
// ref) mutates nothing and emits no event, so a client waiting on that
// event would hang — it resolves on this response instead.
func CmdOpenTab(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.OpenTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !p.Kind.Valid() || !ValidIdent(p.OpID) || !validTabID(p.Parent) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// A chat ref is validated as a chat id here rather than in the store,
	// which treats a ref as opaque text on purpose.
	if p.Kind == vibekit.TabKindChat && !ids.ValidChatID(p.Ref) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	opened, err := mem.OpenTab(ctx, vibekit.OpenTab{
		Kind:   p.Kind,
		Ref:    p.Ref,
		Parent: p.Parent,
		Owns:   p.Owns,
	}, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{
		"subject":  opened.Subject,
		"created":  opened.Created,
		keyVersion: opened.Version,
	}), nil
}

// CmdCloseTab closes a tab and its children.
//
// For a chat tab this also runs the close_chat teardown: the turn is
// cancelled, the chat's runs are cancelled, and the bridge is torn down.
// The record survives — under retention a closed chat is a chat without a
// tab.
//
// `closed` is a list because a parent and its children go as one
// mutation; empty rather than an error for an id that is not open.
func CmdCloseTab(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.CloseTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validTabID(p.ID) || p.ID == "" || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	closed, version, err := mem.CloseTab(ctx, p.ID, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{
		"closed":   subjectIDs(closed),
		keyVersion: version,
	}), nil
}

// CmdReorderTabs replaces the order with the arrangement a drag committed.
//
// No base-version precondition: the exact-set check is the whole
// precondition, and requiring a version would discard a valid drag
// whenever an unrelated pin bumped the version first. A set mismatch is a
// 409 — re-list, never re-send.
func CmdReorderTabs(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.ReorderTabsCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if len(p.Order) > tabs.MaxTabs {
		return nil, StatusError(http.StatusRequestEntityTooLarge, errTooManyOrderIDs)
	}
	for _, id := range p.Order {
		if id == "" || !validTabID(id) {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	version, err := mem.ReorderTabs(ctx, p.Order, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{keyVersion: version}), nil
}

// CmdPinTab pins or unpins one tab. Idempotent in both directions.
//
// The pinned-ahead-of-unpinned partition is a client rendering rule, not
// applied here.
func CmdPinTab(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.PinTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validTabID(p.ID) || p.ID == "" || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	version, err := mem.SetPinned(ctx, p.ID, p.Pinned, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{keyVersion: version}), nil
}

// tabsMaxOrderIDs is deliberately not a constant here: the bound a reorder
// is refused at is the store's decode bound (tabs.MaxTabs).

// validTabID reports whether s is safe to use as a tab id: hex from the
// store's own minting, or empty where the field is optional.
func validTabID(s string) bool {
	return s == "" || ids.ValidIdent(s)
}
