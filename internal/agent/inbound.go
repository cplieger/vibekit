package agent

import (
	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/secretstore"
)

// inbound answers the requests the AGENT makes of vibekit.
//
// ACP is bidirectional: vibekit calls the agent to prompt it, and the agent calls
// back to read a file, write one, list a directory, delete a path, fetch an SSO
// token, persist a credential blob or ask for a URL to be opened. Those are the
// client-side methods of the protocol, and vibekit is the client. This type is all
// of them and nothing else.
//
// It is one type rather than several because the members share a spine: every one
// parses an RPC request, does one confined thing, and answers on the same bridge
// through respondBridge. The token pair is here for a measured reason rather than
// a filing convenience — the auth responder both is reached by the router and
// reaches back through respondBridge, so splitting it out would have made a mutual
// pair out of one request's handling.
//
// The dependency runs one way: nothing in the rest of the package calls a
// responder, and the router is reached from exactly two places (the chat frame
// path and the run frame path). That is what made the extraction possible where
// the run clock's did not.
type inbound struct {
	// lifetime supplies the workspace root every path is confined inside, the
	// in-flight counter each async responder registers on, and the process
	// context those responders outlive their triggering frame under.
	lifetime *lifetime
	// coord resolves the bridge an answer goes back on.
	coord *BridgeCoordinator
	// chats is read-only: one responder counts a chat's messages to decide
	// whether a write is the first in its turn.
	chats runChatReader
	// ignore is the agent-ignore matcher, applied to reads and directory
	// listings. Nil when no config dir was set, and the responders check.
	ignore *ignore.Matcher `wiring:"optional"`
	// bus publishes the open-external-url ask and the token-failure error.
	bus *bus
	// secrets is the credential store KAS asks vibekit to hold. Nil is a real
	// state, reported to KAS as "absent" rather than failing the request.
	secrets *secretstore.Store `wiring:"optional"`
	// kiroToken vends the SSO access token, and authLatch remembers the last
	// outcome so readiness can report a dead sign-in without asking kiro-cli.
	kiroToken *kiroauth.CLISource `wiring:"optional"`
	authLatch *authTokenLatch
}
