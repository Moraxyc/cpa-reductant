package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/redact"
)

func TestEnginePolicyRulesActionsAllowShapeAndStats(t *testing.T) {
	boolFalse := false
	boolTrue := true
	eng := New(&config.Config{
		Secrets:      &boolTrue,
		Personal:     &boolTrue,
		Kinds:        map[string]bool{"ip": true},
		Words:        []string{"ProjectNova"},
		WordsAnyCase: true,
		WordsWhole:   true,
		Rules: []config.RuleConfig{
			{Kind: "CORP_KEY", Prefix: "crp_"},
		},
		Actions: map[string]string{
			"EMAIL": "warn",
		},
		Allow: []string{"45.33.32.156"},
	})
	defer eng.Close()

	// Note: WordsWhole ensures "XProjectNova" is NOT masked while "projectnova" IS masked.
	// Allow lets 45.33.32.156 pass while 93.184.216.34 is masked (via kinds.ip=true).
	// Actions.EMAIL=warn passes bob@acme.io unmasked and counts a warn hit.
	// Custom rule crp_1234567890 is masked as {{CORP_KEY_...}} and restored.
	reqBody := []byte(`{"messages":[{"role":"user","content":"word=projectnova skip=XProjectNova corp=crp_1234567890 ip: 93.184.216.34 allow: 45.33.32.156 mail=bob@acme.io"}]}`)
	masked, err := eng.MaskJSON("route-1", reqBody)
	if err != nil {
		t.Fatalf("MaskJSON failed: %v", err)
	}
	s := string(masked)
	if strings.Contains(s, "word=projectnova") {
		t.Fatalf("expected case-insensitive whole word projectnova to be masked, got %s", s)
	}
	if !strings.Contains(s, "skip=XProjectNova") {
		t.Fatalf("expected partial word XProjectNova to stay unmasked under words_whole, got %s", s)
	}
	if strings.Contains(s, "crp_1234567890") || !strings.Contains(s, "{{CORP_KEY_") {
		t.Fatalf("expected custom rule crp_1234567890 to be masked as CORP_KEY, got %s", s)
	}
	if strings.Contains(s, "93.184.216.34") || !strings.Contains(s, "public-ip-") {
		t.Fatalf("expected IP 93.184.216.34 to be masked as public-ip-*, got %s", s)
	}
	if !strings.Contains(s, "allow: 45.33.32.156") {
		t.Fatalf("expected allowed IP 45.33.32.156 to stay unmasked, got %s", s)
	}
	if !strings.Contains(s, "mail=bob@acme.io") {
		t.Fatalf("expected warned email to stay unmasked, got %s", s)
	}

	restored := string(eng.RestoreJSON(masked))
	if !strings.Contains(restored, "projectnova") || !strings.Contains(restored, "crp_1234567890") || !strings.Contains(restored, "93.184.216.34") {
		t.Fatalf("expected RestoreJSON to restore masked values, got %s", restored)
	}

	// Verify action=block works for non-TERM kinds too.
	eng.Update(&config.Config{
		Secrets:  &boolFalse,
		Personal: &boolTrue,
		Actions:  map[string]string{"EMAIL": "block"},
	})
	if _, err := eng.MaskJSON("route-1", []byte(`{"text":"contact bob@acme.io"}`)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked for EMAIL action=block, got %v", err)
	}

	st := eng.Status()
	if st.Stats.RequestsTotal != 2 || st.Stats.RequestsMasked != 1 || st.Stats.RequestsBlocked != 1 {
		t.Fatalf("unexpected request stats: %+v", st.Stats)
	}
	if st.Stats.HitsWarned != 1 || st.Stats.HitsBlocked != 1 || st.Stats.HitsMasked < 3 {
		t.Fatalf("unexpected hit stats: %+v", st.Stats)
	}
}

func TestEnginePreviewDoesNotMutateStatsOrMemory(t *testing.T) {
	eng := New(&config.Config{
		Shape: true,
		Words: []string{"SYNTHETIC-PREVIEW-ONLY"},
	})
	defer eng.Close()

	res, err := eng.Preview(context.Background(), "token=sk-ant-api03-1234567890abcdefghij word=SYNTHETIC-PREVIEW-ONLY")
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if strings.Contains(res.Masked, "sk-ant-api03-1234567890abcdefghij") || strings.Contains(res.Masked, "SYNTHETIC-PREVIEW-ONLY") {
		t.Fatalf("expected secrets and words masked in preview, got %q", res.Masked)
	}
	if strings.Contains(res.Masked, "{{API_KEY_") {
		t.Fatalf("expected shape=true to emit synthetic key instead of {{API_KEY_...}}, got %q", res.Masked)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("expected 2 preview hits, got %d (%+v)", len(res.Hits), res.Hits)
	}

	// Preview must not increment request stats or store values for RestoreJSON.
	if st := eng.Status(); st.Stats.RequestsTotal != 0 || st.Stats.HitsTotal != 0 {
		t.Fatalf("expected Preview not to mutate stats, got %+v", st.Stats)
	}
	payload, _ := json.Marshal(map[string]string{"text": res.Masked})
	if got := string(eng.RestoreJSON(payload)); strings.Contains(got, "SYNTHETIC-PREVIEW-ONLY") {
		t.Fatalf("expected Preview placeholder not to be stored for RestoreJSON, got %s", got)
	}
}

func TestEngineKeyPathSubprocess(t *testing.T) {
	keyPath := os.Getenv("CPA_TEST_KEY_PATH")
	if keyPath == "" {
		return
	}
	eng := New(&config.Config{
		KeyPath: keyPath,
		Words:   []string{"SYNTHETIC-PERSIST-TERM"},
	})
	defer eng.Close()
	masked, err := eng.MaskJSON("route-1", []byte(`{"text":"SYNTHETIC-PERSIST-TERM"}`))
	if err != nil {
		t.Fatalf("MaskJSON: %v", err)
	}
	fmt.Printf("MASKED=%s\n", masked)
}

func TestEngineKeyPathPersistenceAcrossRestarts(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "nested", "reductant.key")
	runOnce := func() string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEngineKeyPathSubprocess$")
		cmd.Env = append(os.Environ(), "CPA_TEST_KEY_PATH="+keyPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("subprocess failed: %v\n%s", err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "MASKED=") {
				return strings.TrimPrefix(line, "MASKED=")
			}
		}
		t.Fatalf("missing MASKED= output:\n%s", out)
		return ""
	}

	first := runOnce()
	fi, err := os.Stat(keyPath)
	if err != nil || fi.Size() != 32 {
		t.Fatalf("expected 32-byte key file at %s, err=%v size=%v", keyPath, err, fi)
	}
	second := runOnce()
	if first != second {
		t.Fatalf("expected deterministic placeholder across restarts, got %q != %q", first, second)
	}
}

func TestEngineTelemetryRestoreEventsAndKeyPathLock(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "reductant.key")
	secret := "sk-ant-api03-telemetrysecretvalue123456"
	warned := "bob@acme.io"
	eng := New(&config.Config{
		KeyPath: keyPath,
		Actions: map[string]string{"EMAIL": "warn"},
	})
	defer eng.Close()

	var logs []string
	eng.SetLogger(func(level, callbackID, msg string, fields map[string]any) {
		logs = append(logs, fmt.Sprintf("%s:%s:%s", level, callbackID, msg))
	})

	reqBody := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"key=%s mail=%s"}]}`, secret, warned))
	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Route:          "claude-test",
		Protocol:       config.ProtocolClaude,
		Model:          "claude-test",
		HostCallbackID: "cb-1",
	}, reqBody)
	if err != nil {
		t.Fatalf("MaskRequest: %v", err)
	}
	if strings.Contains(string(masked), secret) {
		t.Fatalf("secret should be masked: %s", masked)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "cb-1") {
		t.Fatalf("expected host logger to receive warn hit with callback ID, got %v", logs)
	}

	restored, restoredCount := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-test",
	}, "application/json", masked)
	if restoredCount != 1 || !strings.Contains(string(restored), secret) {
		t.Fatalf("expected 1 placeholder restored with secret back, count=%d out=%s", restoredCount, restored)
	}

	eng.RecordStreamRestore(RequestMeta{
		Protocol: config.ProtocolOpenAI,
		Model:    "gpt-test",
		Stream:   true,
	}, 128, map[string]int{"{{API_KEY_abcd1234}}": 2}, 5*1e6, nil)

	eng.RecordStreamRestore(RequestMeta{
		Protocol:       config.ProtocolOpenAI,
		Model:          "gpt-test",
		Stream:         true,
		HostCallbackID: "cb-2",
	}, 64, nil, 2*1e6, errors.New("upstream EOF"))

	// Changing key_path after redaction started should report key_path_error and key_path_active=false.
	eng.Update(&config.Config{
		KeyPath: filepath.Join(t.TempDir(), "other.key"),
	})

	st := eng.StatusWithOptions(10, "")
	if !st.Policy.KeyPathConfigured || st.Policy.KeyPathActive || st.Policy.KeyPathError == "" {
		t.Fatalf("expected KeyPathError after post-lock reconfigure, got %+v", st.Policy)
	}
	if st.Stats.ResponsesTotal != 1 || st.Stats.ResponsesRestored != 1 ||
		st.Stats.StreamsTotal != 2 || st.Stats.StreamsRestored != 1 || st.Stats.StreamsFailed != 1 ||
		st.Stats.PlaceholdersRestored != 3 {
		t.Fatalf("unexpected restore stats: %+v", st.Stats)
	}
	if st.Stats.BytesMaskedIn == 0 || st.Stats.BytesRestoredOut == 0 || st.Stats.MaskLatency.Count != 1 || st.Stats.RestoreLatency.Count != 3 {
		t.Fatalf("unexpected bytes/latency stats: %+v", st.Stats)
	}
	if st.Stats.ByProtocol[config.ProtocolClaude].PlaceholdersRestored != 1 || st.Stats.ByModel["gpt-test"].PlaceholdersRestored != 2 {
		t.Fatalf("unexpected dimension stats: protocol=%+v model=%+v", st.Stats.ByProtocol, st.Stats.ByModel)
	}
	if len(st.RecentEvents) != 4 {
		t.Fatalf("expected 4 recent events, got %d (%+v)", len(st.RecentEvents), st.RecentEvents)
	}
	rawStatus, _ := json.Marshal(st)
	if strings.Contains(string(rawStatus), secret) || strings.Contains(string(rawStatus), warned) {
		t.Fatalf("telemetry JSON must never contain raw secret or warned plaintext: %s", rawStatus)
	}

	onlyErrors := eng.StatusWithOptions(10, "error")
	if len(onlyErrors.RecentEvents) != 1 || onlyErrors.RecentEvents[0].Action != "error" {
		t.Fatalf("expected action=error filter to return 1 event, got %+v", onlyErrors.RecentEvents)
	}
}

func TestEngineDebugCaptureLifecycleAndTTL(t *testing.T) {
	eng := New(&config.Config{
		Words: []string{"SYNTHETIC-CAPTURE-ALPHA", "SYNTHETIC-CAPTURE-BETA"},
	})
	defer eng.Close()

	if eng.BeginCapture() != 0 {
		t.Fatalf("expected BeginCapture to return 0 when disabled")
	}
	if _, err := eng.MaskJSON("m1", []byte(`{"text":"SYNTHETIC-CAPTURE-ALPHA"}`)); err != nil {
		t.Fatalf("MaskJSON while disabled: %v", err)
	}
	if snap := eng.DebugCaptureSnapshot(0, 0, "", ""); snap.Enabled || len(snap.Entries) != 0 {
		t.Fatalf("expected empty disabled snapshot, got %+v", snap)
	}

	snap := eng.ConfigureDebugCapture(true, 5*time.Minute, 2, false)
	if !snap.Enabled || snap.MaxEntries != 2 || snap.RemainingSeconds <= 0 {
		t.Fatalf("unexpected enabled snapshot: %+v", snap)
	}

	// Capture #1: Mask + Restore.
	cid1 := eng.BeginCapture()
	if cid1 == 0 {
		t.Fatalf("expected non-zero capture ID when enabled")
	}
	raw1 := []byte(`{"messages":[{"role":"user","content":"hello SYNTHETIC-CAPTURE-ALPHA"}]}`)
	masked1, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol:  config.ProtocolClaude,
		Model:     "claude-test",
		CaptureID: cid1,
	}, raw1)
	if err != nil {
		t.Fatalf("MaskRequest #1: %v", err)
	}
	restored1, n1 := eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:  config.ProtocolClaude,
		Model:     "claude-test",
		CaptureID: cid1,
	}, "application/json", masked1)
	if n1 != 1 || !strings.Contains(string(restored1), "SYNTHETIC-CAPTURE-ALPHA") {
		t.Fatalf("expected restored response #1, n=%d out=%s", n1, restored1)
	}

	// Capture #2 & #3 with max_entries=2 should retain #2 and #3 (newest first).
	for _, term := range []string{"SYNTHETIC-CAPTURE-BETA", "SYNTHETIC-CAPTURE-ALPHA"} {
		if _, err := eng.MaskRequest(context.Background(), RequestMeta{
			Protocol: config.ProtocolOpenAI,
			Model:    "gpt-test",
		}, []byte(`{"text":"`+term+`"}`)); err != nil {
			t.Fatalf("MaskRequest: %v", err)
		}
	}

	snap = eng.DebugCaptureSnapshot(0, 0, "", "")
	if snap.EntriesCount != 2 || len(snap.Entries) != 2 {
		t.Fatalf("expected 2 retained entries with max_entries=2, got count=%d len=%d", snap.EntriesCount, len(snap.Entries))
	}
	if snap.Entries[0].ID <= snap.Entries[1].ID {
		t.Fatalf("expected newest-first order, got ids %d, %d", snap.Entries[0].ID, snap.Entries[1].ID)
	}

	// Check exact plaintext capture on #1 before eviction using fresh capture.
	snap = eng.ConfigureDebugCapture(true, 5*time.Minute, 10, true)
	if len(snap.Entries) != 0 {
		t.Fatalf("expected clear=true to reset entries, got %d", len(snap.Entries))
	}
	cid := eng.BeginCapture()
	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol:  config.ProtocolClaude,
		Model:     "claude-test",
		CaptureID: cid,
	}, raw1)
	if err != nil {
		t.Fatalf("MaskRequest: %v", err)
	}
	_, _ = eng.RestoreResponseWithMeta(RequestMeta{
		Protocol:  config.ProtocolClaude,
		Model:     "claude-test",
		CaptureID: cid,
	}, "application/json", masked)

	snap = eng.DebugCaptureSnapshot(0, cid, "", "")
	if len(snap.Entries) != 1 {
		t.Fatalf("expected 1 entry for id=%d, got %d", cid, len(snap.Entries))
	}
	ev := snap.Entries[0]
	if ev.RawRequest != string(raw1) || ev.MaskedRequest != string(masked) {
		t.Fatalf("unexpected raw/masked bodies: %+v", ev)
	}
	if len(ev.Replacements) != 1 || ev.Replacements[0].Original != "SYNTHETIC-CAPTURE-ALPHA" || !strings.HasPrefix(ev.Replacements[0].Placeholder, "{{TERM_") {
		t.Fatalf("unexpected replacements: %+v", ev.Replacements)
	}
	if ev.RestoredCount != 1 || len(ev.RestoredItems) != 1 || ev.RestoredItems[0].Original != "SYNTHETIC-CAPTURE-ALPHA" || ev.RestoredItems[0].Count != 1 {
		t.Fatalf("unexpected restored items: %+v", ev.RestoredItems)
	}

	// TTL expiry disables new captures while preserving existing entries until cleared.
	eng.ConfigureDebugCapture(true, 1*time.Millisecond, 0, false)
	time.Sleep(10 * time.Millisecond)
	snap = eng.DebugCaptureSnapshot(0, 0, "", "")
	if snap.Enabled || len(snap.Entries) != 1 {
		t.Fatalf("expected expired capture to auto-disable and keep entries, got %+v", snap)
	}
	if eng.BeginCapture() != 0 {
		t.Fatalf("expected BeginCapture to return 0 after TTL expiry")
	}

	cleared := eng.ClearDebugCapture()
	if len(cleared.Entries) != 0 || cleared.EntriesCount != 0 {
		t.Fatalf("expected ClearDebugCapture to empty entries, got %+v", cleared)
	}

	// Truncation preserves head + tail and valid UTF-8 boundaries.
	bigPrefix := strings.Repeat("a", captureHeadBytes-1) + "中文中间"
	bigMiddle := strings.Repeat("m", MaxCaptureBodyBytes())
	bigTail := "尾部中文" + strings.Repeat("z", 1024)
	big := []byte(bigPrefix + bigMiddle + bigTail)
	truncStr, total, truncated := truncateCaptureBytes(big, MaxCaptureBodyBytes())
	if !truncated || total != len(big) || len(truncStr) > MaxCaptureBodyBytes() {
		t.Fatalf("unexpected truncation result: len=%d total=%d truncated=%v", len(truncStr), total, truncated)
	}
	if !utf8.ValidString(truncStr) {
		t.Fatalf("expected valid UTF-8 after head+tail truncation")
	}
	if !strings.HasPrefix(truncStr, "aaaa") || !strings.Contains(truncStr, "... [omitted ") || !strings.HasSuffix(truncStr, bigTail) {
		t.Fatalf("expected head+tail marker preservation, got prefix=%q suffix=%q", truncStr[:32], truncStr[len(truncStr)-64:])
	}

	// CaptureBuffer preserves head + tail incrementally across chunked writes.
	cb := NewCaptureBuffer(MaxCaptureBodyBytes())
	for off := 0; off < len(big); off += 4097 {
		end := min(len(big), off+4097)
		cb.Write(big[off:end])
	}
	cbBytes, cbTotal, cbTrunc := cb.Snapshot()
	if !cbTrunc || cbTotal != len(big) || len(cbBytes) > MaxCaptureBodyBytes() || !utf8.Valid(cbBytes) {
		t.Fatalf("unexpected CaptureBuffer snapshot: len=%d total=%d trunc=%v valid=%v", len(cbBytes), cbTotal, cbTrunc, utf8.Valid(cbBytes))
	}
	if !strings.Contains(string(cbBytes), "... [omitted ") || !strings.HasSuffix(string(cbBytes), bigTail) {
		t.Fatalf("expected CaptureBuffer to preserve head+tail, suffix=%q", string(cbBytes[len(cbBytes)-64:]))
	}
}

func TestEnginePerRoutePolicyOverrides(t *testing.T) {
	cfg, err := config.Parse([]byte(`
words: [SYNTHETIC-POLICY-TERM]
policies:
  - name: strict-claude
    models: ["claude-*"]
    block_terms: true
  - name: warn-email-openai
    protocols: [openai]
    actions:
      EMAIL: warn
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	// claude-* should block on SYNTHETIC-POLICY-TERM.
	if _, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    "claude-sonnet-4",
	}, []byte(`{"text":"SYNTHETIC-POLICY-TERM"}`)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked for claude-sonnet-4, got %v", err)
	}

	// openai gpt-5 should mask SYNTHETIC-POLICY-TERM and only warn on email.
	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolOpenAI,
		Model:    "gpt-5",
	}, []byte(`{"text":"SYNTHETIC-POLICY-TERM alice@contoso.io"}`))
	if err != nil {
		t.Fatalf("expected gpt-5 to succeed, got %v", err)
	}
	if strings.Contains(string(masked), "SYNTHETIC-POLICY-TERM") {
		t.Fatalf("expected term to be masked on gpt-5: %s", masked)
	}
	if !strings.Contains(string(masked), "alice@contoso.io") {
		t.Fatalf("expected email to pass through with warn on openai: %s", masked)
	}

	st := eng.Status()
	if st.Policy.PoliciesCount != 2 || st.Stats.HitsWarned == 0 {
		t.Fatalf("expected PoliciesCount=2 and warn hit recorded, got policies=%d warned=%d", st.Policy.PoliciesCount, st.Stats.HitsWarned)
	}
}

func TestEnginePreviewJSONAndWhatIfOverrides(t *testing.T) {
	cfg, err := config.Parse([]byte(`
words: [SYNTHETIC-PREVIEW-JSON-ONLY]
policies:
  - name: block-claude
    models: ["claude-*"]
    block_terms: true
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng := New(cfg)
	defer eng.Close()

	// JSON mode preserves kept protocol keys (model, id, role) while masking content and tool_use input fields.
	sample := `{
		"model": "claude-sonnet-4",
		"id": "SYNTHETIC-PREVIEW-JSON-ONLY",
		"messages": [
			{"role": "user", "content": "reach alice@contoso.io about SYNTHETIC-PREVIEW-JSON-ONLY"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "lookup", "input": {"name": "SYNTHETIC-PREVIEW-JSON-ONLY"}}]}
		]
	}`
	res, err := eng.PreviewWithOptions(context.Background(), PreviewRequest{
		Text:  sample,
		Mode:  PreviewModeJSON,
		Model: "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("PreviewWithOptions json: %v", err)
	}
	if res.Mode != PreviewModeJSON || !res.Blocked {
		t.Fatalf("expected mode=json and blocked=true for claude-sonnet-4, got %+v", res)
	}
	if !strings.Contains(res.Masked, `"model": "claude-sonnet-4"`) {
		t.Fatalf("expected model key preserved: %s", res.Masked)
	}
	if !strings.Contains(res.Masked, `"id": "SYNTHETIC-PREVIEW-JSON-ONLY"`) {
		t.Fatalf("expected top-level id key preserved: %s", res.Masked)
	}
	if strings.Contains(res.Masked, "alice@contoso.io") {
		t.Fatalf("expected email masked in message content: %s", res.Masked)
	}
	// Tool input "name" must be masked even though top-level "name" is kept.
	if strings.Contains(res.Masked, `"name": "SYNTHETIC-PREVIEW-JSON-ONLY"`) {
		t.Fatalf("expected tool_use input.name to be masked: %s", res.Masked)
	}
	// Verify preview did not store placeholder or mutate stats.
	for _, h := range res.Hits {
		if h.Kind == "TERM" && h.Placeholder != "" {
			if _, ok := redact.Value(h.Placeholder); ok {
				t.Fatalf("preview unexpectedly persisted placeholder %s", h.Placeholder)
			}
		}
	}
	if eng.Status().Stats.RequestsTotal != 0 {
		t.Fatalf("preview must not mutate stats")
	}

	// What-if override: add temporary allow and extra word without mutating engine config.
	shapeOn := true
	whatIf, err := eng.PreviewWithOptions(context.Background(), PreviewRequest{
		Text: "contact alice@contoso.io about TEMP-WHATIF-WORD",
		Override: &config.PolicyFields{
			Words: []string{"TEMP-WHATIF-WORD"},
			Allow: []string{"alice@contoso.io"},
			Shape: &shapeOn,
		},
	})
	if err != nil {
		t.Fatalf("what-if preview: %v", err)
	}
	if !strings.Contains(whatIf.Masked, "alice@contoso.io") {
		t.Fatalf("expected override allow to pass email unmasked: %s", whatIf.Masked)
	}
	if strings.Contains(whatIf.Masked, "TEMP-WHATIF-WORD") {
		t.Fatalf("expected override word to be masked: %s", whatIf.Masked)
	}

	// Invalid mode / invalid JSON / invalid override return ErrInvalidPreviewRequest.
	for _, bad := range []PreviewRequest{
		{Text: "hi", Mode: "xml"},
		{Text: "{not-json", Mode: PreviewModeJSON},
		{Text: "hi", Override: &config.PolicyFields{Actions: map[string]string{"EMAIL": "drop"}}},
	} {
		if _, err := eng.PreviewWithOptions(context.Background(), bad); !errors.Is(err, ErrInvalidPreviewRequest) {
			t.Fatalf("expected ErrInvalidPreviewRequest for %+v, got %v", bad, err)
		}
	}
}

func TestEnginePreviewJSONMatchesMaskJSONDefinitions(t *testing.T) {
	word := "SYNTHETIC-DEF-SYNC-WORD"
	eng := New(&config.Config{
		Words: []string{word},
	})
	defer eng.Close()

	sample := []byte(`{
		"model": "gpt-5",
		"stream_options": {"reasoning_summary_delivery": "` + word + `"},
		"client_metadata": {"turn": "` + word + `"},
		"output_config": {"format": "` + word + `", "description": "desc ` + word + `"},
		"reasoning": {"summary": "` + word + `"},
		"include": ["` + word + `"],
		"input": [
			{"type": "additional_tools", "tools": [{"name": "` + word + `", "grammar": "` + word + `", "description": "tool ` + word + `"}]},
			{"type": "message", "role": "user", "content": "body ` + word + `"}
		]
	}`)

	prev, err := eng.PreviewWithOptions(context.Background(), PreviewRequest{
		Text: string(sample),
		Mode: PreviewModeJSON,
	})
	if err != nil {
		t.Fatalf("PreviewWithOptions json: %v", err)
	}
	masked, err := eng.MaskJSON("preview-parity-route", sample)
	if err != nil {
		t.Fatalf("MaskJSON: %v", err)
	}

	var prevObj, maskedObj any
	if err := json.Unmarshal([]byte(prev.Masked), &prevObj); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if err := json.Unmarshal(masked, &maskedObj); err != nil {
		t.Fatalf("unmarshal masked: %v", err)
	}
	prevNorm, _ := json.Marshal(prevObj)
	maskedNorm, _ := json.Marshal(maskedObj)
	if string(prevNorm) != string(maskedNorm) {
		t.Fatalf("preview JSON diverged from MaskJSON:\npreview: %s\nmasked:  %s", prevNorm, maskedNorm)
	}
	// Spot-check that protocol definition values stayed intact while descriptions and user input were masked.
	if !strings.Contains(prev.Masked, `"reasoning_summary_delivery": "`+word+`"`) ||
		!strings.Contains(prev.Masked, `"turn": "`+word+`"`) ||
		!strings.Contains(prev.Masked, `"grammar": "`+word+`"`) {
		t.Fatalf("expected protocol definition values preserved: %s", prev.Masked)
	}
	if strings.Contains(prev.Masked, "desc "+word) || strings.Contains(prev.Masked, "tool "+word) || strings.Contains(prev.Masked, "body "+word) {
		t.Fatalf("expected descriptions and user content masked: %s", prev.Masked)
	}
}
