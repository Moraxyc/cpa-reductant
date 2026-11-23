package adapter

import (
	"encoding/json"
	"testing"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func BenchmarkStreamPayloadNormalizer(b *testing.B) {
	lineChunk := []byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hello world"}}]}`)
	bareJSON := []byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"hello world"}}]}`)

	b.Run("sse-line", func(b *testing.B) {
		n := streamPayloadNormalizer{exitProtocol: config.ProtocolOpenAI, contentType: "text/event-stream"}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			_ = n.Normalize(lineChunk)
		}
	})

	b.Run("bare-json-wrap", func(b *testing.B) {
		n := streamPayloadNormalizer{exitProtocol: config.ProtocolOpenAI, contentType: "text/event-stream"}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			_ = n.Normalize(bareJSON)
		}
	})
}

func BenchmarkModelRouteGlob(b *testing.B) {
	cfg, err := config.Parse([]byte(`
models: ["claude-*", "gpt-*", "gemini-*"]
exclude_models: ["ollama/*", "claude-haiku-*"]
`))
	if err != nil {
		b.Fatal(err)
	}
	p := NewPlugin(engine.New(cfg), &fakeHost{})
	defer p.Shutdown()

	reqBytes, err := json.Marshal(routeRequest{
		SourceFormat:   config.ProtocolClaude,
		RequestedModel: "claude-sonnet-4",
		Stream:         true,
		Body:           []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := p.Dispatch(pluginabi.MethodModelRoute, reqBytes); err != nil {
			b.Fatal(err)
		}
	}
}
