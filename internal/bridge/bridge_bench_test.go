package bridge

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// BenchmarkReadLoop_Notifications measures JSON unmarshal throughput for
// notification-shaped messages (the hot path in readLoop).
func BenchmarkReadLoop_Notifications(b *testing.B) {
	notif := vibekit.RPCResponse{
		JSONRPC: jsonRPCVersion,
		Method:  "session/update",
		Params:  json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"content","text":"hello world"}}}`),
	}
	line, err := json.Marshal(notif)
	if err != nil {
		b.Fatal(err)
	}
	lineStr := string(line)

	b.ReportAllocs()
	for b.Loop() {
		fr := newFrameReader(bufio.NewReaderSize(strings.NewReader(lineStr+"\n"), stdoutBufSize))
		for {
			line, _, rerr := fr.readFrame()
			if rerr != nil {
				break
			}
			var resp vibekit.RPCResponse
			if err := json.Unmarshal(line, &resp); err != nil {
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
			resp := vibekit.RPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      new(int64(42)),
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

			b.ReportAllocs()
			for b.Loop() {
				fr := newFrameReader(bufio.NewReaderSize(strings.NewReader(data), stdoutBufSize))
				for {
					line, _, rerr := fr.readFrame()
					if rerr != nil {
						break
					}
					var resp vibekit.RPCResponse
					if err := json.Unmarshal(line, &resp); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
