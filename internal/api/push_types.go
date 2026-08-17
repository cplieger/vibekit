package api

import "strconv"

// Push types: Web Push subscription shapes and push notification kind
// constants. Consumed by the push package and the hub's push-notification
// path.

// PushSubscription is a Web Push subscription from the browser (RFC 8030).
type PushSubscription struct {
	Endpoint string               `json:"endpoint"`
	Keys     PushSubscriptionKeys `json:"keys"`
}

// PushSubscriptionKeys holds the client-side encryption keys.
type PushSubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushKind identifies a push notification category. The underlying
// string is the wire value stored in settings and matched by the
// client's service worker.
type PushKind string

// Push notification kind constants. These live in the api package so
// hub callers can reference them without importing the push package
// (eliminating the hub→push import that existed solely for constant
// access).
const (
	PushKindAgentFinished PushKind = "agent_finished"
	PushKindPermission    PushKind = "permission"
	// PushKindPRStatus fires when a pull request the connected identity opened
	// flips green or red. Unlike the other two it has no chat behind it: CI
	// finishes minutes after the turn that pushed, often with nothing open, which
	// is exactly why the poll is server-side.
	PushKindPRStatus PushKind = "pr_status"
)

// pushKinds is the authoritative set of valid push notification kinds.
// PushKind.Valid() derives from this set; adding a new kind requires
// only a new entry here.
//
// This map must be updated BEFORE push.kindRegistry gains the same kind: the
// registry's init() panics through validateKindRegistry on a kind Valid() rejects,
// so the wrong order is a boot failure rather than a silent drop. The reverse
// order (here but not in the registry) fails silently instead, because
// preflightSend drops a kind its prefs map has no entry for.
var pushKinds = map[PushKind]struct{}{
	PushKindAgentFinished: {},
	PushKindPermission:    {},
	PushKindPRStatus:      {},
}

// Valid reports whether k is a known push notification kind.
// Used at the settings-load boundary to reject corrupted keys
// at load time rather than at send time.
func (k PushKind) Valid() bool {
	_, ok := pushKinds[k]
	return ok
}

// PushSubject names what a notification is ABOUT.
//
// One value carries both halves of the subject because the service worker derives
// both from it and cannot derive either from the title and body: the OS coalescing
// tag (one tray slot per subject, so an ask on one chat cannot silently replace
// the finished note on another) and the click target. A notification has one
// subject; the tag and the target are two readings of it.
//
// ChatID is a notification about one chat. Key is a notification with NO chat
// behind it, and it carries a kind prefix (`pr:`) rather than a URL because the
// client owns the route vocabulary (router.ts) — a path assembled here would be a
// second copy of it. Exactly one field is set; an empty PushSubject is the
// workspace-global case that coalesces under a constant tag.
type PushSubject struct {
	ChatID ChatID `json:"chat_id,omitempty"`
	Key    string `json:"subject,omitempty"`
}

// ChatSubject is the subject of a notification about one chat. Pass a zero
// PushSubject (or ChatSubject("")) for a workspace-global one.
func ChatSubject(id ChatID) PushSubject { return PushSubject{ChatID: id} }

// PRSubjectPrefix marks a subject key naming a pull request. The client keys its
// route on this, so it is declared here rather than assembled at the call site:
// the poller and the service worker have to agree on it, and one of them is
// TypeScript.
const PRSubjectPrefix = "pr:"

// PRSubject is the subject of a notification about one pull request. `repo` is
// the forge's own owner/name slug and `number` the PR number, which together are
// unique per PR — so two PRs flipping inside one debounce window still occupy
// their own tray slots.
func PRSubject(forgeID, repo string, number int) PushSubject {
	return PushSubject{Key: PRSubjectPrefix + forgeID + ":" + repo + "#" + strconv.Itoa(number)}
}
