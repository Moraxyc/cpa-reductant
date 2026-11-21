package engine

import (
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/redact"
)

var ErrBlocked = errors.New("request blocked by redaction policy")

// Engine schedules redaction and restoration primitives exposed from Magpie.
type Engine struct {
	cfg atomic.Pointer[config.Config]
}

func New(cfg *config.Config) *Engine {
	if cfg == nil {
		cfg = &config.Config{}
	}
	e := &Engine{}
	e.cfg.Store(cfg)
	return e
}

func (e *Engine) Update(cfg *config.Config) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	e.cfg.Store(cfg)
}

func (e *Engine) Config() *config.Config {
	return e.cfg.Load()
}

func (e *Engine) MaskJSON(route string, body []byte) ([]byte, error) {
	cfg := e.Config()
	blocked := false
	options := redact.Options{
		Secrets:  true,
		Personal: true,
		Words:    cfg.Words,
		Route:    route,
		Seen: func(hit redact.Hit) {
			if hit.Action == redact.Block {
				blocked = true
			}
		},
	}
	if cfg.BlockTerms {
		options.Actions = map[string]string{"TERM": redact.Block}
	}
	masked, _ := redact.MaskJSON(body, options)
	if blocked {
		return nil, ErrBlocked
	}
	return masked, nil
}

func (e *Engine) RestoreJSON(body []byte) []byte {
	return redact.RestoreJSON(body)
}

func (e *Engine) NewStreamWriter(w http.ResponseWriter) *redact.Writer {
	return redact.NewWriter(w)
}
