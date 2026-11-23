package adapter

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type uiBarItem struct {
	Label   string
	Count   int64
	Percent float64
	Meta    string
}

type uiDimItem struct {
	Name     string
	Stats    engine.DimensionStats
	Percent  float64
	BytesStr string
}

type uiViewData struct {
	Status          engine.Status
	BaseURL         string
	JSONURL         string
	PreviewURL      string
	DebugCaptureURL string
	ActionFilter    string
	EventLimit      int
	HealthLabel     string
	HealthClass     string
	FilterRate      string
	BlockRate       string
	CacheHitRate    string
	DownloadPercent float64
	UptimeText      string
	BytesInText     string
	BytesOutText    string
	KindBars        []uiBarItem
	FinderBars      []uiBarItem
	ProtocolRows    []uiDimItem
	ModelRows       []uiDimItem
	HasEvents       bool
	RefreshSeconds  int
	RefreshLink     string
	StopRefreshLink string
}

var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"shortTime": func(ts string) string {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			t, err = time.Parse(time.RFC3339, ts)
			if err != nil {
				return ts
			}
		}
		return t.Local().Format("15:04:05")
	},
	"fmtMS": func(v float64) string {
		if v == 0 {
			return "0 ms"
		}
		if v < 1 {
			return fmt.Sprintf("%.2f ms", v)
		}
		return fmt.Sprintf("%.1f ms", v)
	},
	"fmtBytes":   formatBytes,
	"fmtBytes64": formatBytes64,
	"containsQ": func(s string) bool {
		return strings.Contains(s, "?")
	},
	"phaseLabel": func(p string) string {
		switch strings.ToLower(p) {
		case "request":
			return "请求"
		case "response":
			return "响应"
		case "stream":
			return "流式"
		case "model":
			return "模型"
		default:
			return p
		}
	},
	"actionLabel": func(a string) string {
		switch strings.ToLower(a) {
		case "block":
			return "拦截"
		case "warn":
			return "告警"
		case "mask":
			return "脱敏"
		case "restore":
			return "还原"
		case "passthrough":
			return "放行"
		case "error":
			return "错误"
		case "degraded":
			return "降级"
		default:
			return a
		}
	},
	"actionClass": func(a string) string {
		switch strings.ToLower(a) {
		case "block":
			return "badge-red"
		case "warn":
			return "badge-amber"
		case "mask":
			return "badge-blue"
		case "restore":
			return "badge-green"
		case "error", "degraded":
			return "badge-red"
		default:
			return "badge-muted"
		}
	},
}).Parse(strings.Replace(dashboardHTML, "/*__DASHBOARD_CSS__*/", dashboardCSS, 1)))

var (
	//go:embed assets/dashboard.css
	dashboardCSS string
	//go:embed assets/dashboard.html
	dashboardHTML string
	//go:embed assets/portal.html
	portalHTML string
)

func renderPortalHTML() pluginapi.ManagementResponse {
	html := strings.Replace(portalHTML, "/*__DASHBOARD_CSS__*/", dashboardCSS, 1)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  {"text/html; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: []byte(html),
	}
}

func renderDashboardHTML(st engine.Status, baseURL, actionFilter string, eventLimit, refreshSec int) pluginapi.ManagementResponse {
	healthLabel, healthClass := deriveHealth(st)
	dlPct := 0.0
	if st.Model.DownloadTotal > 0 {
		dlPct = float64(st.Model.DownloadDone) * 100 / float64(st.Model.DownloadTotal)
	}

	data := uiViewData{
		Status:          st,
		BaseURL:         baseURL,
		JSONURL:         managementBasePath + managementStatusPath + "?format=json",
		PreviewURL:      managementBasePath + managementPreviewPath,
		DebugCaptureURL: managementBasePath + managementDebugCapturePath,
		ActionFilter:    actionFilter,
		EventLimit:      eventLimit,
		HealthLabel:     healthLabel,
		HealthClass:     healthClass,
		FilterRate:      pctString(st.Stats.RequestsMasked, st.Stats.RequestsTotal),
		BlockRate:       pctString(st.Stats.RequestsBlocked, st.Stats.RequestsTotal),
		CacheHitRate:    pctString(st.Model.CacheHits, st.Model.CacheHits+st.Model.CacheMisses),
		DownloadPercent: dlPct,
		UptimeText:      formatDurationSec(st.UptimeSeconds),
		BytesInText:     formatBytes(int(st.Stats.BytesMaskedIn)),
		BytesOutText:    formatBytes(int(st.Stats.BytesRestoredOut)),
		KindBars:        buildBars(st.Stats.HitsByKind, nil),
		FinderBars:      buildBars(st.Stats.HitsByFinder, finderDescriptions),
		ProtocolRows:    buildDimRows(st.Stats.ByProtocol),
		ModelRows:       buildDimRows(st.Stats.ByModel),
		HasEvents:       len(st.RecentEvents) > 0,
		RefreshSeconds:  refreshSec,
		RefreshLink:     appendQuery(baseURL, actionFilter, 5),
		StopRefreshLink: appendQuery(baseURL, actionFilter, 0),
	}

	var buf bytes.Buffer
	if err := dashboardTmpl.Execute(&buf, data); err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:       buf.Bytes(),
	}
}

func appendQuery(baseURL, actionFilter string, refreshSec int) string {
	sep := "?"
	if strings.Contains(baseURL, "?") {
		sep = "&"
	}
	var params []string
	if actionFilter != "" {
		params = append(params, "action="+actionFilter)
	}
	if refreshSec > 0 {
		params = append(params, fmt.Sprintf("refresh=%d", refreshSec))
	}
	if len(params) == 0 {
		return baseURL
	}
	return baseURL + sep + strings.Join(params, "&")
}

func deriveHealth(st engine.Status) (string, string) {
	if st.Model.Enabled {
		if st.Model.LastError != "" || st.Model.DownloadError != "" {
			return "模型异常（规则兜底）", "badge-red"
		}
		if st.Model.Downloading {
			return "模型下载中", "badge-amber"
		}
		if st.Model.Behind {
			return "模型繁忙（超时降级）", "badge-amber"
		}
		if !st.Model.Installed {
			if st.Model.Strict {
				return "缺失权重（strict 将拒载）", "badge-amber"
			}
			return "缺失权重（仅规则运行）", "badge-amber"
		}
	}
	if st.Stats.RequestsTotal > 0 {
		return "运行中", "badge-green"
	}
	return "空闲", "badge-blue"
}

var finderDescriptions = map[string]string{
	"rule":  "内置规则",
	"yours": "自定义规则",
	"word":  "词表",
	"model": "本地模型",
	"known": "关联匹配",
}

func buildBars(counts map[string]int64, meta map[string]string) []uiBarItem {
	if len(counts) == 0 {
		return nil
	}
	var total int64
	for _, v := range counts {
		total += v
	}
	if total <= 0 {
		return nil
	}
	items := make([]uiBarItem, 0, len(counts))
	for k, v := range counts {
		var m string
		if meta != nil {
			m = meta[k]
		}
		items = append(items, uiBarItem{
			Label:   k,
			Count:   v,
			Percent: float64(v) * 100 / float64(total),
			Meta:    m,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Label < items[j].Label
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func buildDimRows(dims map[string]engine.DimensionStats) []uiDimItem {
	if len(dims) == 0 {
		return nil
	}
	items := make([]uiDimItem, 0, len(dims))
	for name, st := range dims {
		items = append(items, uiDimItem{
			Name:     name,
			Stats:    st,
			BytesStr: formatBytes(int(st.BytesIn)),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Stats.Requests == items[j].Stats.Requests {
			return items[i].Name < items[j].Name
		}
		return items[i].Stats.Requests > items[j].Stats.Requests
	})
	return items
}

func pctString(num, den int64) string {
	if den <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(num)*100/float64(den))
}

func formatDurationSec(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm%ds", sec/60, sec%60)
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	return fmt.Sprintf("%dh%dm", h, m)
}

func formatBytes(b int) string {
	return formatBytes64(int64(b))
}

func formatBytes64(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}
