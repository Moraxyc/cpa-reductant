package adapter

import (
	"encoding/json"
	"net/http"
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

	if f.handler != nil {
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
	return len(f.calls)
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
		t.Fatalf("expected 0 host calls on blocked requests, got %d", host.count())
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
}
