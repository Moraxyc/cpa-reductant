package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
)

func TestAlertWebhookDeliveryZeroPlaintextAndFiltering(t *testing.T) {
	var mu sync.Mutex
	var payloads []AlertPayload
	var rawBodies []string
	var authHeaders []string
	received := make(chan struct{}, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var p AlertPayload
		_ = json.Unmarshal(raw, &p)
		mu.Lock()
		rawBodies = append(rawBodies, string(raw))
		payloads = append(payloads, p)
		authHeaders = append(authHeaders, r.Header.Get("X-Alert-Token"))
		mu.Unlock()
		received <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	secret := "SYNTHETIC-WEBHOOK-TOP-SECRET-999"
	cfg, err := config.Parse([]byte(`
words: [` + secret + `]
block_terms: true
alerts:
  webhook_url: ` + srv.URL + `
  events: [block]
  timeout_ms: 2000
  headers:
    X-Alert-Token: tok-123
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	// Normal passthrough must NOT trigger webhook.
	if _, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolOpenAI,
		Model:    "gpt-5",
	}, []byte(`{"text":"completely harmless text"}`)); err != nil {
		t.Fatalf("passthrough: %v", err)
	}

	// Blocked request MUST trigger webhook with zero plaintext.
	if _, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-sonnet-4",
	}, []byte(`{"text":"leaked `+secret+`"}`)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for webhook alert")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 1 {
		t.Fatalf("expected 1 webhook call, got %d", len(payloads))
	}
	if strings.Contains(rawBodies[0], secret) {
		t.Fatalf("webhook payload leaked plaintext secret: %s", rawBodies[0])
	}
	if authHeaders[0] != "tok-123" {
		t.Fatalf("expected custom header tok-123, got %q", authHeaders[0])
	}
	ev := payloads[0].Event
	if ev.Action != "block" || ev.Protocol != config.ProtocolClaude || ev.Model != "claude-sonnet-4" || len(ev.Hits) != 1 || ev.Hits[0].Kind != "TERM" {
		t.Fatalf("unexpected alert payload event: %+v", ev)
	}

	// Verify /status reports sent=1 and does not leak secret headers or URL.
	statusJSON, err := json.Marshal(eng.Status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(statusJSON), "tok-123") || strings.Contains(string(statusJSON), srv.URL) {
		t.Fatalf("status leaked webhook secret or URL: %s", statusJSON)
	}
	// Wait briefly for atomic counter increment after HTTP 202.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if eng.Status().Alerts.Sent == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := eng.Status().Alerts; !st.Enabled || st.Sent != 1 || st.Failed != 0 || st.Dropped != 0 {
		t.Fatalf("unexpected AlertStatus: %+v", st)
	}
}

func TestAlertDispatcherNonBlockingDropOnFullQueue(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	d := newAlertDispatcher(config.AlertConfig{
		WebhookURL: srv.URL,
		Events:     []string{"block"},
		TimeoutMS:  5000,
	})
	defer d.Close()

	// Flood more than alertQueueCapacity + 1 events; Notify must never block.
	for i := range alertQueueCapacity + 16 {
		d.Notify(FilterEvent{ID: uint64(i + 1), Action: "block"})
	}
	if st := d.Status(); st.Dropped == 0 {
		t.Fatalf("expected dropped > 0 on full queue, got %+v", st)
	}
}
