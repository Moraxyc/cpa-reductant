package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/redact"
)

var ErrBlocked = errors.New("request blocked by redaction policy")

// Engine schedules redaction and restoration primitives exposed from Magpie.
type Engine struct {
	cfg   atomic.Pointer[config.Config]
	model *modelRunner
}

func New(cfg *config.Config) *Engine {
	if cfg == nil {
		cfg = &config.Config{}
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

func (e *Engine) MaskJSON(route string, body []byte) ([]byte, error) {
	return e.MaskJSONContext(context.Background(), route, body)
}

func (e *Engine) MaskJSONContext(ctx context.Context, route string, body []byte) (out []byte, err error) {
	cfg := e.Config()
	blocked := false
	var modelErr error
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
	if cfg.Model.Enabled && e.model != nil {
		options.MinScore = cfg.Model.EffectiveMinScore()
		options.NamesByModel = cfg.Model.EffectiveName() == config.ModelAstrLinkGuard
		options.Model = func(texts []string) [][]redact.Found {
			found, ferr := e.model.Find(ctx, cfg.Model, texts)
			if ferr != nil {
				modelErr = ferr
				return make([][]redact.Found, len(texts))
			}
			return found
		}
	}
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
	if blocked {
		return nil, ErrBlocked
	}
	return masked, nil
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
