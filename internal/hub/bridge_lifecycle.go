package hub

import "github.com/cplieger/vibekit/internal/api"

// primeReason is the reason a bridge needs priming on the next prompt.
type primeReason string

const (
	primeReasonNone   primeReason = ""
	primeReasonSwitch primeReason = "switch"
	// primeReasonReload covers a fresh session created because session/load
	// failed. Without it that path primed nothing and the agent started blind on
	// a chat whose transcript was on screen.
	primeReasonReload primeReason = "load_failed"
	modelAuto                     = api.ModelAuto
)
