package engine

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/moraxyc/cpa-reductant/internal/config"
)

func buildBenchPayload(turns int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"claude-sonnet-4","messages":[`)
	for i := range turns {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"user","content":"Turn %d: check SYNTHETIC-BENCH-SECRET-%d and contact user%d%s with token %s%04d."}`,
			i, i%5, i%3, "@contoso.io", "sk-ant-api03-abcdefghijklmnopqrstuvwx", i)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkMaskJSONSmall(b *testing.B) {
	eng := New(&config.Config{
		Words: []string{"SYNTHETIC-BENCH-SECRET-0", "SYNTHETIC-BENCH-SECRET-1"},
	})
	defer eng.Close()
	payload := buildBenchPayload(2)
	meta := RequestMeta{Protocol: config.ProtocolClaude, Model: "claude-sonnet-4"}

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := eng.MaskRequest(context.Background(), meta, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMaskJSONLarge(b *testing.B) {
	eng := New(&config.Config{
		Words: []string{"SYNTHETIC-BENCH-SECRET-0", "SYNTHETIC-BENCH-SECRET-1", "SYNTHETIC-BENCH-SECRET-2"},
	})
	defer eng.Close()
	payload := buildBenchPayload(250) // ~45-60 KB multi-turn conversation
	meta := RequestMeta{Protocol: config.ProtocolClaude, Model: "claude-sonnet-4"}

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := eng.MaskRequest(context.Background(), meta, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRestoreJSON(b *testing.B) {
	eng := New(&config.Config{
		Words: []string{"SYNTHETIC-BENCH-SECRET-0", "SYNTHETIC-BENCH-SECRET-1"},
	})
	defer eng.Close()
	payload := buildBenchPayload(10)
	meta := RequestMeta{Protocol: config.ProtocolClaude, Model: "claude-sonnet-4"}
	masked, err := eng.MaskRequest(context.Background(), meta, payload)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(masked)))
	b.ResetTimer()
	for b.Loop() {
		eng.RestoreResponseWithMeta(meta, "application/json", masked)
	}
}

func BenchmarkSSEStreamRestoreSplit(b *testing.B) {
	eng := New(&config.Config{
		Words: []string{"SYNTHETIC-BENCH-SSE-SECRET"},
	})
	defer eng.Close()
	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-sonnet-4",
	}, []byte(`{"text":"SYNTHETIC-BENCH-SSE-SECRET"}`))
	if err != nil {
		b.Fatal(err)
	}
	ph := termPlaceholderRE.FindString(string(masked))
	if ph == "" {
		b.Fatalf("placeholder not found in %s", masked)
	}
	mid := len(ph) / 2
	chunks := [][]byte{
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"prefix " + ph[:mid] + "\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + ph[mid:] + " suffix\"}}\n\n"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
	}
	totalBytes := 0
	for _, c := range chunks {
		totalBytes += len(c)
	}

	b.ReportAllocs()
	b.SetBytes(int64(totalBytes))
	b.ResetTimer()
	for b.Loop() {
		buf := &restoreBuffer{header: http.Header{"Content-Type": {"text/event-stream"}}}
		w := eng.NewStreamWriter(buf)
		for _, c := range chunks {
			_, _ = w.Write(c)
		}
		w.Finish()
	}
}

func BenchmarkMaskAndRestoreParallelScoped(b *testing.B) {
	eng := New(&config.Config{
		Words:          []string{"SYNTHETIC-BENCH-SECRET-0", "SYNTHETIC-BENCH-SECRET-1"},
		ScopeIsolation: true,
	})
	defer eng.Close()
	payload := buildBenchPayload(4)

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		meta := RequestMeta{
			Protocol:    config.ProtocolOpenAI,
			Model:       "gpt-5",
			CallerScope: "bench-tenant",
		}
		for pb.Next() {
			masked, err := eng.MaskRequest(context.Background(), meta, payload)
			if err != nil {
				b.Error(err)
				return
			}
			eng.RestoreResponseWithMeta(meta, "application/json", masked)
		}
	})
}

func BenchmarkPrometheusMetricsRender(b *testing.B) {
	eng := New(&config.Config{Words: []string{"SYNTHETIC-BENCH-SECRET-0"}})
	defer eng.Close()
	_, _ = eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-sonnet-4",
	}, buildBenchPayload(2))

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = eng.PrometheusMetrics()
	}
}
