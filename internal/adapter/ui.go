package adapter

import (
	"bytes"
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
	"fmtBytes": formatBytes,
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

const dashboardCSS = `:root{
  --bg:#f6f8fa;--surface:#ffffff;--surface-2:#f0f3f6;--border:#d8dee4;
  --text:#1f2328;--muted:#636c76;
  --blue:#0969da;--blue-bg:#ddf4ff;
  --green:#1a7f37;--green-bg:#dafbe1;
  --amber:#9a6700;--amber-bg:#fff8c5;
  --red:#cf222e;--red-bg:#ffebe9;
  --mono:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace;
  --sans:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif;
}
@media (prefers-color-scheme:dark){
  :root{
    --bg:#0d1117;--surface:#161b22;--surface-2:#21262d;--border:#30363d;
    --text:#e6edf3;--muted:#8b949e;
    --blue:#58a6ff;--blue-bg:rgba(56,139,253,.16);
    --green:#3fb950;--green-bg:rgba(46,160,67,.16);
    --amber:#d29922;--amber-bg:rgba(187,128,9,.16);
    --red:#f85149;--red-bg:rgba(248,81,73,.16);
  }
}
*{box-sizing:border-box}
body{margin:0;padding:20px;background:var(--bg);color:var(--text);font:14px/1.5 var(--sans)}
.wrap{max-width:1280px;margin:0 auto;display:flex;flex-direction:column;gap:16px}
.card{background:var(--surface);border:1px solid var(--border);border-radius:10px;padding:16px}
.header{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px}
.title-row{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
h1{margin:0;font-size:18px;font-weight:600;letter-spacing:-.01em}
h2{margin:0 0 12px;font-size:14px;font-weight:600;color:var(--text);display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap}
.sub{color:var(--muted);font-size:12px}
.mono{font-family:var(--mono);font-size:12px}
.badge{display:inline-flex;align-items:center;gap:5px;padding:2px 8px;border-radius:999px;font-size:12px;font-weight:600;line-height:1.4;white-space:nowrap}
.badge-green{background:var(--green-bg);color:var(--green)}
.badge-blue{background:var(--blue-bg);color:var(--blue)}
.badge-amber{background:var(--amber-bg);color:var(--amber)}
.badge-red{background:var(--red-bg);color:var(--red)}
.badge-muted{background:var(--surface-2);color:var(--muted)}
.dot{width:7px;height:7px;border-radius:50%;background:currentColor;display:inline-block}
.actions{display:flex;gap:8px;flex-wrap:wrap;align-items:center}
.btn{display:inline-flex;align-items:center;gap:6px;padding:5px 10px;border-radius:6px;border:1px solid var(--border);background:var(--surface-2);color:var(--text);text-decoration:none;font-size:12px;font-weight:500;cursor:pointer}
.btn:hover{border-color:var(--blue);color:var(--blue)}
.btn.active{background:var(--blue-bg);border-color:var(--blue);color:var(--blue)}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin-top:10px}
.chip{font-size:12px;padding:2px 8px;border-radius:6px;background:var(--surface-2);border:1px solid var(--border);color:var(--muted)}
.chip strong{color:var(--text)}
.grid-4{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px}
.grid-2{display:grid;grid-template-columns:repeat(auto-fit,minmax(360px,1fr));gap:16px}
.kpi-label{font-size:12px;color:var(--muted);margin-bottom:4px}
.kpi-val{font-size:26px;font-weight:700;letter-spacing:-.02em;line-height:1.15}
.kpi-meta{margin-top:8px;font-size:12px;color:var(--muted);display:flex;flex-wrap:wrap;gap:10px}
.bar-list{display:flex;flex-direction:column;gap:10px}
.bar-row{display:flex;flex-direction:column;gap:4px}
.bar-head{display:flex;justify-content:space-between;font-size:12px;gap:8px}
.bar-track{height:8px;border-radius:999px;background:var(--surface-2);overflow:hidden}
.bar-fill{height:100%;border-radius:999px;background:var(--blue)}
.bar-fill.green{background:var(--green)}
table{width:100%;border-collapse:collapse;font-size:12px}
th,td{padding:8px 10px;text-align:left;border-bottom:1px solid var(--border);vertical-align:top}
th{color:var(--muted);font-weight:600;background:var(--surface-2);white-space:nowrap}
tr:last-child td{border-bottom:none}
.hits{display:flex;flex-wrap:wrap;gap:5px}
.hit-pill{display:inline-flex;align-items:center;gap:4px;padding:2px 7px;border-radius:6px;background:var(--surface-2);border:1px solid var(--border);font-family:var(--mono);font-size:11px}
.hit-pill.warn{border-color:var(--amber);background:var(--amber-bg)}
.hit-pill.block{border-color:var(--red);background:var(--red-bg)}
.empty{padding:20px;text-align:center;color:var(--muted);font-size:13px}
textarea{width:100%;min-height:76px;padding:10px;border-radius:8px;border:1px solid var(--border);background:var(--bg);color:var(--text);font:12px/1.5 var(--mono);resize:vertical}
.preview-out{margin-top:10px;padding:10px;border-radius:8px;background:var(--surface-2);border:1px solid var(--border);font-family:var(--mono);font-size:12px;white-space:pre-wrap;word-break:break-word;display:none}
.err-box{margin-top:8px;padding:8px 10px;border-radius:6px;background:var(--red-bg);color:var(--red);font-size:12px}
.select{padding:5px 8px;border-radius:6px;border:1px solid var(--border);background:var(--surface-2);color:var(--text);font-size:12px}
.cap-list{display:flex;flex-direction:column;gap:10px;margin-top:12px}
.cap-item{border:1px solid var(--border);border-radius:8px;background:var(--surface-2);overflow:hidden}
.cap-summary{padding:10px 12px;cursor:pointer;display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px;list-style:none}
.cap-summary::-webkit-details-marker{display:none}
.cap-body{padding:12px;border-top:1px solid var(--border);background:var(--surface);display:flex;flex-direction:column;gap:12px}
.diff-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:12px}
.diff-pane{display:flex;flex-direction:column;gap:6px}
.diff-title{font-size:12px;font-weight:600;color:var(--muted);display:flex;justify-content:space-between;gap:8px}
.code-box{margin:0;padding:10px;border-radius:8px;background:var(--bg);border:1px solid var(--border);font:12px/1.45 var(--mono);white-space:pre-wrap;word-break:break-word;max-height:360px;overflow:auto}
.orig-val{color:var(--red);font-weight:600;word-break:break-all}
.ph-val{color:var(--blue);word-break:break-all}`

const dashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
{{if gt .RefreshSeconds 0}}<meta http-equiv="refresh" content="{{.RefreshSeconds}}">{{end}}
<title>cpa-reductant 状态监控</title>
<style>
/*__DASHBOARD_CSS__*/
</style>
</head>
<body>
<div class="wrap">
  <div class="card">
    <div class="header">
      <div>
        <div class="title-row">
          <h1>cpa-reductant</h1>
          <span class="badge {{.HealthClass}}"><span class="dot"></span>{{.HealthLabel}}</span>
          <span class="badge badge-muted mono">v{{.Status.Version}}</span>
        </div>
        <div class="sub" style="margin-top:4px">
          启动 <span class="mono">{{.Status.StartedAt}}</span> · 已运行 <span class="mono">{{.UptimeText}}</span>
          {{if .Status.LastActivityAt}} · 最近请求 <span class="mono">{{.Status.LastActivityAt}}</span>{{else}} · 暂无请求{{end}}
          {{if .Status.LastReconfiguredAt}} · 最近重载 <span class="mono">{{.Status.LastReconfiguredAt}}</span>{{end}}
        </div>
      </div>
      <div class="actions">
        {{if gt .RefreshSeconds 0}}
        <a class="btn active" href="{{.StopRefreshLink}}">停止自动刷新 ({{.RefreshSeconds}}s)</a>
        {{else}}
        <a class="btn" href="{{.RefreshLink}}">5s 自动刷新</a>
        {{end}}
        <a class="btn" href="{{.BaseURL}}">刷新</a>
        <a class="btn" href="{{.JSONURL}}" target="_blank" rel="noopener">JSON</a>
      </div>
    </div>

    <div class="chips">
      <span class="chip"><strong>密钥脱敏:</strong> {{if .Status.Policy.Secrets}}开{{else}}关{{end}}</span>
      <span class="chip"><strong>个人隐私:</strong> {{if .Status.Policy.Personal}}开{{else}}关{{end}}</span>
      <span class="chip"><strong>词表:</strong> {{.Status.Policy.WordsCount}}</span>
      <span class="chip"><strong>规则:</strong> {{.Status.Policy.RulesCount}}</span>
      <span class="chip"><strong>白名单:</strong> {{.Status.Policy.AllowCount}}</span>
      <span class="chip"><strong>替换形式:</strong> {{if .Status.Policy.Shape}}保形替换{{else}}占位符{{end}}</span>
      <span class="chip"><strong>HMAC 密钥:</strong>
        {{if not .Status.Policy.KeyPathConfigured}}临时（仅内存）
        {{else if .Status.Policy.KeyPathActive}}已持久化
        {{else}}异常{{end}}
      </span>
      <span class="chip"><strong>PII 模型:</strong>
        {{if not .Status.Model.Enabled}}未启用
        {{else if .Status.Model.Loaded}}{{.Status.Model.Name}} · 已加载 ({{.Status.Model.Cores}} 核)
        {{else if .Status.Model.Downloading}}{{.Status.Model.Name}} · 下载中 ({{printf "%.0f" .DownloadPercent}}%)
        {{else if .Status.Model.Installed}}{{.Status.Model.Name}} · 就绪
        {{else}}{{.Status.Model.Name}} · 缺失{{end}}
      </span>
    </div>
    {{if .Status.Policy.KeyPathError}}<div class="err-box">HMAC 密钥异常: {{.Status.Policy.KeyPathError}}</div>{{end}}
    {{if .Status.Model.LastError}}<div class="err-box">模型错误: {{.Status.Model.LastError}}{{if .Status.Model.RetryAt}}（退避至 {{.Status.Model.RetryAt}}）{{end}}</div>{{end}}
    {{if .Status.Model.DownloadError}}<div class="err-box">模型下载失败: {{.Status.Model.DownloadError}}</div>{{end}}
    {{if .Status.Model.DiskCacheError}}<div class="err-box">磁盘缓存写入失败: {{.Status.Model.DiskCacheError}}</div>{{end}}
  </div>

  <div class="grid-4">
    <div class="card">
      <div class="kpi-label">请求处理</div>
      <div class="kpi-val">{{.Status.Stats.RequestsTotal}}</div>
      <div class="kpi-meta">
        <span>脱敏 <strong>{{.Status.Stats.RequestsMasked}}</strong> ({{.FilterRate}})</span>
        <span>拦截 <strong>{{.Status.Stats.RequestsBlocked}}</strong> ({{.BlockRate}})</span>
        <span>告警 <strong>{{.Status.Stats.RequestsWarned}}</strong></span>
      </div>
      <div class="kpi-meta">
        <span>平均 <strong>{{fmtMS .Status.Stats.MaskLatency.AvgMS}}</strong></span>
        <span>P95 <strong>{{fmtMS .Status.Stats.MaskLatency.P95MS}}</strong></span>
      </div>
    </div>

    <div class="card">
      <div class="kpi-label">敏感命中</div>
      <div class="kpi-val">{{.Status.Stats.HitsTotal}}</div>
      <div class="kpi-meta">
        <span>脱敏替换 <strong>{{.Status.Stats.HitsMasked}}</strong></span>
        <span>告警放行 <strong>{{.Status.Stats.HitsWarned}}</strong></span>
        <span>拒绝拦截 <strong>{{.Status.Stats.HitsBlocked}}</strong></span>
      </div>
    </div>

    <div class="card">
      <div class="kpi-label">响应还原</div>
      <div class="kpi-val">{{.Status.Stats.PlaceholdersRestored}} <span class="sub">次</span></div>
      <div class="kpi-meta">
        <span>JSON <strong>{{.Status.Stats.ResponsesRestored}}/{{.Status.Stats.ResponsesTotal}}</strong></span>
        <span>SSE 流 <strong>{{.Status.Stats.StreamsRestored}}/{{.Status.Stats.StreamsTotal}}</strong></span>
        {{if gt .Status.Stats.StreamsFailed 0}}<span style="color:var(--red)">流失败 <strong>{{.Status.Stats.StreamsFailed}}</strong></span>{{end}}
      </div>
      <div class="kpi-meta">
        <span>平均 <strong>{{fmtMS .Status.Stats.RestoreLatency.AvgMS}}</strong></span>
        <span>P95 <strong>{{fmtMS .Status.Stats.RestoreLatency.P95MS}}</strong></span>
      </div>
    </div>

    <div class="card">
      <div class="kpi-label">流量与缓存</div>
      <div class="kpi-val">{{.BytesInText}} <span class="sub">入站</span></div>
      <div class="kpi-meta">
        <span>出站 <strong>{{.BytesOutText}}</strong></span>
        <span>缓存 <strong>{{.Status.Model.CacheEntries}}</strong> 条</span>
        <span>命中率 <strong>{{.CacheHitRate}}</strong></span>
      </div>
      {{if .Status.Model.Enabled}}
      <div class="kpi-meta">
        <span>推理平均 <strong>{{fmtMS .Status.Model.InferLatency.AvgMS}}</strong></span>
        <span>降级 <strong>{{.Status.Model.PartialTimeouts}}</strong></span>
        <span>排队 <strong>{{.Status.Model.Waiting}}</strong></span>
      </div>
      {{end}}
    </div>
  </div>

  <div class="grid-2">
    <div class="card">
      <h2>命中类别<span class="sub">按脱敏类型</span></h2>
      {{if .KindBars}}
      <div class="bar-list">
        {{range .KindBars}}
        <div class="bar-row">
          <div class="bar-head">
            <span class="mono"><strong>{{.Label}}</strong></span>
            <span class="mono">{{.Count}} ({{printf "%.1f" .Percent}}%)</span>
          </div>
          <div class="bar-track"><div class="bar-fill" style="width:{{printf "%.1f" .Percent}}%"></div></div>
        </div>
        {{end}}
      </div>
      {{else}}
      <div class="empty">暂无命中记录</div>
      {{end}}
    </div>

    <div class="card">
      <h2>检出方式<span class="sub">按识别来源</span></h2>
      {{if .FinderBars}}
      <div class="bar-list">
        {{range .FinderBars}}
        <div class="bar-row">
          <div class="bar-head">
            <span class="mono"><strong>{{.Label}}</strong> <span class="sub">{{.Meta}}</span></span>
            <span class="mono">{{.Count}} ({{printf "%.1f" .Percent}}%)</span>
          </div>
          <div class="bar-track"><div class="bar-fill green" style="width:{{printf "%.1f" .Percent}}%"></div></div>
        </div>
        {{end}}
      </div>
      {{else}}
      <div class="empty">暂无检出记录</div>
      {{end}}
    </div>
  </div>

  <div class="grid-2">
    <div class="card">
      <h2>协议统计</h2>
      {{if .ProtocolRows}}
      <table>
        <thead><tr><th>协议</th><th>请求</th><th>脱敏 / 拦截</th><th>命中</th><th>还原</th><th>入站</th></tr></thead>
        <tbody>
          {{range .ProtocolRows}}
          <tr>
            <td class="mono"><strong>{{.Name}}</strong></td>
            <td class="mono">{{.Stats.Requests}}</td>
            <td class="mono">{{.Stats.Masked}} / {{.Stats.Blocked}}</td>
            <td class="mono">{{.Stats.Hits}}</td>
            <td class="mono">{{.Stats.PlaceholdersRestored}}</td>
            <td class="mono">{{.BytesStr}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="empty">暂无请求数据</div>
      {{end}}
    </div>

    <div class="card">
      <h2>模型统计</h2>
      {{if .ModelRows}}
      <table>
        <thead><tr><th>模型</th><th>请求</th><th>脱敏 / 拦截</th><th>命中</th><th>还原</th><th>入站</th></tr></thead>
        <tbody>
          {{range .ModelRows}}
          <tr>
            <td class="mono"><strong>{{.Name}}</strong></td>
            <td class="mono">{{.Stats.Requests}}</td>
            <td class="mono">{{.Stats.Masked}} / {{.Stats.Blocked}}</td>
            <td class="mono">{{.Stats.Hits}}</td>
            <td class="mono">{{.Stats.PlaceholdersRestored}}</td>
            <td class="mono">{{.BytesStr}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="empty">暂无模型数据</div>
      {{end}}
    </div>
  </div>

  <div class="card">
    <h2>
      <span>最近过滤记录 <span class="sub">仅保留前缀提示与占位符，不保存明文</span></span>
      <span class="actions">
        <a class="btn {{if eq .ActionFilter ""}}active{{end}}" href="{{.BaseURL}}">全部</a>
        <a class="btn {{if eq .ActionFilter "mask"}}active{{end}}" href="{{.BaseURL}}{{if containsQ .BaseURL}}&amp;{{else}}?{{end}}action=mask">脱敏</a>
        <a class="btn {{if eq .ActionFilter "warn"}}active{{end}}" href="{{.BaseURL}}{{if containsQ .BaseURL}}&amp;{{else}}?{{end}}action=warn">告警</a>
        <a class="btn {{if eq .ActionFilter "block"}}active{{end}}" href="{{.BaseURL}}{{if containsQ .BaseURL}}&amp;{{else}}?{{end}}action=block">拦截</a>
        <a class="btn {{if eq .ActionFilter "restore"}}active{{end}}" href="{{.BaseURL}}{{if containsQ .BaseURL}}&amp;{{else}}?{{end}}action=restore">还原</a>
        <a class="btn {{if eq .ActionFilter "passthrough"}}active{{end}}" href="{{.BaseURL}}{{if containsQ .BaseURL}}&amp;{{else}}?{{end}}action=passthrough">放行</a>
        <a class="btn {{if eq .ActionFilter "error"}}active{{end}}" href="{{.BaseURL}}{{if containsQ .BaseURL}}&amp;{{else}}?{{end}}action=error">错误</a>
      </span>
    </h2>
    {{if .HasEvents}}
    <div style="overflow-x:auto">
      <table>
        <thead>
          <tr>
            <th>时间</th>
            <th>阶段</th>
            <th>处理</th>
            <th>协议 / 模型</th>
            <th>大小 / 耗时</th>
            <th>详情</th>
          </tr>
        </thead>
        <tbody>
          {{range .Status.RecentEvents}}
          <tr>
            <td class="mono" title="{{.Timestamp}}">#{{.ID}} {{shortTime .Timestamp}}</td>
            <td class="mono">{{phaseLabel .Phase}}</td>
            <td><span class="badge {{actionClass .Action}}">{{actionLabel .Action}}</span></td>
            <td class="mono">{{if .Protocol}}{{.Protocol}}{{else}}-{{end}}{{if .Model}} / {{.Model}}{{end}}</td>
            <td class="mono">{{fmtBytes .Bytes}} · {{fmtMS .DurationMS}}</td>
            <td>
              {{if .Hits}}
              <div class="hits">
                {{range .Hits}}
                <span class="hit-pill {{.Action}}">
                  <strong>{{.Kind}}</strong>
                  {{if .By}}<span>[{{.By}}]</span>{{end}}
                  {{if .Head}}<span>{{.Head}}*** ({{.Len}} 字符)</span>{{else if gt .Len 0}}<span>({{.Len}} 字符)</span>{{end}}
                  {{if .Placeholder}}<span>→ {{.Placeholder}}</span>{{end}}
                  {{if ne .Action "mask"}}<span>({{actionLabel .Action}})</span>{{end}}
                </span>
                {{end}}
              </div>
              {{else if gt .RestoredCount 0}}
              <span class="badge badge-green">还原 {{.RestoredCount}} 处占位符</span>
              {{else if .Error}}
              <span style="color:var(--red)" class="mono">{{.Error}}</span>
              {{else}}
              <span class="sub">无敏感命中，原样转发</span>
              {{end}}
            </td>
          </tr>
          {{end}}
        </tbody>
      </table>
    </div>
    {{else}}
    <div class="empty">暂无记录</div>
    {{end}}
  </div>

  <div class="card">
    <h2>
      <span>调试抓包 <span class="sub">临时内存开关（支持 TTL 自动关闭），对比原始请求、脱敏报文与响应还原</span></span>
      <span class="actions">
        <span id="cap-badge" class="badge badge-muted">未加载</span>
        <select id="cap-ttl" class="select" title="抓包有效期">
          <option value="60">1 分钟</option>
          <option value="300" selected>5 分钟</option>
          <option value="900">15 分钟</option>
          <option value="1800">30 分钟</option>
        </select>
        <button id="cap-toggle-btn" class="btn" type="button" onclick="toggleCapture()">开启抓包</button>
        <button class="btn" type="button" onclick="loadCapture()">刷新抓包</button>
        <button class="btn" type="button" onclick="clearCapture()">清空记录</button>
      </span>
    </h2>
    <div class="sub">curl 接口：<span class="mono">{{.DebugCaptureURL}}</span></div>
    <div id="cap-err" class="err-box" style="display:none"></div>
    <div id="cap-list" class="cap-list"><div class="empty">点击「刷新抓包」或「开启抓包」加载调试捕获</div></div>
  </div>

  <div class="card">
    <h2>脱敏预览 <span class="sub">只读测试，不计入统计或还原映射</span></h2>
    <textarea id="preview-input" placeholder="输入测试文本（例如：Authorization: Bearer sk-ant-api03-example1234567890, contact alice@example.com）"></textarea>
    <div class="actions" style="margin-top:8px">
      <button class="btn" type="button" onclick="runPreview()">测试</button>
      <span id="preview-meta" class="sub"></span>
    </div>
    <div id="preview-out" class="preview-out"></div>
  </div>
</div>
<script>
var capState = { enabled: false, openIDs: {} };
function escCap(v){
  return String(v == null ? '' : v).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}
function prettyCap(s){
  if(!s) return '';
  try{ return JSON.stringify(JSON.parse(s), null, 2); }catch(_){ return s; }
}
function renderCaptureSnap(snap){
  capState.enabled = !!snap.enabled;
  var badge = document.getElementById('cap-badge');
  var btn = document.getElementById('cap-toggle-btn');
  if(snap.enabled){
    badge.className = 'badge badge-amber';
    badge.textContent = '抓包中 · 剩余 ' + (snap.remaining_seconds || 0) + 's';
    btn.className = 'btn active';
    btn.textContent = '停止抓包';
  }else{
    badge.className = 'badge badge-muted';
    badge.textContent = '已关闭 · 缓存 ' + (snap.entries ? snap.entries.length : 0) + ' 条';
    btn.className = 'btn';
    btn.textContent = '开启抓包';
  }
  var list = document.getElementById('cap-list');
  var entries = snap.entries || [];
  if(!entries.length){
    list.innerHTML = '<div class="empty">' + (snap.enabled ? '抓包已开启，等待新请求...' : '暂无抓包记录') + '</div>';
    return;
  }
  list.innerHTML = entries.map(function(ev){
    var openAttr = capState.openIDs[ev.id] ? ' open' : '';
    var reps = ev.replacements || [];
    var restored = ev.restored_items || [];
    var repHtml = reps.length ? ('<table><thead><tr><th>原文</th><th>替换值 / 占位符</th><th>类型</th><th>方式</th><th>动作</th></tr></thead><tbody>' +
      reps.map(function(r){
        var orig = r.original ? '<span class="orig-val">' + escCap(r.original) + '</span>' : '<span class="sub">(未替换 · 仅告警)</span>';
        return '<tr><td class="mono">' + orig + '</td><td class="mono ph-val">' + escCap(r.placeholder || '-') + '</td><td class="mono">' + escCap(r.kind) + '</td><td class="mono">' + escCap(r.by || '-') + '</td><td class="mono">' + escCap(r.action || 'mask') + '</td></tr>';
      }).join('') + '</tbody></table>') : '<div class="sub">请求阶段无敏感替换</div>';
    var resHtml = restored.length ? ('<table><thead><tr><th>占位符</th><th>还原原文</th><th>次数</th></tr></thead><tbody>' +
      restored.map(function(r){
        return '<tr><td class="mono ph-val">' + escCap(r.placeholder) + '</td><td class="mono orig-val">' + escCap(r.original || '-') + '</td><td class="mono">' + (r.count || 0) + '</td></tr>';
      }).join('') + '</tbody></table>') : '';
    var statusBadge = '';
    if(ev.status === 'streaming') statusBadge = '<span class="badge badge-amber">流式传输中</span>';
    else if(ev.status === 'pending') statusBadge = '<span class="badge badge-muted">等待响应</span>';
    else if(ev.status === 'error') statusBadge = '<span class="badge badge-red">异常</span>';
    var upText = ev.response_binary ? ('[二进制响应 ' + (ev.response_content_type || '') + ', ' + (ev.upstream_response_bytes || 0) + ' B]') : (prettyCap(ev.upstream_response) || (ev.status === 'streaming' ? '[流式传输中，等待数据...]' : ''));
    var reText = ev.response_binary ? ('[二进制响应 ' + (ev.response_content_type || '') + ', ' + (ev.restored_response_bytes || 0) + ' B]') : (prettyCap(ev.restored_response) || (ev.status === 'streaming' ? '[流式传输中，等待输出...]' : ''));
    return '<details class="cap-item" data-cap-id="' + ev.id + '"' + openAttr + '>' +
      '<summary class="cap-summary">' +
        '<span class="mono"><strong>#' + ev.id + '</strong> ' + escCap(ev.timestamp) + ' · ' + escCap(ev.protocol || '-') + (ev.model ? ' / ' + escCap(ev.model) : '') + (ev.stream ? ' [SSE]' : '') + '</span>' +
        '<span class="actions">' + statusBadge + '<span class="badge badge-blue">' + escCap(ev.action) + '</span><span class="sub mono">替换 ' + reps.length + ' · 还原 ' + (ev.restored_count || 0) + '</span></span>' +
      '</summary>' +
      '<div class="cap-body">' +
        (ev.error ? '<div class="err-box">' + escCap(ev.error) + '</div>' : '') +
        '<div><div class="diff-title"><span>请求替换明细 (' + reps.length + ')</span></div>' + repHtml + '</div>' +
        (resHtml ? '<div><div class="diff-title"><span>响应还原明细 (' + (ev.restored_count || 0) + ')</span></div>' + resHtml + '</div>' : '') +
        '<div class="diff-grid">' +
          '<div class="diff-pane"><div class="diff-title"><span>原始请求 (Raw)</span><span class="mono">' + (ev.raw_request_bytes || 0) + ' B' + (ev.raw_request_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + escCap(prettyCap(ev.raw_request)) + '</pre></div>' +
          '<div class="diff-pane"><div class="diff-title"><span>脱敏请求 (Masked → 上游)</span><span class="mono">' + (ev.masked_request_bytes || 0) + ' B' + (ev.masked_request_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + escCap(prettyCap(ev.masked_request)) + '</pre></div>' +
        '</div>' +
        '<div class="diff-grid">' +
          '<div class="diff-pane"><div class="diff-title"><span>上游原始响应 (Upstream)</span><span class="mono">' + (ev.upstream_response_bytes || 0) + ' B' + (ev.upstream_response_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + escCap(upText) + '</pre></div>' +
          '<div class="diff-pane"><div class="diff-title"><span>还原后响应 (Restored → 客户端)</span><span class="mono">' + (ev.restored_response_bytes || 0) + ' B' + (ev.restored_response_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + escCap(reText) + '</pre></div>' +
        '</div>' +
      '</div></details>';
  }).join('');
  Array.prototype.forEach.call(list.querySelectorAll('details[data-cap-id]'), function(el){
    el.ontoggle = function(){ capState.openIDs[el.getAttribute('data-cap-id')] = el.open; };
  });
}
async function callCapture(method, body){
  var errEl = document.getElementById('cap-err');
  errEl.style.display = 'none';
  try{
    var opts = {method: method, headers: {}};
    if(body !== undefined){
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    var resp = await fetch('{{.DebugCaptureURL}}', opts);
    var data = await resp.json();
    if(!resp.ok){
      errEl.style.display = 'block';
      errEl.textContent = '错误: ' + (data.error || resp.status);
      return;
    }
    renderCaptureSnap(data);
  }catch(e){
    errEl.style.display = 'block';
    errEl.textContent = '请求失败: ' + e;
  }
}
function loadCapture(){ return callCapture('GET'); }
function toggleCapture(){
  if(capState.enabled){ return callCapture('POST', {enabled: false}); }
  var ttl = parseInt(document.getElementById('cap-ttl').value || '300', 10);
  return callCapture('POST', {enabled: true, ttl_seconds: ttl});
}
function clearCapture(){
  capState.openIDs = {};
  return callCapture('DELETE');
}
async function runPreview(){
  var input=document.getElementById('preview-input').value;
  var out=document.getElementById('preview-out');
  var meta=document.getElementById('preview-meta');
  if(!input){return;}
  meta.textContent='处理中...';
  try{
    var resp=await fetch('{{.PreviewURL}}',{
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body:JSON.stringify({text:input})
    });
    var data=await resp.json();
    if(!resp.ok){
      out.style.display='block';
      out.textContent='错误: '+(data.error||resp.status);
      meta.textContent='';
      return;
    }
    out.style.display='block';
    var hits=(data.hits||[]).map(function(h){
      return '- '+h.Kind+' ['+(h.By||'-')+'] '+(h.Action||'mask')+' '+(h.Head?h.Head+'*** ':'')+'('+h.Len+' 字符) -> '+(h.Placeholder||'');
    }).join('\n');
    out.textContent='脱敏结果：\n'+data.masked+(hits?'\n\n命中列表：\n'+hits:'\n\n命中列表：\n无');
    meta.textContent='耗时 '+data.duration_ms+' ms · 命中 '+(data.hits?data.hits.length:0)+' 项';
  }catch(e){
    out.style.display='block';
    out.textContent='请求失败（若当前环境未自动携带管理鉴权头，请通过管理端或 curl 调用 /preview）: '+e;
    meta.textContent='';
  }
}
</script>
</body>
</html>
`

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

var portalHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>cpa-reductant 状态监控</title>
<style>
/*__DASHBOARD_CSS__*/
.auth-wrap{max-width:460px;margin:48px auto}
.auth-form{display:flex;gap:8px;margin-top:12px;flex-wrap:wrap}
.input{flex:1;min-width:200px;padding:7px 10px;border-radius:6px;border:1px solid var(--border);background:var(--bg);color:var(--text);font:13px/1.4 var(--mono)}
.hidden{display:none!important}
body.embedded{padding-top:88px}
body.embedded .auth-wrap{margin-top:12px}
@media (max-width:768px){body.embedded{padding-top:72px}}
</style>
</head>
<body>
<div id="auth-card" class="card auth-wrap">
  <div class="title-row">
    <h1>cpa-reductant</h1>
    <span class="badge badge-amber"><span class="dot"></span>需要登录</span>
  </div>
  <div class="sub" style="margin-top:8px">
    请输入 CPA 管理密钥（<span class="mono">management.secret-key</span>）查看监控看板。密钥仅保存在当前标签页。
  </div>
  <form id="auth-form" class="auth-form">
    <input id="secret-input" class="input" type="password" autocomplete="current-password" placeholder="输入 management.secret-key" required>
    <button id="auth-submit" class="btn active" type="submit">进入看板</button>
  </form>
  <div id="auth-err" class="err-box hidden"></div>
</div>

<div id="app" class="wrap hidden">
  <div class="card">
    <div class="header">
      <div>
        <div class="title-row">
          <h1>cpa-reductant</h1>
          <span id="health-badge" class="badge badge-blue"></span>
          <span id="version-badge" class="badge badge-muted mono"></span>
        </div>
        <div id="time-sub" class="sub" style="margin-top:4px"></div>
      </div>
      <div class="actions">
        <button id="auto-refresh-btn" class="btn" type="button" onclick="toggleAutoRefresh()">5s 自动刷新</button>
        <button class="btn" type="button" onclick="loadStatus()">刷新</button>
        <button class="btn" type="button" onclick="openJSON()">JSON</button>
        <button class="btn" type="button" onclick="disconnectKey()">退出登录</button>
      </div>
    </div>
    <div id="policy-chips" class="chips"></div>
    <div id="runtime-errors"></div>
  </div>

  <div id="kpi-grid" class="grid-4"></div>

  <div class="grid-2">
    <div class="card">
      <h2>命中类别<span class="sub">按脱敏类型</span></h2>
      <div id="kind-bars"></div>
    </div>
    <div class="card">
      <h2>检出方式<span class="sub">按识别来源</span></h2>
      <div id="finder-bars"></div>
    </div>
  </div>

  <div class="grid-2">
    <div class="card">
      <h2>协议统计</h2>
      <div id="protocol-table"></div>
    </div>
    <div class="card">
      <h2>模型统计</h2>
      <div id="model-table"></div>
    </div>
  </div>

  <div class="card">
    <h2>
      <span>最近过滤记录 <span class="sub">仅保留前缀提示与占位符，不保存明文</span></span>
      <span id="event-filters" class="actions"></span>
    </h2>
    <div id="events-table"></div>
  </div>

  <div class="card">
    <h2>
      <span>调试抓包 <span class="sub">临时内存开关（支持 TTL 自动关闭），对比原始请求、脱敏报文与响应还原</span></span>
      <span class="actions">
        <span id="cap-badge" class="badge badge-muted">已关闭</span>
        <select id="cap-ttl" class="select" title="抓包有效期">
          <option value="60">1 分钟</option>
          <option value="300" selected>5 分钟</option>
          <option value="900">15 分钟</option>
          <option value="1800">30 分钟</option>
        </select>
        <button id="cap-toggle-btn" class="btn" type="button" onclick="toggleCapture()">开启抓包</button>
        <button class="btn" type="button" onclick="loadCapture()">刷新抓包</button>
        <button class="btn" type="button" onclick="clearCapture()">清空记录</button>
      </span>
    </h2>
    <div class="sub">支持 <span class="mono">curl</span> 调用：<span class="mono">/v0/management/cpa-reductant/debug/capture</span>（GET 查看 / POST 开关 / DELETE 清空）</div>
    <div id="cap-err" class="err-box hidden"></div>
    <div id="cap-list" class="cap-list"><div class="empty">抓包未开启</div></div>
  </div>

  <div class="card">
    <h2>脱敏预览 <span class="sub">只读测试，不计入统计或还原映射</span></h2>
    <textarea id="preview-input" placeholder="输入测试文本（例如：Authorization: Bearer sk-ant-api03-example1234567890, contact alice@example.com）"></textarea>
    <div class="actions" style="margin-top:8px">
      <button class="btn" type="button" onclick="runPreview()">测试</button>
      <span id="preview-meta" class="sub"></span>
    </div>
    <div id="preview-out" class="preview-out"></div>
  </div>
</div>

<script>
(function(){
  try{
    if(window.self !== window.top){
      document.body.classList.add('embedded');
    }
  }catch(_){
    document.body.classList.add('embedded');
  }
  var KEY_STORAGE = 'cpa_reductant_mgmt_key';
  var STATUS_URL = '/v0/management/cpa-reductant/status';
  var PREVIEW_URL = '/v0/management/cpa-reductant/preview';
  var DEBUG_CAPTURE_URL = '/v0/management/cpa-reductant/debug/capture';
  var FINDER_META = {rule:'内置规则',yours:'自定义规则',word:'词表',model:'本地模型',known:'关联匹配'};
  var ACTION_FILTERS = [['','全部'],['mask','脱敏'],['warn','告警'],['block','拦截'],['restore','还原'],['passthrough','放行'],['error','错误']];

  var state = {
    key: '',
    actionFilter: '',
    autoTimer: null,
    lastStatus: null,
    captureEnabled: false,
    openCaptures: {}
  };

  function esc(v){
    return String(v == null ? '' : v)
      .replace(/&/g,'&amp;')
      .replace(/</g,'&lt;')
      .replace(/>/g,'&gt;')
      .replace(/"/g,'&quot;')
      .replace(/'/g,'&#39;');
  }

  function fmtMS(v){
    var n = Number(v || 0);
    if(!n) return '0 ms';
    return n < 1 ? n.toFixed(2)+' ms' : n.toFixed(1)+' ms';
  }

  function fmtBytes(b){
    var n = Number(b || 0);
    if(n < 1024) return n + ' B';
    var units = ['KiB','MiB','GiB','TiB'];
    var exp = 0;
    var div = 1024;
    while(n / div >= 1024 && exp < units.length - 1){
      div *= 1024;
      exp++;
    }
    return (n / div).toFixed(1) + ' ' + units[exp];
  }

  function fmtDuration(sec){
    var s = Number(sec || 0);
    if(s < 60) return s + 's';
    if(s < 3600) return Math.floor(s / 60) + 'm' + (s % 60) + 's';
    return Math.floor(s / 3600) + 'h' + Math.floor((s % 3600) / 60) + 'm';
  }

  function shortTime(ts){
    if(!ts) return '';
    var d = new Date(ts);
    if(isNaN(d.getTime())) return ts;
    var pad = function(n){ return n < 10 ? '0' + n : '' + n; };
    return pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }

  function pctString(num, den){
    if(!den || den <= 0) return '0%';
    return ((num * 100) / den).toFixed(1) + '%';
  }

  function phaseLabel(p){
    switch(String(p || '').toLowerCase()){
      case 'request': return '请求';
      case 'response': return '响应';
      case 'stream': return '流式';
      case 'model': return '模型';
      default: return p || '';
    }
  }

  function actionLabel(a){
    switch(String(a || '').toLowerCase()){
      case 'block': return '拦截';
      case 'warn': return '告警';
      case 'mask': return '脱敏';
      case 'restore': return '还原';
      case 'passthrough': return '放行';
      case 'error': return '错误';
      case 'degraded': return '降级';
      default: return a || '';
    }
  }

  function actionClass(a){
    switch(String(a || '').toLowerCase()){
      case 'block': case 'error': case 'degraded': return 'badge-red';
      case 'warn': return 'badge-amber';
      case 'mask': return 'badge-blue';
      case 'restore': return 'badge-green';
      default: return 'badge-muted';
    }
  }

  function deriveHealth(st){
    var m = st.model || {};
    var s = st.stats || {};
    if(m.enabled){
      if(m.last_error || m.download_error) return ['模型异常（规则兜底）','badge-red'];
      if(m.downloading) return ['模型下载中','badge-amber'];
      if(m.behind) return ['模型繁忙（超时降级）','badge-amber'];
      if(!m.installed) return ['等待模型文件','badge-amber'];
    }
    if((s.requests_total || 0) > 0) return ['运行中','badge-green'];
    return ['空闲','badge-blue'];
  }

  function showAuth(msg){
    stopAutoRefresh();
    state.key = '';
    try{ sessionStorage.removeItem(KEY_STORAGE); }catch(_){}
    document.getElementById('app').classList.add('hidden');
    document.getElementById('auth-card').classList.remove('hidden');
    var errEl = document.getElementById('auth-err');
    if(msg){
      errEl.textContent = msg;
      errEl.classList.remove('hidden');
    } else {
      errEl.textContent = '';
      errEl.classList.add('hidden');
    }
  }

  function showApp(){
    document.getElementById('auth-card').classList.add('hidden');
    document.getElementById('app').classList.remove('hidden');
  }

  function renderBars(elId, counts, metaMap, green){
    var el = document.getElementById(elId);
    var entries = Object.entries(counts || {}).filter(function(kv){ return kv[1] > 0; });
    if(!entries.length){
      el.innerHTML = '<div class="empty">暂无记录</div>';
      return;
    }
    var total = entries.reduce(function(acc, kv){ return acc + kv[1]; }, 0);
    entries.sort(function(a, b){ return b[1] === a[1] ? (a[0] < b[0] ? -1 : 1) : b[1] - a[1]; });
    el.innerHTML = '<div class="bar-list">' + entries.map(function(kv){
      var pct = ((kv[1] * 100) / total).toFixed(1);
      var meta = metaMap && metaMap[kv[0]] ? ' <span class="sub">' + esc(metaMap[kv[0]]) + '</span>' : '';
      return '<div class="bar-row">' +
        '<div class="bar-head"><span class="mono"><strong>' + esc(kv[0]) + '</strong>' + meta + '</span>' +
        '<span class="mono">' + kv[1] + ' (' + pct + '%)</span></div>' +
        '<div class="bar-track"><div class="bar-fill' + (green ? ' green' : '') + '" style="width:' + pct + '%"></div></div>' +
      '</div>';
    }).join('') + '</div>';
  }

  function renderDimTable(elId, dims, label, emptyMsg){
    var el = document.getElementById(elId);
    var entries = Object.entries(dims || {});
    if(!entries.length){
      el.innerHTML = '<div class="empty">' + esc(emptyMsg) + '</div>';
      return;
    }
    entries.sort(function(a, b){
      var ra = a[1].requests || 0, rb = b[1].requests || 0;
      return rb === ra ? (a[0] < b[0] ? -1 : 1) : rb - ra;
    });
    el.innerHTML = '<table><thead><tr><th>' + esc(label) + '</th><th>请求</th><th>脱敏 / 拦截</th><th>命中</th><th>还原</th><th>入站</th></tr></thead><tbody>' +
      entries.map(function(kv){
        var d = kv[1] || {};
        return '<tr>' +
          '<td class="mono"><strong>' + esc(kv[0]) + '</strong></td>' +
          '<td class="mono">' + (d.requests || 0) + '</td>' +
          '<td class="mono">' + (d.masked || 0) + ' / ' + (d.blocked || 0) + '</td>' +
          '<td class="mono">' + (d.hits || 0) + '</td>' +
          '<td class="mono">' + (d.placeholders_restored || 0) + '</td>' +
          '<td class="mono">' + fmtBytes(d.bytes_in || 0) + '</td>' +
        '</tr>';
      }).join('') + '</tbody></table>';
  }

  function renderStatus(st){
    state.lastStatus = st;
    var health = deriveHealth(st);
    var hb = document.getElementById('health-badge');
    hb.className = 'badge ' + health[1];
    hb.innerHTML = '<span class="dot"></span>' + esc(health[0]);
    document.getElementById('version-badge').textContent = 'v' + (st.version || '');

    var tsHtml = '启动 <span class="mono">' + esc(st.started_at) + '</span> · 已运行 <span class="mono">' + esc(fmtDuration(st.uptime_seconds)) + '</span>';
    tsHtml += st.last_activity_at ? ' · 最近请求 <span class="mono">' + esc(st.last_activity_at) + '</span>' : ' · 暂无请求';
    if(st.last_reconfigured_at) tsHtml += ' · 最近重载 <span class="mono">' + esc(st.last_reconfigured_at) + '</span>';
    document.getElementById('time-sub').innerHTML = tsHtml;

    var p = st.policy || {};
    var m = st.model || {};
    var dlPct = m.download_total > 0 ? Math.round((m.download_done || 0) * 100 / m.download_total) : 0;
    var keyStatus = !p.key_path_configured ? '临时（仅内存）' : (p.key_path_active ? '已持久化' : '异常');
    var modelStatus = !m.enabled ? '未启用' :
      m.loaded ? (esc(m.name) + ' · 已加载 (' + (m.cores || 0) + ' 核)') :
      m.downloading ? (esc(m.name) + ' · 下载中 (' + dlPct + '%)') :
      m.installed ? (esc(m.name) + ' · 就绪') : (esc(m.name) + ' · 缺失');
    document.getElementById('policy-chips').innerHTML = [
      '<strong>密钥脱敏:</strong> ' + (p.secrets ? '开' : '关'),
      '<strong>个人隐私:</strong> ' + (p.personal ? '开' : '关'),
      '<strong>词表:</strong> ' + (p.words_count || 0),
      '<strong>规则:</strong> ' + (p.rules_count || 0),
      '<strong>白名单:</strong> ' + (p.allow_count || 0),
      '<strong>替换形式:</strong> ' + (p.shape ? '保形替换' : '占位符'),
      '<strong>HMAC 密钥:</strong> ' + keyStatus,
      '<strong>PII 模型:</strong> ' + modelStatus
    ].map(function(c){ return '<span class="chip">' + c + '</span>'; }).join('');

    var errs = [];
    if(p.key_path_error) errs.push('HMAC 密钥异常: ' + p.key_path_error);
    if(m.last_error) errs.push('模型错误: ' + m.last_error + (m.retry_at ? '（退避至 ' + m.retry_at + '）' : ''));
    if(m.download_error) errs.push('模型下载失败: ' + m.download_error);
    if(m.disk_cache_error) errs.push('磁盘缓存写入失败: ' + m.disk_cache_error);
    document.getElementById('runtime-errors').innerHTML = errs.map(function(e){
      return '<div class="err-box">' + esc(e) + '</div>';
    }).join('');

    var s = st.stats || {};
    var ml = s.mask_latency_ms || {};
    var rl = s.restore_latency_ms || {};
    var il = m.infer_latency_ms || {};
    document.getElementById('kpi-grid').innerHTML =
      '<div class="card"><div class="kpi-label">请求处理</div><div class="kpi-val">' + (s.requests_total || 0) + '</div>' +
      '<div class="kpi-meta"><span>脱敏 <strong>' + (s.requests_masked || 0) + '</strong> (' + pctString(s.requests_masked || 0, s.requests_total || 0) + ')</span>' +
      '<span>拦截 <strong>' + (s.requests_blocked || 0) + '</strong> (' + pctString(s.requests_blocked || 0, s.requests_total || 0) + ')</span>' +
      '<span>告警 <strong>' + (s.requests_warned || 0) + '</strong></span></div>' +
      '<div class="kpi-meta"><span>平均 <strong>' + fmtMS(ml.avg_ms) + '</strong></span><span>P95 <strong>' + fmtMS(ml.p95_ms) + '</strong></span></div></div>' +

      '<div class="card"><div class="kpi-label">敏感命中</div><div class="kpi-val">' + (s.hits_total || 0) + '</div>' +
      '<div class="kpi-meta"><span>脱敏替换 <strong>' + (s.hits_masked || 0) + '</strong></span>' +
      '<span>告警放行 <strong>' + (s.hits_warned || 0) + '</strong></span>' +
      '<span>拒绝拦截 <strong>' + (s.hits_blocked || 0) + '</strong></span></div></div>' +

      '<div class="card"><div class="kpi-label">响应还原</div><div class="kpi-val">' + (s.placeholders_restored || 0) + ' <span class="sub">次</span></div>' +
      '<div class="kpi-meta"><span>JSON <strong>' + (s.responses_restored || 0) + '/' + (s.responses_total || 0) + '</strong></span>' +
      '<span>SSE 流 <strong>' + (s.streams_restored || 0) + '/' + (s.streams_total || 0) + '</strong></span>' +
      ((s.streams_failed || 0) > 0 ? '<span style="color:var(--red)">流失败 <strong>' + s.streams_failed + '</strong></span>' : '') + '</div>' +
      '<div class="kpi-meta"><span>平均 <strong>' + fmtMS(rl.avg_ms) + '</strong></span><span>P95 <strong>' + fmtMS(rl.p95_ms) + '</strong></span></div></div>' +

      '<div class="card"><div class="kpi-label">流量与缓存</div><div class="kpi-val">' + fmtBytes(s.bytes_masked_in || 0) + ' <span class="sub">入站</span></div>' +
      '<div class="kpi-meta"><span>出站 <strong>' + fmtBytes(s.bytes_restored_out || 0) + '</strong></span>' +
      '<span>缓存 <strong>' + (m.cache_entries || 0) + '</strong> 条</span>' +
      '<span>命中率 <strong>' + pctString(m.cache_hits || 0, (m.cache_hits || 0) + (m.cache_misses || 0)) + '</strong></span></div>' +
      (m.enabled ? '<div class="kpi-meta"><span>推理平均 <strong>' + fmtMS(il.avg_ms) + '</strong></span><span>降级 <strong>' + (m.partial_timeouts || 0) + '</strong></span><span>排队 <strong>' + (m.waiting || 0) + '</strong></span></div>' : '') +
      '</div>';

    renderBars('kind-bars', s.hits_by_kind, null, false);
    renderBars('finder-bars', s.hits_by_finder, FINDER_META, true);
    renderDimTable('protocol-table', s.by_protocol, '协议', '暂无请求数据');
    renderDimTable('model-table', s.by_model, '模型', '暂无模型数据');

    document.getElementById('event-filters').innerHTML = ACTION_FILTERS.map(function(item){
      var active = state.actionFilter === item[0] ? ' active' : '';
      return '<button class="btn' + active + '" type="button" data-action="' + esc(item[0]) + '">' + esc(item[1]) + '</button>';
    }).join('');
    Array.prototype.forEach.call(document.querySelectorAll('#event-filters button'), function(btn){
      btn.onclick = function(){
        state.actionFilter = btn.getAttribute('data-action') || '';
        loadStatus();
      };
    });

    var events = st.recent_events || [];
    var evEl = document.getElementById('events-table');
    if(!events.length){
      evEl.innerHTML = '<div class="empty">暂无记录</div>';
      return;
    }
    evEl.innerHTML = '<div style="overflow-x:auto"><table><thead><tr><th>时间</th><th>阶段</th><th>处理</th><th>协议 / 模型</th><th>大小 / 耗时</th><th>详情</th></tr></thead><tbody>' +
      events.map(function(ev){
        var detail = '';
        if(ev.hits && ev.hits.length){
          detail = '<div class="hits">' + ev.hits.map(function(h){
            var cls = h.action && h.action !== 'mask' ? ' ' + esc(h.action) : '';
            var frag = '<strong>' + esc(h.kind) + '</strong>';
            if(h.by) frag += '<span>[' + esc(h.by) + ']</span>';
            if(h.head) frag += '<span>' + esc(h.head) + '*** (' + (h.len || 0) + ' 字符)</span>';
            else if((h.len || 0) > 0) frag += '<span>(' + h.len + ' 字符)</span>';
            if(h.placeholder) frag += '<span>→ ' + esc(h.placeholder) + '</span>';
            if(h.action && h.action !== 'mask') frag += '<span>(' + esc(actionLabel(h.action)) + ')</span>';
            return '<span class="hit-pill' + cls + '">' + frag + '</span>';
          }).join('') + '</div>';
        } else if((ev.restored_count || 0) > 0){
          detail = '<span class="badge badge-green">还原 ' + ev.restored_count + ' 处占位符</span>';
        } else if(ev.error){
          detail = '<span style="color:var(--red)" class="mono">' + esc(ev.error) + '</span>';
        } else {
          detail = '<span class="sub">无敏感命中，原样转发</span>';
        }
        return '<tr>' +
          '<td class="mono" title="' + esc(ev.timestamp) + '">#' + (ev.id || 0) + ' ' + esc(shortTime(ev.timestamp)) + '</td>' +
          '<td class="mono">' + esc(phaseLabel(ev.phase)) + '</td>' +
          '<td><span class="badge ' + actionClass(ev.action) + '">' + esc(actionLabel(ev.action)) + '</span></td>' +
          '<td class="mono">' + esc(ev.protocol || '-') + (ev.model ? ' / ' + esc(ev.model) : '') + '</td>' +
          '<td class="mono">' + fmtBytes(ev.bytes || 0) + ' · ' + fmtMS(ev.duration_ms) + '</td>' +
          '<td>' + detail + '</td>' +
        '</tr>';
      }).join('') + '</tbody></table></div>';
  }

  window.loadStatus = async function(){
    if(!state.key) return;
    var url = STATUS_URL + '?events=50' + (state.actionFilter ? '&action=' + encodeURIComponent(state.actionFilter) : '');
    try{
      var resp = await fetch(url, {
        headers: {'Authorization': 'Bearer ' + state.key, 'Accept': 'application/json'}
      });
      if(resp.status === 401 || resp.status === 403){
        showAuth('管理密钥无效或无权限 (HTTP ' + resp.status + ')');
        return;
      }
      var data = await resp.json();
      if(!resp.ok){
        document.getElementById('runtime-errors').innerHTML = '<div class="err-box">' + esc(data.error || ('HTTP ' + resp.status)) + '</div>';
        return;
      }
      showApp();
      renderStatus(data);
      window.loadCapture();
    }catch(e){
      if(document.getElementById('app').classList.contains('hidden')){
        showAuth('请求失败: ' + e);
      } else {
        document.getElementById('runtime-errors').innerHTML = '<div class="err-box">刷新失败: ' + esc(e) + '</div>';
      }
    }
  };

  function stopAutoRefresh(){
    if(state.autoTimer){
      clearInterval(state.autoTimer);
      state.autoTimer = null;
    }
    var btn = document.getElementById('auto-refresh-btn');
    if(btn){
      btn.classList.remove('active');
      btn.textContent = '5s 自动刷新';
    }
  }

  window.toggleAutoRefresh = function(){
    if(state.autoTimer){
      stopAutoRefresh();
      return;
    }
    state.autoTimer = setInterval(window.loadStatus, 5000);
    var btn = document.getElementById('auto-refresh-btn');
    btn.classList.add('active');
    btn.textContent = '停止自动刷新 (5s)';
  };

  window.openJSON = function(){
    if(!state.lastStatus) return;
    var blob = new Blob([JSON.stringify(state.lastStatus, null, 2)], {type: 'application/json'});
    var url = URL.createObjectURL(blob);
    window.open(url, '_blank', 'noopener');
    setTimeout(function(){ URL.revokeObjectURL(url); }, 30000);
  };

  window.disconnectKey = function(){
    document.getElementById('secret-input').value = '';
    showAuth('');
  };

  function prettyText(s){
    if(!s) return '';
    try{ return JSON.stringify(JSON.parse(s), null, 2); }catch(_){ return s; }
  }

  function renderCapture(snap){
    state.captureEnabled = !!(snap && snap.enabled);
    var badge = document.getElementById('cap-badge');
    var btn = document.getElementById('cap-toggle-btn');
    var entries = (snap && snap.entries) || [];
    if(state.captureEnabled){
      badge.className = 'badge badge-amber';
      badge.textContent = '抓包中 · 剩余 ' + (snap.remaining_seconds || 0) + 's';
      btn.className = 'btn active';
      btn.textContent = '停止抓包';
    }else{
      badge.className = 'badge badge-muted';
      badge.textContent = '已关闭 · 缓存 ' + entries.length + ' 条';
      btn.className = 'btn';
      btn.textContent = '开启抓包';
    }
    var list = document.getElementById('cap-list');
    if(!entries.length){
      list.innerHTML = '<div class="empty">' + (state.captureEnabled ? '抓包已开启，等待新请求...' : '暂无抓包记录') + '</div>';
      return;
    }
    list.innerHTML = entries.map(function(ev){
      var openAttr = state.openCaptures[ev.id] ? ' open' : '';
      var reps = ev.replacements || [];
      var restored = ev.restored_items || [];
      var repHtml = reps.length ? ('<table><thead><tr><th>原文</th><th>替换值 / 占位符</th><th>类型</th><th>方式</th><th>动作</th></tr></thead><tbody>' +
        reps.map(function(r){
          var orig = r.original ? '<span class="orig-val">' + esc(r.original) + '</span>' : '<span class="sub">(未替换 · 仅告警)</span>';
          return '<tr><td class="mono">' + orig + '</td><td class="mono ph-val">' + esc(r.placeholder || '-') + '</td><td class="mono">' + esc(r.kind) + '</td><td class="mono">' + esc(r.by || '-') + '</td><td class="mono">' + esc(actionLabel(r.action || 'mask')) + '</td></tr>';
        }).join('') + '</tbody></table>') : '<div class="sub">请求阶段无敏感替换</div>';
      var resHtml = restored.length ? ('<table><thead><tr><th>占位符</th><th>还原原文</th><th>次数</th></tr></thead><tbody>' +
        restored.map(function(r){
          return '<tr><td class="mono ph-val">' + esc(r.placeholder) + '</td><td class="mono orig-val">' + esc(r.original || '-') + '</td><td class="mono">' + (r.count || 0) + '</td></tr>';
        }).join('') + '</tbody></table>') : '';
      var statusBadge = '';
      if(ev.status === 'streaming') statusBadge = '<span class="badge badge-amber">流式传输中</span>';
      else if(ev.status === 'pending') statusBadge = '<span class="badge badge-muted">等待响应</span>';
      else if(ev.status === 'error') statusBadge = '<span class="badge badge-red">异常</span>';
      var upText = ev.response_binary ? ('[二进制响应 ' + (ev.response_content_type || '') + ', ' + fmtBytes(ev.upstream_response_bytes || 0) + ']') : (prettyText(ev.upstream_response) || (ev.status === 'streaming' ? '[流式传输中，等待数据...]' : ''));
      var reText = ev.response_binary ? ('[二进制响应 ' + (ev.response_content_type || '') + ', ' + fmtBytes(ev.restored_response_bytes || 0) + ']') : (prettyText(ev.restored_response) || (ev.status === 'streaming' ? '[流式传输中，等待输出...]' : ''));
      return '<details class="cap-item" data-cap-id="' + ev.id + '"' + openAttr + '>' +
        '<summary class="cap-summary">' +
          '<span class="mono"><strong>#' + ev.id + '</strong> ' + esc(shortTime(ev.timestamp)) + ' · ' + esc(ev.protocol || '-') + (ev.model ? ' / ' + esc(ev.model) : '') + (ev.stream ? ' [SSE]' : '') + '</span>' +
          '<span class="actions">' + statusBadge + '<span class="badge ' + actionClass(ev.action) + '">' + esc(actionLabel(ev.action)) + '</span><span class="sub mono">替换 ' + reps.length + ' · 还原 ' + (ev.restored_count || 0) + '</span></span>' +
        '</summary>' +
        '<div class="cap-body">' +
          (ev.error ? '<div class="err-box">' + esc(ev.error) + '</div>' : '') +
          '<div><div class="diff-title"><span>请求替换明细 (' + reps.length + ')</span></div>' + repHtml + '</div>' +
          (resHtml ? '<div><div class="diff-title"><span>响应还原明细 (' + (ev.restored_count || 0) + ')</span></div>' + resHtml + '</div>' : '') +
          '<div class="diff-grid">' +
            '<div class="diff-pane"><div class="diff-title"><span>原始请求 (Raw)</span><span class="mono">' + fmtBytes(ev.raw_request_bytes || 0) + (ev.raw_request_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + esc(prettyText(ev.raw_request)) + '</pre></div>' +
            '<div class="diff-pane"><div class="diff-title"><span>脱敏请求 (Masked → 上游)</span><span class="mono">' + fmtBytes(ev.masked_request_bytes || 0) + (ev.masked_request_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + esc(prettyText(ev.masked_request)) + '</pre></div>' +
          '</div>' +
          '<div class="diff-grid">' +
            '<div class="diff-pane"><div class="diff-title"><span>上游原始响应 (Upstream)</span><span class="mono">' + fmtBytes(ev.upstream_response_bytes || 0) + (ev.upstream_response_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + esc(upText) + '</pre></div>' +
            '<div class="diff-pane"><div class="diff-title"><span>还原后响应 (Restored → 客户端)</span><span class="mono">' + fmtBytes(ev.restored_response_bytes || 0) + (ev.restored_response_truncated ? ' · 已截断' : '') + '</span></div><pre class="code-box">' + esc(reText) + '</pre></div>' +
          '</div>' +
        '</div></details>';
    }).join('');
    Array.prototype.forEach.call(list.querySelectorAll('details[data-cap-id]'), function(el){
      el.ontoggle = function(){ state.openCaptures[el.getAttribute('data-cap-id')] = el.open; };
    });
  }

  async function callCapture(method, body){
    if(!state.key) return;
    var errEl = document.getElementById('cap-err');
    errEl.textContent = '';
    errEl.classList.add('hidden');
    try{
      var headers = {'Authorization': 'Bearer ' + state.key, 'Accept': 'application/json'};
      var opts = {method: method, headers: headers};
      if(body !== undefined){
        headers['Content-Type'] = 'application/json';
        opts.body = JSON.stringify(body);
      }
      var resp = await fetch(DEBUG_CAPTURE_URL, opts);
      if(resp.status === 401 || resp.status === 403){
        showAuth('管理密钥无效或无权限 (HTTP ' + resp.status + ')');
        return;
      }
      var data = await resp.json();
      if(!resp.ok){
        errEl.textContent = data.error || ('HTTP ' + resp.status);
        errEl.classList.remove('hidden');
        return;
      }
      renderCapture(data);
    }catch(e){
      errEl.textContent = '抓包请求失败: ' + e;
      errEl.classList.remove('hidden');
    }
  }

  window.loadCapture = function(){ return callCapture('GET'); };
  window.toggleCapture = function(){
    if(state.captureEnabled){ return callCapture('POST', {enabled: false}); }
    var ttl = parseInt(document.getElementById('cap-ttl').value || '300', 10);
    return callCapture('POST', {enabled: true, ttl_seconds: ttl});
  };
  window.clearCapture = function(){
    state.openCaptures = {};
    return callCapture('DELETE');
  };

  window.runPreview = async function(){
    var input = document.getElementById('preview-input').value;
    var out = document.getElementById('preview-out');
    var meta = document.getElementById('preview-meta');
    if(!input || !state.key) return;
    meta.textContent = '处理中...';
    try{
      var resp = await fetch(PREVIEW_URL, {
        method: 'POST',
        headers: {
          'Authorization': 'Bearer ' + state.key,
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({text: input})
      });
      if(resp.status === 401 || resp.status === 403){
        showAuth('管理密钥无效或无权限 (HTTP ' + resp.status + ')');
        return;
      }
      var data = await resp.json();
      out.style.display = 'block';
      if(!resp.ok){
        out.textContent = '错误: ' + (data.error || resp.status);
        meta.textContent = '';
        return;
      }
      var hits = (data.hits || []).map(function(h){
        return '- ' + h.Kind + ' [' + (h.By || '-') + '] ' + (h.Action || 'mask') + ' ' + (h.Head ? h.Head + '*** ' : '') + '(' + h.Len + ' 字符) -> ' + (h.Placeholder || '');
      }).join('\n');
      out.textContent = '脱敏结果：\n' + data.masked + (hits ? '\n\n命中列表：\n' + hits : '\n\n命中列表：\n无');
      meta.textContent = '耗时 ' + data.duration_ms + ' ms · 命中 ' + (data.hits ? data.hits.length : 0) + ' 项';
    }catch(e){
      out.style.display = 'block';
      out.textContent = '请求失败: ' + e;
      meta.textContent = '';
    }
  };

  document.getElementById('auth-form').onsubmit = function(ev){
    ev.preventDefault();
    var val = document.getElementById('secret-input').value.trim();
    if(!val) return;
    state.key = val;
    try{ sessionStorage.setItem(KEY_STORAGE, val); }catch(_){}
    window.loadStatus();
  };

  try{
    var saved = sessionStorage.getItem(KEY_STORAGE);
    if(saved){
      state.key = saved;
      window.loadStatus();
    }
  }catch(_){}
})();
</script>
</body>
</html>
`

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
			return "等待模型文件", "badge-amber"
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
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := int64(b) / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}
