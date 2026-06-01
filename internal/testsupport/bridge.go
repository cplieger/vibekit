package testsupport

import (
	"context"

	"vibekit/internal/api"
)

// NopACPBridge is a minimal no-op implementation of api.ACPBridge for
// benchmarks and tests that need a bridge but don't care about its
// behavior. All methods return zero values.
type NopACPBridge struct{}

func (*NopACPBridge) Start(context.Context, *api.StartOpts) error              { return nil }
func (*NopACPBridge) Stop()                                                    {}
func (*NopACPBridge) Call(context.Context, string, any) (*api.RPCResponse, error) { return nil, nil }
func (*NopACPBridge) Notify(context.Context, string, any) error                { return nil }
func (*NopACPBridge) Respond(context.Context, int64, any, error) error         { return nil }
func (*NopACPBridge) SessionID() api.SessionID                                 { return "" }
func (*NopACPBridge) ModelID() api.ModelID                                     { return "" }
func (*NopACPBridge) CurrentMode() string                                      { return "" }
func (*NopACPBridge) Modes() []api.SessionMode                                 { return nil }
func (*NopACPBridge) Models() []api.SessionModel                               { return nil }
func (*NopACPBridge) SetModel(context.Context, string) error                   { return nil }
func (*NopACPBridge) NotifCh() <-chan *api.RPCResponse                         { return nil }
