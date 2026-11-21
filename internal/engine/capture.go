package engine

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/pkg/redact"
)

const (
	defaultCaptureTTL      = 5 * time.Minute
	maxCaptureTTL          = 30 * time.Minute
	defaultCaptureCapacity = 30
	maxCaptureCapacity     = 100
	maxCaptureBodyBytes    = 128 * 1024
	captureHeadBytes       = 16 * 1024
	captureMarkerReserve   = 64
)

// CaptureReplacement records one detected sensitive value together with its
// resolved plaintext (for masked/blocked items) and emitted placeholder/stand-in.
type CaptureReplacement struct {
	Kind        string `json:"kind"`
	By          string `json:"by,omitempty"`
	Action      string `json:"action"`
	Placeholder string `json:"placeholder,omitempty"`
	Original    string `json:"original,omitempty"`
	Head        string `json:"head,omitempty"`
	Len         int    `json:"len,omitempty"`
}

// CaptureRestoredItem records one placeholder/stand-in restored back to its
// original plaintext in an upstream response or stream.
type CaptureRestoredItem struct {
	Placeholder string `json:"placeholder"`
	Original    string `json:"original,omitempty"`
	Count       int    `json:"count"`
}

// DebugCaptureEntry holds a single correlated request/response debug capture.
type DebugCaptureEntry struct {
	ID                        uint64                `json:"id"`
	Timestamp                 string                `json:"timestamp"`
	Status                    string                `json:"status,omitempty"`
	Protocol                  string                `json:"protocol,omitempty"`
	Model                     string                `json:"model,omitempty"`
	Stream                    bool                  `json:"stream,omitempty"`
	Action                    string                `json:"action"`
	MaskDurationMS            float64               `json:"mask_duration_ms"`
	RestoreDurationMS         float64               `json:"restore_duration_ms,omitempty"`
	RawRequest                string                `json:"raw_request,omitempty"`
	RawRequestTruncated       bool                  `json:"raw_request_truncated,omitempty"`
	RawRequestBytes           int                   `json:"raw_request_bytes,omitempty"`
	MaskedRequest             string                `json:"masked_request,omitempty"`
	MaskedRequestTruncated    bool                  `json:"masked_request_truncated,omitempty"`
	MaskedRequestBytes        int                   `json:"masked_request_bytes,omitempty"`
	Replacements              []CaptureReplacement  `json:"replacements,omitempty"`
	UpstreamResponse          string                `json:"upstream_response,omitempty"`
	UpstreamResponseTruncated bool                  `json:"upstream_response_truncated,omitempty"`
	UpstreamResponseBytes     int                   `json:"upstream_response_bytes,omitempty"`
	RestoredResponse          string                `json:"restored_response,omitempty"`
	RestoredResponseTruncated bool                  `json:"restored_response_truncated,omitempty"`
	RestoredResponseBytes     int                   `json:"restored_response_bytes,omitempty"`
	ResponseBinary            bool                  `json:"response_binary,omitempty"`
	ResponseContentType       string                `json:"response_content_type,omitempty"`
	RestoredCount             int                   `json:"restored_count,omitempty"`
	RestoredItems             []CaptureRestoredItem `json:"restored_items,omitempty"`
	Error                     string                `json:"error,omitempty"`
}

// DebugCaptureSnapshot is returned by the debug capture management API.
type DebugCaptureSnapshot struct {
	Enabled          bool                `json:"enabled"`
	EnabledAt        string              `json:"enabled_at,omitempty"`
	ExpiresAt        string              `json:"expires_at,omitempty"`
	RemainingSeconds int64               `json:"remaining_seconds,omitempty"`
	MaxEntries       int                 `json:"max_entries"`
	MaxBodyBytes     int                 `json:"max_body_bytes"`
	EntriesCount     int                 `json:"entries_count"`
	Entries          []DebugCaptureEntry `json:"entries"`
}

// MaxCaptureBodyBytes returns the per-field capture size limit in bytes.
func MaxCaptureBodyBytes() int {
	return maxCaptureBodyBytes
}

type debugCapture struct {
	mu        sync.Mutex
	enabled   bool
	enabledAt time.Time
	expiresAt time.Time
	cap       int
	seq       uint64
	buf       []DebugCaptureEntry
	next      int
	full      bool
}

func newDebugCapture(capacity int) *debugCapture {
	if capacity <= 0 {
		capacity = defaultCaptureCapacity
	}
	if capacity > maxCaptureCapacity {
		capacity = maxCaptureCapacity
	}
	return &debugCapture{
		cap: capacity,
		buf: make([]DebugCaptureEntry, capacity),
	}
}

func (d *debugCapture) checkExpiryLocked(now time.Time) {
	if d.enabled && !d.expiresAt.IsZero() && !now.Before(d.expiresAt) {
		d.enabled = false
		d.enabledAt = time.Time{}
		d.expiresAt = time.Time{}
	}
}

func (d *debugCapture) beginIfEnabled() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checkExpiryLocked(time.Now().UTC())
	if !d.enabled {
		return 0
	}
	d.seq++
	return d.seq
}

func (d *debugCapture) recordRequest(entry DebugCaptureEntry) {
	if entry.ID == 0 {
		return
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	entry.MaskDurationMS = roundMS(entry.MaskDurationMS)

	d.mu.Lock()
	defer d.mu.Unlock()
	if idx := d.findIndexLocked(entry.ID); idx >= 0 {
		d.buf[idx] = entry
		return
	}
	d.buf[d.next] = entry
	d.next = (d.next + 1) % d.cap
	if d.next == 0 {
		d.full = true
	}
}

func (d *debugCapture) update(id uint64, fn func(*DebugCaptureEntry)) {
	if id == 0 || fn == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if idx := d.findIndexLocked(id); idx >= 0 {
		fn(&d.buf[idx])
	}
}

func (d *debugCapture) findIndexLocked(id uint64) int {
	total := d.next
	if d.full {
		total = d.cap
	}
	for i := range total {
		idx := (d.next - 1 - i + d.cap) % d.cap
		if d.buf[idx].ID == id {
			return idx
		}
	}
	return -1
}

func (d *debugCapture) resizeLocked(newCap int) {
	if newCap <= 0 {
		return
	}
	if newCap > maxCaptureCapacity {
		newCap = maxCaptureCapacity
	}
	if newCap == d.cap {
		return
	}
	existing := d.orderedEntriesLocked()
	if len(existing) > newCap {
		existing = existing[len(existing)-newCap:]
	}
	nextBuf := make([]DebugCaptureEntry, newCap)
	copy(nextBuf, existing)
	d.cap = newCap
	d.buf = nextBuf
	d.next = len(existing) % newCap
	d.full = len(existing) == newCap
}

// orderedEntriesLocked returns stored entries from oldest to newest.
func (d *debugCapture) orderedEntriesLocked() []DebugCaptureEntry {
	total := d.next
	if d.full {
		total = d.cap
	}
	if total == 0 {
		return nil
	}
	out := make([]DebugCaptureEntry, 0, total)
	start := 0
	if d.full {
		start = d.next
	}
	for i := range total {
		idx := (start + i) % d.cap
		out = append(out, d.buf[idx])
	}
	return out
}

func (d *debugCapture) clearLocked() {
	for i := range d.buf {
		d.buf[i] = DebugCaptureEntry{}
	}
	d.next = 0
	d.full = false
}

func (d *debugCapture) configure(enabled bool, ttl time.Duration, maxEntries int, clear bool) DebugCaptureSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	if maxEntries > 0 {
		d.resizeLocked(maxEntries)
	}
	if clear {
		d.clearLocked()
	}
	if enabled {
		if ttl <= 0 {
			ttl = defaultCaptureTTL
		}
		if ttl > maxCaptureTTL {
			ttl = maxCaptureTTL
		}
		d.enabled = true
		d.enabledAt = now
		d.expiresAt = now.Add(ttl)
	} else {
		d.enabled = false
		d.enabledAt = time.Time{}
		d.expiresAt = time.Time{}
	}
	return d.snapshotLocked(now, 0, 0, "", "")
}

func (d *debugCapture) clear() DebugCaptureSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	d.checkExpiryLocked(now)
	d.clearLocked()
	return d.snapshotLocked(now, 0, 0, "", "")
}

func (d *debugCapture) snapshot(limit int, idFilter uint64, protocolFilter, modelFilter string) DebugCaptureSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	d.checkExpiryLocked(now)
	return d.snapshotLocked(now, limit, idFilter, protocolFilter, modelFilter)
}

func (d *debugCapture) snapshotLocked(now time.Time, limit int, idFilter uint64, protocolFilter, modelFilter string) DebugCaptureSnapshot {
	protocolFilter = strings.TrimSpace(protocolFilter)
	modelFilter = strings.TrimSpace(modelFilter)

	total := d.next
	if d.full {
		total = d.cap
	}
	entries := make([]DebugCaptureEntry, 0, total)
	for i := range total {
		idx := (d.next - 1 - i + d.cap) % d.cap
		ev := d.buf[idx]
		if idFilter != 0 && ev.ID != idFilter {
			continue
		}
		if protocolFilter != "" && !strings.EqualFold(ev.Protocol, protocolFilter) {
			continue
		}
		if modelFilter != "" && ev.Model != modelFilter {
			continue
		}
		if len(ev.Replacements) > 0 {
			ev.Replacements = slices.Clone(ev.Replacements)
		}
		if len(ev.RestoredItems) > 0 {
			ev.RestoredItems = slices.Clone(ev.RestoredItems)
		}
		entries = append(entries, ev)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}

	out := DebugCaptureSnapshot{
		Enabled:      d.enabled,
		MaxEntries:   d.cap,
		MaxBodyBytes: maxCaptureBodyBytes,
		EntriesCount: total,
		Entries:      entries,
	}
	if d.enabled {
		out.EnabledAt = d.enabledAt.Format(time.RFC3339)
		if !d.expiresAt.IsZero() {
			out.ExpiresAt = d.expiresAt.Format(time.RFC3339)
			rem := int64(d.expiresAt.Sub(now).Seconds())
			if rem < 0 {
				rem = 0
			}
			out.RemainingSeconds = rem
		}
	}
	return out
}

func captureHeadTailBudgets(limit int) (headCap, tailCap int, headTail bool) {
	if limit < 256 {
		return limit, 0, false
	}
	headCap = min(captureHeadBytes, limit/8)
	tailCap = limit - headCap - captureMarkerReserve
	if headCap <= 0 || tailCap <= 0 {
		return limit, 0, false
	}
	return headCap, tailCap, true
}

func trimIncompleteTrailingRune(b []byte) []byte {
	n := len(b)
	if n == 0 {
		return b
	}
	minStart := max(0, n-utf8.UTFMax)
	for i := n - 1; i >= minStart; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			return b
		}
	}
	return b
}

func trimIncompleteLeadingRune(b []byte) []byte {
	limit := min(len(b), utf8.UTFMax)
	i := 0
	for i < limit && !utf8.RuneStart(b[i]) {
		i++
	}
	return b[i:]
}

func formatHeadTailBytes(head, tail []byte, total int) []byte {
	validHead := trimIncompleteTrailingRune(head)
	validTail := trimIncompleteLeadingRune(tail)
	omitted := total - len(validHead) - len(validTail)
	if omitted <= 0 {
		out := make([]byte, 0, len(validHead)+len(validTail))
		out = append(out, validHead...)
		out = append(out, validTail...)
		return out
	}
	marker := fmt.Sprintf("\n... [omitted %d bytes] ...\n", omitted)
	out := make([]byte, 0, len(validHead)+len(marker)+len(validTail))
	out = append(out, validHead...)
	out = append(out, marker...)
	out = append(out, validTail...)
	return out
}

func truncateCaptureBytes(b []byte, limit int) (string, int, bool) {
	total := len(b)
	if total == 0 {
		return "", 0, false
	}
	if limit <= 0 || total <= limit {
		return string(b), total, false
	}
	headCap, tailCap, ok := captureHeadTailBudgets(limit)
	if !ok {
		cut := limit
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		if cut == 0 {
			cut = limit
		}
		return string(b[:cut]), total, true
	}
	out := formatHeadTailBytes(b[:headCap], b[total-tailCap:], total)
	return string(out), total, true
}

// CaptureBuffer stores bounded request/response payloads while preserving both
// the leading context and the latest trailing bytes when exceeding Limit.
type CaptureBuffer struct {
	limit    int
	headCap  int
	tailCap  int
	headTail bool
	total    int
	overflow bool
	headBuf  []byte
	tailRing []byte
	tailPos  int
	tailLen  int
}

func NewCaptureBuffer(limit int) *CaptureBuffer {
	if limit <= 0 {
		limit = maxCaptureBodyBytes
	}
	headCap, tailCap, headTail := captureHeadTailBudgets(limit)
	return &CaptureBuffer{
		limit:    limit,
		headCap:  headCap,
		tailCap:  tailCap,
		headTail: headTail,
	}
}

func (b *CaptureBuffer) Write(p []byte) {
	if b == nil || len(p) == 0 {
		return
	}
	if !b.headTail {
		b.total += len(p)
		if len(b.headBuf) < b.limit {
			remain := b.limit - len(b.headBuf)
			if len(p) <= remain {
				b.headBuf = append(b.headBuf, p...)
			} else {
				b.headBuf = append(b.headBuf, p[:remain]...)
				b.overflow = true
			}
		} else {
			b.overflow = true
		}
		return
	}
	if !b.overflow {
		if b.total+len(p) <= b.limit {
			b.headBuf = append(b.headBuf, p...)
			b.total += len(p)
			return
		}
		b.overflow = true
		b.tailRing = make([]byte, b.tailCap)
		if len(b.headBuf) >= b.headCap {
			rem := b.headBuf[b.headCap:]
			b.headBuf = slices.Clone(b.headBuf[:b.headCap])
			b.writeTail(rem)
			b.writeTail(p)
		} else {
			need := b.headCap - len(b.headBuf)
			if len(p) <= need {
				b.headBuf = append(b.headBuf, p...)
			} else {
				b.headBuf = append(b.headBuf, p[:need]...)
				b.writeTail(p[need:])
			}
		}
		b.total += len(p)
		return
	}
	b.writeTail(p)
	b.total += len(p)
}

func (b *CaptureBuffer) writeTail(p []byte) {
	if b.tailCap <= 0 || len(p) == 0 {
		return
	}
	if len(p) >= b.tailCap {
		copy(b.tailRing, p[len(p)-b.tailCap:])
		b.tailPos = 0
		b.tailLen = b.tailCap
		return
	}
	first := min(len(p), b.tailCap-b.tailPos)
	copy(b.tailRing[b.tailPos:], p[:first])
	if first < len(p) {
		copy(b.tailRing[0:], p[first:])
	}
	b.tailPos = (b.tailPos + len(p)) % b.tailCap
	b.tailLen = min(b.tailCap, b.tailLen+len(p))
}

func (b *CaptureBuffer) Snapshot() ([]byte, int, bool) {
	if b == nil || b.total == 0 {
		return nil, 0, false
	}
	if !b.overflow {
		return slices.Clone(b.headBuf), b.total, false
	}
	if !b.headTail {
		valid := trimIncompleteTrailingRune(b.headBuf)
		return slices.Clone(valid), b.total, true
	}
	tail := make([]byte, b.tailLen)
	if b.tailLen < b.tailCap {
		copy(tail, b.tailRing[:b.tailLen])
	} else {
		n := copy(tail, b.tailRing[b.tailPos:])
		copy(tail[n:], b.tailRing[:b.tailPos])
	}
	return formatHeadTailBytes(b.headBuf, tail, b.total), b.total, true
}

func resolveCaptureReplacements(hits []redact.Hit, shape bool) []CaptureReplacement {
	if len(hits) == 0 {
		return nil
	}
	out := make([]CaptureReplacement, 0, len(hits))
	for _, h := range hits {
		action := h.Action
		if action == "" {
			action = "mask"
		}
		orig := ""
		if h.Placeholder != "" && action != redact.Warn {
			orig = lookupOriginalValue(h.Placeholder, shape)
		}
		out = append(out, CaptureReplacement{
			Kind:        h.Kind,
			By:          h.By,
			Action:      action,
			Placeholder: h.Placeholder,
			Original:    orig,
			Head:        h.Head,
			Len:         h.Len,
		})
	}
	return out
}

func lookupOriginalValue(placeholder string, shape bool) string {
	if v, ok := redact.Value(placeholder); ok {
		return v
	}
	if shape {
		if idx := strings.IndexAny(placeholder, "=:"); idx >= 0 {
			restStart := idx + 1
			for restStart < len(placeholder) && (placeholder[restStart] == ' ' || placeholder[restStart] == '\t') {
				restStart++
			}
			if k, ok := redact.Value(placeholder[restStart:]); ok {
				return placeholder[:restStart] + k
			}
		}
	}
	return ""
}

func resolveCaptureRestoredItems(put map[string]int, shape bool) []CaptureRestoredItem {
	if len(put) == 0 {
		return nil
	}
	keys := make([]string, 0, len(put))
	for k, n := range put {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	out := make([]CaptureRestoredItem, 0, len(keys))
	for _, k := range keys {
		out = append(out, CaptureRestoredItem{
			Placeholder: k,
			Original:    lookupOriginalValue(k, shape),
			Count:       put[k],
		})
	}
	return out
}
