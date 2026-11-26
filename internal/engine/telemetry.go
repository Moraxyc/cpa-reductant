package engine

import (
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	latencySampleCap   = 256
	defaultEventRing   = 100
	defaultEventReturn = 50
)

// LatencySummary summarizes execution duration in milliseconds.
type LatencySummary struct {
	Count  int64   `json:"count"`
	AvgMS  float64 `json:"avg_ms"`
	P95MS  float64 `json:"p95_ms"`
	MaxMS  float64 `json:"max_ms"`
	LastMS float64 `json:"last_ms"`
}

type latencyTracker struct {
	mu      sync.Mutex
	count   int64
	sumMS   float64
	maxMS   float64
	lastMS  float64
	samples [latencySampleCap]float64
	pos     int
	filled  int
}

func (l *latencyTracker) record(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000.0
	if ms < 0 {
		ms = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count++
	l.sumMS += ms
	l.lastMS = ms
	if ms > l.maxMS {
		l.maxMS = ms
	}
	l.samples[l.pos] = ms
	l.pos = (l.pos + 1) % latencySampleCap
	if l.filled < latencySampleCap {
		l.filled++
	}
}

func (l *latencyTracker) snapshot() LatencySummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count == 0 {
		return LatencySummary{}
	}
	p95 := l.maxMS
	if l.filled > 0 {
		buf := make([]float64, l.filled)
		copy(buf, l.samples[:l.filled])
		slices.Sort(buf)
		idx := int(math.Ceil(0.95*float64(len(buf)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(buf) {
			idx = len(buf) - 1
		}
		p95 = buf[idx]
	}
	return LatencySummary{
		Count:  l.count,
		AvgMS:  roundMS(l.sumMS / float64(l.count)),
		P95MS:  roundMS(p95),
		MaxMS:  roundMS(l.maxMS),
		LastMS: roundMS(l.lastMS),
	}
}

// DimensionStats aggregates filter activity per protocol or per model.
type DimensionStats struct {
	Requests             int64 `json:"requests"`
	Masked               int64 `json:"masked"`
	Warned               int64 `json:"warned"`
	Blocked              int64 `json:"blocked"`
	Restored             int64 `json:"restored"`
	Hits                 int64 `json:"hits"`
	PlaceholdersRestored int64 `json:"placeholders_restored"`
	BytesIn              int64 `json:"bytes_in"`
	BytesOut             int64 `json:"bytes_out"`
}

type eventRing struct {
	mu   sync.Mutex
	cap  int
	seq  uint64
	buf  []FilterEvent
	next int
	full bool
}

func newEventRing(capacity int) *eventRing {
	if capacity <= 0 {
		capacity = defaultEventRing
	}
	return &eventRing{
		cap: capacity,
		buf: make([]FilterEvent, capacity),
	}
}

func (r *eventRing) push(ev FilterEvent) FilterEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	ev.ID = r.seq
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	ev.DurationMS = roundMS(ev.DurationMS)
	r.buf[r.next] = ev
	r.next = (r.next + 1) % r.cap
	if r.next == 0 {
		r.full = true
	}
	return ev
}

func (r *eventRing) recent(limit int, actionFilter string) []FilterEvent {
	if limit <= 0 {
		return nil
	}
	actionFilter = strings.ToLower(strings.TrimSpace(actionFilter))
	r.mu.Lock()
	defer r.mu.Unlock()

	total := r.next
	if r.full {
		total = r.cap
	}
	if total == 0 {
		return nil
	}
	out := make([]FilterEvent, 0, min(limit, total))
	for i := range total {
		idx := (r.next - 1 - i + r.cap) % r.cap
		ev := r.buf[idx]
		if actionFilter != "" && !strings.EqualFold(ev.Action, actionFilter) {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out
}
