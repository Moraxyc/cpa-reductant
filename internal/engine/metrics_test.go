package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/moraxyc/cpa-reductant/internal/config"
)

func TestRenderPrometheusMetrics(t *testing.T) {
	eng := New(&config.Config{
		Words: []string{"SYNTHETIC-PROM-SECRET"},
	})
	defer eng.Close()

	masked, err := eng.MaskRequest(context.Background(), RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    `claude-"quoted"\model`,
	}, []byte(`{"text":"SYNTHETIC-PROM-SECRET"}`))
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	_, _ = eng.RestoreResponseWithMeta(RequestMeta{
		Protocol: config.ProtocolClaude,
		Model:    `claude-"quoted"\model`,
	}, "application/json", masked)

	out := eng.PrometheusMetrics()
	for _, want := range []string{
		"# TYPE cpa_reductant_requests_total counter",
		"cpa_reductant_requests_total 1",
		`cpa_reductant_requests_by_action_total{action="masked"} 1`,
		`cpa_reductant_hits_by_kind_total{kind="TERM"} 1`,
		`cpa_reductant_protocol_requests_total{protocol="claude"} 1`,
		`cpa_reductant_model_requests_total{model="claude-\"quoted\"\\model"} 1`,
		`cpa_reductant_placeholders_restored_total 1`,
		`cpa_reductant_mask_latency_ms{stat="p95"}`,
		`cpa_reductant_scope_blocked_restores_total 0`,
		`cpa_reductant_alerts_total{outcome="sent"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prometheus metrics missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SYNTHETIC-PROM-SECRET") {
		t.Fatalf("prometheus metrics leaked plaintext secret")
	}
}
