package engine

import (
	"bytes"
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultScopeMaxScopes  = 4096
	defaultScopeMaxEntries = 10000
	defaultScopeTTL        = 24 * time.Hour
)

type scopeIsolator interface {
	Record(scope string, placeholders ...string)
	SetTTL(ttl time.Duration)
	Snapshot(enabled bool) ScopeStats
	WrapWriter(w http.ResponseWriter, store placeholderStore, scope string, shape bool) StreamWriter
}

// ScopeStats exposes multi-tenant scope isolation telemetry.
type ScopeStats struct {
	Enabled         bool  `json:"enabled"`
	ActiveScopes    int   `json:"active_scopes"`
	TrackedMappings int   `json:"tracked_mappings"`
	BlockedRestores int64 `json:"blocked_restores"`
}

type scopeEntry struct {
	scope    string
	lastUsed time.Time
	items    map[string]*list.Element // placeholder -> *list.Element of string
	order    *list.List               // MRU at front
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

func (v *scopeVault) WrapWriter(w http.ResponseWriter, store placeholderStore, scope string, shape bool) StreamWriter {
	return newScopedStreamWriter(w, v, store, scope, shape)
}

var scopePlaceholderBodyRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*_[A-Za-z2-7]{8}`)

type scopePlaceholderMatch struct {
	start int
	end   int
	canon string
}

func isScopeASCIILetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isScopeWordByte(c byte) bool {
	return isScopeASCIILetter(c) || (c >= '0' && c <= '9') || c == '_'
}

// scopeEncodedBefore mirrors Magpie's redact.encodedBefore so percent-encoded
// braces (%7B/%7b) and optional %20 spaces before a placeholder body are
// recognized as non-word boundaries.
func scopeEncodedBefore(pre []byte) bool {
	for bytes.HasSuffix(pre, []byte("%20")) {
		pre = pre[:len(pre)-3]
	}
	return len(pre) >= 3 && bytes.EqualFold(pre[len(pre)-3:], []byte("%7B"))
}

func findScopePlaceholders(p []byte) []scopePlaceholderMatch {
	if len(p) == 0 || bytes.IndexByte(p, '_') < 0 {
		return nil
	}
	indices := scopePlaceholderBodyRE.FindAllIndex(p, -1)
	if len(indices) == 0 {
		return nil
	}
	var out []scopePlaceholderMatch
	last := 0
	for _, m := range indices {
		a, z := m[0], m[1]
		if a >= 2 && p[a-2] == '%' && p[a-1] == '7' && (p[a] == 'B' || p[a] == 'b') && a+1 < z && isScopeASCIILetter(p[a+1]) {
			a++
		}
		if a < last || (a > 0 && isScopeWordByte(p[a-1]) && !scopeEncodedBefore(p[:a])) || (z < len(p) && isScopeWordByte(p[z])) {
			continue
		}
		canon := canonicalPlaceholder(string(p[a:z]))
		if canon == "" {
			continue
		}
		out = append(out, scopePlaceholderMatch{start: a, end: z, canon: canon})
		last = z
	}
	return out
}

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
	store    placeholderStore
	scope    string
	shape    bool
	innerBuf *restoreBuffer
	inner    StreamWriter
	bypass   bool

	sentinelPrefix string
	shields        map[string]string // sentinel -> original matched text
	prevPut        map[string]int
	allowedPut     map[string]int
}

func newScopedStreamWriter(w http.ResponseWriter, vault *scopeVault, store placeholderStore, scope string, shape bool) StreamWriter {
	if store == nil {
		store = magpiePlaceholderStore{}
	}
	scope = strings.TrimSpace(scope)
	if vault == nil || scope == "" {
		return store.NewWriter(w)
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	innerBuf := &restoreBuffer{header: w.Header()}
	return &scopedStreamWriter{
		dst:            w,
		vault:          vault,
		store:          store,
		scope:          scope,
		shape:          shape,
		innerBuf:       innerBuf,
		inner:          store.NewWriter(innerBuf),
		bypass:         !vault.HasAny(scope),
		sentinelPrefix: "@CPASCOPE:" + hex.EncodeToString(rnd[:]) + ":",
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
	matches := findScopePlaceholders(p)
	if len(matches) == 0 {
		return p
	}
	var out bytes.Buffer
	last := 0
	changed := false
	for _, m := range matches {
		// Only shield placeholders that actually exist in the placeholder store
		// AND are not authorized for this CallerScope.
		if !sw.store.HasValue(m.canon) {
			continue
		}
		if sw.vault.Allows(sw.scope, m.canon) {
			continue
		}
		if !changed {
			out.Grow(len(p) + 32)
			changed = true
		}
		out.Write(p[last:m.start])
		rawBody := string(p[m.start:m.end])
		token := sw.sentinelPrefix + hex.EncodeToString([]byte{byte(len(sw.shields) >> 8), byte(len(sw.shields))}) + "@"
		sw.shields[token] = rawBody
		out.WriteString(token)
		last = m.end
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
		if orig := sw.store.LookupOriginal(p, sw.shape); orig != "" {
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
	if orig == "" {
		return b
	}
	seen := make(map[string]struct{}, 6)
	var forms []string
	addForm := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		forms = append(forms, s)
		if encoded, err := json.Marshal(s); err == nil && len(encoded) >= 2 {
			escaped := string(encoded[1 : len(encoded)-1])
			if _, ok := seen[escaped]; !ok && escaped != "" {
				seen[escaped] = struct{}{}
				forms = append(forms, escaped)
			}
		}
	}
	addForm(orig)
	addForm(url.PathEscape(orig))
	addForm(html.EscapeString(orig))
	// Replace longer encoded variants first so shorter substrings do not partially match them.
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
	out := b
	phBytes := []byte(placeholder)
	for _, form := range forms {
		out = bytes.ReplaceAll(out, []byte(form), phBytes)
	}
	return out
}

func (sw *scopedStreamWriter) Write(p []byte) (int, error) {
	if sw.bypass {
		// Record blocked attempts if the response references a placeholder from another scope.
		for _, m := range findScopePlaceholders(p) {
			if sw.store.HasValue(m.canon) {
				sw.vault.recordBlocked(1)
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
