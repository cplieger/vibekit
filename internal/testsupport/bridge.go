package testsupport

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// NopACPBridge is a minimal no-op implementation of api.ACPBridge for
// benchmarks and tests that need a bridge but don't care about its
// behavior. All methods return zero values.
type NopACPBridge struct{}

// Start is a no-op; implements api.ACPBridge.
func (*NopACPBridge) Start(context.Context, *api.StartOpts) error { return nil }

// Stop is a no-op; implements api.ACPBridge.
func (*NopACPBridge) Stop() {}

// Call is a no-op; implements api.ACPBridge.
func (*NopACPBridge) Call(context.Context, string, any) (*api.RPCResponse, error) { return nil, nil }

// Notify is a no-op; implements api.ACPBridge.
func (*NopACPBridge) Notify(context.Context, string, any) error { return nil }

// Respond is a no-op; implements api.ACPBridge.
func (*NopACPBridge) Respond(context.Context, int64, any, error) error { return nil }

// SessionID returns an empty session ID; implements api.ACPBridge.
func (*NopACPBridge) SessionID() api.SessionID { return "" }

// ModelID returns an empty model ID; implements api.ACPBridge.
func (*NopACPBridge) ModelID() api.ModelID { return "" }

// CurrentMode returns an empty mode string; implements api.ACPBridge.
func (*NopACPBridge) CurrentMode() string { return "" }

// Modes returns nil; implements api.ACPBridge.
func (*NopACPBridge) Modes() []api.SessionMode { return nil }

// Models returns nil; implements api.ACPBridge.
func (*NopACPBridge) Models() []api.SessionModel { return nil }

// SetModel is a no-op; implements api.ACPBridge.
func (*NopACPBridge) SetModel(context.Context, string) error { return nil }

// NotifCh returns nil; implements api.ACPBridge.
func (*NopACPBridge) NotifCh() <-chan *api.RPCResponse { return nil }
