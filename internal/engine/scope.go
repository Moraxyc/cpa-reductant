package engine

import (
	"bytes"
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/pkg/redact"
)

const (
	defaultScopeMaxScopes   = 4096
	defaultScopeMaxEntries  = 10000
	defaultScopeTTL         = 24 * time.Hour
)

// ScopeStats exposes multi-tenant scope isolation telemetry.
type ScopeStats struct {
	Enabled         bool  `json:"enabled"`
	ActiveScopes    int   `json:"active_scopes"`
	TrackedMappings int   `json:"tracked_mappings"`
	BlockedRestores int64 `json:"blocked_restores"`
}

type scopeEntry struct {
	scope      string
	lastUsed   time.Time
	items      map[string]*list.Element // placeholder -> *list.Element of string
	order      *list.List               // MRU at front
}

type scopeVault struct {
	mu              sync.Mutex
	maxScopes       int
	maxPerScope     int
	ttl             time.Duration
	scopes          map[string]*list.Element // scope -> *list.Element of *scopeEntry
	scopeOrder      *list.List               // MRU at front
	totalMappings   int
	blockedRestores atomic.Int64
}

func newScopeVault(maxScopes, maxPerScope int, ttl time.Duration) *scopeVault {
	if maxScopes <= 0 {
		maxScopes = defaultScopeMaxScopes
	}
	if maxPerScope <= 0 {
		maxPerScope = defaultScopeMaxEntries
	}
	if ttl <= 0 {
		ttl = defaultScopeTTL
	}
	return &scopeVault{
		maxScopes:   maxScopes,
		maxPerScope: maxPerScope,
		ttl:         ttl,
		scopes:      make(map[string]*list.Element),
		scopeOrder:  list.New(),
	}
}

func (v *scopeVault) SetTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = defaultScopeTTL
	}
	v.mu.Lock()
	v.ttl = ttl
	v.mu.Unlock()
}

func (v *scopeVault) pruneExpiredLocked(now time.Time) {
	if v.ttl <= 0 {
		return
	}
	cutoff := now.Add(-v.ttl)
	for elem := v.scopeOrder.Back(); elem != nil; {
		prev := elem.Prev()
		entry := elem.Value.(*scopeEntry)
		if entry.lastUsed.After(cutoff) {
			break
		}
		v.removeScopeElemLocked(elem)
		elem = prev
	}
}

func (v *scopeVault) removeScopeElemLocked(elem *list.Element) {
	entry := elem.Value.(*scopeEntry)
	v.totalMappings -= len(entry.items)
	delete(v.scopes, entry.scope)
	v.scopeOrder.Remove(elem)
}

func (v *scopeVault) Record(scope string, placeholders ...string) {
	scope = strings.TrimSpace(scope)
	if scope == "" || len(placeholders) == 0 {
		return
	}
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneExpiredLocked(now)

	elem, ok := v.scopes[scope]
	var entry *scopeEntry
	if ok {
		entry = elem.Value.(*scopeEntry)
		entry.lastUsed = now
		v.scopeOrder.MoveToFront(elem)
	} else {
		for len(v.scopes) >= v.maxScopes {
			if back := v.scopeOrder.Back(); back != nil {
				v.removeScopeElemLocked(back)
			} else {
				break
			}
		}
		entry = &scopeEntry{
			scope:    scope,
			lastUsed: now,
			items:    make(map[string]*list.Element),
			order:    list.New(),
		}
		elem = v.scopeOrder.PushFront(entry)
		v.scopes[scope] = elem
	}

	for _, p := range placeholders {
		if p == "" {
			continue
		}
		if itemElem, exists := entry.items[p]; exists {
			entry.order.MoveToFront(itemElem)
			continue
		}
		for len(entry.items) >= v.maxPerScope {
			if oldest := entry.order.Back(); oldest != nil {
				oldKey := oldest.Value.(string)
				delete(entry.items, oldKey)
				entry.order.Remove(oldest)
				v.totalMappings--
			} else {
				break
			}
		}
		itemElem := entry.order.PushFront(p)
		entry.items[p] = itemElem
		v.totalMappings++
	}
}

func (v *scopeVault) HasAny(scope string) bool {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return false
	}
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneExpiredLocked(now)
	elem, ok := v.scopes[scope]
	if !ok {
		return false
	}
	entry := elem.Value.(*scopeEntry)
	return len(entry.items) > 0
}

func (v *scopeVault) Allows(scope, placeholder string) bool {
	scope = strings.TrimSpace(scope)
	if scope == "" || placeholder == "" {
		return false
	}
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneExpiredLocked(now)
	elem, ok := v.scopes[scope]
	if !ok {
		return false
	}
	entry := elem.Value.(*scopeEntry)
	itemElem, exists := entry.items[placeholder]
	if !exists {
		return false
	}
	entry.lastUsed = now
	v.scopeOrder.MoveToFront(elem)
	entry.order.MoveToFront(itemElem)
	return true
}

func (v *scopeVault) recordBlocked(n int64) {
	if n > 0 {
		v.blockedRestores.Add(n)
	}
}

func (v *scopeVault) Snapshot(enabled bool) ScopeStats {
	now := time.Now()
	v.mu.Lock()
	v.pruneExpiredLocked(now)
	active := len(v.scopes)
	tracked := v.totalMappings
	v.mu.Unlock()
	return ScopeStats{
		Enabled:         enabled,
		ActiveScopes:    active,
		TrackedMappings: tracked,
		BlockedRestores: v.blockedRestores.Load(),
	}
}

// StreamWriter abstracts redact.Writer so both unscoped and CallerScope-isolated
// stream writers share a single interface in the adapter.
type StreamWriter interface {
	Header() http.Header
	WriteHeader(int)
	Write([]byte) (int, error)
	Finish()
	Put() map[string]int
}

var scopePlaceholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_]*_[A-Za-z2-7]{8})\s*\}\}|\b([A-Za-z][A-Za-z0-9_]*_[A-Za-z2-7]{8})\b`)

func canonicalPlaceholder(body string) string {
	idx := strings.LastIndexByte(body, '_')
	if idx <= 0 {
		return ""
	}
	return "{{" + strings.ToUpper(body[:idx]) + "_" + strings.ToLower(body[idx+1:]) + "}}"
}

type scopedStreamWriter struct {
	dst      http.ResponseWriter
	vault    *scopeVault
	scope    string
	shape    bool
	innerBuf *restoreBuffer
	inner    *redact.Writer
	bypass   bool

	sentinelPrefix string
	shields        map[string]string // sentinel -> original matched text
	prevPut        map[string]int
	allowedPut     map[string]int
}

func newScopedStreamWriter(w http.ResponseWriter, vault *scopeVault, scope string, shape bool) StreamWriter {
	scope = strings.TrimSpace(scope)
	if vault == nil || scope == "" {
		return redact.NewWriter(w)
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	innerBuf := &restoreBuffer{header: w.Header()}
	return &scopedStreamWriter{
		dst:            w,
		vault:          vault,
		scope:          scope,
		shape:          shape,
		innerBuf:       innerBuf,
		inner:          redact.NewWriter(innerBuf),
		bypass:         !vault.HasAny(scope),
		sentinelPrefix: "__CPASCOPE_" + hex.EncodeToString(rnd[:]) + "_",
		shields:        make(map[string]string),
		prevPut:        make(map[string]int),
		allowedPut:     make(map[string]int),
	}
}

func (sw *scopedStreamWriter) Header() http.Header {
	return sw.dst.Header()
}

func (sw *scopedStreamWriter) WriteHeader(code int) {
	sw.dst.WriteHeader(code)
}

func (sw *scopedStreamWriter) shieldUnauthorized(p []byte) []byte {
	if len(p) == 0 || !bytes.ContainsAny(p, "_") {
		return p
	}
	matches := scopePlaceholderRE.FindAllSubmatchIndex(p, -1)
	if len(matches) == 0 {
		return p
	}
	var out bytes.Buffer
	last := 0
	changed := false
	for _, m := range matches {
		fullStart, fullEnd := m[0], m[1]
		bodyStart, bodyEnd := m[2], m[3]
		if bodyStart < 0 {
			bodyStart, bodyEnd = m[4], m[5]
		}
		canon := canonicalPlaceholder(string(p[bodyStart:bodyEnd]))
		if canon == "" {
			continue
		}
		// Only shield placeholders that actually exist in Magpie's global store
		// AND are not authorized for this CallerScope.
		if _, exists := redact.Value(canon); !exists {
			continue
		}
		if sw.vault.Allows(sw.scope, canon) {
			continue
		}
		if !changed {
			out.Grow(len(p) + 32)
			changed = true
		}
		out.Write(p[last:fullStart])
		rawMatch := string(p[fullStart:fullEnd])
		token := sw.sentinelPrefix + hex.EncodeToString([]byte{byte(len(sw.shields) >> 8), byte(len(sw.shields))}) + "__"
		sw.shields[token] = rawMatch
		out.WriteString(token)
		last = fullEnd
		sw.vault.recordBlocked(1)
	}
	if !changed {
		return p
	}
	out.Write(p[last:])
	return out.Bytes()
}

func (sw *scopedStreamWriter) unshieldAndVerify(emitted []byte) []byte {
	if len(emitted) == 0 {
		return emitted
	}
	// 1. Check inner.Put() delta for any split-across-chunks unauthorized restoration.
	curPut := sw.inner.Put()
	for p, count := range curPut {
		delta := count - sw.prevPut[p]
		if delta <= 0 {
			continue
		}
		sw.prevPut[p] = count
		if sw.vault.Allows(sw.scope, p) {
			sw.allowedPut[p] += delta
			continue
		}
		// Defense in depth: re-seal unauthorized placeholder restored across chunk boundary.
		sw.vault.recordBlocked(int64(delta))
		if orig := lookupOriginalValue(p, sw.shape); orig != "" {
			emitted = resealValue(emitted, orig, p)
		}
	}
	// 2. Restore shielded sentinels back to the literal unauthorized placeholder strings.
	if len(sw.shields) > 0 && bytes.Contains(emitted, []byte(sw.sentinelPrefix)) {
		s := string(emitted)
		for token, raw := range sw.shields {
			if strings.Contains(s, token) {
				s = strings.ReplaceAll(s, token, raw)
				delete(sw.shields, token)
			}
		}
		emitted = []byte(s)
	}
	return emitted
}

func resealValue(b []byte, orig, placeholder string) []byte {
	out := bytes.ReplaceAll(b, []byte(orig), []byte(placeholder))
	if encoded, err := json.Marshal(orig); err == nil && len(encoded) >= 2 {
		escapedOrig := encoded[1 : len(encoded)-1]
		if !bytes.Equal(escapedOrig, []byte(orig)) {
			out = bytes.ReplaceAll(out, escapedOrig, []byte(placeholder))
		}
	}
	return out
}

func (sw *scopedStreamWriter) Write(p []byte) (int, error) {
	if sw.bypass {
		// Record blocked attempts if the response references a placeholder from another scope.
		if bytes.ContainsAny(p, "_") {
			for _, m := range scopePlaceholderRE.FindAllSubmatchIndex(p, -1) {
				bodyStart, bodyEnd := m[2], m[3]
				if bodyStart < 0 {
					bodyStart, bodyEnd = m[4], m[5]
				}
				if canon := canonicalPlaceholder(string(p[bodyStart:bodyEnd])); canon != "" {
					if _, exists := redact.Value(canon); exists {
						sw.vault.recordBlocked(1)
					}
				}
			}
		}
		return sw.dst.Write(p)
	}
	shielded := sw.shieldUnauthorized(p)
	if _, err := sw.inner.Write(shielded); err != nil {
		return 0, err
	}
	if sw.innerBuf.Len() > 0 {
		out := sw.unshieldAndVerify(bytes.Clone(sw.innerBuf.Bytes()))
		sw.innerBuf.Reset()
		if _, err := sw.dst.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (sw *scopedStreamWriter) Finish() {
	if sw.bypass {
		return
	}
	sw.inner.Finish()
	if sw.innerBuf.Len() > 0 {
		out := sw.unshieldAndVerify(bytes.Clone(sw.innerBuf.Bytes()))
		sw.innerBuf.Reset()
		_, _ = sw.dst.Write(out)
	}
}

func (sw *scopedStreamWriter) Put() map[string]int {
	if sw.bypass {
		return nil
	}
	return sw.allowedPut
}
