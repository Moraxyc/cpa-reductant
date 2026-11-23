package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/moraxyc/cpa-reductant/internal/config"
)

func TestHTTPNERGenericMaskRestoreAndCaching(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-NER-Key") != "ner-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		const target = "Zephryx Quasar"
		idx := strings.Index(req.Text, target)
		var spans []map[string]any
		if idx >= 0 {
			spans = append(spans, map[string]any{
				"start": idx,
				"end":   idx + len(target),
				"label": "private_person",
				"score": 0.96,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"spans": spans})
	}))
	defer srv.Close()

	cfg, err := config.Parse([]byte(`
model:
  enabled: true
  provider: http
  format: generic
  endpoint: ` + srv.URL + `
  headers:
    X-NER-Key: ner-secret
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	body := []byte(`{"messages":[{"role":"user","content":"Please notify Zephryx Quasar tomorrow."}]}`)
	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-test",
	}, body)
	if err != nil {
		t.Fatalf("MaskRequest: %v", err)
	}
	if strings.Contains(string(masked), "Zephryx Quasar") {
		t.Fatalf("expected remote NER entity to be masked: %s", masked)
	}
	restored := eng.RestoreResponse(config.ProtocolClaude, "application/json", masked)
	if !strings.Contains(string(restored), "Zephryx Quasar") {
		t.Fatalf("expected remote NER entity to restore: %s", restored)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 remote NER call, got %d", calls.Load())
	}

	// Second identical request should hit the in-memory LRU cache without another HTTP call.
	if _, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-test",
	}, body); err != nil {
		t.Fatalf("MaskRequest #2: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected cached second request to avoid extra HTTP call, got %d calls", calls.Load())
	}

	st := eng.Status()
	if st.Model.Provider != config.ModelProviderHTTP || st.Model.Layout != "remote-http" || !st.Model.Loaded {
		t.Fatalf("unexpected remote model status: %+v", st.Model)
	}
	if _, err := eng.TriggerModelDownload(""); err == nil {
		t.Fatal("expected TriggerModelDownload to fail for remote http provider")
	}
}

func TestHTTPNERPresidioRuneOffsetsAndStrictError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "backend down", http.StatusBadGateway)
			return
		}
		var req struct {
			Text     string `json:"text"`
			Language string `json:"language"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Language != "zh" {
			http.Error(w, "wrong language", http.StatusBadRequest)
			return
		}
		const target = "Linus Torvalds"
		byteIdx := strings.Index(req.Text, target)
		var out []map[string]any
		if byteIdx >= 0 {
			// Presidio returns rune (character) offsets, not UTF-8 byte offsets.
			runeStart := utf8.RuneCountInString(req.Text[:byteIdx])
			runeEnd := runeStart + utf8.RuneCountInString(target)
			out = append(out, map[string]any{
				"start":       runeStart,
				"end":         runeEnd,
				"entity_type": "PERSON",
				"score":       0.92,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	cfg, err := config.Parse([]byte(`
model:
  enabled: true
  provider: http
  format: presidio
  language: zh
  strict: true
  endpoint: ` + srv.URL + `
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	// Prefix contains multi-byte Chinese characters to exercise rune -> byte offset conversion.
	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolOpenAI,
		Model:    "gpt-test",
	}, []byte(`{"text":"请联系负责人 Linus Torvalds 确认进度"}`))
	if err != nil {
		t.Fatalf("MaskRequest presidio: %v", err)
	}
	if strings.Contains(string(masked), "Linus Torvalds") {
		t.Fatalf("expected Presidio PERSON entity to be masked: %s", masked)
	}
	if !strings.Contains(string(masked), "请联系负责人 ") || !strings.Contains(string(masked), " 确认进度") {
		t.Fatalf("UTF-8 surrounding text corrupted after rune offset mapping: %s", masked)
	}

	// Strict mode returns ErrModelUnavailable when HTTP NER fails.
	fail.Store(true)
	_, err = eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolOpenAI,
		Model:    "gpt-test",
	}, []byte(`{"text":"全新未缓存文本触发远程失败"}`))
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("expected ErrModelUnavailable on strict HTTP 502, got %v", err)
	}
}
