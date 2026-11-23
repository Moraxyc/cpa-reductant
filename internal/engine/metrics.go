package engine

import (
	"fmt"
	"slices"
	"strings"
)

const PrometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

func (e *Engine) PrometheusMetrics() string {
	return RenderPrometheusMetrics(e.StatusWithOptions(0, ""))
}

func escapePromLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

func promBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// RenderPrometheusMetrics formats an engine Status snapshot in Prometheus text
// exposition format (0.0.4) with deterministic ordering and zero plaintext.
func RenderPrometheusMetrics(st Status) string {
	var b strings.Builder
	s := st.Stats

	fmt.Fprintf(&b, "# HELP cpa_reductant_info Build information for cpa-reductant.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_info gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_info{version=\"%s\"} 1\n", escapePromLabel(st.Version))

	fmt.Fprintf(&b, "# HELP cpa_reductant_uptime_seconds Plugin uptime in seconds.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_uptime_seconds %d\n", st.UptimeSeconds)

	fmt.Fprintf(&b, "# HELP cpa_reductant_requests_total Total requests inspected by cpa-reductant.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_requests_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_requests_total %d\n", s.RequestsTotal)

	fmt.Fprintf(&b, "# HELP cpa_reductant_requests_by_action_total Requests grouped by redaction outcome.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_requests_by_action_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_requests_by_action_total{action=\"masked\"} %d\n", s.RequestsMasked)
	fmt.Fprintf(&b, "cpa_reductant_requests_by_action_total{action=\"warned\"} %d\n", s.RequestsWarned)
	fmt.Fprintf(&b, "cpa_reductant_requests_by_action_total{action=\"blocked\"} %d\n", s.RequestsBlocked)
	fmt.Fprintf(&b, "cpa_reductant_requests_by_action_total{action=\"error\"} %d\n", s.RequestsError)

	fmt.Fprintf(&b, "# HELP cpa_reductant_responses_total Total non-streaming responses processed.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_responses_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_responses_total %d\n", s.ResponsesTotal)

	fmt.Fprintf(&b, "# HELP cpa_reductant_responses_restored_total Non-streaming responses with at least one restored placeholder.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_responses_restored_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_responses_restored_total %d\n", s.ResponsesRestored)

	fmt.Fprintf(&b, "# HELP cpa_reductant_streams_total Total streaming responses processed.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_streams_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_streams_total %d\n", s.StreamsTotal)

	fmt.Fprintf(&b, "# HELP cpa_reductant_streams_restored_total Streaming responses with at least one restored placeholder.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_streams_restored_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_streams_restored_total %d\n", s.StreamsRestored)

	fmt.Fprintf(&b, "# HELP cpa_reductant_streams_failed_total Streaming responses terminated with an error.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_streams_failed_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_streams_failed_total %d\n", s.StreamsFailed)

	fmt.Fprintf(&b, "# HELP cpa_reductant_placeholders_restored_total Total placeholder occurrences restored.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_placeholders_restored_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_placeholders_restored_total %d\n", s.PlaceholdersRestored)

	fmt.Fprintf(&b, "# HELP cpa_reductant_bytes_total Total bytes processed inbound and outbound.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_bytes_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_bytes_total{direction=\"in\"} %d\n", s.BytesMaskedIn)
	fmt.Fprintf(&b, "cpa_reductant_bytes_total{direction=\"out\"} %d\n", s.BytesRestoredOut)

	fmt.Fprintf(&b, "# HELP cpa_reductant_hits_total Sensitive matches grouped by action.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_hits_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_hits_total{action=\"masked\"} %d\n", s.HitsMasked)
	fmt.Fprintf(&b, "cpa_reductant_hits_total{action=\"warned\"} %d\n", s.HitsWarned)
	fmt.Fprintf(&b, "cpa_reductant_hits_total{action=\"blocked\"} %d\n", s.HitsBlocked)

	if len(s.HitsByKind) > 0 {
		fmt.Fprintf(&b, "# HELP cpa_reductant_hits_by_kind_total Sensitive matches grouped by kind.\n")
		fmt.Fprintf(&b, "# TYPE cpa_reductant_hits_by_kind_total counter\n")
		for _, k := range sortedMapKeys(s.HitsByKind) {
			fmt.Fprintf(&b, "cpa_reductant_hits_by_kind_total{kind=\"%s\"} %d\n", escapePromLabel(k), s.HitsByKind[k])
		}
	}

	if len(s.HitsByFinder) > 0 {
		fmt.Fprintf(&b, "# HELP cpa_reductant_hits_by_finder_total Sensitive matches grouped by detector.\n")
		fmt.Fprintf(&b, "# TYPE cpa_reductant_hits_by_finder_total counter\n")
		for _, k := range sortedMapKeys(s.HitsByFinder) {
			fmt.Fprintf(&b, "cpa_reductant_hits_by_finder_total{finder=\"%s\"} %d\n", escapePromLabel(k), s.HitsByFinder[k])
		}
	}

	if len(s.ByProtocol) > 0 {
		fmt.Fprintf(&b, "# HELP cpa_reductant_protocol_requests_total Requests processed per protocol.\n")
		fmt.Fprintf(&b, "# TYPE cpa_reductant_protocol_requests_total counter\n")
		for _, k := range sortedMapKeys(s.ByProtocol) {
			fmt.Fprintf(&b, "cpa_reductant_protocol_requests_total{protocol=\"%s\"} %d\n", escapePromLabel(k), s.ByProtocol[k].Requests)
		}
		fmt.Fprintf(&b, "# HELP cpa_reductant_protocol_hits_total Sensitive hits per protocol.\n")
		fmt.Fprintf(&b, "# TYPE cpa_reductant_protocol_hits_total counter\n")
		for _, k := range sortedMapKeys(s.ByProtocol) {
			fmt.Fprintf(&b, "cpa_reductant_protocol_hits_total{protocol=\"%s\"} %d\n", escapePromLabel(k), s.ByProtocol[k].Hits)
		}
	}

	if len(s.ByModel) > 0 {
		fmt.Fprintf(&b, "# HELP cpa_reductant_model_requests_total Requests processed per LLM model.\n")
		fmt.Fprintf(&b, "# TYPE cpa_reductant_model_requests_total counter\n")
		for _, k := range sortedMapKeys(s.ByModel) {
			fmt.Fprintf(&b, "cpa_reductant_model_requests_total{model=\"%s\"} %d\n", escapePromLabel(k), s.ByModel[k].Requests)
		}
		fmt.Fprintf(&b, "# HELP cpa_reductant_model_hits_total Sensitive hits per LLM model.\n")
		fmt.Fprintf(&b, "# TYPE cpa_reductant_model_hits_total counter\n")
		for _, k := range sortedMapKeys(s.ByModel) {
			fmt.Fprintf(&b, "cpa_reductant_model_hits_total{model=\"%s\"} %d\n", escapePromLabel(k), s.ByModel[k].Hits)
		}
	}

	writeLatencyGauge := func(metric, help string, l LatencySummary) {
		fmt.Fprintf(&b, "# HELP %s %s\n", metric, help)
		fmt.Fprintf(&b, "# TYPE %s gauge\n", metric)
		fmt.Fprintf(&b, "%s{stat=\"avg\"} %.2f\n", metric, l.AvgMS)
		fmt.Fprintf(&b, "%s{stat=\"p95\"} %.2f\n", metric, l.P95MS)
		fmt.Fprintf(&b, "%s{stat=\"max\"} %.2f\n", metric, l.MaxMS)
		fmt.Fprintf(&b, "%s{stat=\"last\"} %.2f\n", metric, l.LastMS)
	}
	writeLatencyGauge("cpa_reductant_mask_latency_ms", "Request masking duration in milliseconds.", s.MaskLatency)
	writeLatencyGauge("cpa_reductant_restore_latency_ms", "Response restoration duration in milliseconds.", s.RestoreLatency)

	m := st.Model
	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_enabled Whether the PII semantic model is enabled (1=yes, 0=no).\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_enabled gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_enabled %d\n", promBool(m.Enabled))

	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_loaded Whether the PII semantic model is loaded and ready (1=yes, 0=no).\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_loaded gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_loaded %d\n", promBool(m.Loaded))

	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_cache_entries Number of cached text pieces in the PII model LRU cache.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_cache_entries gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_cache_entries %d\n", m.CacheEntries)

	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_cache_hits_total PII model cache hits.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_cache_hits_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_cache_hits_total %d\n", m.CacheHits)

	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_cache_misses_total PII model cache misses.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_cache_misses_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_cache_misses_total %d\n", m.CacheMisses)

	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_partial_timeouts_total Requests that degraded due to PII model wait timeout.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_partial_timeouts_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_partial_timeouts_total %d\n", m.PartialTimeouts)

	fmt.Fprintf(&b, "# HELP cpa_reductant_pii_model_waiting Number of requests currently waiting for PII model inference.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_pii_model_waiting gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_pii_model_waiting %d\n", m.Waiting)

	sc := st.Scope
	fmt.Fprintf(&b, "# HELP cpa_reductant_scope_active Active CallerScope namespaces currently tracked.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_scope_active gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_scope_active %d\n", sc.ActiveScopes)

	fmt.Fprintf(&b, "# HELP cpa_reductant_scope_tracked_mappings Total tracked scope-to-placeholder mappings.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_scope_tracked_mappings gauge\n")
	fmt.Fprintf(&b, "cpa_reductant_scope_tracked_mappings %d\n", sc.TrackedMappings)

	fmt.Fprintf(&b, "# HELP cpa_reductant_scope_blocked_restores_total Cross-scope placeholder restoration attempts blocked.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_scope_blocked_restores_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_scope_blocked_restores_total %d\n", sc.BlockedRestores)

	al := st.Alerts
	fmt.Fprintf(&b, "# HELP cpa_reductant_alerts_total Async webhook alert delivery outcomes.\n")
	fmt.Fprintf(&b, "# TYPE cpa_reductant_alerts_total counter\n")
	fmt.Fprintf(&b, "cpa_reductant_alerts_total{outcome=\"sent\"} %d\n", al.Sent)
	fmt.Fprintf(&b, "cpa_reductant_alerts_total{outcome=\"failed\"} %d\n", al.Failed)
	fmt.Fprintf(&b, "cpa_reductant_alerts_total{outcome=\"dropped\"} %d\n", al.Dropped)

	return b.String()
}
