package adapter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type hostCall struct {
	method  string
	payload []byte
}

type fakeHost struct {
	mu      sync.Mutex
	calls   []hostCall
	handler func(method string, payload []byte, reply any) error
	closed  chan struct{}
}

func newFakeHost(handler func(method string, payload []byte, reply any) error) *fakeHost {
	return &fakeHost{
		handler: handler,
		closed:  make(chan struct{}),
	}
}

func (f *fakeHost) Call(method string, request, reply any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.calls = append(f.calls, hostCall{method: method, payload: raw})
	f.mu.Unlock()

	if f.handler != nil && method != pluginabi.MethodHostLog {
		if err := f.handler(method, raw, reply); err != nil {
			return err
		}
	}
	if method == pluginabi.MethodHostStreamClose {
		var u streamUpdate
		if json.Unmarshal(raw, &u) == nil && u.StreamID == "out-1" {
			select {
			case <-f.closed:
			default:
				close(f.closed)
			}
		}
	}
	return nil
}

func (f *fakeHost) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method != pluginabi.MethodHostLog {
			n++
		}
	}
	return n
}

func (f *fakeHost) logCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == pluginabi.MethodHostLog {
			n++
		}
	}
	return n
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestModelRouteAndExecuteRestore(t *testing.T) {
	term := "SYNTHETIC-TEST-PROJECT-ALPHA"
	var upstreamSeen string
	host := newFakeHost(func(method string, payload []byte, reply any) error {
		if method != pluginabi.MethodHostModelExecute {
			t.Fatalf("unexpected host method %s", method)
		}
		var req pluginapi.HostModelExecutionRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return err
		}
		upstreamSeen = string(req.Body)
		if strings.Contains(upstreamSeen, term) {
			t.Fatalf("upstream saw unmasked term: %s", upstreamSeen)
		}
		var msg map[string]any
		if err := json.Unmarshal(req.Body, &msg); err != nil {
			return err
		}
		msgs := msg["messages"].([]any)
		content := msgs[0].(map[string]any)["content"].(string)

		resp := pluginapi.HostModelExecutionResponse{
			Body:    mustJSON(t, map[string]any{"content": []map[string]any{{"type": "text", "text": content}}}),
			Headers: http.Header{"Content-Length": {"999"}, "X-Test": {"ok"}},
		}
		*reply.(*pluginapi.HostModelExecutionResponse) = resp
		return nil
	})

	p := NewPlugin(engine.New(nil), host)
	_, err := p.Dispatch(pluginabi.MethodPluginRegister, mustJSON(t, map[string]any{
		"config_yaml": []byte("models: [claude-sonnet-test]\nwords: [" + term + "]\n"),
	}))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	routeSelected, err := p.Dispatch(pluginabi.MethodModelRoute, mustJSON(t, map[string]string{
		"SourceFormat":   config.DefaultProtocol,
		"RequestedModel": "claude-sonnet-test",
	}))
	if err != nil || !routeSelected.(pluginapi.ModelRouteResponse).Handled {
		t.Fatalf("expected selected model to be handled, got %#v, err=%v", routeSelected, err)
	}

	routeSkipped, err := p.Dispatch(pluginabi.MethodModelRoute, mustJSON(t, map[string]string{
		"SourceFormat":   config.DefaultProtocol,
		"RequestedModel": "claude-haiku-other",
	}))
	if err != nil || routeSkipped.(pluginapi.ModelRouteResponse).Handled {
		t.Fatalf("expected unselected model to pass through, got %#v, err=%v", routeSkipped, err)
	}

	rawReq := mustJSON(t, map[string]any{
		"model":    "claude-sonnet-test",
		"messages": []map[string]any{{"role": "user", "content": term}},
	})
	execResp, err := p.Dispatch(pluginabi.MethodExecutorExecute, mustJSON(t, executorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "claude-sonnet-test",
			OriginalRequest: rawReq,
		},
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := execResp.(pluginapi.ExecutorResponse)
	if !strings.Contains(string(res.Payload), term) {
		t.Fatalf("expected restored response to contain %q, got %s", term, res.Payload)
	}
	if res.Headers.Get("Content-Length") != "" {
		t.Fatalf("expected Content-Length to be stripped, got %q", res.Headers.Get("Content-Length"))
	}
	if host.count() != 1 {
		t.Fatalf("expected 1 host call, got %d", host.count())
	}
}

func TestBlockTermsRejectsWithoutUpstreamCall(t *testing.T) {
	term := "SYNTHETIC-BLOCKED-WORD"
	host := newFakeHost(nil)
	p := NewPlugin(engine.New(&config.Config{
		Models:     []string{"claude-sonnet-test"},
		Words:      []string{term},
		BlockTerms: true,
	}), host)

	rawReq := mustJSON(t, map[string]any{
		"model":    "claude-sonnet-test",
		"messages": []map[string]any{{"role": "user", "content": term}},
	})
	for _, method := range []string{pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream} {
		_, err := p.Dispatch(method, mustJSON(t, executorRequest{
			ExecutorRequest: pluginapi.ExecutorRequest{
				Model:           "claude-sonnet-test",
				OriginalRequest: rawReq,
			},
			StreamID: "out-1",
		}))
		abiErr, ok := err.(*pluginabi.Error)
		if !ok || abiErr.StatusCode() != http.StatusBadRequest || abiErr.Code != "redaction_blocked" {
			t.Fatalf("expected 400 redaction_blocked on %s, got %#v", method, err)
		}
	}
	if host.count() != 0 {
		t.Fatalf("expected 0 upstream host calls on blocked requests, got %d", host.count())
	}
	if host.logCount() != 2 {
		t.Fatalf("expected 2 host.log calls on blocked requests, got %d", host.logCount())
	}
}

func TestStreamRestoreSplitPlaceholder(t *testing.T) {
	term := "SYNTHETIC-STREAM-SECRET"
	var chunks [][]byte
	var emitted strings.Builder
	readIdx := 0

	host := newFakeHost(func(method string, payload []byte, reply any) error {
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			var req pluginapi.HostModelExecutionRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				return err
			}
			var msg map[string]any
			if err := json.Unmarshal(req.Body, &msg); err != nil {
				return err
			}
			placeholder := msg["messages"].([]any)[0].(map[string]any)["content"].(string)
			mid := len(placeholder) / 2
			chunks = [][]byte{
				[]byte("event: content_block_delta\ndata: " + string(mustJSON(t, map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": placeholder[:mid]},
				})) + "\n\n"),
				[]byte("event: content_block_delta\ndata: " + string(mustJSON(t, map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": placeholder[mid:]},
				})) + "\n\n"),
				[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			}
			*reply.(*pluginapi.HostModelStreamResponse) = pluginapi.HostModelStreamResponse{StreamID: "up-1"}
		case pluginabi.MethodHostModelStreamRead:
			out := reply.(*pluginapi.HostModelStreamReadResponse)
			if readIdx < len(chunks) {
				out.Payload = chunks[readIdx]
				readIdx++
			}
			if readIdx == len(chunks) {
				out.Done = true
			}
		case pluginabi.MethodHostStreamEmit:
			var u streamUpdate
			if err := json.Unmarshal(payload, &u); err != nil {
				return err
			}
			emitted.Write(u.Payload)
		}
		return nil
	})

	p := NewPlugin(engine.New(&config.Config{
		Words: []string{term},
	}), host)

	rawReq := mustJSON(t, map[string]any{
		"model":    "claude-sonnet-test",
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": term}},
	})
	_, err := p.Dispatch(pluginabi.MethodExecutorExecuteStream, mustJSON(t, executorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "claude-sonnet-test",
			Stream:          true,
			OriginalRequest: rawReq,
		},
		StreamID: "out-1",
	}))
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}

	select {
	case <-host.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream close")
	}

	got := emitted.String()
	if !strings.Contains(got, term) || !strings.Contains(got, "message_stop") {
		t.Fatalf("expected restored term and message_stop in stream, got:\n%s", got)
	}
	st := p.engine.Status()
	if st.Stats.StreamsTotal != 1 || st.Stats.StreamsRestored != 1 || st.Stats.PlaceholdersRestored < 1 {
		t.Fatalf("expected stream restore telemetry to record restored stream, got %+v", st.Stats)
	}
}

func TestModelRouteSupportedAndSkippedCases(t *testing.T) {
	p := NewPlugin(engine.New(nil), newFakeHost(nil))
	reg, err := p.Dispatch(pluginabi.MethodPluginRegister, mustJSON(t, map[string]any{
		"config_yaml": []byte("words: [SYNTHETIC-TEST]\n"),
	}))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	caps := reg.(map[string]any)["capabilities"].(map[string]any)
	inputs := caps["executor_input_formats"].([]string)
	outputs := caps["executor_output_formats"].([]string)
	if len(inputs) != len(config.SupportedProtocols()) || len(outputs) != len(config.SupportedProtocols()) {
		t.Fatalf("unexpected formats: in=%v out=%v", inputs, outputs)
	}

	for _, format := range config.SupportedProtocols() {
		resp, err := p.Dispatch(pluginabi.MethodModelRoute, mustJSON(t, routeRequest{
			SourceFormat:   format,
			RequestedModel: "any-model",
			Body:           []byte(`{"model":"any-model"}`),
		}))
		if err != nil || !resp.(pluginapi.ModelRouteResponse).Handled {
			t.Fatalf("expected %q to be handled, got %#v, err=%v", format, resp, err)
		}
	}

	skipCases := []struct {
		name string
		req  routeRequest
	}{
		{
			name: "claude count_tokens",
			req: routeRequest{
				SourceFormat:   config.ProtocolClaude,
				RequestedModel: "claude-sonnet-test",
				Body:           []byte(`{"model":"claude-sonnet-test","messages":[]}`),
				Metadata:       map[string]any{requestPathMetadataKey: "/v1/messages/count_tokens"},
			},
		},
		{
			name: "interactions agent",
			req: routeRequest{
				SourceFormat:   config.ProtocolInteractions,
				RequestedModel: "deep-research-agent",
				Body:           []byte(`{"agent":"deep-research-agent","input":"hi"}`),
			},
		},
		{
			name: "gemini generateContentRequest",
			req: routeRequest{
				SourceFormat:   config.ProtocolGemini,
				RequestedModel: "gemini-2.5-flash",
				Body:           []byte(`{"generateContentRequest":{"contents":[]}}`),
			},
		},
		{
			name: "codex alpha search",
			req: routeRequest{
				SourceFormat:   "codex-alpha-search",
				RequestedModel: "gpt-5-codex",
				Body:           []byte(`{"model":"gpt-5-codex"}`),
			},
		},
	}

	for _, tc := range skipCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := p.Dispatch(pluginabi.MethodModelRoute, mustJSON(t, tc.req))
			if err != nil {
				t.Fatalf("route: %v", err)
			}
			if resp.(pluginapi.ModelRouteResponse).Handled {
				t.Fatalf("expected %s to pass through unhandled", tc.name)
			}
		})
	}
}

func TestExecuteNonStreamAcrossProtocols(t *testing.T) {
	term := "SYNTHETIC-MULTI-PROTO-TERM"
	cases := []struct {
		name       string
		format     string
		reqPath    string
		reqBody    map[string]any
		respHeader http.Header
		makeResp   func(maskedBody []byte) []byte
		verify     func(t *testing.T, resp []byte)
	}{
		{
			name:   "openai chat",
			format: config.ProtocolOpenAI,
			reqBody: map[string]any{
				"model":    "gpt-test",
				"messages": []map[string]any{{"role": "user", "content": term}},
			},
			makeResp: func(maskedBody []byte) []byte {
				var m map[string]any
				_ = json.Unmarshal(maskedBody, &m)
				p := m["messages"].([]any)[0].(map[string]any)["content"].(string)
				return mustJSON(t, map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": p}}}})
			},
			verify: func(t *testing.T, resp []byte) {
				if !strings.Contains(string(resp), term) {
					t.Fatalf("expected %q in %s", term, resp)
				}
			},
		},
		{
			name:   "openai-response",
			format: config.ProtocolOpenAIResponse,
			reqBody: map[string]any{
				"model": "codex-test",
				"input": term,
			},
			makeResp: func(maskedBody []byte) []byte {
				var m map[string]any
				_ = json.Unmarshal(maskedBody, &m)
				p := m["input"].(string)
				return mustJSON(t, map[string]any{"output": []map[string]any{{"type": "message", "content": []map[string]any{{"type": "output_text", "text": p}}}}})
			},
			verify: func(t *testing.T, resp []byte) {
				if !strings.Contains(string(resp), term) {
					t.Fatalf("expected %q in %s", term, resp)
				}
			},
		},
		{
			name:   "gemini",
			format: config.ProtocolGemini,
			reqBody: map[string]any{
				"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": term}}}},
			},
			makeResp: func(maskedBody []byte) []byte {
				var m map[string]any
				_ = json.Unmarshal(maskedBody, &m)
				p := m["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
				return mustJSON(t, map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{{"text": p}}}}}})
			},
			verify: func(t *testing.T, resp []byte) {
				if !strings.Contains(string(resp), term) {
					t.Fatalf("expected %q in %s", term, resp)
				}
			},
		},
		{
			name:   "interactions",
			format: config.ProtocolInteractions,
			reqBody: map[string]any{
				"model": "gemini-2.5-flash",
				"input": term,
			},
			makeResp: func(maskedBody []byte) []byte {
				var m map[string]any
				_ = json.Unmarshal(maskedBody, &m)
				p := m["input"].(string)
				return mustJSON(t, map[string]any{"steps": []map[string]any{{"type": "model", "content": []map[string]any{{"text": p}}}}})
			},
			verify: func(t *testing.T, resp []byte) {
				if !strings.Contains(string(resp), term) {
					t.Fatalf("expected %q in %s", term, resp)
				}
			},
		},
		{
			name:    "openai-image edits path forwarded",
			format:  config.ProtocolOpenAIImage,
			reqPath: "/v1/images/edits",
			reqBody: map[string]any{
				"model":  "grok-imagine-image",
				"prompt": term,
			},
			makeResp: func(maskedBody []byte) []byte {
				var m map[string]any
				_ = json.Unmarshal(maskedBody, &m)
				p := m["prompt"].(string)
				return mustJSON(t, map[string]any{"data": []map[string]any{{"url": "https://example.com/1.png", "revised_prompt": p}}})
			},
			verify: func(t *testing.T, resp []byte) {
				if !strings.Contains(string(resp), term) {
					t.Fatalf("expected %q in %s", term, resp)
				}
			},
		},
		{
			name:   "openai-speech binary preserved",
			format: config.ProtocolOpenAISpeech,
			reqBody: map[string]any{
				"text":     term,
				"voice_id": "eve",
			},
			respHeader: http.Header{"Content-Type": {"audio/mpeg"}, "Content-Length": {"4"}},
			makeResp: func([]byte) []byte {
				return []byte{0xff, 0xfb, 0x90, 0x64}
			},
			verify: func(t *testing.T, resp []byte) {
				if !bytes.Equal(resp, []byte{0xff, 0xfb, 0x90, 0x64}) {
					t.Fatalf("expected binary audio unchanged, got %x", resp)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq pluginapi.HostModelExecutionRequest
			host := newFakeHost(func(method string, payload []byte, reply any) error {
				if method != pluginabi.MethodHostModelExecute {
					t.Fatalf("unexpected method %s", method)
				}
				if err := json.Unmarshal(payload, &gotReq); err != nil {
					return err
				}
				if strings.Contains(string(gotReq.Body), term) {
					t.Fatalf("upstream saw unmasked term: %s", gotReq.Body)
				}
				hdr := tc.respHeader
				if hdr == nil {
					hdr = http.Header{"Content-Type": {"application/json"}}
				}
				*reply.(*pluginapi.HostModelExecutionResponse) = pluginapi.HostModelExecutionResponse{
					Body:    tc.makeResp(gotReq.Body),
					Headers: hdr,
				}
				return nil
			})
			p := NewPlugin(engine.New(&config.Config{Words: []string{term}}), host)

			var meta map[string]any
			if tc.reqPath != "" {
				meta = map[string]any{requestPathMetadataKey: tc.reqPath}
			}
			execResp, err := p.Dispatch(pluginabi.MethodExecutorExecute, mustJSON(t, executorRequest{
				ExecutorRequest: pluginapi.ExecutorRequest{
					Model:           "test-model",
					Format:          tc.format,
					SourceFormat:    tc.format,
					OriginalRequest: mustJSON(t, tc.reqBody),
					Metadata:        meta,
				},
			}))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if gotReq.EntryProtocol != tc.format || gotReq.ExitProtocol != tc.format {
				t.Fatalf("protocols = (%q, %q), want (%q, %q)", gotReq.EntryProtocol, gotReq.ExitProtocol, tc.format, tc.format)
			}
			if gotReq.Path != tc.reqPath {
				t.Fatalf("path = %q, want %q", gotReq.Path, tc.reqPath)
			}
			tc.verify(t, execResp.(pluginapi.ExecutorResponse).Payload)
		})
	}
}

func TestStreamRestoreBareJSONAndResponsesSplit(t *testing.T) {
	term := "SYNTHETIC-QUOTE-\"SECRET\""
	cases := []struct {
		name         string
		format       string
		reqBody      map[string]any
		buildChunks  func(maskedReq []byte) [][]byte
		wantContains []string
	}{
		{
			name:   "openai bare json split placeholder",
			format: config.ProtocolOpenAI,
			reqBody: map[string]any{
				"model":    "gpt-test",
				"stream":   true,
				"messages": []map[string]any{{"role": "user", "content": term}},
			},
			buildChunks: func(maskedReq []byte) [][]byte {
				var m map[string]any
				_ = json.Unmarshal(maskedReq, &m)
				p := m["messages"].([]any)[0].(map[string]any)["content"].(string)
				mid := len(p) / 2
				return [][]byte{
					mustJSON(t, map[string]any{"id": "c1", "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": p[:mid]}}}}),
					mustJSON(t, map[string]any{"id": "c1", "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": p[mid:]}}}}),
					mustJSON(t, map[string]any{"id": "c1", "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
				}
			},
			wantContains: []string{`data: `, `SYNTHETIC-QUOTE-\"SECRET\"`, `"finish_reason":"stop"`},
		},
		{
			name:   "gemini bare json split placeholder",
			format: config.ProtocolGemini,
			reqBody: map[string]any{
				"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": term}}}},
			},
			buildChunks: func(maskedReq []byte) [][]byte {
				var m map[string]any
				_ = json.Unmarshal(maskedReq, &m)
				p := m["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
				mid := len(p) / 2
				return [][]byte{
					mustJSON(t, map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{{"text": p[:mid]}}}}}}),
					mustJSON(t, map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{{"text": p[mid:]}}}, "finishReason": "STOP"}}}),
				}
			},
			wantContains: []string{`data: `, `SYNTHETIC-QUOTE-\"SECRET\"`, `"finishReason":"STOP"`},
		},
		{
			name:   "openai-response sse split placeholder",
			format: config.ProtocolOpenAIResponse,
			reqBody: map[string]any{
				"model":  "codex-test",
				"stream": true,
				"input":  term,
			},
			buildChunks: func(maskedReq []byte) [][]byte {
				var m map[string]any
				_ = json.Unmarshal(maskedReq, &m)
				p := m["input"].(string)
				mid := len(p) / 2
				return [][]byte{
					[]byte("event: response.output_text.delta\ndata: " + string(mustJSON(t, map[string]any{
						"type":          "response.output_text.delta",
						"item_id":       "msg_1",
						"output_index":  0,
						"content_index": 0,
						"delta":         p[:mid],
					})) + "\n\n"),
					[]byte("event: response.output_text.delta\ndata: " + string(mustJSON(t, map[string]any{
						"type":          "response.output_text.delta",
						"item_id":       "msg_1",
						"output_index":  0,
						"content_index": 0,
						"delta":         p[mid:],
					})) + "\n\n"),
					[]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"),
				}
			},
			wantContains: []string{`SYNTHETIC-QUOTE-\"SECRET\"`, `response.completed`},
		},
		{
			name:   "openai-response line-by-line sse without newlines",
			format: config.ProtocolOpenAIResponse,
			reqBody: map[string]any{
				"model":  "codex-test",
				"stream": true,
				"input":  term,
			},
			buildChunks: func(maskedReq []byte) [][]byte {
				var m map[string]any
				_ = json.Unmarshal(maskedReq, &m)
				p := m["input"].(string)
				mid := len(p) / 2
				return [][]byte{
					[]byte("event: response.output_text.delta"),
					[]byte("data: " + string(mustJSON(t, map[string]any{
						"type":          "response.output_text.delta",
						"item_id":       "msg_1",
						"output_index":  0,
						"content_index": 0,
						"delta":         p[:mid],
					}))),
					[]byte("event: response.output_text.delta"),
					[]byte("data: " + string(mustJSON(t, map[string]any{
						"type":          "response.output_text.delta",
						"item_id":       "msg_1",
						"output_index":  0,
						"content_index": 0,
						"delta":         p[mid:],
					}))),
					[]byte("event: response.completed"),
					[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
				}
			},
			wantContains: []string{`SYNTHETIC-QUOTE-\"SECRET\"`, `response.completed`},
		},
		{
			name:   "claude partial json split across chunks",
			format: config.ProtocolClaude,
			reqBody: map[string]any{
				"model":    "claude-sonnet-test",
				"stream":   true,
				"messages": []map[string]any{{"role": "user", "content": term}},
			},
			buildChunks: func(maskedReq []byte) [][]byte {
				var m map[string]any
				_ = json.Unmarshal(maskedReq, &m)
				p := m["messages"].([]any)[0].(map[string]any)["content"].(string)
				ev := "event: content_block_delta\ndata: " + string(mustJSON(t, map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": p},
				})) + "\n\n"
				split := len("event: content_block_delta\ndata: {\"type\":")
				return [][]byte{
					[]byte(ev[:split]),
					[]byte(ev[split:]),
					[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
				}
			},
			wantContains: []string{`SYNTHETIC-QUOTE-\"SECRET\"`, `message_stop`},
		},
		{
			name:   "interactions bare json split placeholder",
			format: config.ProtocolInteractions,
			reqBody: map[string]any{
				"model":  "gemini-2.5-flash",
				"stream": true,
				"input":  term,
			},
			buildChunks: func(maskedReq []byte) [][]byte {
				var m map[string]any
				_ = json.Unmarshal(maskedReq, &m)
				p := m["input"].(string)
				mid := len(p) / 2
				return [][]byte{
					mustJSON(t, map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"text": p[:mid]}}),
					mustJSON(t, map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"text": p[mid:]}}),
					mustJSON(t, map[string]any{"event_type": "interaction.completed"}),
				}
			},
			wantContains: []string{`SYNTHETIC-QUOTE-\"SECRET\"`, `interaction.completed`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var chunks [][]byte
			var emitted strings.Builder
			readIdx := 0

			host := newFakeHost(func(method string, payload []byte, reply any) error {
				switch method {
				case pluginabi.MethodHostModelExecuteStream:
					var req pluginapi.HostModelExecutionRequest
					if err := json.Unmarshal(payload, &req); err != nil {
						return err
					}
					chunks = tc.buildChunks(req.Body)
					*reply.(*pluginapi.HostModelStreamResponse) = pluginapi.HostModelStreamResponse{StreamID: "up-1"}
				case pluginabi.MethodHostModelStreamRead:
					out := reply.(*pluginapi.HostModelStreamReadResponse)
					if readIdx < len(chunks) {
						out.Payload = chunks[readIdx]
						readIdx++
					}
					if readIdx == len(chunks) {
						out.Done = true
					}
				case pluginabi.MethodHostStreamEmit:
					var u streamUpdate
					if err := json.Unmarshal(payload, &u); err != nil {
						return err
					}
					emitted.Write(u.Payload)
				}
				return nil
			})

			p := NewPlugin(engine.New(&config.Config{Words: []string{term}}), host)
			_, err := p.Dispatch(pluginabi.MethodExecutorExecuteStream, mustJSON(t, executorRequest{
				ExecutorRequest: pluginapi.ExecutorRequest{
					Model:           "test-model",
					Format:          tc.format,
					SourceFormat:    tc.format,
					Stream:          true,
					OriginalRequest: mustJSON(t, tc.reqBody),
				},
				StreamID: "out-1",
			}))
			if err != nil {
				t.Fatalf("execute stream: %v", err)
			}

			select {
			case <-host.closed:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for stream close")
			}

			got := emitted.String()
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("expected %q in stream output:\n%s", want, got)
				}
			}
		})
	}
}

func TestStreamExecutePropagatesInitialHostError(t *testing.T) {
	wantErr := pluginabi.NewError("rate_limit", "upstream limited", http.StatusTooManyRequests)
	host := newFakeHost(func(method string, payload []byte, reply any) error {
		if method == pluginabi.MethodHostModelExecuteStream {
			return wantErr
		}
		return nil
	})
	p := NewPlugin(engine.New(&config.Config{Words: []string{"SECRET"}}), host)
	_, err := p.Dispatch(pluginabi.MethodExecutorExecuteStream, mustJSON(t, executorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "test-model",
			Format:          config.ProtocolOpenAI,
			SourceFormat:    config.ProtocolOpenAI,
			Stream:          true,
			OriginalRequest: []byte(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		},
		StreamID: "out-1",
	}))
	abiErr, ok := err.(*pluginabi.Error)
	if !ok || abiErr.StatusCode() != http.StatusTooManyRequests || abiErr.Code != "rate_limit" {
		t.Fatalf("expected 429 rate_limit error, got %#v", err)
	}
}

func TestModelUnavailableStrictReturns503WithoutUpstream(t *testing.T) {
	host := newFakeHost(nil)
	p := NewPlugin(engine.New(nil), host)
	_, err := p.Dispatch(pluginabi.MethodPluginRegister, mustJSON(t, map[string]any{
		"config_yaml": []byte("model:\n  enabled: true\n  strict: true\n  dir: " + t.TempDir() + "\n"),
	}))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err = p.Dispatch(pluginabi.MethodExecutorExecute, mustJSON(t, executorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "test-model",
			Format:          config.ProtocolOpenAI,
			SourceFormat:    config.ProtocolOpenAI,
			OriginalRequest: []byte(`{"model":"test-model","messages":[{"role":"user","content":"Contact Alice Smith"}]}`),
		},
	}))
	abiErr, ok := err.(*pluginabi.Error)
	if !ok || abiErr.StatusCode() != http.StatusServiceUnavailable || abiErr.Code != pluginabi.ErrorCodeRequestScoped {
		t.Fatalf("expected 503 request_scoped error, got %#v", err)
	}
	if host.count() != 0 {
		t.Fatalf("expected 0 upstream calls when strict model unavailable, got %d", host.count())
	}
	if host.logCount() != 1 {
		t.Fatalf("expected 1 host.log call on strict unavailable, got %d", host.logCount())
	}
	if _, err := p.Dispatch(pluginabi.MethodPluginShutdown, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestManagementStatusAndPreview(t *testing.T) {
	host := newFakeHost(nil)
	p := NewPlugin(engine.New(nil), host)
	regResp, err := p.Dispatch(pluginabi.MethodPluginRegister, mustJSON(t, map[string]any{
		"config_yaml": []byte("words: [SYNTHETIC-MGMT-SECRET]\nactions:\n  EMAIL: warn\n"),
	}))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	caps := regResp.(map[string]any)["capabilities"].(map[string]any)
	if caps["management_api"] != true {
		t.Fatalf("expected management_api=true capability, got %#v", caps)
	}

	routesResp, err := p.Dispatch(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register: %v", err)
	}
	mgmtReg := routesResp.(pluginapi.ManagementRegistrationResponse)
	if len(mgmtReg.Routes) != 6 || len(mgmtReg.Resources) != 1 {
		t.Fatalf("expected 6 authenticated management routes and 1 resource portal route, got routes=%#v resources=%#v", mgmtReg.Routes, mgmtReg.Resources)
	}
	for _, r := range mgmtReg.Routes {
		if r.Menu != "" {
			t.Fatalf("expected no unauthenticated Menu on management routes, got %#v", r)
		}
	}
	if mgmtReg.Resources[0].Path != "/ui" || mgmtReg.Resources[0].Menu != config.PluginID {
		t.Fatalf("unexpected resource menu entry: %#v", mgmtReg.Resources[0])
	}

	// Trigger one mask + restore so stats, dimensions, and recent events are populated.
	masked, err := p.engine.MaskRequest(t.Context(), engine.RequestMeta{
		Route:    "claude-sonnet-test",
		Protocol: config.ProtocolClaude,
		Model:    "claude-sonnet-test",
	}, []byte(`{"text":"secret SYNTHETIC-MGMT-SECRET and mail bob@acme.io"}`))
	if err != nil {
		t.Fatalf("MaskRequest: %v", err)
	}
	if _, n := p.engine.RestoreResponseWithMeta(engine.RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-sonnet-test",
	}, "application/json", masked); n != 1 {
		t.Fatalf("expected 1 restored placeholder, got %d", n)
	}

	previewRespRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/cpa-reductant/preview",
		Body:   []byte(`{"text":"SYNTHETIC-MGMT-SECRET"}`),
	}))
	if err != nil {
		t.Fatalf("management preview: %v", err)
	}
	previewResp := previewRespRaw.(pluginapi.ManagementResponse)
	if previewResp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, body=%s", previewResp.StatusCode, previewResp.Body)
	}
	var previewBody engine.PreviewResult
	if err := json.Unmarshal(previewResp.Body, &previewBody); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if strings.Contains(previewBody.Masked, "SYNTHETIC-MGMT-SECRET") || len(previewBody.Hits) != 1 {
		t.Fatalf("unexpected preview body: %+v", previewBody)
	}

	statusRespRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/cpa-reductant/status",
	}))
	if err != nil {
		t.Fatalf("management status: %v", err)
	}
	statusResp := statusRespRaw.(pluginapi.ManagementResponse)
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", statusResp.StatusCode, statusResp.Body)
	}
	var st engine.Status
	if err := json.Unmarshal(statusResp.Body, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.Policy.WordsCount != 1 || st.Stats.RequestsTotal != 1 || st.Stats.HitsWarned != 1 || st.Stats.PlaceholdersRestored != 1 || len(st.RecentEvents) != 2 {
		t.Fatalf("unexpected management status: %+v", st)
	}

	// Machine status supports ?events=0 to omit event feed.
	compactRespRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/cpa-reductant/status",
		Query:  url.Values{"events": {"0"}},
	}))
	if err != nil {
		t.Fatalf("management compact status: %v", err)
	}
	var compactSt engine.Status
	if err := json.Unmarshal(compactRespRaw.(pluginapi.ManagementResponse).Body, &compactSt); err != nil {
		t.Fatalf("decode compact status: %v", err)
	}
	if len(compactSt.RecentEvents) != 0 {
		t.Fatalf("expected 0 events with ?events=0, got %d", len(compactSt.RecentEvents))
	}

	// Resource UI portal loads without leaking any runtime status, model names, or secrets.
	portalRespRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-reductant/ui",
	}))
	if err != nil {
		t.Fatalf("resource ui portal: %v", err)
	}
	portalResp := portalRespRaw.(pluginapi.ManagementResponse)
	if portalResp.StatusCode != http.StatusOK || !strings.Contains(portalResp.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("expected 200 text/html for resource portal, got status=%d headers=%v", portalResp.StatusCode, portalResp.Headers)
	}
	portalBody := string(portalResp.Body)
	if !strings.Contains(portalBody, "auth-form") || !strings.Contains(portalBody, "/v0/management/cpa-reductant/status") || !strings.Contains(portalBody, "body.embedded") {
		t.Fatalf("expected login form, status API endpoint, and embedded layout rule in:\n%s", portalBody)
	}
	for _, forbidden := range []string{"claude-sonnet-test", "SYNTHETIC-MGMT-SECRET", "bob@acme.io", "TERM_"} {
		if strings.Contains(portalBody, forbidden) {
			t.Fatalf("resource portal must not leak runtime/config data %q", forbidden)
		}
	}

	// Visual HTML dashboard works via ?format=html, Accept: text/html, and /ui without leaking plaintext.
	for _, req := range []pluginapi.ManagementRequest{
		{Method: http.MethodGet, Path: "/v0/management/cpa-reductant/status", Query: url.Values{"format": {"html"}}},
		{Method: http.MethodGet, Path: "/v0/management/cpa-reductant/status", Headers: http.Header{"Accept": {"text/html"}}},
		{Method: http.MethodGet, Path: "/v0/management/cpa-reductant/ui", Query: url.Values{"action": {"mask"}}},
	} {
		htmlRespRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, req))
		if err != nil {
			t.Fatalf("management HTML %s: %v", req.Path, err)
		}
		htmlResp := htmlRespRaw.(pluginapi.ManagementResponse)
		if htmlResp.StatusCode != http.StatusOK || !strings.Contains(htmlResp.Headers.Get("Content-Type"), "text/html") {
			t.Fatalf("expected 200 text/html for %s, got status=%d headers=%v", req.Path, htmlResp.StatusCode, htmlResp.Headers)
		}
		htmlBody := string(htmlResp.Body)
		if !strings.Contains(htmlBody, "cpa-reductant") || !strings.Contains(htmlBody, "claude-sonnet-test") || !strings.Contains(htmlBody, "TERM") {
			t.Fatalf("expected dashboard HTML to contain service, model, and kind metrics:\n%s", htmlBody)
		}
		if strings.Contains(htmlBody, "SYNTHETIC-MGMT-SECRET") || strings.Contains(htmlBody, "bob@acme.io") {
			t.Fatalf("dashboard HTML must never leak plaintext secrets or warned emails")
		}
	}

	badReqRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/cpa-reductant/preview",
		Body:   []byte(`{bad`),
	}))
	if err != nil {
		t.Fatalf("management bad preview: %v", err)
	}
	if badReqRaw.(pluginapi.ManagementResponse).StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad preview JSON, got %#v", badReqRaw)
	}

	notFoundRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/cpa-reductant/unknown",
	}))
	if err != nil {
		t.Fatalf("management unknown route: %v", err)
	}
	if notFoundRaw.(pluginapi.ManagementResponse).StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on unknown route, got %#v", notFoundRaw)
	}
}

func TestDebugCaptureManagementAndStreamCorrelation(t *testing.T) {
	term := "SYNTHETIC-DEBUG-CAPTURE-TERM"
	streamChunks := []pluginapi.HostModelStreamReadResponse{}
	chunkIdx := 0

	var host *fakeHost
	host = newFakeHost(func(method string, payload []byte, reply any) error {
		switch method {
		case pluginabi.MethodHostModelExecute:
			var req pluginapi.HostModelExecutionRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				return err
			}
			var body map[string]any
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return err
			}
			msg := body["messages"].([]any)[0].(map[string]any)["content"].(string)
			resp := pluginapi.HostModelExecutionResponse{
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    mustJSON(t, map[string]any{"echo": msg}),
			}
			return json.Unmarshal(mustJSON(t, resp), reply)
		case pluginabi.MethodHostModelExecuteStream:
			var req pluginapi.HostModelExecutionRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				return err
			}
			var body map[string]any
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return err
			}
			msg := body["messages"].([]any)[0].(map[string]any)["content"].(string)
			streamChunks = []pluginapi.HostModelStreamReadResponse{
				{Payload: []byte("data: {\"delta\":" + string(mustJSON(t, msg)) + "}\n\n")},
				{Done: true},
			}
			chunkIdx = 0
			return json.Unmarshal(mustJSON(t, pluginapi.HostModelStreamResponse{
				StreamID: "up-cap-1",
				Headers:  http.Header{"Content-Type": {"text/event-stream"}},
			}), reply)
		case pluginabi.MethodHostModelStreamRead:
			if chunkIdx >= len(streamChunks) {
				return json.Unmarshal(mustJSON(t, pluginapi.HostModelStreamReadResponse{Done: true}), reply)
			}
			c := streamChunks[chunkIdx]
			chunkIdx++
			return json.Unmarshal(mustJSON(t, c), reply)
		case pluginabi.MethodHostStreamEmit, pluginabi.MethodHostStreamClose, pluginabi.MethodHostModelStreamClose:
			return nil
		default:
			t.Fatalf("unexpected host method %s", method)
		}
		return nil
	})

	p := NewPlugin(engine.New(nil), host)
	defer p.Shutdown()

	if _, err := p.Dispatch(pluginabi.MethodPluginRegister, mustJSON(t, map[string]any{
		"config_yaml": []byte("words:\n  - " + term + "\n"),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Enable debug capture via POST.
	enableRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/cpa-reductant/debug/capture",
		Body:   []byte(`{"enabled":true,"ttl_seconds":120}`),
	}))
	if err != nil {
		t.Fatalf("enable debug capture: %v", err)
	}
	enableResp := enableRaw.(pluginapi.ManagementResponse)
	if enableResp.StatusCode != http.StatusOK || enableResp.Headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected enable response: status=%d headers=%v body=%s", enableResp.StatusCode, enableResp.Headers, enableResp.Body)
	}

	// 1. Non-stream execute.
	execRaw, err := p.Dispatch(pluginabi.MethodExecutorExecute, mustJSON(t, executorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:        "claude-sonnet-test",
			SourceFormat: config.ProtocolClaude,
			Format:       config.ProtocolClaude,
			Payload:      mustJSON(t, map[string]any{"messages": []map[string]string{{"role": "user", "content": "secret " + term}}}),
		},
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(string(execRaw.(pluginapi.ExecutorResponse).Payload), term) {
		t.Fatalf("expected restored non-stream response, got %s", execRaw.(pluginapi.ExecutorResponse).Payload)
	}

	// 2. Stream execute.
	if _, err := p.Dispatch(pluginabi.MethodExecutorExecuteStream, mustJSON(t, executorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:        "gpt-chat-test",
			SourceFormat: config.ProtocolOpenAI,
			Format:       config.ProtocolOpenAI,
			Payload:      mustJSON(t, map[string]any{"stream": true, "messages": []map[string]string{{"role": "user", "content": "stream " + term}}}),
		},
		StreamID: "out-1",
	})); err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	select {
	case <-host.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream close")
	}
	// Give defer RecordStreamCapture a brief moment to complete after HostStreamClose if needed.
	var snap engine.DebugCaptureSnapshot
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		getRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/cpa-reductant/debug/capture",
		}))
		if err != nil {
			t.Fatalf("get debug capture: %v", err)
		}
		if err := json.Unmarshal(getRaw.(pluginapi.ManagementResponse).Body, &snap); err != nil {
			t.Fatalf("decode capture snapshot: %v", err)
		}
		if len(snap.Entries) == 2 && snap.Entries[0].RestoredResponse != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !snap.Enabled || len(snap.Entries) != 2 {
		t.Fatalf("expected 2 captured entries while enabled, got %+v", snap)
	}
	streamEntry := snap.Entries[0]
	unaryEntry := snap.Entries[1]
	if !streamEntry.Stream || streamEntry.RestoredCount != 1 || !strings.Contains(streamEntry.RestoredResponse, term) || strings.Contains(streamEntry.UpstreamResponse, term) {
		t.Fatalf("unexpected stream capture entry: %+v", streamEntry)
	}
	if unaryEntry.Stream || unaryEntry.RestoredCount != 1 || !strings.Contains(unaryEntry.RawRequest, term) || strings.Contains(unaryEntry.MaskedRequest, term) || !strings.Contains(unaryEntry.RestoredResponse, term) {
		t.Fatalf("unexpected unary capture entry: %+v", unaryEntry)
	}
	if len(unaryEntry.Replacements) != 1 || unaryEntry.Replacements[0].Original != term {
		t.Fatalf("expected original plaintext in unary replacements, got %+v", unaryEntry.Replacements)
	}

	// Disable and clear.
	disableRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/cpa-reductant/debug/capture",
		Body:   []byte(`{"enabled":false}`),
	}))
	if err != nil {
		t.Fatalf("disable debug capture: %v", err)
	}
	var disabledSnap engine.DebugCaptureSnapshot
	_ = json.Unmarshal(disableRaw.(pluginapi.ManagementResponse).Body, &disabledSnap)
	if disabledSnap.Enabled || len(disabledSnap.Entries) != 2 {
		t.Fatalf("expected disabled snapshot to keep entries until cleared, got %+v", disabledSnap)
	}

	clearRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodDelete,
		Path:   "/v0/management/cpa-reductant/debug/capture",
	}))
	if err != nil {
		t.Fatalf("clear debug capture: %v", err)
	}
	var clearedSnap engine.DebugCaptureSnapshot
	_ = json.Unmarshal(clearRaw.(pluginapi.ManagementResponse).Body, &clearedSnap)
	if len(clearedSnap.Entries) != 0 {
		t.Fatalf("expected 0 entries after DELETE, got %+v", clearedSnap)
	}

	badRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/cpa-reductant/debug/capture",
		Body:   []byte(`{"enabled":true,"ttl_seconds":-5}`),
	}))
	if err != nil {
		t.Fatalf("bad debug capture post: %v", err)
	}
	if badRaw.(pluginapi.ManagementResponse).StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on negative ttl_seconds, got %#v", badRaw)
	}
}
