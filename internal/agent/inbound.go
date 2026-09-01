package agent

import (
	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/secretstore"
)

// inbound answers the requests the AGENT makes of vibekit (the client side
// of ACP): reading/writing/listing/deleting a path, fetching an SSO token,
// persisting a credential blob, or asking for a URL to be opened.
//
// One type because every member shares a spine — parse an RPC request, do
// one confined thing, answer on the same bridge through respondBridge —
// and because the auth responder both is reached by the router and reaches
// back through respondBridge, so splitting it out would make a mutual pair
// out of one request's handling.
type inbound struct {
	// lifetime supplies the workspace root every path is confined inside,
	// the in-flight counter async responders register on, and the process
	// context those responders outlive their triggering frame under.
	lifetime *lifetime
	// coord resolves the bridge an answer goes back on.
	coord *BridgeCoordinator
	chats runChatReader
	// ignore is the agent-ignore matcher, applied to reads and directory
	// listings. Nil when no config dir was set.
	ignore *ignore.Matcher `wiring:"optional"`
	// bus publishes the open-external-url ask and the token-failure error.
	bus *bus
	// secrets is the credential store KAS asks vibekit to hold. Nil is a
	// real state, reported to KAS as "absent" rather than failing.
	secrets *secretstore.Store `wiring:"optional"`
	// kiroToken vends the SSO access token, and authLatch remembers the
	// last outcome so readiness can report a dead sign-in without asking
	// kiro-cli.
	kiroToken *kiroauth.CLISource `wiring:"optional"`
	authLatch *authTokenLatch
}
