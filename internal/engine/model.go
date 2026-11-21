package engine

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
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
	landmarkLen    = 48
	minSpread      = 8
	cacheCap       = 20000
	handBack       = 50 * time.Millisecond
)

type spanFinder interface {
	Find(text string) []guard.Span
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
	mu        sync.Mutex
	cfg       config.ModelConfig
	loader    func(dir string) (spanFinder, error)
	loaded    spanFinder
	loadedDir string
	lastErr   error
	fails     int
	retryAt   time.Time

	turn     chan struct{}
	inflight map[[32]byte]chan struct{}
	cache    *textLRU
}

func newModelRunner(cfg config.ModelConfig) *modelRunner {
	cores := guard.Cores()
	if cores < 1 {
		cores = 1
	}
	return &modelRunner{
		cfg: cfg,
		loader: func(dir string) (spanFinder, error) {
			return guard.Load(dir)
		},
		turn:     make(chan struct{}, cores),
		inflight: make(map[[32]byte]chan struct{}),
		cache:    newTextLRU(cacheCap),
	}
}

func (r *modelRunner) Configure(cfg config.ModelConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prevDir := r.cfg.EffectiveDir()
	nextDir := cfg.EffectiveDir()
	if !cfg.Enabled || prevDir != nextDir {
		r.loaded = nil
		r.loadedDir = ""
		r.lastErr = nil
		r.fails = 0
		r.retryAt = time.Time{}
	}
	if prevDir != nextDir {
		r.cache.clear()
	}
	r.cfg = cfg
}

func (r *modelRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaded = nil
	r.loadedDir = ""
	r.lastErr = nil
	r.fails = 0
	r.retryAt = time.Time{}
}

func modelInstalled(dir string) bool {
	if dir == "" {
		return false
	}
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

func (r *modelRunner) ensureModel(dir string) (spanFinder, error) {
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

	if !modelInstalled(dir) {
		return nil, fmt.Errorf("%w: model files not found in %q", ErrModelUnavailable, dir)
	}

	m, err := loadSafe(loader, dir)

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.fails++
		r.lastErr = fmt.Errorf("%w: %v", ErrModelUnavailable, err)
		r.retryAt = time.Now().Add(retryBackoff(r.fails))
		return nil, r.lastErr
	}
	// Re-check whether configuration changed while loading.
	if !r.cfg.Enabled || r.cfg.EffectiveDir() != dir {
		return nil, ErrModelUnavailable
	}
	r.loaded = m
	r.loadedDir = dir
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

func findSafe(m spanFinder, text string) (spans []guard.Span, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			spans = nil
			err = fmt.Errorf("panic running guard model: %v", rec)
		}
	}()
	return m.Find(text), nil
}

// Find analyzes texts up to cfg.EffectiveWait() under ctx. On timeout, cached
// and already-finished pieces are returned so rule masking handles the rest,
// while an in-flight piece finishes into the cache for subsequent turns.
func (r *modelRunner) Find(ctx context.Context, cfg config.ModelConfig, texts []string) ([][]redact.Found, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out, miss := r.known(texts)
	if len(miss) == 0 {
		return filterByScore(out, cfg.EffectiveMinScore()), nil
	}

	m, err := r.ensureModel(cfg.EffectiveDir())
	if err != nil {
		if cfg.Strict {
			return nil, err
		}
		return filterByScore(out, cfg.EffectiveMinScore()), nil
	}

	wait := cfg.EffectiveWait()
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}

	// Process newest texts and newest pieces first: chat histories append new
	// turns at the end, while older turns are most likely unchanged/cached.
	units := r.missingUnits(texts, miss)
	for _, piece := range units {
		if ctx.Err() != nil {
			break
		}
		if err := r.analyzePiece(ctx, m, piece); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				break
			}
			if cfg.Strict {
				return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
			}
			break
		}
	}

	out, _ = r.known(texts)
	return filterByScore(out, cfg.EffectiveMinScore()), nil
}

func (r *modelRunner) analyzePiece(ctx context.Context, m spanFinder, piece string) error {
	k := sha256.Sum256([]byte(piece))
	if _, ok := r.cache.get(k); ok {
		return nil
	}

	// Deduplicate concurrent analyses of identical chunks.
	r.mu.Lock()
	if ch, busy := r.inflight[k]; busy {
		r.mu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
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

	select {
	case r.turn <- struct{}{}:
	case <-ctx.Done():
		releaseInflight()
		return ctx.Err()
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
		spans, err := findSafe(m, piece)
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
		return res.err
	case <-ctx.Done():
		t := time.NewTimer(handBack)
		defer t.Stop()
		select {
		case res := <-done:
			return res.err
		case <-t.C:
			return ctx.Err()
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

func (r *modelRunner) missingUnits(texts []string, miss []int) []string {
	var out []string
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
			out = append(out, sub)
		}
	}
	return out
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

func (c *textLRU) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	clear(c.m)
}
