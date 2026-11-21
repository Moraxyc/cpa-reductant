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
	smokeClaudeModel       = "claude-sonnet-test"
	smokeOpenAIModel       = "gpt-chat-test"
	smokeCodexModel        = "gpt-5-codex-test"
	smokeGeminiModel       = "gemini-2.5-test"
	smokeInteractionsModel = "gemini-2.5-interactions-test"
	smokeImageModel        = "grok-imagine-image"
	smokeSpeechModel       = "grok-tts"
	smokeVideoModel        = "grok-imagine-video"
	smokeUnselectedModel   = "claude-haiku-test"

	smokeTerm        = "SYNTHETIC-PRIVATE-PROJECT-ZEBRA"
	smokeBlockedTerm = "SYNTHETIC-BLOCKED-PROJECT"
)

var placeholderPattern = regexp.MustCompile(`\{\{TERM_[a-z2-7]{8}\}\}`)

type recordedRequest struct {
	Path string
	Raw  string
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
	m.requests = append(m.requests, recordedRequest{Path: r.URL.Path, Raw: string(raw), Body: body})
	m.mu.Unlock()

	text := smokeTerm
	if match := placeholderPattern.FindString(string(raw)); match != "" {
		text = match
	}

	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens"):
		writeJSON(w, map[string]any{"input_tokens": 42})

	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		m.handleClaude(w, body, text)

	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		m.handleOpenAIChat(w, body, text)

	case strings.HasSuffix(r.URL.Path, "/responses/compact"):
		writeJSON(w, map[string]any{
			"id":     "cmp_mock",
			"object": "response.compaction",
			"output": []map[string]any{{
				"id":      "msg_1",
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": []map[string]any{{"type": "output_text", "text": text}},
			}},
		})

	case strings.HasSuffix(r.URL.Path, "/responses"):
		m.handleResponses(w, body, text)

	case strings.Contains(r.URL.Path, ":generateContent"):
		writeJSON(w, map[string]any{
			"candidates": []map[string]any{{
				"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": text}}},
				"finishReason": "STOP",
			}},
		})

	case strings.Contains(r.URL.Path, ":streamGenerateContent"):
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		mid := len(text) / 2
		frames := []string{
			"data: " + string(mustMarshal(map[string]any{
				"candidates": []map[string]any{{
					"content": map[string]any{"role": "model", "parts": []map[string]any{{"text": text[:mid]}}},
				}},
			})) + "\n\n",
			"data: " + string(mustMarshal(map[string]any{
				"candidates": []map[string]any{{
					"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": text[mid:]}}},
					"finishReason": "STOP",
				}},
			})) + "\n\n",
		}
		writeFrames(w, flusher, frames)

	case strings.HasSuffix(r.URL.Path, "/interactions"):
		m.handleInteractions(w, body, text)

	case strings.HasSuffix(r.URL.Path, "/images/generations"):
		writeJSON(w, map[string]any{
			"created": 1,
			"data": []map[string]any{{
				"url":            "https://example.com/mock.png",
				"revised_prompt": text,
			}},
		})

	case strings.HasSuffix(r.URL.Path, "/tts"):
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ID3MOCKAUDIO"))

	case strings.HasSuffix(r.URL.Path, "/videos/generations"):
		writeJSON(w, map[string]any{"request_id": "vid_mock_1"})

	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}
}

func (m *mockProvider) handleClaude(w http.ResponseWriter, body map[string]any, text string) {
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
		writeFrames(w, flusher, frames)
		return
	}
	msg["content"] = []map[string]any{{"type": "text", "text": text}}
	msg["stop_reason"] = "end_turn"
	writeJSON(w, msg)
}

func (m *mockProvider) handleOpenAIChat(w http.ResponseWriter, body map[string]any, text string) {
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		mid := len(text) / 2
		frames := []string{
			"data: " + string(mustMarshal(map[string]any{
				"id":      "chatcmpl-mock",
				"object":  "chat.completion.chunk",
				"model":   body["model"],
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": text[:mid]}}},
			})) + "\n\n",
			"data: " + string(mustMarshal(map[string]any{
				"id":      "chatcmpl-mock",
				"object":  "chat.completion.chunk",
				"model":   body["model"],
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": text[mid:]}}},
			})) + "\n\n",
			"data: " + string(mustMarshal(map[string]any{
				"id":      "chatcmpl-mock",
				"object":  "chat.completion.chunk",
				"model":   body["model"],
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			})) + "\n\n",
			"data: [DONE]\n\n",
		}
		writeFrames(w, flusher, frames)
		return
	}
	writeJSON(w, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"model":   body["model"],
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
	})
}

func (m *mockProvider) handleResponses(w http.ResponseWriter, body map[string]any, text string) {
	respObj := map[string]any{
		"id":     "resp_mock",
		"object": "response",
		"status": "completed",
		"model":  body["model"],
		"output": []map[string]any{{
			"id":      "msg_1",
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": []map[string]any{{"type": "output_text", "text": text}},
		}},
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		mid := len(text) / 2
		frames := []string{
			sseEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_mock", "status": "in_progress"}}),
			sseEvent("response.output_text.delta", map[string]any{
				"item_id":       "msg_1",
				"output_index":  0,
				"content_index": 0,
				"delta":         text[:mid],
			}),
			sseEvent("response.output_text.delta", map[string]any{
				"item_id":       "msg_1",
				"output_index":  0,
				"content_index": 0,
				"delta":         text[mid:],
			}),
			sseEvent("response.completed", map[string]any{"response": respObj}),
		}
		writeFrames(w, flusher, frames)
		return
	}
	writeJSON(w, respObj)
}

func (m *mockProvider) handleInteractions(w http.ResponseWriter, body map[string]any, text string) {
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		mid := len(text) / 2
		frames := []string{
			sseEvent("interaction.created", map[string]any{"event_type": "interaction.created", "interaction": map[string]any{"id": "int_1", "model": body["model"]}}),
			sseEvent("step.delta", map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"type": "text", "text": text[:mid]}}),
			sseEvent("step.delta", map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"type": "text", "text": text[mid:]}}),
			sseEvent("interaction.completed", map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"id": "int_1", "status": "completed"}}),
		}
		writeFrames(w, flusher, frames)
		return
	}
	writeJSON(w, map[string]any{
		"id":     "int_1",
		"model":  body["model"],
		"status": "completed",
		"steps": []map[string]any{{
			"type":    "model",
			"content": []map[string]any{{"type": "text", "text": text}},
		}},
	})
}

func writeFrames(w http.ResponseWriter, flusher http.Flusher, frames []string) {
	for _, frame := range frames {
		_, _ = io.WriteString(w, frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	data := mustMarshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
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
  secret-key: test-management-key
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
  codex:
    - name: mock-codex
      base-url: %s
      proxy-url: direct
      models:
        - name: %s
      keys:
        - api-key: test-upstream-key
  gemini:
    - name: mock-gemini
      base-url: %s
      proxy-url: direct
      models:
        - name: %s
      keys:
        - api-key: test-upstream-key
  interactions:
    - name: mock-interactions
      base-url: %s
      proxy-url: direct
      models:
        - name: %s
      keys:
        - api-key: test-upstream-key
  xai:
    - name: mock-xai
      base-url: %s
      proxy-url: direct
      models:
        - name: %s
        - name: %s
        - name: %s
      keys:
        - api-key: test-upstream-key
  openai-compatibility:
    - name: mock-openai
      base-url: %s/v1
      proxy-url: direct
      models:
        - name: %s
      keys:
        - api-key: test-upstream-key
plugins:
  enabled: true
  dir: %q
  configs:
    cpa-reductant:
      enabled: true
      models: [%s, %s, %s, %s, %s, %s, %s, %s]
      words: [%s, %s]
      block_terms: %t
`,
		port,
		filepath.Join(workDir, "auth"),
		upstreamURL, smokeClaudeModel, smokeUnselectedModel,
		upstreamURL, smokeCodexModel,
		upstreamURL, smokeGeminiModel,
		upstreamURL, smokeInteractionsModel,
		upstreamURL, smokeImageModel, smokeSpeechModel, smokeVideoModel,
		upstreamURL, smokeOpenAIModel,
		filepath.Join(workDir, "plugins"),
		smokeClaudeModel, smokeOpenAIModel, smokeCodexModel, smokeGeminiModel, smokeInteractionsModel, smokeImageModel, smokeSpeechModel, smokeVideoModel,
		smokeTerm, smokeBlockedTerm, blockTerms,
	)
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

func doJSONPost(t *testing.T, url string, headers map[string]string, body any) (int, []byte) {
	t.Helper()
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer test-frontend-key")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, out
}

func expectLastUpstreamMasked(t *testing.T, upstream *mockProvider, wantCount int, wantPathSubstr string) {
	t.Helper()
	reqs := upstream.Requests()
	if len(reqs) != wantCount {
		t.Fatalf("expected %d upstream requests, got %d", wantCount, len(reqs))
	}
	req := reqs[len(reqs)-1]
	if !strings.Contains(req.Path, wantPathSubstr) {
		t.Fatalf("unexpected upstream path %q, want substring %q", req.Path, wantPathSubstr)
	}
	if strings.Contains(req.Raw, smokeTerm) || !placeholderPattern.MatchString(req.Raw) {
		t.Fatalf("expected masked upstream request without %q, got %s", smokeTerm, req.Raw)
	}
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

	// Phase 1: mask + restore across all CPA formats + count_tokens + unselected passthrough
	writeHostConfig(t, configPath, workDir, upstream.server.URL, port, false)
	stop := startHost(t, hostBin, workDir, configPath, baseURL)

	reqCount := 0
	claudeHeaders := map[string]string{"x-api-key": "test-frontend-key", "anthropic-version": "2023-06-01"}

	// 1. Claude /v1/messages (JSON & SSE) + /v1/messages/count_tokens
	status, body := doJSONPost(t, baseURL+"/v1/messages", claudeHeaders, map[string]any{
		"model":      smokeClaudeModel,
		"max_tokens": 128,
		"messages":   []map[string]any{{"role": "user", "content": smokeTerm}},
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("Claude JSON status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/v1/messages")

	status, body = doJSONPost(t, baseURL+"/v1/messages", claudeHeaders, map[string]any{
		"model":      smokeClaudeModel,
		"max_tokens": 128,
		"stream":     true,
		"messages":   []map[string]any{{"role": "user", "content": smokeTerm}},
	})
	if status != http.StatusOK {
		stop()
		t.Fatalf("Claude SSE status=%d body=%s", status, body)
	}
	var claudeDeltas strings.Builder
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
			t.Fatalf("decode Claude SSE %q: %v", line, err)
		}
		lastType = frame.Type
		if frame.Type == "content_block_delta" {
			claudeDeltas.WriteString(frame.Delta.Text)
		}
	}
	if claudeDeltas.String() != smokeTerm || lastType != "message_stop" {
		stop()
		t.Fatalf("Claude SSE restored=%q last=%q\n%s", claudeDeltas.String(), lastType, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/v1/messages")

	status, body = doJSONPost(t, baseURL+"/v1/messages/count_tokens", claudeHeaders, map[string]any{
		"model":    smokeClaudeModel,
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
	})
	if status != http.StatusOK || !strings.Contains(string(body), `"input_tokens"`) {
		stop()
		t.Fatalf("Claude count_tokens status=%d body=%s", status, body)
	}
	reqCount = len(upstream.Requests())

	// 2. OpenAI /v1/chat/completions (JSON & SSE) + /v1/completions
	status, body = doJSONPost(t, baseURL+"/v1/chat/completions", nil, map[string]any{
		"model":    smokeOpenAIModel,
		"messages": []map[string]any{{"role": "user", "content": smokeTerm}},
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("OpenAI Chat JSON status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/chat/completions")

	status, body = doJSONPost(t, baseURL+"/v1/chat/completions", nil, map[string]any{
		"model":    smokeOpenAIModel,
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": smokeTerm}},
	})
	if status != http.StatusOK {
		stop()
		t.Fatalf("OpenAI Chat SSE status=%d body=%s", status, body)
	}
	var openaiText strings.Builder
	sawDone := false
	scanner = bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		rawData := strings.TrimPrefix(line, "data: ")
		if rawData == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(rawData), &chunk); err != nil {
			stop()
			t.Fatalf("decode OpenAI SSE %q: %v", line, err)
		}
		if len(chunk.Choices) > 0 {
			openaiText.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if openaiText.String() != smokeTerm || !sawDone {
		stop()
		t.Fatalf("OpenAI Chat SSE restored=%q sawDone=%t\n%s", openaiText.String(), sawDone, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/chat/completions")

	status, body = doJSONPost(t, baseURL+"/v1/completions", nil, map[string]any{
		"model":  smokeOpenAIModel,
		"prompt": smokeTerm,
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("OpenAI Completions JSON status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/chat/completions")

	// 3. OpenAI Responses /v1/responses (JSON & SSE) + /v1/responses/compact
	status, body = doJSONPost(t, baseURL+"/v1/responses", nil, map[string]any{
		"model": smokeCodexModel,
		"input": smokeTerm,
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("Responses JSON status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/responses")

	status, body = doJSONPost(t, baseURL+"/v1/responses", nil, map[string]any{
		"model":  smokeCodexModel,
		"stream": true,
		"input":  smokeTerm,
	})
	if status != http.StatusOK {
		stop()
		t.Fatalf("Responses SSE status=%d body=%s", status, body)
	}
	var respDeltas strings.Builder
	sawCompleted := false
	scanner = bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			stop()
			t.Fatalf("decode Responses SSE %q: %v", line, err)
		}
		if frame.Type == "response.output_text.delta" {
			respDeltas.WriteString(frame.Delta)
		}
		if frame.Type == "response.completed" {
			sawCompleted = true
		}
	}
	if respDeltas.String() != smokeTerm || !sawCompleted {
		stop()
		t.Fatalf("Responses SSE restored=%q completed=%t\n%s", respDeltas.String(), sawCompleted, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/responses")

	status, body = doJSONPost(t, baseURL+"/v1/responses/compact", nil, map[string]any{
		"model": smokeCodexModel,
		"input": smokeTerm,
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("Responses Compact status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/responses/compact")

	// 4. Gemini /v1beta/models/*:generateContent & :streamGenerateContent
	status, body = doJSONPost(t, baseURL+"/v1beta/models/"+smokeGeminiModel+":generateContent", nil, map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": smokeTerm}}}},
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("Gemini JSON status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, ":generateContent")

	status, body = doJSONPost(t, baseURL+"/v1beta/models/"+smokeGeminiModel+":streamGenerateContent?alt=sse", nil, map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": smokeTerm}}}},
	})
	if status != http.StatusOK {
		stop()
		t.Fatalf("Gemini SSE status=%d body=%s", status, body)
	}
	var gemText strings.Builder
	scanner = bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			stop()
			t.Fatalf("decode Gemini SSE %q: %v", line, err)
		}
		if len(chunk.Candidates) > 0 && len(chunk.Candidates[0].Content.Parts) > 0 {
			gemText.WriteString(chunk.Candidates[0].Content.Parts[0].Text)
		}
	}
	if gemText.String() != smokeTerm {
		stop()
		t.Fatalf("Gemini SSE restored=%q\n%s", gemText.String(), body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, ":streamGenerateContent")

	// 5. Interactions /v1beta/interactions (JSON & SSE)
	status, body = doJSONPost(t, baseURL+"/v1beta/interactions", nil, map[string]any{
		"model": smokeInteractionsModel,
		"input": smokeTerm,
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("Interactions JSON status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/interactions")

	status, body = doJSONPost(t, baseURL+"/v1beta/interactions", nil, map[string]any{
		"model":  smokeInteractionsModel,
		"stream": true,
		"input":  smokeTerm,
	})
	if status != http.StatusOK {
		stop()
		t.Fatalf("Interactions SSE status=%d body=%s", status, body)
	}
	var intText strings.Builder
	sawIntDone := false
	scanner = bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame struct {
			EventType string `json:"event_type"`
			Delta     struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			stop()
			t.Fatalf("decode Interactions SSE %q: %v", line, err)
		}
		if frame.EventType == "step.delta" {
			intText.WriteString(frame.Delta.Text)
		}
		if frame.EventType == "interaction.completed" {
			sawIntDone = true
		}
	}
	if intText.String() != smokeTerm || !sawIntDone {
		stop()
		t.Fatalf("Interactions SSE restored=%q completed=%t\n%s", intText.String(), sawIntDone, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/interactions")

	// 6. Media protocols: openai-image, openai-speech, openai-video
	status, body = doJSONPost(t, baseURL+"/v1/images/generations", nil, map[string]any{
		"model":  smokeImageModel,
		"prompt": smokeTerm,
	})
	if status != http.StatusOK || !strings.Contains(string(body), smokeTerm) {
		stop()
		t.Fatalf("OpenAI Image status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/images/generations")

	status, body = doJSONPost(t, baseURL+"/v1/audio/speech", nil, map[string]any{
		"model": smokeSpeechModel,
		"input": smokeTerm,
		"voice": "nova",
	})
	if status != http.StatusOK || string(body) != "ID3MOCKAUDIO" {
		stop()
		t.Fatalf("OpenAI Speech status=%d body=%q", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/tts")

	status, body = doJSONPost(t, baseURL+"/v1/videos/generations", nil, map[string]any{
		"model":  smokeVideoModel,
		"prompt": smokeTerm,
	})
	if status != http.StatusOK || !strings.Contains(string(body), "vid_mock_1") {
		stop()
		t.Fatalf("OpenAI Video status=%d body=%s", status, body)
	}
	reqCount++
	expectLastUpstreamMasked(t, upstream, reqCount, "/videos/generations")

	// 7. Unselected model passthrough
	status, body = doJSONPost(t, baseURL+"/v1/messages", claudeHeaders, map[string]any{
		"model":      smokeUnselectedModel,
		"max_tokens": 128,
		"messages":   []map[string]any{{"role": "user", "content": smokeTerm}},
	})
	if status != http.StatusOK {
		stop()
		t.Fatalf("unselected model status=%d body=%s", status, body)
	}
	reqCount++
	reqs := upstream.Requests()
	if len(reqs) != reqCount || !strings.Contains(reqs[len(reqs)-1].Raw, smokeTerm) {
		stop()
		t.Fatalf("expected unselected model passthrough unmasked, got %+v", reqs[len(reqs)-1])
	}

	// 8. Plugin Management API (/v0/management/cpa-reductant/status & preview)
	mgmtReq, _ := http.NewRequest(http.MethodGet, baseURL+"/v0/management/cpa-reductant/status", nil)
	mgmtReq.Header.Set("Authorization", "Bearer test-management-key")
	mgmtResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(mgmtReq)
	if err != nil {
		stop()
		t.Fatalf("management status GET: %v", err)
	}
	mgmtBody, _ := io.ReadAll(mgmtResp.Body)
	_ = mgmtResp.Body.Close()
	if mgmtResp.StatusCode != http.StatusOK || !strings.Contains(string(mgmtBody), `"requests_total"`) || !strings.Contains(string(mgmtBody), `"recent_events"`) {
		stop()
		t.Fatalf("management status=%d body=%s", mgmtResp.StatusCode, mgmtBody)
	}
	uiReq, _ := http.NewRequest(http.MethodGet, baseURL+"/v0/management/cpa-reductant/ui", nil)
	uiReq.Header.Set("Authorization", "Bearer test-management-key")
	uiResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(uiReq)
	if err != nil {
		stop()
		t.Fatalf("management ui GET: %v", err)
	}
	uiBody, _ := io.ReadAll(uiResp.Body)
	_ = uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK || !strings.Contains(uiResp.Header.Get("Content-Type"), "text/html") || strings.Contains(string(uiBody), smokeTerm) {
		stop()
		t.Fatalf("management ui=%d ct=%s", uiResp.StatusCode, uiResp.Header.Get("Content-Type"))
	}
	unauthReq, _ := http.NewRequest(http.MethodGet, baseURL+"/v0/management/cpa-reductant/ui", nil)
	unauthResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(unauthReq)
	if err != nil {
		stop()
		t.Fatalf("unauth ui GET: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		stop()
		t.Fatalf("expected 401 for unauthenticated ui access, got %d", unauthResp.StatusCode)
	}
	previewReq, _ := http.NewRequest(http.MethodPost, baseURL+"/v0/management/cpa-reductant/preview", strings.NewReader(`{"text":"`+smokeTerm+`"}`))
	previewReq.Header.Set("Authorization", "Bearer test-management-key")
	previewReq.Header.Set("Content-Type", "application/json")
	previewResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(previewReq)
	if err != nil {
		stop()
		t.Fatalf("management preview POST: %v", err)
	}
	previewBody, _ := io.ReadAll(previewResp.Body)
	_ = previewResp.Body.Close()
	if previewResp.StatusCode != http.StatusOK || strings.Contains(string(previewBody), smokeTerm) || !placeholderPattern.Match(previewBody) {
		stop()
		t.Fatalf("management preview status=%d body=%s", previewResp.StatusCode, previewBody)
	}
	stop()

	// Phase 2: block_terms rejects across protocols without upstream calls
	writeHostConfig(t, configPath, workDir, upstream.server.URL, port, true)
	stop = startHost(t, hostBin, workDir, configPath, baseURL)
	defer stop()

	before := len(upstream.Requests())
	blockRequests := []struct {
		url     string
		headers map[string]string
		body    map[string]any
	}{
		{
			url:     baseURL + "/v1/messages",
			headers: claudeHeaders,
			body:    map[string]any{"model": smokeClaudeModel, "max_tokens": 64, "messages": []map[string]any{{"role": "user", "content": smokeBlockedTerm}}},
		},
		{
			url:  baseURL + "/v1/chat/completions",
			body: map[string]any{"model": smokeOpenAIModel, "stream": true, "messages": []map[string]any{{"role": "user", "content": smokeBlockedTerm}}},
		},
		{
			url:  baseURL + "/v1/responses",
			body: map[string]any{"model": smokeCodexModel, "stream": true, "input": smokeBlockedTerm},
		},
		{
			url:  baseURL + "/v1beta/models/" + smokeGeminiModel + ":generateContent",
			body: map[string]any{"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": smokeBlockedTerm}}}}},
		},
	}
	for _, br := range blockRequests {
		status, body = doJSONPost(t, br.url, br.headers, br.body)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "request blocked by redaction policy") {
			t.Fatalf("expected 400 blocked error on %s, got status=%d body=%s", br.url, status, body)
		}
		if got := len(upstream.Requests()); got != before {
			t.Fatalf("expected zero upstream calls when blocked on %s, got %d -> %d", br.url, before, got)
		}
	}
}
