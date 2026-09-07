package agent

import (
	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/secretstore"
)

// inbound answers the requests the agent makes of vibekit over ACP.
type inbound struct {
	lifetime *lifetime
	coord    *BridgeCoordinator
	chats    runChatReader
	ignore   *ignore.Matcher `wiring:"optional"`
	bus      *bus
	secrets  *secretstore.Store `wiring:"optional"`
}
