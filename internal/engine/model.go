package engine

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/guard"
	"github.com/yetone/magpie/pkg/redact"
)

var ErrModelUnavailable = errors.New("local redaction model unavailable")

const (
	guardChunkSize = 4 << 10 // 4 KB per piece, matching Magpie's guardChunk
	pieceOverlap   = 1 << 10 // 1 KB overlap across adjacent pieces
	backlogCap     = guardChunkSize
	sentMax        = 50000
	landmarkLen    = 48
	minSpread      = 8
	cacheCap       = 20000
	handBack       = 50 * time.Millisecond
	behindAge      = time.Minute
)

type spanFinder interface {
	Find(text string) []guard.Span
}

// ModelStatus exposes runtime observability for the local PII model.
type ModelStatus struct {
	Enabled         bool           `json:"enabled"`
	Provider        string         `json:"provider,omitempty"`
	Name            string         `json:"name"`
	Format          string         `json:"format,omitempty"`
	Revision        string         `json:"revision,omitempty"`
	Strict          bool           `json:"strict"`
	AutoDownload    bool           `json:"auto_download"`
	MinScore        float64        `json:"min_score,omitempty"`
	WaitMS          int64          `json:"wait_ms,omitempty"`
	Mirror          string         `json:"mirror,omitempty"`
	Dir             string         `json:"dir"`
	ResolvedDir     string         `json:"resolved_dir,omitempty"`
	Layout          string         `json:"layout,omitempty"`
	Installed       bool           `json:"installed"`
	Loaded          bool           `json:"loaded"`
	Cores           int            `json:"cores,omitempty"`
	Downloading     bool           `json:"downloading,omitempty"`
	DownloadDone    int64          `json:"download_done,omitempty"`
	DownloadTotal   int64          `json:"download_total,omitempty"`
	DownloadError   string         `json:"download_error,omitempty"`
	CacheEntries    int            `json:"cache_entries"`
	CacheHits       int64          `json:"cache_hits"`
	CacheMisses     int64          `json:"cache_misses"`
	PartialTimeouts int64          `json:"partial_timeouts"`
	Behind          bool           `json:"behind,omitempty"`
	Waiting         int32          `json:"waiting"`
	Fails           int            `json:"fails,omitempty"`
	RetryAt         string         `json:"retry_at,omitempty"`
	LastError       string         `json:"last_error,omitempty"`
	DiskCacheError  string         `json:"disk_cache_error,omitempty"`
	InferLatency    LatencySummary `json:"infer_latency_ms"`
}

// guardLabels maps AstrLink Guard BIO labels into the label namespace expected
// by Magpie's redact.modelKinds. Non-PII labels (url, ip_address, private_date)
// are preserved here and filtered out by redact.modelKinds.
var guardLabels = map[string]string{
	"private_person":  "private_person",
	"private_address": "private_address",
	"phone":           "private_phone",
	"email":           "private_email",
	"account":         "account_number",
	"payment_card":    "account_number",
	"common_secret":   "secret",
	"url":             "private_url",
	"ip_address":      "ip_address",
	"private_date":    "private_date",
}

func mapGuardLabel(label string) string {
	if mapped, ok := guardLabels[label]; ok {
		return mapped
	}
	return label
}

type modelRunner struct {
	mu            sync.Mutex
	cfg           config.ModelConfig
	loader        func(dir string) (spanFinder, error)
	loaded        spanFinder
	loadedDir     string
	loadedGen     string
	lastErr       error
	fails         int
	retryAt       time.Time
	missingWarned bool

	turn       chan struct{}
	queue      turnQueue
	waiting    atomic.Int32
	inflight   map[[32]byte]chan struct{}
	cache      *textLRU
	disk       diskCache
	sent       sentSet
	downloader *modelDownloader

	behindAt      atomic.Int64
	partialLogged atomic.Int64

	cacheHits       atomic.Int64
	cacheMisses     atomic.Int64
	partialTimeouts atomic.Int64
	inferLatency    latencyTracker
	logFn           func(level, msg string, fields map[string]any)
}

func newModelRunner(cfg config.ModelConfig) *modelRunner {
	cores := guard.Cores()
	if cores < 1 {
		cores = 1
	}
	r := &modelRunner{
		cfg: cfg,
		loader: func(dir string) (spanFinder, error) {
			return guard.Load(dir)
		},
		turn:       make(chan struct{}, cores),
		inflight:   make(map[[32]byte]chan struct{}),
		cache:      newTextLRU(cacheCap),
		downloader: newModelDownloader(),
	}
	r.disk.setOnError(func(err error) {
		r.logf("warn", fmt.Sprintf("disk cache save failed: %v", err), map[string]any{
			"component": "model_disk_cache",
			"error":     err.Error(),
		})
	})
	if cfg.Enabled && cfg.EffectiveAutoDownload() {
		r.maybeDownload(cfg)
	}
	return r
}

func (r *modelRunner) setLogger(fn func(level, msg string, fields map[string]any)) {
	r.mu.Lock()
	r.logFn = fn
	r.mu.Unlock()
	if r.downloader != nil {
		r.downloader.setLogger(fn)
	}
}

func (r *modelRunner) logf(level, msg string, fields map[string]any) {
	r.mu.Lock()
	fn := r.logFn
	r.mu.Unlock()
	if fn != nil {
		fn(level, msg, fields)
		return
	}
	log.Printf("cpa-reductant: %s", msg)
}

func (r *modelRunner) Configure(cfg config.ModelConfig) {
	r.mu.Lock()
	prevDir := r.cfg.EffectiveDir()
	prevMirror := r.cfg.Mirror
	prevProvider := r.cfg.EffectiveProvider()
	prevEndpoint := strings.TrimSpace(r.cfg.Endpoint)
	prevFormat := r.cfg.EffectiveFormat()
	prevLang := r.cfg.EffectiveLanguage()
	nextDir := cfg.EffectiveDir()
	dirChanged := prevDir != nextDir
	backendChanged := prevProvider != cfg.EffectiveProvider() ||
		prevEndpoint != strings.TrimSpace(cfg.Endpoint) ||
		prevFormat != cfg.EffectiveFormat() ||
		prevLang != cfg.EffectiveLanguage()
	if !cfg.Enabled || dirChanged || backendChanged {
		r.loaded = nil
		r.loadedDir = ""
		r.loadedGen = ""
		r.lastErr = nil
		r.fails = 0
		r.retryAt = time.Time{}
		r.behindAt.Store(0)
	}
	if !cfg.Enabled || dirChanged || backendChanged || cfg.EffectiveAutoDownload() != r.cfg.EffectiveAutoDownload() || cfg.Strict != r.cfg.Strict {
		r.missingWarned = false
	}
	r.cfg = cfg
	r.mu.Unlock()

	if !cfg.Enabled || dirChanged || backendChanged || prevMirror != cfg.Mirror {
		r.downloader.Reset()
	}
	if dirChanged || backendChanged {
		if prevDir != "" {
			r.disk.flush(prevDir, r.cache)
		}
		r.disk.reset()
		r.cache.clear()
	} else if !cfg.Enabled && prevDir != "" {
		r.disk.flush(prevDir, r.cache)
	}
	if cfg.Enabled && !cfg.IsRemoteHTTP() {
		if cfg.EffectiveAutoDownload() {
			r.maybeDownload(cfg)
		} else if !modelInstalled(nextDir) {
			r.warnMissingWeights(cfg, nextDir)
		}
	}
}

func (r *modelRunner) Close() {
	r.downloader.Cancel()

	r.mu.Lock()
	dir := r.cfg.EffectiveDir()
	r.loaded = nil
	r.loadedDir = ""
	r.loadedGen = ""
	r.lastErr = nil
	r.fails = 0
	r.retryAt = time.Time{}
	r.behindAt.Store(0)
	r.mu.Unlock()

	r.disk.flush(dir, r.cache)
}

func (r *modelRunner) Status() ModelStatus {
	r.mu.Lock()
	cfg := r.cfg
	dir := cfg.EffectiveDir()
	loaded := r.loaded != nil && r.loadedDir == dir
	fails := r.fails
	var retryAt string
	if !r.retryAt.IsZero() && time.Now().Before(r.retryAt) {
		retryAt = r.retryAt.UTC().Format(time.RFC3339)
	}
	var lastErr string
	if r.lastErr != nil {
		lastErr = r.lastErr.Error()
	}
	cores := cap(r.turn)
	r.mu.Unlock()

	dlActive, dlDone, dlTotal, dlErr := r.downloader.status()
	behind := false
	if t := r.behindAt.Load(); t != 0 && time.Since(time.Unix(0, t)) < behindAge {
		behind = true
	}
	var resolvedDir, layout, revision, format string
	var installed bool
	if cfg.IsRemoteHTTP() {
		format = cfg.EffectiveFormat()
		installed = strings.TrimSpace(cfg.Endpoint) != ""
		loaded = cfg.Enabled && installed
		if cfg.Enabled {
			layout = "remote-http"
		} else {
			layout = "disabled"
		}
	} else {
		revision = guard.Rev[:12]
		resolvedDir, layout, installed = resolveModelDirWithLayout(dir)
		if !cfg.Enabled && !installed {
			layout = "disabled"
		}
	}
	return ModelStatus{
		Enabled:         cfg.Enabled,
		Provider:        cfg.EffectiveProvider(),
		Name:            cfg.EffectiveName(),
		Format:          format,
		Revision:        revision,
		Strict:          cfg.Strict,
		AutoDownload:    cfg.EffectiveAutoDownload(),
		MinScore:        cfg.EffectiveMinScore(),
		WaitMS:          cfg.EffectiveWait().Milliseconds(),
		Mirror:          strings.TrimSpace(cfg.Mirror),
		Dir:             dir,
		ResolvedDir:     resolvedDir,
		Layout:          layout,
		Installed:       installed,
		Loaded:          loaded,
		Cores:           cores,
		Downloading:     dlActive,
		DownloadDone:    dlDone,
		DownloadTotal:   dlTotal,
		DownloadError:   dlErr,
		CacheEntries:    r.cache.len(),
		CacheHits:       r.cacheHits.Load(),
		CacheMisses:     r.cacheMisses.Load(),
		PartialTimeouts: r.partialTimeouts.Load(),
		Behind:          behind,
		Waiting:         r.waiting.Load(),
		Fails:           fails,
		RetryAt:         retryAt,
		LastError:       lastErr,
		DiskCacheError:  r.disk.lastError(),
		InferLatency:    r.inferLatency.snapshot(),
	}
}

func (r *modelRunner) warnMissingWeights(cfg config.ModelConfig, dir string) {
	r.mu.Lock()
	if r.missingWarned || r.logFn == nil {
		r.mu.Unlock()
		return
	}
	r.missingWarned = true
	r.mu.Unlock()

	r.logf("warn", fmt.Sprintf("astrlink-guard enabled but weights not found in %q and auto_download is false; place weights manually or remove auto_download: false", dir), map[string]any{
		"component":     "local_model",
		"dir":           dir,
		"auto_download": cfg.EffectiveAutoDownload(),
		"strict":        cfg.Strict,
	})
}

func (r *modelRunner) TriggerDownload(mirrorOverride string) error {
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()
	if cfg.IsRemoteHTTP() {
		return errors.New("model download is only supported for local astrlink-guard")
	}
	dir := cfg.EffectiveDir()
	if dir == "" {
		return errors.New("model directory could not be resolved; set model.dir or CPA_REDUCTANT_MODEL_DIR")
	}
	if modelInstalled(dir) {
		r.mu.Lock()
		r.lastErr = nil
		r.fails = 0
		r.retryAt = time.Time{}
		r.mu.Unlock()
		return nil
	}
	mirror := strings.TrimSpace(mirrorOverride)
	if mirror == "" {
		mirror = cfg.Mirror
	}
	r.downloader.ForceAsync(dir, mirror, func() {
		r.mu.Lock()
		if r.cfg.EffectiveDir() == dir {
			r.lastErr = nil
			r.fails = 0
			r.retryAt = time.Time{}
			r.missingWarned = false
		}
		r.mu.Unlock()
	})
	return nil
}

func (r *modelRunner) maybeDownload(cfg config.ModelConfig) {
	if !cfg.Enabled || !cfg.EffectiveAutoDownload() || r.downloader == nil {
		return
	}
	dir := cfg.EffectiveDir()
	if dir == "" || modelInstalled(dir) {
		return
	}
	r.downloader.EnsureAsync(dir, cfg.Mirror, func() {
		r.mu.Lock()
		if r.cfg.Enabled && r.cfg.EffectiveDir() == dir {
			r.lastErr = nil
			r.fails = 0
			r.retryAt = time.Time{}
		}
		r.mu.Unlock()
	})
}

func modelInstalled(dir string) bool {
	_, ok := resolveModelDir(dir)
	return ok
}

// resolveModelDir locates the directory containing the model weights: first
// checking the pinned revision subdirectory (<dir>/<rev[:12]>, matching
// Magpie's layout and ensuring exact pinned byte sizes), then falling back to
// flat files directly inside <dir> for manual placement.
func resolveModelDir(dir string) (string, bool) {
	resolved, _, ok := resolveModelDirWithLayout(dir)
	return resolved, ok
}

func resolveModelDirWithLayout(dir string) (string, string, bool) {
	if dir == "" {
		return "", "missing", false
	}
	vdir := versionedModelDir(dir)
	if hasPinnedFiles(vdir) {
		return vdir, "versioned", true
	}
	if hasFlatFiles(dir) {
		return dir, "flat", true
	}
	return "", "missing", false
}

func hasPinnedFiles(dir string) bool {
	if dir == "" {
		return false
	}
	for _, f := range guard.PinnedFiles {
		fi, err := os.Stat(filepath.Join(dir, filepath.Base(f.Path)))
		if err != nil || fi.IsDir() || fi.Size() != f.Size {
			return false
		}
	}
	return true
}

func hasFlatFiles(dir string) bool {
	for _, rel := range guard.Files {
		fi, err := os.Stat(filepath.Join(dir, filepath.Base(rel)))
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			return false
		}
	}
	return true
}

func retryBackoff(fails int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < fails && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

func (r *modelRunner) effectiveWait(cfg config.ModelConfig) time.Duration {
	wait := cfg.EffectiveWait()
	if cfg.Strict && cfg.WaitMS == 0 {
		if t := r.behindAt.Load(); t != 0 && time.Since(time.Unix(0, t)) < behindAge {
			return min(wait, config.DefaultModelWaitMS*time.Millisecond)
		}
	}
	return wait
}

func (r *modelRunner) Generation(cfg config.ModelConfig) string {
	if !cfg.Enabled {
		return ""
	}
	if cfg.IsRemoteHTTP() {
		return "http|" + strings.TrimSpace(cfg.Endpoint) + "|" + cfg.EffectiveFormat() + "|" + cfg.EffectiveLanguage()
	}
	dir := cfg.EffectiveDir()
	r.mu.Lock()
	if r.loaded != nil && r.loadedDir == dir && r.loadedGen != "" {
		gen := r.loadedGen
		r.mu.Unlock()
		return gen
	}
	r.mu.Unlock()
	loadDir, ok := resolveModelDir(dir)
	if !ok {
		return "pending|" + cfg.EffectiveName() + "|" + dir
	}
	return localModelGeneration(cfg, loadDir)
}

func localModelGeneration(cfg config.ModelConfig, loadDir string) string {
	return "local|" + cfg.EffectiveName() + "|" + loadDir + "|" + guard.Rev[:12] + "|" + modelDirStamp(loadDir)
}

func modelDirStamp(dir string) string {
	h := sha256.New()
	for _, rel := range guard.Files {
		name := filepath.Base(rel)
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s:%d:%d\n", name, fi.Size(), fi.ModTime().UnixNano())
	}
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:6])
}

func (r *modelRunner) ensureModel(cfg config.ModelConfig) (spanFinder, error) {
	if cfg.IsRemoteHTTP() {
		endpoint := strings.TrimSpace(cfg.Endpoint)
		if endpoint == "" {
			return nil, fmt.Errorf("%w: model.endpoint is not configured", ErrModelUnavailable)
		}
		key := "http|" + endpoint + "|" + cfg.EffectiveFormat() + "|" + cfg.EffectiveLanguage()
		r.mu.Lock()
		if r.loaded != nil && r.loadedDir == key {
			m := r.loaded
			r.mu.Unlock()
			return m, nil
		}
		finder := newHTTPNERFinder(cfg)
		r.loaded = finder
		r.loadedDir = key
		r.lastErr = nil
		r.fails = 0
		r.retryAt = time.Time{}
		r.mu.Unlock()
		return finder, nil
	}
	dir := cfg.EffectiveDir()
	r.mu.Lock()
	if r.loaded != nil && r.loadedDir == dir {
		m := r.loaded
		r.mu.Unlock()
		return m, nil
	}
	if r.lastErr != nil && time.Now().Before(r.retryAt) {
		err := r.lastErr
		r.mu.Unlock()
		return nil, err
	}
	loader := r.loader
	r.mu.Unlock()

	loadDir, ok := resolveModelDir(dir)
	if !ok {
		if cfg.EffectiveAutoDownload() {
			r.maybeDownload(cfg)
		} else {
			r.warnMissingWeights(cfg, dir)
		}
		return nil, fmt.Errorf("%w: model files not found in %q", ErrModelUnavailable, dir)
	}

	m, err := loadSafe(loader, loadDir)

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.fails++
		r.lastErr = fmt.Errorf("%w: %v", ErrModelUnavailable, err)
		r.retryAt = time.Now().Add(retryBackoff(r.fails))
		return nil, r.lastErr
	}
	if !r.cfg.Enabled || r.cfg.EffectiveDir() != dir {
		return nil, ErrModelUnavailable
	}
	r.loaded = m
	r.loadedDir = dir
	r.loadedGen = localModelGeneration(cfg, loadDir)
	r.lastErr = nil
	r.fails = 0
	r.retryAt = time.Time{}
	return m, nil
}

func loadSafe(loader func(string) (spanFinder, error), dir string) (m spanFinder, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			m = nil
			err = fmt.Errorf("panic loading guard model: %v", rec)
		}
	}()
	return loader(dir)
}

func findSafe(ctx context.Context, m spanFinder, text string) (spans []guard.Span, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			spans = nil
			err = fmt.Errorf("panic running guard model: %v", rec)
		}
	}()
	if cf, ok := m.(contextSpanFinder); ok {
		return cf.FindContext(ctx, text)
	}
	return m.Find(text), nil
}

// Find analyzes texts up to effectiveWait(cfg) under ctx. New pieces are
// prioritized ahead of previously-sent backlog pieces; non-strict requests
// cap backlog work per request while strict requests return once all new
// pieces finish. Timeout falls back to partial hits plus rule masking.
func (r *modelRunner) Find(ctx context.Context, cfg config.ModelConfig, texts []string) ([][]redact.Found, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	dir := cfg.EffectiveDir()
	r.disk.loadOnce(dir, r.cache)

	out, miss := r.known(texts)
	if len(miss) == 0 {
		r.cacheHits.Add(int64(len(texts)))
		return filterByScore(out, cfg.EffectiveMinScore()), nil
	}
	r.cacheHits.Add(int64(len(texts) - len(miss)))
	r.cacheMisses.Add(int64(len(miss)))

	m, err := r.ensureModel(cfg)
	if err != nil {
		if cfg.Strict {
			return nil, err
		}
		return filterByScore(out, cfg.EffectiveMinScore()), nil
	}

	units, backlog := r.missingUnits(texts, miss)
	if len(units) == 0 {
		return filterByScore(out, cfg.EffectiveMinScore()), nil
	}

	wait := r.effectiveWait(cfg)
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}

	// Calculate remaining byte weights for shortest-job-first turn scheduling.
	leftBytes := make([]int, len(units)+1)
	for i := len(units) - 1; i >= 0; i-- {
		leftBytes[i] = leftBytes[i+1] + len(units[i].text)
	}

	backlogRead := 0
	newTotal := 0
	for _, u := range units {
		if !u.backlog {
			newTotal++
		}
	}
	newDone := 0
	timedOut := false
	inferStart := time.Now()
	defer func() {
		r.inferLatency.record(time.Since(inferStart))
	}()

	for idx, u := range units {
		if u.backlog {
			if cfg.Strict && newDone == newTotal {
				// All brand-new pieces finished; don't hold the request for old backlog.
				break
			}
			if !cfg.Strict && backlogRead >= backlogCap {
				break
			}
		}
		if ctx.Err() != nil {
			timedOut = true
			break
		}
		didRun, err := r.analyzePiece(ctx, m, u.text, leftBytes[idx])
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				timedOut = true
				break
			}
			if cfg.Strict {
				return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
			}
			break
		}
		if didRun {
			r.disk.scheduleSave(dir, r.cache)
		}
		if u.backlog {
			backlogRead += len(u.text)
		} else {
			newDone++
		}
	}

	if timedOut {
		r.partialTimeouts.Add(1)
		if cfg.Strict && newDone < newTotal && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.behindAt.Store(time.Now().UnixNano())
		}
		r.notePartial(newTotal-newDone, len(units), wait, ctx.Err())
	} else if cfg.Strict && newDone == newTotal && len(backlog) >= 0 {
		r.behindAt.Store(0)
	}

	out, _ = r.known(texts)
	return filterByScore(out, cfg.EffectiveMinScore()), nil
}

func (r *modelRunner) notePartial(missingNew, totalUnits int, wait time.Duration, err error) {
	now := time.Now().Unix()
	if prev := r.partialLogged.Load(); prev != 0 && now-prev < 60 || !r.partialLogged.CompareAndSwap(prev, now) {
		return
	}
	r.logf("warn", fmt.Sprintf("local model timed out after %s (%d/%d new pieces remaining, %v); degraded to partial+rules",
		wait, max(0, missingNew), totalUnits, err), map[string]any{
		"component":         "local_model",
		"wait_ms":           wait.Milliseconds(),
		"missing_new_units": max(0, missingNew),
		"total_units":       totalUnits,
		"error":             fmt.Sprint(err),
	})
}

func (r *modelRunner) analyzePiece(ctx context.Context, m spanFinder, piece string, priorityLeftBytes int) (bool, error) {
	k := sha256.Sum256([]byte(piece))
	if _, ok := r.cache.get(k); ok {
		return false, nil
	}

	// Deduplicate concurrent analyses of identical chunks.
	r.mu.Lock()
	if ch, busy := r.inflight[k]; busy {
		r.mu.Unlock()
		select {
		case <-ch:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	ch := make(chan struct{})
	r.inflight[k] = ch
	r.mu.Unlock()

	claimed := true
	releaseInflight := func() {
		if !claimed {
			return
		}
		claimed = false
		r.mu.Lock()
		delete(r.inflight, k)
		r.mu.Unlock()
		close(ch)
	}

	if !r.takeTurn(ctx, priorityLeftBytes) {
		releaseInflight()
		return false, ctx.Err()
	}

	type pieceResult struct {
		err error
	}
	done := make(chan pieceResult, 1)
	go func() {
		defer func() {
			<-r.turn
			releaseInflight()
		}()
		spans, err := findSafe(ctx, m, piece)
		if err == nil {
			found := make([]redact.Found, 0, len(spans))
			for _, s := range spans {
				if s.Start < 0 || s.End > len(piece) || s.Start >= s.End {
					continue
				}
				found = append(found, redact.Found{
					Start: s.Start,
					End:   s.End,
					Label: mapGuardLabel(s.Label),
					Score: float64(float32(s.Score)),
				})
			}
			r.cache.put(k, found)
		}
		done <- pieceResult{err: err}
	}()

	select {
	case res := <-done:
		return res.err == nil, res.err
	case <-ctx.Done():
		t := time.NewTimer(handBack)
		defer t.Stop()
		select {
		case res := <-done:
			return res.err == nil, res.err
		case <-t.C:
			return false, ctx.Err()
		}
	}
}

func (r *modelRunner) takeTurn(ctx context.Context, left int) bool {
	r.waiting.Add(1)
	defer r.waiting.Add(-1)
	w := r.queue.add(left)
	defer r.queue.remove(w)
	for {
		next, changed := r.queue.next(w)
		if !next {
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return false
			}
		}
		select {
		case r.turn <- struct{}{}:
			return true
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

func (r *modelRunner) known(texts []string) (out [][]redact.Found, miss []int) {
	out = make([][]redact.Found, len(texts))
	for i, t := range texts {
		if t == "" {
			continue
		}
		k := sha256.Sum256([]byte(t))
		if fs, ok := r.cache.get(k); ok && spansFit(fs, t) {
			out[i] = fs
			continue
		}
		ps := cutChunks(t, guardChunkSize)
		if len(ps) < 2 {
			miss = append(miss, i)
			continue
		}
		var all []redact.Found
		complete := true
		for _, p := range ps {
			sub := t[p.at:p.end]
			fs, ok := r.cache.get(sha256.Sum256([]byte(sub)))
			if !ok || !spansFit(fs, sub) {
				complete = false
				continue
			}
			for _, f := range fs {
				all = append(all, redact.Found{
					Start: f.Start + p.at,
					End:   f.End + p.at,
					Label: f.Label,
					Score: f.Score,
				})
			}
		}
		merged := spreadFound(t, mergeFound(all))
		out[i] = merged
		if complete {
			r.cache.put(k, merged)
		} else {
			miss = append(miss, i)
		}
	}
	return out, miss
}

type missingUnit struct {
	text    string
	backlog bool
}

func (r *modelRunner) missingUnits(texts []string, miss []int) ([]missingUnit, map[[32]byte]bool) {
	var raw []string
	var hashes [][32]byte
	seen := make(map[[32]byte]bool)
	for j := len(miss) - 1; j >= 0; j-- {
		t := texts[miss[j]]
		ps := cutChunks(t, guardChunkSize)
		for q := len(ps) - 1; q >= 0; q-- {
			p := ps[q]
			sub := t[p.at:p.end]
			k := sha256.Sum256([]byte(sub))
			if seen[k] {
				continue
			}
			seen[k] = true
			if fs, ok := r.cache.get(k); ok && spansFit(fs, sub) {
				continue
			}
			raw = append(raw, sub)
			hashes = append(hashes, k)
		}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	backlog := r.sent.mark(hashes)
	freshUnits := make([]missingUnit, 0, len(raw))
	backlogUnits := make([]missingUnit, 0, len(raw))
	for i, s := range raw {
		if backlog[hashes[i]] {
			backlogUnits = append(backlogUnits, missingUnit{text: s, backlog: true})
		} else {
			freshUnits = append(freshUnits, missingUnit{text: s, backlog: false})
		}
	}
	return append(freshUnits, backlogUnits...), backlog
}

// sentSet tracks hashes of chunks already sent in previous turns using a
// two-generation rotating map so old unread history is deprioritized.
type sentSet struct {
	mu        sync.Mutex
	cur, prev map[[32]byte]bool
}

func (s *sentSet) mark(ks [][32]byte) map[[32]byte]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := make(map[[32]byte]bool)
	for _, k := range ks {
		if s.cur[k] || s.prev[k] {
			was[k] = true
		}
	}
	for _, k := range ks {
		if s.cur == nil || len(s.cur) >= sentMax {
			s.prev, s.cur = s.cur, make(map[[32]byte]bool)
		}
		s.cur[k] = true
	}
	return was
}

// turnQueue orders waiting requests by shortest remaining uncached bytes first.
type turnQueue struct {
	mu      sync.Mutex
	seq     uint64
	waiters map[*turnWaiter]bool
	changed chan struct{}
}

type turnWaiter struct {
	left int
	seq  uint64
}

func (q *turnQueue) add(left int) *turnWaiter {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	w := &turnWaiter{left: left, seq: q.seq}
	if q.waiters == nil {
		q.waiters = make(map[*turnWaiter]bool)
	}
	q.waiters[w] = true
	q.notifyLocked()
	return w
}

func (q *turnQueue) remove(w *turnWaiter) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.waiters, w)
	q.notifyLocked()
}

func (q *turnQueue) notifyLocked() {
	if q.changed != nil {
		close(q.changed)
	}
	q.changed = make(chan struct{})
}

func (q *turnQueue) next(w *turnWaiter) (bool, <-chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for other := range q.waiters {
		if other.left < w.left || (other.left == w.left && other.seq < w.seq) {
			return false, q.changed
		}
	}
	return true, q.changed
}

func filterByScore(all [][]redact.Found, minScore float64) [][]redact.Found {
	out := make([][]redact.Found, len(all))
	for i, fs := range all {
		for _, f := range fs {
			if f.Score >= minScore {
				out[i] = append(out[i], f)
			}
		}
	}
	return out
}

func spansFit(fs []redact.Found, t string) bool {
	for _, f := range fs {
		if f.Start < 0 || f.End > len(t) || f.Start >= f.End {
			return false
		}
	}
	return true
}

type chunkSpan struct {
	at, end int
}

// cutChunks splits t into pieces of at most size bytes with pieceOverlap overlap,
// preferring newline and space boundaries picked deterministically by FNV-1a landmark
// hashing so prefix edits do not invalidate unchanged suffix chunks.
func cutChunks(t string, size int) []chunkSpan {
	if t == "" {
		return nil
	}
	if len(t) <= size {
		return []chunkSpan{{0, len(t)}}
	}
	var out []chunkSpan
	for at := 0; ; {
		end := at + size
		if end >= len(t) {
			return append(out, chunkSpan{at, len(t)})
		}
		half := at + size/2
		if i := landmark(t, half, end, '\n'); i >= 0 {
			end = i
		} else if i := landmark(t, half, end, ' '); i >= 0 {
			end = i
		} else {
			for end > half && !utf8.RuneStart(t[end]) {
				end--
			}
		}
		out = append(out, chunkSpan{at, end})
		next := end - pieceOverlap
		if i := strings.IndexByte(t[next:end-pieceOverlap/2], '\n'); i >= 0 {
			next += i + 1
		} else if i := strings.IndexByte(t[next:end-pieceOverlap/2], ' '); i >= 0 {
			next += i + 1
		} else {
			for next < end && !utf8.RuneStart(t[next]) {
				next++
			}
		}
		if next <= at {
			next = end
		}
		at = next
	}
}

func landmark(t string, from, to int, c byte) int {
	best, at := uint64(0), -1
	for i := from; i < to; i++ {
		if t[i] != c {
			continue
		}
		e := i + 1
		h := uint64(14695981039346656037)
		for _, b := range []byte(t[max(0, e-landmarkLen):e]) {
			h ^= uint64(b)
			h *= 1099511628211
		}
		if at < 0 || h < best {
			best, at = h, e
		}
	}
	return at
}

func mergeFound(fs []redact.Found) []redact.Found {
	if len(fs) == 0 {
		return nil
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Start != fs[j].Start {
			return fs[i].Start < fs[j].Start
		}
		return fs[i].End > fs[j].End
	})
	var out []redact.Found
	for _, f := range fs {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if f.Start < last.End || (f.Start == last.End && f.Label == last.Label) {
				if f.End-f.Start > last.End-last.Start {
					last.Label = f.Label
				}
				last.End = max(last.End, f.End)
				last.Score = max(last.Score, f.Score)
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

func spreadFound(t string, fs []redact.Found) []redact.Found {
	if len(fs) == 0 {
		return fs
	}
	type bestFind struct {
		label string
		score float64
	}
	best := make(map[string]bestFind)
	for _, f := range fs {
		v := t[f.Start:f.End]
		if len(v) < minSpread {
			continue
		}
		if b, ok := best[v]; !ok || f.Score > b.score {
			best[v] = bestFind{f.Label, f.Score}
		}
	}
	if len(best) == 0 {
		return fs
	}
	out := slices.Clone(fs)
	more := false
	for v, b := range best {
		for at := 0; ; {
			i := strings.Index(t[at:], v)
			if i < 0 {
				break
			}
			out = append(out, redact.Found{
				Start: at + i,
				End:   at + i + len(v),
				Label: b.label,
				Score: b.score,
			})
			more = true
			at += i + len(v)
		}
	}
	if !more {
		return fs
	}
	return mergeFound(out)
}

type textLRU struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[[32]byte]*list.Element
}

type lruEntry struct {
	k  [32]byte
	fs []redact.Found
}

func newTextLRU(maxEntries int) *textLRU {
	return &textLRU{
		max: maxEntries,
		ll:  list.New(),
		m:   make(map[[32]byte]*list.Element),
	}
}

func (c *textLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

func (c *textLRU) get(k [32]byte) ([]redact.Found, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(e)
	return e.Value.(*lruEntry).fs, true
}

func (c *textLRU) put(k [32]byte, fs []redact.Found) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		e.Value.(*lruEntry).fs = fs
		return
	}
	c.m[k] = c.ll.PushFront(&lruEntry{k: k, fs: fs})
	for c.ll.Len() > c.max {
		e := c.ll.Back()
		c.ll.Remove(e)
		delete(c.m, e.Value.(*lruEntry).k)
	}
}

func (c *textLRU) seed(entries []lruEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, en := range entries {
		if _, ok := c.m[en.k]; ok {
			continue
		}
		c.m[en.k] = c.ll.PushFront(&lruEntry{k: en.k, fs: en.fs})
		for c.ll.Len() > c.max {
			e := c.ll.Back()
			c.ll.Remove(e)
			delete(c.m, e.Value.(*lruEntry).k)
		}
	}
}

func (c *textLRU) snapshotOldestFirst() []lruEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]lruEntry, 0, c.ll.Len())
	for e := c.ll.Back(); e != nil; e = e.Prev() {
		en := e.Value.(*lruEntry)
		out = append(out, lruEntry{
			k:  en.k,
			fs: slices.Clone(en.fs),
		})
	}
	return out
}

func (c *textLRU) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	clear(c.m)
}
