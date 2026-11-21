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

	"github.com/moraxyc/cpa-reductant/internal/config"
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
