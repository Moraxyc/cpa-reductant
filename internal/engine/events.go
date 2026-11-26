package engine

import (
	"math"

	"github.com/yetone/magpie/pkg/redact"
)

// Logger emits structured plugin log events (wired to CPA host.log in adapter).
type Logger func(level, callbackID, msg string, fields map[string]any)

// RequestMeta describes the CPA request/response context for telemetry, alerts, and logging.
type RequestMeta struct {
	Route          string
	Protocol       string
	Model          string
	Stream         bool
	HostCallbackID string
	CaptureID      uint64
	CallerScope    string
}

// EventHit is a sanitized record of a single matched item (never holds plaintext).
type EventHit struct {
	Kind        string `json:"kind"`
	By          string `json:"by,omitempty"`
	Action      string `json:"action"`
	Placeholder string `json:"placeholder,omitempty"`
	Head        string `json:"head,omitempty"`
	Len         int    `json:"len,omitempty"`
}

func toEventHits(hits []redact.Hit) []EventHit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]EventHit, 0, len(hits))
	for _, h := range hits {
		action := h.Action
		if action == "" {
			action = "mask"
		}
		out = append(out, EventHit{
			Kind:        h.Kind,
			By:          h.By,
			Action:      action,
			Placeholder: h.Placeholder,
			Head:        h.Head,
			Len:         h.Len,
		})
	}
	return out
}

// FilterEvent represents one sanitized request, restore, or model event.
type FilterEvent struct {
	ID            uint64     `json:"id"`
	Timestamp     string     `json:"timestamp"`
	Phase         string     `json:"phase"`
	Protocol      string     `json:"protocol,omitempty"`
	Model         string     `json:"model,omitempty"`
	Action        string     `json:"action"`
	DurationMS    float64    `json:"duration_ms"`
	Bytes         int        `json:"bytes,omitempty"`
	RestoredCount int        `json:"restored_count,omitempty"`
	Error         string     `json:"error,omitempty"`
	Hits          []EventHit `json:"hits,omitempty"`
}

func roundMS(v float64) float64 {
	return math.Round(v*100) / 100
}

func sumPutCounts(put map[string]int) int {
	total := 0
	for _, n := range put {
		if n > 0 {
			total += n
		}
	}
	return total
}
