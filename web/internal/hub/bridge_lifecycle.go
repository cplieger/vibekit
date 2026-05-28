package hub

import (
	"context"

	"vibekit/internal/api"
)



// primeReason is the reason a bridge needs priming on the next prompt.
type primeReason string

const (
	primeReasonNone   primeReason = ""
	primeReasonSwitch primeReason = "switch"
	modelAuto                     = "auto"
)

// getOrCreateBridge delegates to the BridgeCoordinator.
func (h *Hub) getOrCreateBridge(ctx context.Context, chatID api.ChatID, agentOverride, modelOverride string) (*sharedBridge, error) {
	return h.coord.GetOrCreateBridge(ctx, chatID, agentOverride, modelOverride)
}

// getBridge returns the bridge for chatID, or nil.
func (h *Hub) getBridge(chatID api.ChatID) *sharedBridge {
	return h.coord.GetBridge(chatID)
}

// closeBridge stops a bridge and removes it from the map.
func (h *Hub) closeBridge(chatID api.ChatID) {
	h.coord.CloseBridge(chatID)
}

// forward delegates to the BridgeCoordinator.
func (h *Hub) forward(chatID api.ChatID, bridge api.ACPBridge) {
	h.coord.Forward(chatID, bridge)
}

// primeIfNeeded delegates to the BridgeCoordinator.
func (h *Hub) primeIfNeeded(ctx context.Context, chatID api.ChatID, sb *sharedBridge) {
	h.coord.PrimeIfNeeded(ctx, chatID, sb)
}

// restoreEffort delegates to the BridgeCoordinator.
func (h *Hub) restoreEffort(ctx context.Context, chatID api.ChatID, model string, b api.ACPBridge) {
	h.coord.RestoreEffort(ctx, chatID, model, b)
}
