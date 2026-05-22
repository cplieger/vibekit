package hub

import "maps"

// sessionParams builds the base ACP parameter map with the "sessionId"
// key set from sb's bridge. Extra key-value pairs from extra maps are
// merged in (last-wins). Centralises the wire key so a future rename
// (e.g. "session_id") requires a single-site update.
func sessionParams(sb *sharedBridge, extra ...map[string]any) map[string]any {
	m := map[string]any{"sessionId": sb.bridge.SessionID()}
	for _, e := range extra {
		maps.Copy(m, e)
	}
	return m
}
