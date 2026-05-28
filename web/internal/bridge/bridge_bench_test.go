package bridge

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"vibekit/internal/api"
)

// BenchmarkReadLoop_Notifications measures JSON unmarshal throughput for
// notification-shaped messages (the hot path in readLoop).
func BenchmarkReadLoop_Notifications(b *testing.B) {
	notif := api.RPCResponse{
		JSONRPC: jsonRPCVersion,
		Method:  "session/update",
		Params:  json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"content","text":"hello world"}}}`),
	}
	line, err := json.Marshal(notif)
	if err != nil {
		b.Fatal(err)
	}
	lineStr := string(line)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		r := strings.NewReader(lineStr + "\n")
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 4096), scannerLineCap)
		for sc.Scan() {
			var resp api.RPCResponse
			if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkReadLoop_Responses measures JSON unmarshal throughput for
// response-shaped messages with varying payload sizes.
func BenchmarkReadLoop_Responses(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"small", 64},
		{"medium", 4096},
		{"large", 65536},
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			payload := strings.Repeat("x", sz.size)
			resp := api.RPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      ptrInt64(42),
				Result:  json.RawMessage(`"` + payload + `"`),
			}
			line, err := json.Marshal(resp)
			if err != nil {
				b.Fatal(err)
			}
			// Build N lines for the benchmark.
			var sb strings.Builder
			for range 100 {
				sb.Write(line)
				sb.WriteByte('\n')
			}
			data := sb.String()

			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				r := strings.NewReader(data)
				sc := bufio.NewScanner(r)
				sc.Buffer(make([]byte, 0, 4096), scannerLineCap)
				for sc.Scan() {
					var resp api.RPCResponse
					if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }
