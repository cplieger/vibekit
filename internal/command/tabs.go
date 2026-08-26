package command

// The four tab commands. Each one validates its payload, hands the operation to
// the membership coordinator, and returns what the coordinator committed.
//
// Nothing here touches either store directly, and that is the point: the
// ordering, the capacity reservation and the event all live in one type, so a
// handler cannot get them in the wrong order or forget one. What a handler owns
// is the DOOR — the payload's shape, the identifier bounds, and the HTTP status
// its refusal answers with.
//
// Every payload carries a client-minted op_id, echoed back on the event so the
// caller can correlate the frame with its own dispatch. It is not an idempotency
// key: retry safety is the Idempotency-Key HEADER's job (one middleware over
// every mutating route), and an op_id has no TTL, no cache and no 409 branch.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// errTooManyOrderIDs is reorder_tabs' 413: a list longer than the decode bound
// is refused before it reaches the store, because the exact-set check would
// reject it anyway and there is no reason to allocate two maps for it first.
var errTooManyOrderIDs = errors.New("order names more ids than the store can hold")

// CmdOpenTab opens a tab for something that already exists.
//
// It NEVER mints a chat — create_chat does that, and opens the tab through the
// same coordinator. Conflating the two is what made a client mint chat ids: there
// was no server operation that opened a tab and returned its identity.
//
// The response carries `created`, and that flag is load-bearing rather than
// informational. An already-open (kind, ref) mutates nothing, so it emits no
// event; a client that resolved only on the event would hang, so it resolves on
// this response instead.
func CmdOpenTab(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.OpenTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !p.Kind.Valid() || !ValidIdent(p.OpID) || !validTabID(p.Parent) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// A CHAT ref is validated as a chat id HERE rather than in the store. The
	// store treats a ref as opaque text on purpose — whether a string is a valid
	// chat id or a path inside a granted root is the command boundary's question,
	// because that is where ids.ValidChatID and the file-browser roots live.
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
		"subject": opened.Subject,
		"created": opened.Created,
		"version": opened.Version,
	}), nil
}

// CmdCloseTab closes a tab and its children.
//
// For a CHAT tab this also runs the teardown close_chat used to be a client
// command for: the × means "kill the work" (user decision), so the turn is
// cancelled, the chat's runs are cancelled and the bridge is torn down. The
// RECORD survives — under retention a closed chat is a chat without a tab, and
// reopening it session/loads everything back.
//
// `closed` is a LIST because a parent and its children go as one mutation, and
// it is empty rather than an error for an id that is not open: two devices can
// close the same tab.
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
		"closed":  subjectIDs(closed),
		"version": version,
	}), nil
}

// CmdReorderTabs replaces the order with the arrangement a drag committed.
//
// There is NO base-version precondition: the exact-set check is the whole
// precondition, and requiring a version would discard a perfectly valid drag
// whenever any unrelated mutation landed first — a pin elsewhere bumps the
// version without changing the order. A set mismatch is a 409, which is the
// signal to re-list rather than to re-send.
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
	return responseWith(map[string]any{"version": version}), nil
}

// CmdPinTab pins or unpins one tab. Idempotent in both directions.
//
// The pinned-ahead-of-unpinned partition is NOT applied server-side: it is a
// rendering rule the client owns, and rearranging the stored order here would
// contradict the exact-set contract reorder_tabs is checked against.
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
	return responseWith(map[string]any{"version": version}), nil
}

// tabsMaxOrderIDs is deliberately not a constant here: the bound a reorder is
// refused at IS the store's decode bound (tabs.MaxTabs), because an order longer
// than the most tabs the store will ever hold cannot name the open set. A second
// number would be one to keep in step for no gain.

// validTabID reports whether s is safe to use as a tab id: hex from the store's
// own minting, or empty where the field is optional.
//
// It delegates to the SAME identifier rule every other opaque id on this
// boundary uses (ids.ValidIdent: ASCII alphanumerics plus `_.-`, bounded), so
// there is one answer to "what may an id contain" rather than a per-field one.
// The bound is what matters: a tab id reaches a map key, a log line and an SSE
// frame.
func validTabID(s string) bool {
	return s == "" || ids.ValidIdent(s)
}
