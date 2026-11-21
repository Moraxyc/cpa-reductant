package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/redact"
)

var ErrBlocked = errors.New("request blocked by redaction policy")

type StatsSnapshot struct {
	RequestsTotal   int64            `json:"requests_total"`
	RequestsMasked  int64            `json:"requests_masked"`
	RequestsBlocked int64            `json:"requests_blocked"`
	HitsTotal       int64            `json:"hits_total"`
	HitsMasked      int64            `json:"hits_masked"`
	HitsWarned      int64            `json:"hits_warned"`
	HitsBlocked     int64            `json:"hits_blocked"`
	HitsByKind      map[string]int64 `json:"hits_by_kind,omitempty"`
	HitsByFinder    map[string]int64 `json:"hits_by_finder,omitempty"`
}

type PolicySummary struct {
	Models            []string          `json:"models,omitempty"`
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
	Shape             bool              `json:"shape"`
	KeyPathConfigured bool              `json:"key_path_configured"`
}

type Status struct {
	Version string        `json:"version"`
	Policy  PolicySummary `json:"policy"`
	Stats   StatsSnapshot `json:"stats"`
	Model   ModelStatus   `json:"model"`
}

type PreviewResult struct {
	Masked string       `json:"masked"`
	Hits   []redact.Hit `json:"hits,omitempty"`
}

type engineStats struct {
	mu              sync.Mutex
	requestsTotal   int64
	requestsMasked  int64
	requestsBlocked int64
	hitsTotal       int64
	hitsMasked      int64
	hitsWarned      int64
	hitsBlocked     int64
	byKind          map[string]int64
	byFinder        map[string]int64
}

func (s *engineStats) record(hits []redact.Hit, blocked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestsTotal++
	if blocked {
		s.requestsBlocked++
	}
	var maskedInReq int64
	for _, h := range hits {
		s.hitsTotal++
		switch h.Action {
		case redact.Warn:
			s.hitsWarned++
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
	if !blocked && maskedInReq > 0 {
		s.requestsMasked++
	}
}

func (s *engineStats) snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := StatsSnapshot{
		RequestsTotal:   s.requestsTotal,
		RequestsMasked:  s.requestsMasked,
		RequestsBlocked: s.requestsBlocked,
		HitsTotal:       s.hitsTotal,
		HitsMasked:      s.hitsMasked,
		HitsWarned:      s.hitsWarned,
		HitsBlocked:     s.hitsBlocked,
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
	return out
}

// Engine schedules redaction and restoration primitives exposed from Magpie.
type Engine struct {
	cfg   atomic.Pointer[config.Config]
	model *modelRunner
	stats engineStats
}

func New(cfg *config.Config) *Engine {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if keyPath := cfg.EffectiveKeyPath(); keyPath != "" {
		redact.SetKeyPath(keyPath)
	}
	e := &Engine{
		model: newModelRunner(cfg.Model),
	}
	e.cfg.Store(cfg)
	return e
}

func (e *Engine) Update(cfg *config.Config) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if keyPath := cfg.EffectiveKeyPath(); keyPath != "" {
		redact.SetKeyPath(keyPath)
	}
	e.cfg.Store(cfg)
	if e.model != nil {
		e.model.Configure(cfg.Model)
	}
}

func (e *Engine) Close() {
	if e != nil && e.model != nil {
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
	cfg := e.Config()
	if cfg == nil {
		cfg = &config.Config{}
	}
	return Status{
		Version: config.Version,
		Policy: PolicySummary{
			Models:            append([]string(nil), cfg.Models...),
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
			Shape:             cfg.Shape,
			KeyPathConfigured: cfg.EffectiveKeyPath() != "",
		},
		Stats: e.stats.snapshot(),
		Model: e.ModelStatus(),
	}
}

func (e *Engine) redactOptions(ctx context.Context, cfg *config.Config, route string, seen func(redact.Hit), modelErr *error) redact.Options {
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
		options.NamesByModel = cfg.Model.EffectiveName() == config.ModelAstrLinkGuard
		options.Model = func(texts []string) [][]redact.Found {
			found, ferr := e.model.Find(ctx, cfg.Model, texts)
			if ferr != nil {
				*modelErr = ferr
				return make([][]redact.Found, len(texts))
			}
			return found
		}
	}
	return options
}

func (e *Engine) MaskJSON(route string, body []byte) ([]byte, error) {
	return e.MaskJSONContext(context.Background(), route, body)
}

func (e *Engine) MaskJSONContext(ctx context.Context, route string, body []byte) (out []byte, err error) {
	cfg := e.Config()
	blocked := false
	var hits []redact.Hit
	var modelErr error
	options := e.redactOptions(ctx, cfg, route, func(hit redact.Hit) {
		hits = append(hits, hit)
		if hit.Action == redact.Block {
			blocked = true
		}
	}, &modelErr)
	defer func() {
		if rec := recover(); rec != nil {
			out = nil
			err = fmt.Errorf("redaction panic: %v", rec)
		}
	}()
	masked, _ := redact.MaskJSON(body, options)
	if modelErr != nil {
		return nil, modelErr
	}
	e.stats.record(hits, blocked)
	if blocked {
		return nil, ErrBlocked
	}
	return masked, nil
}

func (e *Engine) Preview(ctx context.Context, text string) (res PreviewResult, err error) {
	cfg := e.Config()
	var hits []redact.Hit
	var modelErr error
	options := e.redactOptions(ctx, cfg, "", func(hit redact.Hit) {
		hits = append(hits, hit)
	}, &modelErr)
	defer func() {
		if rec := recover(); rec != nil {
			res = PreviewResult{}
			err = fmt.Errorf("redaction preview panic: %v", rec)
		}
	}()
	masked := redact.Preview(text, options)
	if modelErr != nil {
		return PreviewResult{}, modelErr
	}
	return PreviewResult{
		Masked: masked,
		Hits:   hits,
	}, nil
}

func (e *Engine) RestoreJSON(body []byte) []byte {
	return redact.RestoreJSON(body)
}

func (e *Engine) RestoreResponse(protocol, contentType string, body []byte) []byte {
	if len(body) == 0 || IsBinaryContentType(contentType) {
		return body
	}
	if config.NormalizeProtocol(protocol) == config.ProtocolOpenAISpeech && !json.Valid(body) {
		return body
	}
	return redact.RestoreJSON(body)
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
