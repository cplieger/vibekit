package checkpoint

import (
	"context"
	"testing"
)

func BenchmarkEventLogAppend(b *testing.B) {
	dir := b.TempDir()
	el := newEventLog(dir, "bench-chat")

	ctx := context.Background()
	evt := &event{Kind: kindSnapshot, TS: 1700000000000, Tag: "t-1", V: currentEventVersion}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := el.Append(ctx, evt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventLogAppend_Burst(b *testing.B) {
	dir := b.TempDir()
	el := newEventLog(dir, "bench-chat")

	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		evt := &event{Kind: kindSnapshot, TS: int64(1700000000000 + i), Tag: "t-1", V: currentEventVersion}
		if err := el.Append(ctx, evt); err != nil {
			b.Fatal(err)
		}
	}
}
