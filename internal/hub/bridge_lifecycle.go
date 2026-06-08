package hub

import "github.com/cplieger/vibekit/internal/api"

// primeReason is the reason a bridge needs priming on the next prompt.
type primeReason string

const (
	primeReasonNone   primeReason = ""
	primeReasonSwitch primeReason = "switch"
	modelAuto                     = api.ModelAuto
)
