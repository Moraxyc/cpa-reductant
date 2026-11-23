package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
)

const alertQueueCapacity = 64

// AlertStatus reports runtime telemetry for async webhook alerts without
// exposing webhook URLs or secret headers.
type AlertStatus struct {
	Enabled   bool     `json:"enabled"`
	Events    []string `json:"events,omitempty"`
	Sent      int64    `json:"sent"`
	Failed    int64    `json:"failed"`
	Dropped   int64    `json:"dropped"`
	LastError string   `json:"last_error,omitempty"`
}

// AlertPayload is the zero-plaintext JSON body posted to the configured webhook.
type AlertPayload struct {
	Plugin  string      `json:"plugin"`
	Version string      `json:"version"`
	Event   FilterEvent `json:"event"`
}

type alertJob struct {
	url     string
	timeout time.Duration
	headers map[string]string
	event   FilterEvent
}

type alertDispatcher struct {
	mu      sync.RWMutex
	cfg     config.AlertConfig
	events  []string
	closed  bool
	started bool
	queue   chan alertJob
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	client  *http.Client
	logFn   func(level, msg string, fields map[string]any)

	sent    atomic.Int64
	failed  atomic.Int64
	dropped atomic.Int64

	errMu   sync.Mutex
	lastErr string
}

func newAlertDispatcher(cfg config.AlertConfig) *alertDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	d := &alertDispatcher{
		queue:  make(chan alertJob, alertQueueCapacity),
		ctx:    ctx,
		cancel: cancel,
		client: &http.Client{Transport: tr},
	}
	d.Configure(cfg)
	return d
}

func (d *alertDispatcher) setLogger(fn func(level, msg string, fields map[string]any)) {
	d.mu.Lock()
	d.logFn = fn
	d.mu.Unlock()
}

func (d *alertDispatcher) Configure(cfg config.AlertConfig) {
	d.mu.Lock()
	d.cfg = cfg
	d.events = cfg.EffectiveEvents()
	shouldStart := !d.closed && cfg.Enabled() && !d.started
	if shouldStart {
		d.started = true
		d.wg.Add(1)
		go d.run()
	}
	d.mu.Unlock()
}

func (d *alertDispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
}

func (d *alertDispatcher) Notify(ev FilterEvent) {
	if d == nil {
		return
	}
	action := strings.ToLower(strings.TrimSpace(ev.Action))
	if action == "" {
		return
	}
	d.mu.RLock()
	if d.closed || !d.cfg.Enabled() || !slices.Contains(d.events, action) {
		d.mu.RUnlock()
		return
	}
	job := alertJob{
		url:     strings.TrimSpace(d.cfg.WebhookURL),
		timeout: d.cfg.EffectiveTimeout(),
		event:   ev,
	}
	if len(d.cfg.Headers) > 0 {
		job.headers = make(map[string]string, len(d.cfg.Headers))
		for k, v := range d.cfg.Headers {
			job.headers[k] = v
		}
	}
	d.mu.RUnlock()

	select {
	case d.queue <- job:
	default:
		d.dropped.Add(1)
	}
}

func (d *alertDispatcher) run() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case job := <-d.queue:
			d.deliver(job)
		}
	}
}

func (d *alertDispatcher) deliver(job alertJob) {
	payload, err := json.Marshal(AlertPayload{
		Plugin:  config.PluginID,
		Version: config.Version,
		Event:   job.event,
	})
	if err != nil {
		d.recordError(err)
		return
	}
	reqCtx, cancel := context.WithTimeout(d.ctx, job.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, job.url, bytes.NewReader(payload))
	if err != nil {
		d.recordError(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", config.PluginID+"/"+config.Version)
	for k, v := range job.headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		d.recordError(err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		d.recordError(fmt.Errorf("webhook returned HTTP %s", resp.Status))
		return
	}
	d.sent.Add(1)
	d.errMu.Lock()
	d.lastErr = ""
	d.errMu.Unlock()
}

func (d *alertDispatcher) recordError(err error) {
	if err == nil {
		return
	}
	d.failed.Add(1)
	msg := err.Error()
	d.errMu.Lock()
	d.lastErr = msg
	d.errMu.Unlock()

	d.mu.RLock()
	fn := d.logFn
	d.mu.RUnlock()
	if fn != nil {
		fn("warn", "cpa-reductant webhook alert delivery failed: "+msg, map[string]any{
			"component": "alert_webhook",
			"error":     msg,
		})
	}
}

func (d *alertDispatcher) Status() AlertStatus {
	if d == nil {
		return AlertStatus{}
	}
	d.mu.RLock()
	enabled := d.cfg.Enabled()
	events := slices.Clone(d.events)
	d.mu.RUnlock()
	d.errMu.Lock()
	lastErr := d.lastErr
	d.errMu.Unlock()
	return AlertStatus{
		Enabled:   enabled,
		Events:    events,
		Sent:      d.sent.Load(),
		Failed:    d.failed.Load(),
		Dropped:   d.dropped.Load(),
		LastError: lastErr,
	}
}
