package hub

import "github.com/cplieger/vibekit/internal/vibekit"

// primeReason is the reason a bridge needs priming on the next prompt.
type primeReason string

const (
	primeReasonNone   primeReason = ""
	primeReasonSwitch primeReason = "switch"
	// primeReasonReload covers a fresh session created because session/load
	// failed. Without it that path primed nothing and the agent started blind on
	// a chat whose transcript was on screen.
	primeReasonReload primeReason = "load_failed"
	// primeReasonFork covers a TANGENT whose session/fork was refused. The fork
	// is what normally carries the parent's context, so without it the tangent's
	// fresh session would start blind on a conversation the user opened it
	// FROM — the one case where a chat's own transcript is not the history it
	// needs, which is why this reason carries a source chat (sharedBridge.primeFrom).
	primeReasonFork primeReason = "fork_refused"
	modelAuto                   = vibekit.ModelAuto
)
