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
}).Parse(dashboardHTML))

const dashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
{{if gt .RefreshSeconds 0}}<meta http-equiv="refresh" content="{{.RefreshSeconds}}">{{end}}
<title>cpa-reductant 状态监控</title>
<style>
:root{
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
