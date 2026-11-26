package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/redact"
)

var ErrBlocked = errors.New("request blocked by redaction policy")

type StatsSnapshot struct {
	RequestsTotal        int64                     `json:"requests_total"`
	RequestsMasked       int64                     `json:"requests_masked"`
	RequestsWarned       int64                     `json:"requests_warned"`
	RequestsBlocked      int64                     `json:"requests_blocked"`
	RequestsError        int64                     `json:"requests_error"`
	ResponsesTotal       int64                     `json:"responses_total"`
	ResponsesRestored    int64                     `json:"responses_restored"`
	StreamsTotal         int64                     `json:"streams_total"`
	StreamsRestored      int64                     `json:"streams_restored"`
	StreamsFailed        int64                     `json:"streams_failed"`
	PlaceholdersRestored int64                     `json:"placeholders_restored"`
	BytesMaskedIn        int64                     `json:"bytes_masked_in"`
	BytesRestoredOut     int64                     `json:"bytes_restored_out"`
	MaskLatency          LatencySummary            `json:"mask_latency_ms"`
	RestoreLatency       LatencySummary            `json:"restore_latency_ms"`
	HitsTotal            int64                     `json:"hits_total"`
	HitsMasked           int64                     `json:"hits_masked"`
	HitsWarned           int64                     `json:"hits_warned"`
	HitsBlocked          int64                     `json:"hits_blocked"`
	HitsByKind           map[string]int64          `json:"hits_by_kind,omitempty"`
	HitsByFinder         map[string]int64          `json:"hits_by_finder,omitempty"`
	ByProtocol           map[string]DimensionStats `json:"by_protocol,omitempty"`
	ByModel              map[string]DimensionStats `json:"by_model,omitempty"`
}

type PolicySummary struct {
	Models            []string          `json:"models,omitempty"`
	ExcludeModels     []string          `json:"exclude_models,omitempty"`
	Protocols         []string          `json:"protocols,omitempty"`
	Secrets           bool              `json:"secrets"`
	Personal          bool              `json:"personal"`
	Kinds             map[string]bool   `json:"kinds,omitempty"`
	WordsCount        int               `json:"words_count"`
	WordsAnyCase      bool              `json:"words_any_case"`
	WordsWhole        bool              `json:"words_whole"`
	RulesCount        int               `json:"rules_count"`
	Actions           map[string]string `json:"actions,omitempty"`
	AllowCount        int               `json:"allow_count"`
	PoliciesCount     int               `json:"policies_count,omitempty"`
	Shape             bool              `json:"shape"`
	KeyPathConfigured bool              `json:"key_path_configured"`
	KeyPathActive     bool              `json:"key_path_active"`
	KeyPathError      string            `json:"key_path_error,omitempty"`
	ScopeIsolation    bool              `json:"scope_isolation,omitempty"`
}

type Status struct {
	Version            string        `json:"version"`
	StartedAt          string        `json:"started_at"`
	UptimeSeconds      int64         `json:"uptime_seconds"`
	LastReconfiguredAt string        `json:"last_reconfigured_at,omitempty"`
	LastActivityAt     string        `json:"last_activity_at,omitempty"`
	Policy             PolicySummary `json:"policy"`
	Stats              StatsSnapshot `json:"stats"`
	Model              ModelStatus   `json:"model"`
	Scope              ScopeStats    `json:"scope"`
	Alerts             AlertStatus   `json:"alerts"`
	RecentEvents       []FilterEvent `json:"recent_events,omitempty"`
}

type PreviewResult struct {
	Mode       string       `json:"mode,omitempty"`
	Blocked    bool         `json:"blocked,omitempty"`
	Masked     string       `json:"masked"`
	Hits       []redact.Hit `json:"hits,omitempty"`
	DurationMS float64      `json:"duration_ms"`
}

type engineStats struct {
	mu                   sync.Mutex
	requestsTotal        int64
	requestsMasked       int64
	requestsWarned       int64
	requestsBlocked      int64
	requestsError        int64
	responsesTotal       int64
	responsesRestored    int64
	streamsTotal         int64
	streamsRestored      int64
	streamsFailed        int64
	placeholdersRestored int64
	bytesMaskedIn        int64
	bytesRestoredOut     int64
	hitsTotal            int64
	hitsMasked           int64
	hitsWarned           int64
	hitsBlocked          int64
	byKind               map[string]int64
	byFinder             map[string]int64
	byProtocol           map[string]DimensionStats
	byModel              map[string]DimensionStats
	maskLatency          latencyTracker
	restoreLatency       latencyTracker
}

func (s *engineStats) recordMask(protocol, model string, bytesIn int, hits []redact.Hit, blocked bool, dur time.Duration) {
	s.maskLatency.record(dur)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestsTotal++
	s.bytesMaskedIn += int64(bytesIn)
	if blocked {
		s.requestsBlocked++
	}
	var maskedInReq, warnedInReq int64
	for _, h := range hits {
		s.hitsTotal++
		switch h.Action {
		case redact.Warn:
			s.hitsWarned++
			warnedInReq++
		case redact.Block:
			s.hitsBlocked++
		default:
			s.hitsMasked++
			maskedInReq++
		}
		if h.Kind != "" {
			if s.byKind == nil {
				s.byKind = make(map[string]int64)
			}
			s.byKind[h.Kind]++
		}
		if h.By != "" {
			if s.byFinder == nil {
				s.byFinder = make(map[string]int64)
			}
			s.byFinder[h.By]++
		}
	}
	if !blocked {
		if maskedInReq > 0 {
			s.requestsMasked++
		}
		if warnedInReq > 0 {
			s.requestsWarned++
		}
	}

	updateDim := func(m map[string]DimensionStats, key string) map[string]DimensionStats {
		if key == "" {
			return m
		}
		if m == nil {
			m = make(map[string]DimensionStats)
		}
		d := m[key]
		d.Requests++
		d.BytesIn += int64(bytesIn)
		d.Hits += int64(len(hits))
		if blocked {
			d.Blocked++
		} else {
			if maskedInReq > 0 {
				d.Masked++
			}
			if warnedInReq > 0 {
				d.Warned++
			}
		}
		m[key] = d
		return m
	}
	s.byProtocol = updateDim(s.byProtocol, protocol)
	s.byModel = updateDim(s.byModel, model)
}

func (s *engineStats) recordMaskError(protocol, model string, bytesIn int, dur time.Duration) {
	s.maskLatency.record(dur)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestsTotal++
	s.requestsError++
	s.bytesMaskedIn += int64(bytesIn)

	updateDim := func(m map[string]DimensionStats, key string) map[string]DimensionStats {
		if key == "" {
			return m
		}
		if m == nil {
			m = make(map[string]DimensionStats)
		}
		d := m[key]
		d.Requests++
		d.BytesIn += int64(bytesIn)
		m[key] = d
		return m
	}
	s.byProtocol = updateDim(s.byProtocol, protocol)
	s.byModel = updateDim(s.byModel, model)
}

func (s *engineStats) recordRestore(protocol, model string, stream bool, bytesOut, restored int, failed bool, dur time.Duration) {
	s.restoreLatency.record(dur)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bytesRestoredOut += int64(bytesOut)
	if stream {
		s.streamsTotal++
		if failed {
			s.streamsFailed++
		}
		if restored > 0 {
			s.streamsRestored++
			s.placeholdersRestored += int64(restored)
		}
	} else {
		s.responsesTotal++
		if restored > 0 {
			s.responsesRestored++
			s.placeholdersRestored += int64(restored)
		}
	}

	updateDim := func(m map[string]DimensionStats, key string) map[string]DimensionStats {
		if key == "" {
			return m
		}
		if m == nil {
			m = make(map[string]DimensionStats)
		}
		d := m[key]
		d.BytesOut += int64(bytesOut)
		if restored > 0 {
			d.Restored++
			d.PlaceholdersRestored += int64(restored)
		}
		m[key] = d
		return m
	}
	s.byProtocol = updateDim(s.byProtocol, protocol)
	s.byModel = updateDim(s.byModel, model)
}

func (s *engineStats) snapshot() StatsSnapshot {
	maskLat := s.maskLatency.snapshot()
	restoreLat := s.restoreLatency.snapshot()

	s.mu.Lock()
	defer s.mu.Unlock()
	out := StatsSnapshot{
		RequestsTotal:        s.requestsTotal,
		RequestsMasked:       s.requestsMasked,
		RequestsWarned:       s.requestsWarned,
		RequestsBlocked:      s.requestsBlocked,
		RequestsError:        s.requestsError,
		ResponsesTotal:       s.responsesTotal,
		ResponsesRestored:    s.responsesRestored,
		StreamsTotal:         s.streamsTotal,
		StreamsRestored:      s.streamsRestored,
		StreamsFailed:        s.streamsFailed,
		PlaceholdersRestored: s.placeholdersRestored,
		BytesMaskedIn:        s.bytesMaskedIn,
		BytesRestoredOut:     s.bytesRestoredOut,
		MaskLatency:          maskLat,
		RestoreLatency:       restoreLat,
		HitsTotal:            s.hitsTotal,
		HitsMasked:           s.hitsMasked,
		HitsWarned:           s.hitsWarned,
		HitsBlocked:          s.hitsBlocked,
	}
	if len(s.byKind) > 0 {
		out.HitsByKind = make(map[string]int64, len(s.byKind))
		for k, v := range s.byKind {
			out.HitsByKind[k] = v
		}
	}
	if len(s.byFinder) > 0 {
		out.HitsByFinder = make(map[string]int64, len(s.byFinder))
		for k, v := range s.byFinder {
			out.HitsByFinder[k] = v
		}
	}
	if len(s.byProtocol) > 0 {
		out.ByProtocol = make(map[string]DimensionStats, len(s.byProtocol))
		for k, v := range s.byProtocol {
			out.ByProtocol[k] = v
		}
	}
	if len(s.byModel) > 0 {
		out.ByModel = make(map[string]DimensionStats, len(s.byModel))
		for k, v := range s.byModel {
			out.ByModel[k] = v
		}
	}
	return out
}

// Engine schedules redaction and restoration primitives exposed from Magpie.
type Engine struct {
	cfg       atomic.Pointer[config.Config]
	weights   *modelDownloader
	model     *modelRunner
	stats     engineStats
	events    *eventRing
	capture   *debugCapture
	scopes    *scopeVault
	alerts    *alertDispatcher
	startedAt time.Time

	metaMu             sync.Mutex
	lastReconfiguredAt time.Time
	lastActivityAt     atomic.Int64
	keyPathInitial     string
	keyPathLocked      bool
	keyPathErr         string

	logMu  sync.RWMutex
	logger Logger
}

func New(cfg *config.Config) *Engine {
	if cfg == nil {
		cfg = &config.Config{}
	}
	keyPath := cfg.EffectiveKeyPath()
	if keyPath != "" {
		redact.SetKeyPath(keyPath)
	}
	weights := newModelDownloader()
	e := &Engine{
		weights:        weights,
		model:          newModelRunnerWithWeights(cfg.EffectiveModelRunnerConfig(), weights),
		events:         newEventRing(defaultEventRing),
		capture:        newDebugCapture(defaultCaptureCapacity),
		scopes:         newScopeVault(defaultScopeMaxScopes, defaultScopeMaxEntries, cfg.EffectiveScopeTTL()),
		alerts:         newAlertDispatcher(cfg.Alerts),
		startedAt:      time.Now().UTC(),
		keyPathInitial: keyPath,
	}
	e.cfg.Store(cfg)
	return e
}

func (e *Engine) SetLogger(fn Logger) {
	if e == nil {
		return
	}
	e.logMu.Lock()
	e.logger = fn
	e.logMu.Unlock()
	if e.model != nil {
		if fn == nil {
			e.model.setLogger(nil)
		} else {
			e.model.setLogger(func(level, msg string, fields map[string]any) {
				fn(level, "", msg, fields)
			})
		}
	}
	if e.alerts != nil {
		if fn == nil {
			e.alerts.setLogger(nil)
		} else {
			e.alerts.setLogger(func(level, msg string, fields map[string]any) {
				fn(level, "", msg, fields)
			})
		}
	}
}

func (e *Engine) logEvent(level, callbackID, msg string, fields map[string]any) {
	if e == nil {
		return
	}
	e.logMu.RLock()
	fn := e.logger
	e.logMu.RUnlock()
	if fn != nil {
		fn(level, callbackID, msg, fields)
	}
}

func (e *Engine) touchActivity() {
	e.lastActivityAt.Store(time.Now().UTC().UnixNano())
}

func (e *Engine) lockKeyPath() {
	e.metaMu.Lock()
	e.keyPathLocked = true
	e.metaMu.Unlock()
}

func (e *Engine) Update(cfg *config.Config) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	keyPath := cfg.EffectiveKeyPath()
	e.metaMu.Lock()
	e.lastReconfiguredAt = time.Now().UTC()
	if e.keyPathLocked {
		if keyPath != e.keyPathInitial {
			e.keyPathErr = "key_path changed after redaction started; restart CPA to apply new key_path"
		} else {
			e.keyPathErr = ""
		}
	} else {
		e.keyPathInitial = keyPath
		e.keyPathErr = ""
		if keyPath != "" {
			redact.SetKeyPath(keyPath)
		}
	}
	e.metaMu.Unlock()

	e.cfg.Store(cfg)
	if e.scopes != nil {
		e.scopes.SetTTL(cfg.EffectiveScopeTTL())
	}
	if e.alerts != nil {
		e.alerts.Configure(cfg.Alerts)
	}
	if e.model != nil {
		e.model.Configure(cfg.EffectiveModelRunnerConfig())
	}
}

func (e *Engine) TriggerModelDownload(mirror string) (ModelStatus, error) {
	if e == nil || e.weights == nil || e.model == nil {
		return ModelStatus{}, errors.New("model runner not initialized")
	}
	if err := e.weights.Trigger(e.Config().EffectiveModelRunnerConfig(), mirror, func(dir string) {
		e.model.clearLoadError(dir, true)
	}); err != nil {
		return e.ModelStatus(), err
	}
	return e.ModelStatus(), nil
}

func (e *Engine) Close() {
	if e == nil {
		return
	}
	if e.alerts != nil {
		e.alerts.Close()
	}
	if e.model != nil {
		e.model.Close()
	}
}

func (e *Engine) Config() *config.Config {
	return e.cfg.Load()
}

func (e *Engine) ModelStatus() ModelStatus {
	if e == nil || e.model == nil {
		return ModelStatus{}
	}
	return e.model.Status()
}

func (e *Engine) Status() Status {
	return e.StatusWithOptions(defaultEventReturn, "")
}

func (e *Engine) StatusWithOptions(eventLimit int, actionFilter string) Status {
	cfg := e.Config()
	if cfg == nil {
		cfg = &config.Config{}
	}
	keyPath := cfg.EffectiveKeyPath()

	e.metaMu.Lock()
	reconfAt := e.lastReconfiguredAt
	keyLocked := e.keyPathLocked
	keyErr := e.keyPathErr
	effectiveKP := e.keyPathInitial
	e.metaMu.Unlock()

	if !keyLocked {
		effectiveKP = keyPath
	}

	keyActive := false
	if keyPath != "" && keyErr == "" {
		if keyLocked {
			if fi, err := os.Stat(effectiveKP); err == nil && fi.Size() >= 32 {
				keyActive = true
			} else if err != nil {
				keyErr = err.Error()
			} else {
				keyErr = "key file shorter than 32 bytes"
			}
		} else {
			keyActive = true
		}
	}

	var reconfStr string
	if !reconfAt.IsZero() {
		reconfStr = reconfAt.Format(time.RFC3339)
	}
	var activityStr string
	if ns := e.lastActivityAt.Load(); ns > 0 {
		activityStr = time.Unix(0, ns).UTC().Format(time.RFC3339)
	}

	now := time.Now().UTC()
	uptime := int64(now.Sub(e.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}

	var recent []FilterEvent
	if e.events != nil && eventLimit > 0 {
		recent = e.events.recent(eventLimit, actionFilter)
	}

	return Status{
		Version:            config.Version,
		StartedAt:          e.startedAt.Format(time.RFC3339),
		UptimeSeconds:      uptime,
		LastReconfiguredAt: reconfStr,
		LastActivityAt:     activityStr,
		Policy: PolicySummary{
			Models:            append([]string(nil), cfg.Models...),
			ExcludeModels:     append([]string(nil), cfg.ExcludeModels...),
			Protocols:         append([]string(nil), cfg.Protocols...),
			Secrets:           cfg.EffectiveSecrets(),
			Personal:          cfg.EffectivePersonal(),
			Kinds:             cfg.EffectiveKinds(),
			WordsCount:        len(cfg.Words),
			WordsAnyCase:      cfg.WordsAnyCase,
			WordsWhole:        cfg.WordsWhole,
			RulesCount:        len(cfg.EffectiveRules()),
			Actions:           cfg.EffectiveActions(),
			AllowCount:        len(cfg.Allow),
			PoliciesCount:     len(cfg.Policies),
			Shape:             cfg.Shape,
			KeyPathConfigured: keyPath != "",
			KeyPathActive:     keyActive,
			KeyPathError:      keyErr,
			ScopeIsolation:    cfg.ScopeIsolation,
		},
		Stats:        e.stats.snapshot(),
		Model:        e.ModelStatus(),
		Scope:        e.scopeStats(cfg.ScopeIsolation),
		Alerts:       e.alertStatus(),
		RecentEvents: recent,
	}
}

func (e *Engine) alertStatus() AlertStatus {
	if e == nil || e.alerts == nil {
		return AlertStatus{}
	}
	return e.alerts.Status()
}

func (e *Engine) scopeStats(enabled bool) ScopeStats {
	if e == nil || e.scopes == nil {
		return ScopeStats{Enabled: enabled}
	}
	return e.scopes.Snapshot(enabled)
}

func (e *Engine) redactOptions(ctx context.Context, cfg *config.Config, route string, seen func(redact.Hit), modelErr *error) redact.Options {
	return e.redactOptionsWithMode(ctx, cfg, route, seen, modelErr, false)
}

func (e *Engine) redactOptionsWithMode(ctx context.Context, cfg *config.Config, route string, seen func(redact.Hit), modelErr *error, dryRun bool) redact.Options {
	options := redact.Options{
		Secrets:      cfg.EffectiveSecrets(),
		Personal:     cfg.EffectivePersonal(),
		Kinds:        cfg.EffectiveKinds(),
		Words:        cfg.Words,
		WordsAnyCase: cfg.WordsAnyCase,
		WordsWhole:   cfg.WordsWhole,
		Rules:        cfg.EffectiveRules(),
		Actions:      cfg.EffectiveActions(),
		Allow:        cfg.Allow,
		Shape:        cfg.Shape,
		Route:        route,
		Seen:         seen,
	}
	if cfg.Model.Enabled && e.model != nil {
		options.MinScore = cfg.Model.EffectiveMinScore()
		options.NamesByModel = !cfg.Model.IsRemoteHTTP() && cfg.Model.EffectiveName() == config.ModelAstrLinkGuard
		options.ModelGeneration = e.model.Generation(cfg.Model)
		options.Model = func(texts []string) [][]redact.Found {
			var (
				found [][]redact.Found
				ferr  error
			)
			if dryRun {
				found, ferr = e.model.FindDryRun(ctx, cfg.Model, texts)
			} else {
				found, ferr = e.model.Find(ctx, cfg.Model, texts)
			}
			if ferr != nil {
				*modelErr = ferr
				return make([][]redact.Found, len(texts))
			}
			return found
		}
	}
	return options
}

func (e *Engine) BeginCapture() uint64 {
	if e == nil || e.capture == nil {
		return 0
	}
	return e.capture.beginIfEnabled()
}

func (e *Engine) ConfigureDebugCapture(enabled bool, ttl time.Duration, maxEntries int, clear bool) DebugCaptureSnapshot {
	if e == nil || e.capture == nil {
		return DebugCaptureSnapshot{Entries: []DebugCaptureEntry{}}
	}
	return e.capture.configure(enabled, ttl, maxEntries, clear)
}

func (e *Engine) ClearDebugCapture() DebugCaptureSnapshot {
	if e == nil || e.capture == nil {
		return DebugCaptureSnapshot{Entries: []DebugCaptureEntry{}}
	}
	return e.capture.clear()
}

func (e *Engine) DebugCaptureSnapshot(limit int, idFilter uint64, protocolFilter, modelFilter string) DebugCaptureSnapshot {
	if e == nil || e.capture == nil {
		return DebugCaptureSnapshot{Entries: []DebugCaptureEntry{}}
	}
	return e.capture.snapshot(limit, idFilter, protocolFilter, modelFilter)
}

func (e *Engine) CaptureError(captureID uint64, err error) {
	if e == nil || e.capture == nil || captureID == 0 || err == nil {
		return
	}
	e.capture.update(captureID, func(ev *DebugCaptureEntry) {
		ev.Status = "error"
		ev.Error = err.Error()
	})
}

func (e *Engine) MarkCaptureStreaming(captureID uint64, contentType string) {
	if e == nil || e.capture == nil || captureID == 0 {
		return
	}
	binary := IsBinaryContentType(contentType)
	e.capture.update(captureID, func(ev *DebugCaptureEntry) {
		ev.Status = "streaming"
		ev.ResponseBinary = binary
		if contentType != "" {
			ev.ResponseContentType = contentType
		}
	})
}

func (e *Engine) MaskJSON(route string, body []byte) ([]byte, error) {
	return e.MaskJSONContext(context.Background(), route, body)
}

func (e *Engine) MaskJSONContext(ctx context.Context, route string, body []byte) ([]byte, error) {
	return e.MaskRequest(ctx, RequestMeta{Route: route, Model: route}, body)
}

func (e *Engine) MaskRequest(ctx context.Context, meta RequestMeta, body []byte) (out []byte, err error) {
	start := time.Now()
	e.lockKeyPath()
	cfg := e.Config().ResolvePolicy(meta.Protocol, meta.Model)
	captureID := meta.CaptureID
	if captureID == 0 {
		captureID = e.BeginCapture()
	}
	blocked := false
	var hits []redact.Hit
	var modelErr error
	route := meta.Route
	if route == "" {
		route = meta.Model
	}
	options := e.redactOptions(ctx, cfg, route, func(hit redact.Hit) {
		hits = append(hits, hit)
		if hit.Action == redact.Block {
			blocked = true
		}
	}, &modelErr)
	defer func() {
		if rec := recover(); rec != nil {
			dur := time.Since(start)
			durMS := float64(dur.Microseconds()) / 1000.0
			out = nil
			err = fmt.Errorf("redaction panic: %v", rec)
			e.stats.recordMaskError(meta.Protocol, meta.Model, len(body), dur)
			e.touchActivity()
			ev := e.events.push(FilterEvent{
				Phase:      "request",
				Protocol:   meta.Protocol,
				Model:      meta.Model,
				Action:     "error",
				DurationMS: durMS,
				Bytes:      len(body),
				Error:      err.Error(),
			})
			e.alerts.Notify(ev)
			if captureID != 0 && e.capture != nil {
				rawStr, rawBytes, rawTrunc := truncateCaptureBytes(body, maxCaptureBodyBytes)
				e.capture.recordRequest(DebugCaptureEntry{
					ID:                  captureID,
					Status:              "error",
					Protocol:            meta.Protocol,
					Model:               meta.Model,
					Stream:              meta.Stream,
					Action:              "error",
					MaskDurationMS:      durMS,
					RawRequest:          rawStr,
					RawRequestTruncated: rawTrunc,
					RawRequestBytes:     rawBytes,
					Error:               err.Error(),
				})
			}
			e.logEvent("error", meta.HostCallbackID, "cpa-reductant request redaction panic", map[string]any{
				"protocol": meta.Protocol,
				"model":    meta.Model,
				"error":    err.Error(),
			})
		}
	}()
	masked, _ := redact.MaskJSON(body, options)
	dur := time.Since(start)
	durMS := float64(dur.Microseconds()) / 1000.0
	if modelErr != nil {
		e.stats.recordMaskError(meta.Protocol, meta.Model, len(body), dur)
		e.touchActivity()
		ev := e.events.push(FilterEvent{
			Phase:      "request",
			Protocol:   meta.Protocol,
			Model:      meta.Model,
			Action:     "error",
			DurationMS: durMS,
			Bytes:      len(body),
			Error:      modelErr.Error(),
		})
		e.alerts.Notify(ev)
		if captureID != 0 && e.capture != nil {
			rawStr, rawBytes, rawTrunc := truncateCaptureBytes(body, maxCaptureBodyBytes)
			e.capture.recordRequest(DebugCaptureEntry{
				ID:                  captureID,
				Status:              "error",
				Protocol:            meta.Protocol,
				Model:               meta.Model,
				Stream:              meta.Stream,
				Action:              "error",
				MaskDurationMS:      durMS,
				RawRequest:          rawStr,
				RawRequestTruncated: rawTrunc,
				RawRequestBytes:     rawBytes,
				Error:               modelErr.Error(),
			})
		}
		e.logEvent("warn", meta.HostCallbackID, "cpa-reductant strict local model unavailable", map[string]any{
			"protocol": meta.Protocol,
			"model":    meta.Model,
			"error":    modelErr.Error(),
		})
		return nil, modelErr
	}

	e.stats.recordMask(meta.Protocol, meta.Model, len(body), hits, blocked, dur)
	e.touchActivity()

	if !blocked && e.scopes != nil && meta.CallerScope != "" {
		baseCfg := e.Config()
		if baseCfg != nil && baseCfg.ScopeIsolation {
			var placeholders []string
			for _, h := range hits {
				if h.Placeholder != "" && h.Action != redact.Warn && h.Action != redact.Block {
					placeholders = append(placeholders, h.Placeholder)
				}
			}
			if len(placeholders) > 0 {
				e.scopes.Record(meta.CallerScope, placeholders...)
			}
		}
	}

	action := "passthrough"
	warnCount := 0
	maskCount := 0
	for _, h := range hits {
		switch h.Action {
		case redact.Warn:
			warnCount++
		case redact.Block:
		default:
			maskCount++
		}
	}
	switch {
	case blocked:
		action = "block"
	case maskCount > 0:
		action = "mask"
	case warnCount > 0:
		action = "warn"
	}

	ev := e.events.push(FilterEvent{
		Phase:      "request",
		Protocol:   meta.Protocol,
		Model:      meta.Model,
		Action:     action,
		DurationMS: durMS,
		Bytes:      len(body),
		Hits:       toEventHits(hits),
	})
	e.alerts.Notify(ev)

	if captureID != 0 && e.capture != nil {
		rawStr, rawBytes, rawTrunc := truncateCaptureBytes(body, maxCaptureBodyBytes)
		maskedStr, maskedBytes, maskedTrunc := truncateCaptureBytes(masked, maxCaptureBodyBytes)
		status := "pending"
		if blocked {
			status = "done"
		}
		entry := DebugCaptureEntry{
			ID:                     captureID,
			Status:                 status,
			Protocol:               meta.Protocol,
			Model:                  meta.Model,
			Stream:                 meta.Stream,
			Action:                 action,
			MaskDurationMS:         durMS,
			RawRequest:             rawStr,
			RawRequestTruncated:    rawTrunc,
			RawRequestBytes:        rawBytes,
			MaskedRequest:          maskedStr,
			MaskedRequestTruncated: maskedTrunc,
			MaskedRequestBytes:     maskedBytes,
			Replacements:           resolveCaptureReplacements(hits, cfg.Shape),
		}
		if blocked {
			entry.Error = ErrBlocked.Error()
		}
		e.capture.recordRequest(entry)
	}

	if blocked {
		e.logEvent("warn", meta.HostCallbackID, "cpa-reductant blocked request by policy", map[string]any{
			"protocol": meta.Protocol,
			"model":    meta.Model,
			"hits":     len(hits),
		})
		return nil, ErrBlocked
	}
	if warnCount > 0 {
		e.logEvent("warn", meta.HostCallbackID, "cpa-reductant passed warned sensitive values", map[string]any{
			"protocol":   meta.Protocol,
			"model":      meta.Model,
			"warn_count": warnCount,
			"mask_count": maskCount,
		})
	}
	return masked, nil
}

func (e *Engine) RestoreJSON(body []byte) []byte {
	return redact.RestoreJSON(body)
}

func (e *Engine) RestoreResponse(protocol, contentType string, body []byte) []byte {
	out, _ := e.RestoreResponseWithMeta(RequestMeta{Protocol: protocol}, contentType, body)
	return out
}

type restoreBuffer struct {
	bytes.Buffer
	header http.Header
}

func (b *restoreBuffer) Header() http.Header { return b.header }
func (*restoreBuffer) WriteHeader(int)       {}

func (e *Engine) RestoreResponseWithMeta(meta RequestMeta, contentType string, body []byte) ([]byte, int) {
	start := time.Now()
	if len(body) == 0 || IsBinaryContentType(contentType) {
		dur := time.Since(start)
		e.stats.recordRestore(meta.Protocol, meta.Model, false, len(body), 0, false, dur)
		if meta.CaptureID != 0 && e.capture != nil {
			durMS := roundMS(float64(dur.Microseconds()) / 1000.0)
			binary := IsBinaryContentType(contentType)
			e.capture.update(meta.CaptureID, func(ev *DebugCaptureEntry) {
				ev.Status = "done"
				ev.RestoreDurationMS = durMS
				ev.ResponseBinary = binary
				ev.ResponseContentType = contentType
				ev.UpstreamResponseBytes = len(body)
				ev.RestoredResponseBytes = len(body)
			})
		}
		return body, 0
	}
	if config.NormalizeProtocol(meta.Protocol) == config.ProtocolOpenAISpeech && !json.Valid(body) {
		dur := time.Since(start)
		e.stats.recordRestore(meta.Protocol, meta.Model, false, len(body), 0, false, dur)
		if meta.CaptureID != 0 && e.capture != nil {
			durMS := roundMS(float64(dur.Microseconds()) / 1000.0)
			e.capture.update(meta.CaptureID, func(ev *DebugCaptureEntry) {
				ev.Status = "done"
				ev.RestoreDurationMS = durMS
				ev.ResponseBinary = true
				ev.ResponseContentType = contentType
				ev.UpstreamResponseBytes = len(body)
				ev.RestoredResponseBytes = len(body)
			})
		}
		return body, 0
	}

	hdr := make(http.Header)
	if strings.TrimSpace(contentType) != "" {
		hdr.Set("Content-Type", contentType)
	} else {
		hdr.Set("Content-Type", "application/json")
	}
	buf := &restoreBuffer{header: hdr}
	w := e.NewStreamWriterForScope(buf, meta.CallerScope, meta.Protocol, meta.Model)
	_, _ = w.Write(body)
	w.Finish()

	out := buf.Bytes()
	put := w.Put()
	restoredCount := sumPutCounts(put)
	dur := time.Since(start)

	e.stats.recordRestore(meta.Protocol, meta.Model, false, len(out), restoredCount, false, dur)
	if restoredCount > 0 {
		e.touchActivity()
		e.events.push(FilterEvent{
			Phase:         "response",
			Protocol:      meta.Protocol,
			Model:         meta.Model,
			Action:        "restore",
			DurationMS:    float64(dur.Microseconds()) / 1000.0,
			Bytes:         len(out),
			RestoredCount: restoredCount,
		})
	}
	if meta.CaptureID != 0 && e.capture != nil {
		cfg := e.Config().ResolvePolicy(meta.Protocol, meta.Model)
		shape := cfg != nil && cfg.Shape
		upStr, upBytes, upTrunc := truncateCaptureBytes(body, maxCaptureBodyBytes)
		resStr, resBytes, resTrunc := truncateCaptureBytes(out, maxCaptureBodyBytes)
		durMS := roundMS(float64(dur.Microseconds()) / 1000.0)
		items := resolveCaptureRestoredItems(put, shape)
		e.capture.update(meta.CaptureID, func(ev *DebugCaptureEntry) {
			ev.Status = "done"
			ev.RestoreDurationMS = durMS
			ev.ResponseContentType = contentType
			ev.UpstreamResponse = upStr
			ev.UpstreamResponseBytes = upBytes
			ev.UpstreamResponseTruncated = upTrunc
			ev.RestoredResponse = resStr
			ev.RestoredResponseBytes = resBytes
			ev.RestoredResponseTruncated = resTrunc
			ev.RestoredCount = restoredCount
			ev.RestoredItems = items
		})
	}
	return out, restoredCount
}

func (e *Engine) RecordStreamCaptureProgress(
	captureID uint64,
	contentType string,
	upstream []byte,
	upstreamTotal int,
	upstreamTruncated bool,
	restored []byte,
	restoredTotal int,
	restoredTruncated bool,
	put map[string]int,
	dur time.Duration,
) {
	e.recordStreamCapture(
		captureID,
		"streaming",
		contentType,
		upstream,
		upstreamTotal,
		upstreamTruncated,
		restored,
		restoredTotal,
		restoredTruncated,
		put,
		dur,
		nil,
	)
}

func (e *Engine) RecordStreamCapture(
	captureID uint64,
	contentType string,
	upstream []byte,
	upstreamTotal int,
	upstreamTruncated bool,
	restored []byte,
	restoredTotal int,
	restoredTruncated bool,
	put map[string]int,
	dur time.Duration,
	err error,
) {
	status := "done"
	if err != nil {
		status = "error"
	}
	e.recordStreamCapture(
		captureID,
		status,
		contentType,
		upstream,
		upstreamTotal,
		upstreamTruncated,
		restored,
		restoredTotal,
		restoredTruncated,
		put,
		dur,
		err,
	)
}

func (e *Engine) recordStreamCapture(
	captureID uint64,
	status string,
	contentType string,
	upstream []byte,
	upstreamTotal int,
	upstreamTruncated bool,
	restored []byte,
	restoredTotal int,
	restoredTruncated bool,
	put map[string]int,
	dur time.Duration,
	err error,
) {
	if e == nil || e.capture == nil || captureID == 0 {
		return
	}
	cfg := e.Config()
	shape := cfg != nil && cfg.Shape
	binary := IsBinaryContentType(contentType)
	var upStr, resStr string
	var upTrunc, resTrunc bool
	if !binary {
		var actualUpTotal, actualResTotal int
		upStr, actualUpTotal, upTrunc = truncateCaptureBytes(upstream, maxCaptureBodyBytes)
		if upstreamTotal > actualUpTotal {
			actualUpTotal = upstreamTotal
		}
		if upstreamTruncated {
			upTrunc = true
		}
		upstreamTotal = actualUpTotal

		resStr, actualResTotal, resTrunc = truncateCaptureBytes(restored, maxCaptureBodyBytes)
		if restoredTotal > actualResTotal {
			actualResTotal = restoredTotal
		}
		if restoredTruncated {
			resTrunc = true
		}
		restoredTotal = actualResTotal
	}
	restoredCount := sumPutCounts(put)
	items := resolveCaptureRestoredItems(put, shape)
	durMS := roundMS(float64(dur.Microseconds()) / 1000.0)
	e.capture.update(captureID, func(ev *DebugCaptureEntry) {
		ev.Status = status
		ev.RestoreDurationMS = durMS
		ev.ResponseBinary = binary
		ev.ResponseContentType = contentType
		ev.UpstreamResponse = upStr
		ev.UpstreamResponseBytes = upstreamTotal
		ev.UpstreamResponseTruncated = upTrunc
		ev.RestoredResponse = resStr
		ev.RestoredResponseBytes = restoredTotal
		ev.RestoredResponseTruncated = resTrunc
		ev.RestoredCount = restoredCount
		ev.RestoredItems = items
		if err != nil {
			ev.Error = err.Error()
		}
	})
}

func (e *Engine) RecordStreamRestore(meta RequestMeta, bytesOut int, put map[string]int, dur time.Duration, err error) {
	restoredCount := sumPutCounts(put)
	failed := err != nil
	e.stats.recordRestore(meta.Protocol, meta.Model, true, bytesOut, restoredCount, failed, dur)
	if failed || restoredCount > 0 {
		e.touchActivity()
	}
	durMS := float64(dur.Microseconds()) / 1000.0
	if failed {
		e.events.push(FilterEvent{
			Phase:         "stream",
			Protocol:      meta.Protocol,
			Model:         meta.Model,
			Action:        "error",
			DurationMS:    durMS,
			Bytes:         bytesOut,
			RestoredCount: restoredCount,
			Error:         err.Error(),
		})
		e.logEvent("warn", meta.HostCallbackID, "cpa-reductant stream restore failed", map[string]any{
			"protocol":       meta.Protocol,
			"model":          meta.Model,
			"restored_count": restoredCount,
			"error":          err.Error(),
		})
		return
	}
	if restoredCount > 0 {
		e.events.push(FilterEvent{
			Phase:         "stream",
			Protocol:      meta.Protocol,
			Model:         meta.Model,
			Action:        "restore",
			DurationMS:    durMS,
			Bytes:         bytesOut,
			RestoredCount: restoredCount,
		})
	}
}

func IsBinaryContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	for _, prefix := range []string{
		"audio/",
		"image/",
		"video/",
		"application/octet-stream",
		"application/pdf",
		"application/zip",
	} {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}

func (e *Engine) NewStreamWriter(w http.ResponseWriter) *redact.Writer {
	return redact.NewWriter(w)
}

func (e *Engine) NewStreamWriterForScope(w http.ResponseWriter, scope, protocol, model string) StreamWriter {
	if e != nil && e.scopes != nil && strings.TrimSpace(scope) != "" {
		baseCfg := e.Config()
		if baseCfg != nil && baseCfg.ScopeIsolation {
			policyCfg := baseCfg.ResolvePolicy(protocol, model)
			shape := policyCfg != nil && policyCfg.Shape
			return newScopedStreamWriter(w, e.scopes, scope, shape)
		}
	}
	return redact.NewWriter(w)
}
