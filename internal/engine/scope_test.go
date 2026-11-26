package engine

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
)

var termPlaceholderRE = regexp.MustCompile(`\{\{TERM_[a-z2-7]{8}\}\}`)

func TestScopeIsolationJSONAndSplitSSE(t *testing.T) {
	cfg, err := config.Parse([]byte(`
words:
  - SYNTHETIC-TENANT-A-SECRET
  - SYNTHETIC-TENANT-B-SECRET
scope_isolation: true
scope_ttl_seconds: 3600
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	// Tenant A masks its secret.
	maskedA, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-a",
	}, []byte(`{"text":"secret is SYNTHETIC-TENANT-A-SECRET"}`))
	if err != nil {
		t.Fatalf("mask tenant-a: %v", err)
	}
	phA := termPlaceholderRE.FindString(string(maskedA))
	if phA == "" {
		t.Fatalf("expected placeholder for tenant-a, got %s", maskedA)
	}

	// Tenant A restores its own placeholder.
	restoredA, countA := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-a",
	}, "application/json", []byte(`{"reply":"got `+phA+`"}`))
	if countA != 1 || !strings.Contains(string(restoredA), "SYNTHETIC-TENANT-A-SECRET") {
		t.Fatalf("expected tenant-a to restore its own placeholder: count=%d body=%s", countA, restoredA)
	}

	// Tenant B (with zero secrets) tries to oracle-restore Tenant A's placeholder -> MUST NOT restore!
	restoredB0, countB0 := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-b",
	}, "application/json", []byte(`{"reply":"probe `+phA+`"}`))
	if countB0 != 0 || strings.Contains(string(restoredB0), "SYNTHETIC-TENANT-A-SECRET") || !strings.Contains(string(restoredB0), phA) {
		t.Fatalf("cross-tenant leak to empty tenant-b! count=%d body=%s", countB0, restoredB0)
	}

	// Tenant B masks its own secret.
	maskedB, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-b",
	}, []byte(`{"text":"mine is SYNTHETIC-TENANT-B-SECRET"}`))
	if err != nil {
		t.Fatalf("mask tenant-b: %v", err)
	}
	phB := termPlaceholderRE.FindString(string(maskedB))
	if phB == "" || phB == phA {
		t.Fatalf("expected distinct placeholder for tenant-b, got %q (phA=%q)", phB, phA)
	}

	// Tenant B receives a JSON response containing BOTH phB (owned) and phA (foreign).
	restoredMixed, countMixed := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-b",
	}, "application/json", []byte(`{"reply":"own=`+phB+` foreign=`+phA+`"}`))
	if countMixed != 1 {
		t.Fatalf("expected exactly 1 allowed restore, got %d (body=%s)", countMixed, restoredMixed)
	}
	if !strings.Contains(string(restoredMixed), "own=SYNTHETIC-TENANT-B-SECRET") {
		t.Fatalf("expected tenant-b own secret restored: %s", restoredMixed)
	}
	if strings.Contains(string(restoredMixed), "SYNTHETIC-TENANT-A-SECRET") || !strings.Contains(string(restoredMixed), "foreign="+phA) {
		t.Fatalf("expected foreign tenant-a placeholder to remain masked: %s", restoredMixed)
	}

	// SSE stream test: split foreign phA across two chunks alongside valid phB!
	midA := len(phA) / 2
	buf := &restoreBuffer{header: http.Header{"Content-Type": {"text/event-stream"}}}
	sw := eng.NewStreamWriterForScope(buf, "tenant-b", config.ProtocolClaude, "claude-test")
	chunk1 := `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"own=` + phB + ` foreign=` + phA[:midA] + `"}}` + "\n\n"
	chunk2 := `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + phA[midA:] + ` done"}}` + "\n\n"
	if _, err := sw.Write([]byte(chunk1)); err != nil {
		t.Fatalf("write chunk1: %v", err)
	}
	if _, err := sw.Write([]byte(chunk2)); err != nil {
		t.Fatalf("write chunk2: %v", err)
	}
	sw.Finish()

	streamOut := buf.String()
	if !strings.Contains(streamOut, "SYNTHETIC-TENANT-B-SECRET") {
		t.Fatalf("expected tenant-b secret restored in SSE stream: %s", streamOut)
	}
	if strings.Contains(streamOut, "SYNTHETIC-TENANT-A-SECRET") {
		t.Fatalf("cross-tenant leak in split SSE stream: %s", streamOut)
	}
	if sw.Put()[phB] != 1 || sw.Put()[phA] != 0 {
		t.Fatalf("unexpected scoped Put map: %#v", sw.Put())
	}

	st := eng.Status()
	if !st.Scope.Enabled || st.Scope.ActiveScopes != 2 || st.Scope.TrackedMappings != 2 || st.Scope.BlockedRestores < 3 {
		t.Fatalf("unexpected scope stats: %+v", st.Scope)
	}
}

func TestScopeVaultBoundsAndTTL(t *testing.T) {
	v := newScopeVault(2, 2, 30*time.Millisecond)
	v.Record("s1", "p1", "p2", "p3") // p1 evicted by maxPerScope=2
	if v.Allows("s1", "p1") {
		t.Fatal("expected p1 to be evicted by maxPerScope")
	}
	if !v.Allows("s1", "p2") || !v.Allows("s1", "p3") {
		t.Fatal("expected p2 and p3 to remain in s1")
	}

	v.Record("s2", "q1")
	if !v.Allows("s1", "p2") { // promote s1 ahead of s2
		t.Fatal("expected s1 to allow p2")
	}
	v.Record("s3", "r1") // s2 is now least recently used -> evicted by maxScopes=2
	if v.Allows("s2", "q1") {
		t.Fatal("expected s2 to be evicted by maxScopes=2")
	}
	if !v.Allows("s1", "p2") || !v.Allows("s3", "r1") {
		t.Fatal("expected s1 and s3 to remain")
	}

	time.Sleep(45 * time.Millisecond)
	if v.Allows("s1", "p2") || v.Allows("s3", "r1") {
		t.Fatal("expected all scopes to expire after TTL")
	}
	snap := v.Snapshot(true)
	if snap.ActiveScopes != 0 || snap.TrackedMappings != 0 {
		t.Fatalf("expected 0 active scopes after TTL expiry, got %+v", snap)
	}
}

func TestScopeIsolationEncodedPlaceholdersAndSplitSSE(t *testing.T) {
	secretA := "机密 配置&A"
	secretB := "TENANT-B-OWN-SECRET"
	cfg, err := config.Parse([]byte(`
words:
  - "` + secretA + `"
  - "` + secretB + `"
scope_isolation: true
scope_ttl_seconds: 3600
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	maskedA, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-a",
	}, []byte(`{"text":"path=`+secretA+`"}`))
	if err != nil {
		t.Fatalf("mask tenant-a: %v", err)
	}
	phA := termPlaceholderRE.FindString(string(maskedA))
	if phA == "" {
		t.Fatalf("expected placeholder for tenant-a, got %s", maskedA)
	}
	bodyA := phA[2 : len(phA)-2]

	maskedB, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-b",
	}, []byte(`{"text":"mine=`+secretB+`"}`))
	if err != nil {
		t.Fatalf("mask tenant-b: %v", err)
	}
	phB := termPlaceholderRE.FindString(string(maskedB))
	if phB == "" {
		t.Fatalf("expected placeholder for tenant-b, got %s", maskedB)
	}
	bodyB := phB[2 : len(phB)-2]

	// Tenant A restores its own percent-encoded, HTML-entity, and Markdown-escaped placeholders.
	ownEncoded := `{"reply":"url=/p/%7B%7B` + bodyA + `%7D%7D html=&#123;&#123;` + bodyA + `&#125;&#125; md=\\{\\{` + bodyA + `\\}\\}"}`
	restoredA, countA := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-a",
	}, "application/json", []byte(ownEncoded))
	if countA != 3 || !strings.Contains(string(restoredA), "%E6%9C%BA%E5%AF%86%20%E9%85%8D%E7%BD%AE&A") || !strings.Contains(string(restoredA), "机密 配置&amp;A") {
		t.Fatalf("expected tenant-a to restore its own encoded placeholders: count=%d body=%s", countA, restoredA)
	}

	// Tenant B must restore its own %7B%7B...%7D%7D while keeping all encoded forms of Tenant A's placeholder sealed.
	mixedEncoded := `{"reply":"own=%7B%7B` + bodyB + `%7D%7D url=%7B%7B` + bodyA + `%7D%7D spaced=%7b%7b%20` + bodyA + `%20%7d%7d html=&#123;&#123;` + bodyA + `&#125;&#125; md=\\{\\{` + bodyA + `\\}\\}"}`
	restoredB, countB := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-b",
	}, "application/json", []byte(mixedEncoded))
	sB := string(restoredB)
	if countB != 1 || !strings.Contains(sB, "own="+secretB) {
		t.Fatalf("expected tenant-b to restore only its own placeholder: count=%d body=%s", countB, sB)
	}
	for _, leaked := range []string{secretA, "%E6%9C%BA%E5%AF%86", "&amp;A"} {
		if strings.Contains(sB, leaked) {
			t.Fatalf("cross-tenant encoded leak (%q) in %s", leaked, sB)
		}
	}
	for _, kept := range []string{
		`url=%7B%7B` + bodyA + `%7D%7D`,
		`spaced=%7b%7b%20` + bodyA + `%20%7d%7d`,
		`html=&#123;&#123;` + bodyA + `&#125;&#125;`,
		`md=\\{\\{` + bodyA + `\\}\\}`,
	} {
		if !strings.Contains(sB, kept) {
			t.Fatalf("expected unauthorized encoded placeholder %q preserved verbatim in %s", kept, sB)
		}
	}

	// Empty tenant-c (bypass mode) counts blocked encoded probes and never leaks.
	beforeBlocked := eng.Status().Scope.BlockedRestores
	restoredC, countC := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:    config.ProtocolClaude,
		Model:       "claude-test",
		CallerScope: "tenant-c",
	}, "application/json", []byte(`{"reply":"/p/%7B%7B`+bodyA+`%7D%7D"}`))
	if countC != 0 || strings.Contains(string(restoredC), "%E6%9C%BA%E5%AF%86") || !strings.Contains(string(restoredC), "%7B%7B"+bodyA+"%7D%7D") {
		t.Fatalf("empty scope leaked encoded placeholder: count=%d body=%s", countC, restoredC)
	}
	if got := eng.Status().Scope.BlockedRestores; got <= beforeBlocked {
		t.Fatalf("expected bypass mode to increment BlockedRestores for encoded placeholder, before=%d after=%d", beforeBlocked, got)
	}

	// Split percent-encoded foreign placeholder across SSE chunks must still be resealed (including URL-escaped CJK/spaces).
	encodedA := "%7B%7B" + bodyA + "%7D%7D"
	cut := len(encodedA) / 2
	buf := &restoreBuffer{header: http.Header{"Content-Type": {"text/event-stream"}}}
	sw := eng.NewStreamWriterForScope(buf, "tenant-b", config.ProtocolClaude, "claude-test")
	chunk1 := `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"own=%7B%7B` + bodyB + `%7D%7D foreign=` + encodedA[:cut] + `"}}` + "\n\n"
	chunk2 := `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + encodedA[cut:] + ` end"}}` + "\n\n"
	if _, err := sw.Write([]byte(chunk1)); err != nil {
		t.Fatalf("write chunk1: %v", err)
	}
	if _, err := sw.Write([]byte(chunk2)); err != nil {
		t.Fatalf("write chunk2: %v", err)
	}
	sw.Finish()

	streamOut := buf.String()
	if !strings.Contains(streamOut, secretB) {
		t.Fatalf("expected tenant-b secret restored in SSE stream: %s", streamOut)
	}
	for _, leaked := range []string{secretA, "%E6%9C%BA%E5%AF%86", "%20"} {
		if strings.Contains(streamOut, leaked) {
			t.Fatalf("split SSE encoded cross-tenant leak (%q) in %s", leaked, streamOut)
		}
	}
	if sw.Put()[phB] != 1 || sw.Put()[phA] != 0 {
		t.Fatalf("unexpected scoped Put map after split encoded SSE: %#v", sw.Put())
	}
}
