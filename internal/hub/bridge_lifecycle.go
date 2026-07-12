package hub

import "github.com/cplieger/vibekit/internal/api"

// primeReason is the reason a bridge needs priming on the next prompt.
type primeReason string

const (
	primeReasonNone   primeReason = ""
	primeReasonSwitch primeReason = "switch"
	// primeReasonRewind marks a rewind chat that degraded to a fresh
	// session/new because session/fork was unavailable or failed. Its
	// truncated transcript must be injected so the model has the prior
	// context the UI already shows. See BridgeCoordinator.spawnBridge.
	primeReasonRewind primeReason = "rewind"
	modelAuto                     = api.ModelAuto
)
