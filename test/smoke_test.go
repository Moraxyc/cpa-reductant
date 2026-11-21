package test_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	smokeModel           = "claude-sonnet-test"
	smokeUnselectedModel = "claude-haiku-test"
	smokeTerm            = "SYNTHETIC-PRIVATE-PROJECT-ZEBRA"
	smokeBlockedTerm     = "SYNTHETIC-BLOCKED-PROJECT"
)

var placeholderPattern = regexp.MustCompile(`\{\{TERM_[a-z2-7]{8}\}\}`)

type recordedRequest struct {
	Path string
	Body map[string]any
}

type mockProvider struct {
	mu       sync.Mutex
	requests []recordedRequest
	server   *httptest.Server
}

func newMockProvider() *mockProvider {
	m := &mockProvider{}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockProvider) Close() {
	m.server.Close()
}

func (m *mockProvider) Requests() []recordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]recordedRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

func (m *mockProvider) handle(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.requests = append(m.requests, recordedRequest{Path: r.URL.Path, Body: body})
	m.mu.Unlock()

	msgsBytes, _ := json.Marshal(body["messages"])
	text := smokeTerm
	if match := placeholderPattern.FindString(string(msgsBytes)); match != "" {
		text = match
	}

	msg := map[string]any{
		"id":            "msg_mock",
		"type":          "message",
		"role":          "assistant",
		"model":         body["model"],
		"content":       []any{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 12, "output_tokens": 1},
	}

	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		mid := len(text) / 2
		frames := []string{
			sseEvent("message_start", map[string]any{"message": msg}),
			sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}}),
			sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text[:mid]}}),
			sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text[mid:]}}),
			sseEvent("content_block_stop", map[string]any{"index": 0}),
			sseEvent("message_delta", map[string]any{
				"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": 2},
			}),
			sseEvent("message_stop", nil),
		}
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}

	msg["content"] = []map[string]any{{"type": "text", "text": text}}
	msg["stop_reason"] = "end_turn"
	data, _ := json.Marshal(msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func sseEvent(name string, fields map[string]any) string {
	payload := map[string]any{"type": name}
	for k, v := range fields {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, b)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func buildPluginSO(t *testing.T, dest string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", dest, "../cmd/plugin")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin .so: %v\n%s", err, out)
	}
}

func writeHostConfig(t *testing.T, path, workDir, upstreamURL string, port int, blockTerms bool) {
	t.Helper()
	cfg := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: %d
management:
  disable-control-panel: true
access:
  api-keys: [test-frontend-key]
oauth:
  auth-dir: %q
api-keys:
  claude:
    - name: mock-claude
      base-url: %s
      proxy-url: direct
      models:
        - name: %s
        - name: %s
      keys:
        - api-key: test-upstream-key
          cloak:
            mode: never
plugins:
  enabled: true
  dir: %q
  configs:
    cpa-reductant:
      enabled: true
      models: [%s]
      words: [%s, %s]
      block_terms: %t
`, port, filepath.Join(workDir, "auth"), upstreamURL, smokeModel, smokeUnselectedModel, filepath.Join(workDir, "plugins"), smokeModel, smokeTerm, smokeBlockedTerm, blockTerms)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func startHost(t *testing.T, hostBin, workDir, configPath, baseURL string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, hostBin, "-config", configPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+workDir,
		"XDG_CONFIG_HOME="+workDir,
		"XDG_DATA_HOME="+workDir,
		"XDG_CACHE_HOME="+workDir,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start CPA: %v", err)
	}

	cleanup := func() {
		cancel()
		_ = cmd.Wait()
	}

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-frontend-key")
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return cleanup
		}
		time.Sleep(100 * time.Millisecond)
	}
	cleanup()
	t.Fatalf("CPA did not become ready; logs:\n%s", logs.String())
	return nil
}

func callMessages(t *testing.T, baseURL, model, text string, stream bool) (int, []byte) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 128,
		"stream":     stream,
		"messages":   []map[string]any{{"role": "user", "content": text}},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", "test-frontend-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, body
}

func TestSmokeCPA(t *testing.T) {
	hostBin := os.Getenv("CPA_BIN")
	if hostBin == "" {
		t.Skip("set CPA_BIN to official cli-proxy-api binary path to run host smoke tests")
	}

	upstream := newMockProvider()
	defer upstream.Close()

	workDir := t.TempDir()
	pluginsDir := filepath.Join(workDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	pluginDest := filepath.Join(pluginsDir, "cpa-reductant.so")
	if existing := os.Getenv("CPA_PLUGIN_PATH"); existing != "" {
		data, err := os.ReadFile(existing)
		if err != nil {
			t.Fatalf("read CPA_PLUGIN_PATH: %v", err)
		}
		if err := os.WriteFile(pluginDest, data, 0o755); err != nil {
			t.Fatalf("copy plugin: %v", err)
		}
	} else {
		buildPluginSO(t, pluginDest)
	}

	port := freePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	configPath := filepath.Join(workDir, "config.yaml")

	expectMasked := func(req recordedRequest) {
		t.Helper()
		raw, _ := json.Marshal(req.Body)
		if req.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path %q", req.Path)
		}
		if req.Body["model"] != smokeModel {
			t.Fatalf("expected original model %q, got %v", smokeModel, req.Body["model"])
		}
		if strings.Contains(string(raw), smokeTerm) || !placeholderPattern.Match(raw) {
			t.Fatalf("expected masked request without %q, got %s", smokeTerm, raw)
		}
	}

	// Phase 1: mask + restore (JSON & SSE) + unselected model passthrough
	writeHostConfig(t, configPath, workDir, upstream.server.URL, port, false)
	stop := startHost(t, hostBin, workDir, configPath, baseURL)

	status, body := callMessages(t, baseURL, smokeModel, smokeTerm, false)
	if status != http.StatusOK {
		stop()
		t.Fatalf("JSON status=%d body=%s", status, body)
	}
	var jsonResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &jsonResp); err != nil || len(jsonResp.Content) == 0 || jsonResp.Content[0].Text != smokeTerm {
		stop()
		t.Fatalf("expected restored JSON text %q, got %s", smokeTerm, body)
	}
	reqs := upstream.Requests()
	if len(reqs) != 1 {
		stop()
		t.Fatalf("expected 1 upstream request, got %d", len(reqs))
	}
	expectMasked(reqs[0])

	status, body = callMessages(t, baseURL, smokeModel, smokeTerm, true)
	if status != http.StatusOK {
		stop()
		t.Fatalf("SSE status=%d body=%s", status, body)
	}
	var deltas strings.Builder
	var lastType string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			stop()
			t.Fatalf("decode SSE frame %q: %v", line, err)
		}
		lastType = frame.Type
		if frame.Type == "content_block_delta" {
			deltas.WriteString(frame.Delta.Text)
		}
	}
	if deltas.String() != smokeTerm || lastType != "message_stop" {
		stop()
		t.Fatalf("expected restored SSE %q ending with message_stop, got text=%q last=%q\n%s", smokeTerm, deltas.String(), lastType, body)
	}
	reqs = upstream.Requests()
	if len(reqs) != 2 {
		stop()
		t.Fatalf("expected 2 upstream requests, got %d", len(reqs))
	}
	expectMasked(reqs[1])

	status, body = callMessages(t, baseURL, smokeUnselectedModel, smokeTerm, false)
	if status != http.StatusOK {
		stop()
		t.Fatalf("unselected model status=%d body=%s", status, body)
	}
	reqs = upstream.Requests()
	if len(reqs) != 3 {
		stop()
		t.Fatalf("expected 3 upstream requests, got %d", len(reqs))
	}
	if raw, _ := json.Marshal(reqs[2].Body); !strings.Contains(string(raw), smokeTerm) {
		stop()
		t.Fatalf("expected unselected model to pass through unmasked, got %s", raw)
	}
	stop()

	// Phase 2: block_terms rejects JSON & SSE without upstream calls
	writeHostConfig(t, configPath, workDir, upstream.server.URL, port, true)
	stop = startHost(t, hostBin, workDir, configPath, baseURL)
	defer stop()

	before := len(upstream.Requests())
	for _, stream := range []bool{false, true} {
		status, body = callMessages(t, baseURL, smokeModel, smokeBlockedTerm, stream)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "request blocked by redaction policy") {
			t.Fatalf("expected 400 blocked error (stream=%t), got status=%d body=%s", stream, status, body)
		}
		if got := len(upstream.Requests()); got != before {
			t.Fatalf("expected zero upstream calls when blocked, got %d -> %d", before, got)
		}
	}
}
