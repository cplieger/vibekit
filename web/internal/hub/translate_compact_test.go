package hub

import (
	"context"
	"encoding/json"
	"testing"

	"vibekit/internal/api"
)

func BenchmarkHandleCompactionStatus(b *testing.B) {
	started, _ := json.Marshal(map[string]any{
		"status": map[string]string{"type": "started"},
	})
	completed, _ := json.Marshal(map[string]any{
		"status":  map[string]string{"type": "completed"},
		"summary": "Context was compacted. The conversation covered debugging a flaky test in hub_test.go and adding retry logic to the bridge lifecycle.",
	})

	cases := []struct {
		name   string
		params json.RawMessage
	}{
		{"started", started},
		{"completed", completed},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(context.Background(), "bench", func(c *api.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			msg := &api.RPCResponse{
				Method: "_kiro.dev/compaction/status",
				Params: tc.params,
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				h.translator.HandleCompactionStatus(context.Background(), "bench", msg)
			}
		})
	}
}
