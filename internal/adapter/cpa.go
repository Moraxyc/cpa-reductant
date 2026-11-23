package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	managementBasePath          = "/v0/management"
	managementStatusPath        = "/cpa-reductant/status"
	managementMetricsPath       = "/cpa-reductant/metrics"
	managementUIPath            = "/cpa-reductant/ui"
	managementPreviewPath       = "/cpa-reductant/preview"
	managementDebugCapturePath  = "/cpa-reductant/debug/capture"
	managementModelDownloadPath = "/cpa-reductant/model/download"

	resourceBasePath   = "/v0/resource/plugins/" + config.PluginID
	resourceUIPath     = "/ui"
	resourceUIFullPath = resourceBasePath + resourceUIPath
)

// requestPathMetadataKey matches CLIProxyAPI/sdk/cliproxy/executor.RequestPathMetadataKey.
const requestPathMetadataKey = "request_path"

type HostCaller interface {
	Call(method string, request, reply any) error
}

type routeRequest struct {
	SourceFormat   string         `json:"SourceFormat"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

type requestInterceptRequest struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id"`
}

type responseInterceptRequest struct {
	pluginapi.ResponseInterceptRequest
	HostCallbackID string `json:"host_callback_id"`
}

type streamChunkInterceptRequest struct {
	pluginapi.StreamChunkInterceptRequest
	HostCallbackID string `json:"host_callback_id"`
}

type requestCompletionRequest struct {
	pluginapi.RequestCompletion
	HostCallbackID string `json:"host_callback_id"`
}

type executorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id"`
	StreamID       string `json:"stream_id"`
}

type modelRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id"`
	CaptureID      uint64 `json:"-"`
}

type streamUpdate struct {
	StreamID    string           `json:"stream_id"`
	Payload     []byte           `json:"payload,omitempty"`
	Error       string           `json:"error,omitempty"`
	ErrorDetail *pluginabi.Error `json:"error_detail,omitempty"`
}

type Plugin struct {
	engine          *engine.Engine
	host            HostCaller
	sessionsMu      sync.Mutex
	sessions        map[string]*interceptSession
	lifecycleMu     sync.RWMutex
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
}

type interceptSession struct {
	mu             sync.Mutex
	protocol       string
	model          string
	stream         bool
	captureID      uint64
	hostCallbackID string
	callerScope    string
	startedAt      time.Time

	streamInit   bool
	writer       engine.StreamWriter
	buffer       *sseBuffer
	normalizer   streamPayloadNormalizer
	contentType  string
	upstreamCap  *engine.CaptureBuffer
	restoredCap  *engine.CaptureBuffer
	bytesOut     int
	lastCapFlush time.Time
	captureDirty bool
}

func NewPlugin(eng *engine.Engine, host HostCaller) *Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Plugin{
		engine:          eng,
		host:            host,
		sessions:        make(map[string]*interceptSession),
		lifecycleCtx:    ctx,
		lifecycleCancel: cancel,
	}
	if eng != nil && host != nil {
		eng.SetLogger(func(level, callbackID, msg string, fields map[string]any) {
			req := map[string]any{
				"level":   level,
				"message": msg,
			}
			if callbackID != "" {
				req["host_callback_id"] = callbackID
			}
			if len(fields) > 0 {
				req["fields"] = fields
			}
			_ = host.Call(pluginabi.MethodHostLog, req, nil)
		})
	}
	return p
}

func (p *Plugin) requestContext() context.Context {
	if p == nil {
		return context.Background()
	}
	p.lifecycleMu.RLock()
	ctx := p.lifecycleCtx
	p.lifecycleMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (p *Plugin) ensureLifecycleContext() {
	if p == nil {
		return
	}
	p.lifecycleMu.Lock()
	if p.lifecycleCtx == nil || p.lifecycleCtx.Err() != nil {
		p.lifecycleCtx, p.lifecycleCancel = context.WithCancel(context.Background())
	}
	p.lifecycleMu.Unlock()
}

func (p *Plugin) Shutdown() {
	if p == nil {
		return
	}
	p.lifecycleMu.Lock()
	cancel := p.lifecycleCancel
	p.lifecycleCancel = nil
	p.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.sessionsMu.Lock()
	clear(p.sessions)
	p.sessionsMu.Unlock()
	if p.engine != nil {
		p.engine.Close()
	}
}

func (p *Plugin) Dispatch(method string, input []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		p.ensureLifecycleContext()
		var request struct {
			Config []byte `json:"config_yaml"`
		}
		if len(input) != 0 {
			if err := json.Unmarshal(input, &request); err != nil {
				return nil, err
			}
		}
		cfg, err := config.Parse(request.Config)
		if err != nil {
			return nil, err
		}
		p.engine.Update(cfg)
		protocols := config.SupportedProtocols()
		return map[string]any{
			"schema_version": pluginabi.SchemaVersion,
			"metadata":       config.Metadata(),
			"capabilities": map[string]any{
				"model_router":                true,
				"executor":                    true,
				"executor_model_scope":        "static",
				"executor_input_formats":      protocols,
				"executor_output_formats":     protocols,
				"request_interceptor":         true,
				"request_lifecycle_plugin":    true,
				"response_interceptor":        true,
				"response_stream_interceptor": true,
				"management_api":              true,
			},
		}, nil
	case pluginabi.MethodPluginQuiesce:
		return nil, nil
	case pluginabi.MethodPluginShutdown:
		p.Shutdown()
		return nil, nil
	case pluginabi.MethodExecutorIdentifier:
		return map[string]string{"identifier": config.PluginID}, nil
	case pluginabi.MethodModelRoute:
		var request routeRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		protocol := config.NormalizeProtocol(request.SourceFormat)
		if !p.engine.Config().AllowsModel(protocol, request.RequestedModel) || shouldSkipRoute(protocol, request.Stream, request.Body, request.Metadata) {
			return pluginapi.ModelRouteResponse{}, nil
		}
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
		}, nil
	case pluginabi.MethodRequestInterceptBefore:
		var request requestInterceptRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		return p.handleRequestInterceptBefore(request), nil
	case pluginabi.MethodRequestInterceptAfter:
		return pluginapi.RequestInterceptResponse{}, nil
	case pluginabi.MethodResponseInterceptAfter:
		var request responseInterceptRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		return p.handleResponseInterceptAfter(request), nil
	case pluginabi.MethodResponseInterceptStreamChunk:
		var request streamChunkInterceptRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		return p.handleResponseInterceptStreamChunk(request), nil
	case pluginabi.MethodRequestComplete:
		var request requestCompletionRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		p.handleRequestComplete(request)
		return map[string]any{}, nil
	case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream:
		var request executorRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		stream := method == pluginabi.MethodExecutorExecuteStream
		masked, err := p.prepareModelRequest(request, stream)
		if err != nil {
			return nil, err
		}
		if stream {
			if request.StreamID == "" {
				p.engine.CaptureError(masked.CaptureID, fmt.Errorf("missing executor stream ID"))
				return nil, fmt.Errorf("missing executor stream ID")
			}
			var upstream pluginapi.HostModelStreamResponse
			if err := p.host.Call(pluginabi.MethodHostModelExecuteStream, masked, &upstream); err != nil {
				p.engine.CaptureError(masked.CaptureID, err)
				return nil, err
			}
			headers := streamResponseHeaders(upstream.Headers, masked.ExitProtocol, masked.Alt)
			go p.forwardStream(upstream.StreamID, request.StreamID, masked.ExitProtocol, masked.Model, masked.HostCallbackID, masked.CallerScope, masked.Alt, headers, masked.CaptureID)
			return map[string]any{"headers": headers}, nil
		}
		var reply pluginapi.HostModelExecutionResponse
		if err := p.host.Call(pluginabi.MethodHostModelExecute, masked, &reply); err != nil {
			p.engine.CaptureError(masked.CaptureID, err)
			return nil, err
		}
		restored, _ := p.engine.RestoreResponseWithMeta(engine.RequestMeta{
			Protocol:       masked.ExitProtocol,
			Model:          masked.Model,
			HostCallbackID: masked.HostCallbackID,
			CaptureID:      masked.CaptureID,
			CallerScope:    masked.CallerScope,
		}, reply.Headers.Get("Content-Type"), reply.Body)
		return pluginapi.ExecutorResponse{
			Payload: restored,
			Headers: restoredHeaders(reply.Headers),
		}, nil
	case pluginabi.MethodManagementRegister:
		return pluginapi.ManagementRegistrationResponse{
			Routes: []pluginapi.ManagementRoute{
				{
					Method:      http.MethodGet,
					Path:        managementStatusPath,
					Description: "Inspect cpa-reductant redaction policy, telemetry, recent events, and local model status (JSON or ?format=html)",
				},
				{
					Method:      http.MethodGet,
					Path:        managementMetricsPath,
					Description: "Export cpa-reductant telemetry in Prometheus text exposition format",
				},
				{
					Method:      http.MethodGet,
					Path:        managementUIPath,
					Description: "Visual observability dashboard for cpa-reductant (HTML)",
				},
				{
					Method:      http.MethodPost,
					Path:        managementPreviewPath,
					Description: "Preview redaction on sample text using the active policy without storing placeholders",
				},
				{
					Method:      http.MethodGet,
					Path:        managementDebugCapturePath,
					Description: "Inspect temporary in-memory debug capture state and captured raw/masked payloads",
				},
				{
					Method:      http.MethodPost,
					Path:        managementDebugCapturePath,
					Description: "Enable or disable temporary in-memory debug capture with TTL",
				},
				{
					Method:      http.MethodDelete,
					Path:        managementDebugCapturePath,
					Description: "Clear all in-memory debug capture entries",
				},
				{
					Method:      http.MethodPost,
					Path:        managementModelDownloadPath,
					Description: "Trigger async download of local PII model weights into configured model directory",
				},
			},
			Resources: []pluginapi.ResourceRoute{
				{
					Path:        resourceUIPath,
					Menu:        config.PluginID,
					Description: "Observability dashboard and redaction preview",
				},
			},
		}, nil
	case pluginabi.MethodManagementHandle:
		var request pluginapi.ManagementRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		return p.handleManagement(request), nil
	case pluginabi.MethodExecutorCountTokens:
		return nil, pluginabi.NewError(
			pluginabi.ErrorCodeNotImplemented,
			"count_tokens is not supported yet",
			http.StatusNotImplemented,
		)
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func (p *Plugin) handleManagement(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method == http.MethodGet && isResourceUIPath(req.Path) {
		return renderPortalHTML(resolveLocale(req))
	}
	path := normalizeManagementPath(req.Path)
	switch {
	case method == http.MethodGet && (path == managementStatusPath || path == managementUIPath):
		eventLimit := 50
		if rawEvents := strings.TrimSpace(req.Query.Get("events")); rawEvents != "" {
			if n, err := strconv.Atoi(rawEvents); err == nil {
				if n < 0 {
					n = 0
				} else if n > 100 {
					n = 100
				}
				eventLimit = n
			}
		}
		actionFilter := strings.TrimSpace(req.Query.Get("action"))
		refreshSec := 0
		if rawRefresh := strings.TrimSpace(req.Query.Get("refresh")); rawRefresh != "" {
			if n, err := strconv.Atoi(rawRefresh); err == nil && n > 0 {
				refreshSec = max(n, 5)
			}
		}
		st := p.engine.StatusWithOptions(eventLimit, actionFilter)
		if path == managementUIPath || wantsHTML(req) {
			baseURL := managementBasePath + managementUIPath
			if path == managementStatusPath {
				baseURL = managementBasePath + managementStatusPath + "?format=html"
			}
			explicitLang, _ := normalizeLocale(req.Query.Get("lang"))
			return renderDashboardHTML(st, baseURL, actionFilter, eventLimit, refreshSec, resolveLocale(req), explicitLang)
		}
		return jsonManagementResponse(http.StatusOK, st)
	case method == http.MethodGet && path == managementMetricsPath:
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  {engine.PrometheusContentType},
				"Cache-Control": {"no-store"},
			},
			Body: []byte(p.engine.PrometheusMetrics()),
		}
	case method == http.MethodPost && path == managementPreviewPath:
		var body engine.PreviewRequest
		if len(bytes.TrimSpace(req.Body)) == 0 {
			return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": "request body is required"})
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		}
		res, err := p.engine.PreviewWithOptions(p.requestContext(), body)
		if err != nil {
			if errors.Is(err, engine.ErrInvalidPreviewRequest) {
				return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			if errors.Is(err, engine.ErrModelUnavailable) {
				return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			}
			return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return jsonManagementResponse(http.StatusOK, res)
	case method == http.MethodGet && path == managementDebugCapturePath:
		limit := 0
		if rawLimit := strings.TrimSpace(req.Query.Get("limit")); rawLimit != "" {
			if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 {
				limit = n
			}
		}
		var idFilter uint64
		if rawID := strings.TrimSpace(req.Query.Get("id")); rawID != "" {
			if n, err := strconv.ParseUint(rawID, 10, 64); err == nil {
				idFilter = n
			}
		}
		snap := p.engine.DebugCaptureSnapshot(
			limit,
			idFilter,
			req.Query.Get("protocol"),
			req.Query.Get("model"),
		)
		return noStoreJSONManagementResponse(http.StatusOK, snap)
	case method == http.MethodPost && path == managementDebugCapturePath:
		if len(bytes.TrimSpace(req.Body)) == 0 {
			return noStoreJSONManagementResponse(http.StatusBadRequest, map[string]string{"error": "request body is required"})
		}
		var body struct {
			Enabled    bool `json:"enabled"`
			TTLSeconds int  `json:"ttl_seconds"`
			MaxEntries int  `json:"max_entries"`
			Clear      bool `json:"clear"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return noStoreJSONManagementResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		}
		if body.TTLSeconds < 0 {
			return noStoreJSONManagementResponse(http.StatusBadRequest, map[string]string{"error": "ttl_seconds must be >= 0"})
		}
		if body.MaxEntries < 0 {
			return noStoreJSONManagementResponse(http.StatusBadRequest, map[string]string{"error": "max_entries must be >= 0"})
		}
		snap := p.engine.ConfigureDebugCapture(
			body.Enabled,
			time.Duration(body.TTLSeconds)*time.Second,
			body.MaxEntries,
			body.Clear,
		)
		return noStoreJSONManagementResponse(http.StatusOK, snap)
	case method == http.MethodDelete && path == managementDebugCapturePath:
		return noStoreJSONManagementResponse(http.StatusOK, p.engine.ClearDebugCapture())
	case method == http.MethodPost && path == managementModelDownloadPath:
		var body struct {
			Mirror string `json:"mirror"`
		}
		if len(bytes.TrimSpace(req.Body)) > 0 {
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return noStoreJSONManagementResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			}
		}
		st, err := p.engine.TriggerModelDownload(body.Mirror)
		if err != nil {
			return noStoreJSONManagementResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return noStoreJSONManagementResponse(http.StatusOK, st)
	default:
		return jsonManagementResponse(http.StatusNotFound, map[string]string{"error": "route not found"})
	}
}

func isResourceUIPath(raw string) bool {
	path := strings.TrimSpace(raw)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	return path == resourceUIFullPath
}

func normalizeManagementPath(raw string) string {
	path := strings.TrimSpace(raw)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasPrefix(path, managementBasePath+"/") {
		path = strings.TrimPrefix(path, managementBasePath)
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "/"
	}
	return path
}

func wantsHTML(req pluginapi.ManagementRequest) bool {
	switch strings.ToLower(strings.TrimSpace(req.Query.Get("format"))) {
	case "html":
		return true
	case "json":
		return false
	}
	accept := strings.ToLower(req.Headers.Get("Accept"))
	return strings.Contains(accept, "text/html") && !strings.Contains(accept, "application/json")
}

func jsonManagementResponse(status int, payload any) pluginapi.ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"failed to marshal response"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       body,
	}
}

func noStoreJSONManagementResponse(status int, payload any) pluginapi.ManagementResponse {
	resp := jsonManagementResponse(status, payload)
	resp.Headers.Set("Cache-Control", "no-store")
	return resp
}

func shouldSkipRoute(protocol string, stream bool, body []byte, metadata map[string]any) bool {
	if reqPath, _ := metadata[requestPathMetadataKey].(string); strings.HasSuffix(strings.TrimSpace(reqPath), "/count_tokens") {
		return true
	}
	if protocol == config.ProtocolInteractions && isInteractionsAgentRequest(body) {
		return true
	}
	if protocol == config.ProtocolGemini && !stream {
		return true
	}
	return false
}

func (p *Plugin) putSession(requestID string, sess *interceptSession) {
	if requestID == "" || sess == nil {
		return
	}
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()
	if p.sessions == nil {
		p.sessions = make(map[string]*interceptSession)
	}
	// Safety cap if a host omits request.complete callbacks.
	if len(p.sessions) >= 1024 {
		cutoff := time.Now().Add(-30 * time.Minute)
		for id, existing := range p.sessions {
			if existing.startedAt.Before(cutoff) {
				delete(p.sessions, id)
			}
		}
	}
	p.sessions[requestID] = sess
}

func (p *Plugin) getSession(requestID string) *interceptSession {
	if requestID == "" {
		return nil
	}
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()
	return p.sessions[requestID]
}

func (p *Plugin) popSession(requestID string) *interceptSession {
	if requestID == "" {
		return nil
	}
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()
	sess := p.sessions[requestID]
	if sess != nil {
		delete(p.sessions, requestID)
	}
	return sess
}

func jsonTerminationResponse(status int, errType, code, msg string) pluginapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
			"code":    code,
		},
	})
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}},
		ResponseBody:    body,
	}
}

func (p *Plugin) handleRequestInterceptBefore(req requestInterceptRequest) pluginapi.RequestInterceptResponse {
	protocol := config.NormalizeProtocol(req.SourceFormat)
	requestedModel := strings.TrimSpace(req.RequestedModel)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(req.Model)
	}
	if !p.engine.Config().AllowsModel(protocol, requestedModel) || !shouldSkipRoute(protocol, req.Stream, req.Body, req.Metadata) {
		return pluginapi.RequestInterceptResponse{}
	}
	if !json.Valid(req.Body) {
		return jsonTerminationResponse(http.StatusBadRequest, "invalid_request_error", "invalid_json", "expected a JSON body")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = requestedModel
	}
	callerScope, _ := req.Metadata[pluginapi.CallerScopeMetadataKey].(string)
	captureID := p.engine.BeginCapture()
	masked, err := p.engine.MaskRequest(p.requestContext(), engine.RequestMeta{
		Route:          model,
		Protocol:       protocol,
		Model:          model,
		Stream:         req.Stream,
		HostCallbackID: req.HostCallbackID,
		CaptureID:      captureID,
		CallerScope:    callerScope,
	}, req.Body)
	if err != nil {
		if errors.Is(err, engine.ErrBlocked) {
			return jsonTerminationResponse(http.StatusBadRequest, "invalid_request_error", "redaction_blocked", err.Error())
		}
		if errors.Is(err, engine.ErrModelUnavailable) {
			return jsonTerminationResponse(http.StatusServiceUnavailable, "api_error", pluginabi.ErrorCodeRequestScoped, err.Error())
		}
		return jsonTerminationResponse(http.StatusInternalServerError, "api_error", "redaction_error", err.Error())
	}
	p.putSession(req.RequestID, &interceptSession{
		protocol:       protocol,
		model:          model,
		stream:         req.Stream,
		captureID:      captureID,
		hostCallbackID: req.HostCallbackID,
		callerScope:    callerScope,
		startedAt:      time.Now(),
	})
	return pluginapi.RequestInterceptResponse{Body: masked}
}

func (p *Plugin) handleResponseInterceptAfter(req responseInterceptRequest) pluginapi.ResponseInterceptResponse {
	if req.Kind == pluginapi.ResponseInterceptKindModelList {
		return pluginapi.ResponseInterceptResponse{}
	}
	sess := p.popSession(req.RequestID)
	if sess == nil {
		return pluginapi.ResponseInterceptResponse{}
	}
	callbackID := req.HostCallbackID
	if callbackID == "" {
		callbackID = sess.hostCallbackID
	}
	restored, restoredCount := p.engine.RestoreResponseWithMeta(engine.RequestMeta{
		Protocol:       sess.protocol,
		Model:          sess.model,
		HostCallbackID: callbackID,
		CaptureID:      sess.captureID,
		CallerScope:    sess.callerScope,
	}, req.ResponseHeaders.Get("Content-Type"), req.Body)
	if restoredCount == 0 && bytes.Equal(restored, req.Body) {
		return pluginapi.ResponseInterceptResponse{}
	}
	return pluginapi.ResponseInterceptResponse{Body: restored}
}

func (s *interceptSession) initStream(eng *engine.Engine, respHeaders http.Header) {
	if s.streamInit {
		return
	}
	s.streamInit = true
	s.contentType = respHeaders.Get("Content-Type")
	s.buffer = &sseBuffer{headers: writerHeadersForStream(respHeaders, s.protocol, "")}
	s.writer = eng.NewStreamWriterForScope(s.buffer, s.callerScope, s.protocol, s.model)
	s.normalizer = streamPayloadNormalizer{
		exitProtocol: s.protocol,
		contentType:  s.contentType,
	}
	if s.captureID != 0 {
		limit := engine.MaxCaptureBodyBytes()
		s.upstreamCap = engine.NewCaptureBuffer(limit)
		s.restoredCap = engine.NewCaptureBuffer(limit)
		eng.MarkCaptureStreaming(s.captureID, s.contentType)
	}
}

func (p *Plugin) handleResponseInterceptStreamChunk(req streamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	sess := p.getSession(req.RequestID)
	if sess == nil {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
		sess.initStream(p.engine, req.ResponseHeaders)
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if !sess.streamInit {
		sess.initStream(p.engine, req.ResponseHeaders)
	}
	if len(req.Body) == 0 {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if sess.upstreamCap != nil {
		sess.upstreamCap.Write(req.Body)
		sess.captureDirty = true
	}
	payload := sess.normalizer.Normalize(req.Body)
	if _, err := sess.writer.Write(payload); err != nil {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if sess.buffer.Len() == 0 {
		return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
	}
	outBytes := bytes.Clone(sess.buffer.Bytes())
	sess.bytesOut += len(outBytes)
	if sess.restoredCap != nil {
		sess.restoredCap.Write(outBytes)
		sess.captureDirty = true
	}
	sess.buffer.Reset()
	if sess.captureID != 0 && sess.captureDirty && (sess.lastCapFlush.IsZero() || time.Since(sess.lastCapFlush) >= streamCaptureFlushInterval) {
		upBytes, upTotal, upTrunc := sess.upstreamCap.Snapshot()
		resBytes, resTotal, resTrunc := sess.restoredCap.Snapshot()
		p.engine.RecordStreamCaptureProgress(
			sess.captureID,
			sess.contentType,
			upBytes,
			upTotal,
			upTrunc,
			resBytes,
			resTotal,
			resTrunc,
			sess.writer.Put(),
			time.Since(sess.startedAt),
		)
		sess.lastCapFlush = time.Now()
		sess.captureDirty = false
	}
	return pluginapi.StreamChunkInterceptResponse{Body: outBytes}
}

func (p *Plugin) handleRequestComplete(req requestCompletionRequest) {
	sess := p.popSession(req.RequestID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	callbackID := req.HostCallbackID
	if callbackID == "" {
		callbackID = sess.hostCallbackID
	}
	var completionErr error
	if req.Outcome != pluginapi.RequestCompletionSucceeded {
		msg := strings.TrimSpace(req.Error)
		if msg == "" {
			msg = fmt.Sprintf("request %s", req.Outcome)
		}
		completionErr = errors.New(msg)
	}
	if sess.stream && sess.streamInit && sess.writer != nil {
		sess.writer.Finish()
		if sess.buffer.Len() > 0 {
			outBytes := bytes.Clone(sess.buffer.Bytes())
			sess.bytesOut += len(outBytes)
			if sess.restoredCap != nil {
				sess.restoredCap.Write(outBytes)
			}
			sess.buffer.Reset()
		}
		dur := time.Since(sess.startedAt)
		put := sess.writer.Put()
		p.engine.RecordStreamRestore(engine.RequestMeta{
			Protocol:       sess.protocol,
			Model:          sess.model,
			Stream:         true,
			HostCallbackID: callbackID,
			CaptureID:      sess.captureID,
		}, sess.bytesOut, put, dur, completionErr)
		if sess.captureID != 0 {
			upBytes, upTotal, upTrunc := sess.upstreamCap.Snapshot()
			resBytes, resTotal, resTrunc := sess.restoredCap.Snapshot()
			p.engine.RecordStreamCapture(
				sess.captureID,
				sess.contentType,
				upBytes,
				upTotal,
				upTrunc,
				resBytes,
				resTotal,
				resTrunc,
				put,
				dur,
				completionErr,
			)
		}
		return
	}
	if completionErr != nil && sess.captureID != 0 {
		p.engine.CaptureError(sess.captureID, completionErr)
	}
}

func isInteractionsAgentRequest(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var payload struct {
		Model string `json:"model"`
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return strings.TrimSpace(payload.Agent) != "" && strings.TrimSpace(payload.Model) == ""
}

func (p *Plugin) prepareModelRequest(request executorRequest, stream bool) (modelRequest, error) {
	body := request.OriginalRequest
	if len(body) == 0 {
		body = request.Payload
	}
	if !json.Valid(body) {
		return modelRequest{}, pluginabi.NewError("invalid_json", "expected a JSON body", http.StatusBadRequest)
	}
	entryProtocol := config.NormalizeProtocol(request.SourceFormat)
	if entryProtocol == "" {
		entryProtocol = config.NormalizeProtocol(request.Format)
	}
	if entryProtocol == "" {
		entryProtocol = config.DefaultProtocol
	}
	exitProtocol := config.NormalizeProtocol(request.Format)
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	callerScope, _ := request.Metadata[pluginapi.CallerScopeMetadataKey].(string)
	captureID := p.engine.BeginCapture()
	masked, err := p.engine.MaskRequest(p.requestContext(), engine.RequestMeta{
		Route:          request.Model,
		Protocol:       entryProtocol,
		Model:          request.Model,
		Stream:         stream,
		HostCallbackID: request.HostCallbackID,
		CaptureID:      captureID,
		CallerScope:    callerScope,
	}, body)
	if err != nil {
		if errors.Is(err, engine.ErrBlocked) {
			return modelRequest{}, pluginabi.NewError("redaction_blocked", err.Error(), http.StatusBadRequest)
		}
		if errors.Is(err, engine.ErrModelUnavailable) {
			return modelRequest{}, pluginabi.NewError(pluginabi.ErrorCodeRequestScoped, err.Error(), http.StatusServiceUnavailable)
		}
		return modelRequest{}, err
	}
	reqPath, _ := request.Metadata[requestPathMetadataKey].(string)
	return modelRequest{
		HostCallbackID: request.HostCallbackID,
		CaptureID:      captureID,
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entryProtocol,
			ExitProtocol:  exitProtocol,
			Model:         request.Model,
			Stream:        stream,
			Body:          masked,
			Headers:       request.Headers,
			Query:         request.Query,
			Alt:           request.Alt,
			Path:          strings.TrimSpace(reqPath),
			CallerScope:   callerScope,
		},
	}, nil
}

func (p *Plugin) forwardStream(upstreamID, outputID, exitProtocol, model, callbackID, callerScope, alt string, headers http.Header, captureID uint64) {
	update := streamUpdate{StreamID: outputID}
	if err := p.restoreStream(upstreamID, outputID, exitProtocol, model, callbackID, callerScope, alt, headers, captureID); err != nil {
		update.Error = err.Error()
		var abiErr *pluginabi.Error
		if errors.As(err, &abiErr) && abiErr != nil {
			update.ErrorDetail = abiErr
		}
	}
	_ = p.host.Call(pluginabi.MethodHostStreamClose, update, nil)
}

const streamCaptureFlushInterval = 250 * time.Millisecond

func (p *Plugin) restoreStream(upstreamID, outputID, exitProtocol, model, callbackID, callerScope, alt string, headers http.Header, captureID uint64) error {
	start := time.Now()
	stream := pluginapi.HostModelStreamCloseRequest{StreamID: upstreamID}
	defer func() { _ = p.host.Call(pluginabi.MethodHostModelStreamClose, stream, nil) }()

	buffer := &sseBuffer{headers: writerHeadersForStream(headers, exitProtocol, alt)}
	writer := p.engine.NewStreamWriterForScope(buffer, callerScope, exitProtocol, model)
	contentType := headers.Get("Content-Type")
	normalizer := streamPayloadNormalizer{
		exitProtocol: exitProtocol,
		alt:          alt,
		contentType:  contentType,
	}
	var upstreamCap, restoredCap *engine.CaptureBuffer
	var lastCaptureFlush time.Time
	captureDirty := false
	if captureID != 0 {
		limit := engine.MaxCaptureBodyBytes()
		upstreamCap = engine.NewCaptureBuffer(limit)
		restoredCap = engine.NewCaptureBuffer(limit)
		p.engine.MarkCaptureStreaming(captureID, contentType)
	}
	var bytesOut int
	var streamErr error
	defer func() {
		dur := time.Since(start)
		put := writer.Put()
		p.engine.RecordStreamRestore(engine.RequestMeta{
			Protocol:       exitProtocol,
			Model:          model,
			Stream:         true,
			HostCallbackID: callbackID,
			CaptureID:      captureID,
		}, bytesOut, put, dur, streamErr)
		if captureID != 0 {
			upBytes, upTotal, upTrunc := upstreamCap.Snapshot()
			resBytes, resTotal, resTrunc := restoredCap.Snapshot()
			p.engine.RecordStreamCapture(
				captureID,
				contentType,
				upBytes,
				upTotal,
				upTrunc,
				resBytes,
				resTotal,
				resTrunc,
				put,
				dur,
				streamErr,
			)
		}
	}()

	for {
		var chunk pluginapi.HostModelStreamReadResponse
		if err := p.host.Call(
			pluginabi.MethodHostModelStreamRead,
			pluginapi.HostModelStreamReadRequest{StreamID: stream.StreamID},
			&chunk,
		); err != nil {
			streamErr = err
			return err
		}
		if chunk.Error != "" {
			streamErr = fmt.Errorf("upstream stream: %s", chunk.Error)
			return streamErr
		}
		if len(chunk.Payload) != 0 {
			if upstreamCap != nil {
				upstreamCap.Write(chunk.Payload)
				captureDirty = true
			}
			payload := normalizer.Normalize(chunk.Payload)
			if _, err := writer.Write(payload); err != nil {
				streamErr = err
				return err
			}
		}
		if chunk.Done {
			writer.Finish()
		}
		if buffer.Len() > 0 {
			outBytes := buffer.Bytes()
			bytesOut += len(outBytes)
			if restoredCap != nil {
				restoredCap.Write(outBytes)
				captureDirty = true
			}
			if err := p.host.Call(
				pluginabi.MethodHostStreamEmit,
				streamUpdate{StreamID: outputID, Payload: outBytes},
				nil,
			); err != nil {
				streamErr = err
				return err
			}
			buffer.Reset()
		}
		if chunk.Done {
			return nil
		}
		if captureID != 0 && captureDirty && (lastCaptureFlush.IsZero() || time.Since(lastCaptureFlush) >= streamCaptureFlushInterval) {
			upBytes, upTotal, upTrunc := upstreamCap.Snapshot()
			resBytes, resTotal, resTrunc := restoredCap.Snapshot()
			p.engine.RecordStreamCaptureProgress(
				captureID,
				contentType,
				upBytes,
				upTotal,
				upTrunc,
				resBytes,
				resTotal,
				resTrunc,
				writer.Put(),
				time.Since(start),
			)
			lastCaptureFlush = time.Now()
			captureDirty = false
		}
	}
}

type streamPayloadNormalizer struct {
	exitProtocol string
	alt          string
	contentType  string
	sawSSE       bool
}

func (n *streamPayloadNormalizer) Normalize(payload []byte) []byte {
	if len(payload) == 0 || engine.IsBinaryContentType(n.contentType) {
		return payload
	}
	if n.exitProtocol == config.ProtocolGemini && strings.TrimSpace(n.alt) != "" {
		return payload
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return payload
	}
	if startsWithSSEPrefix(trimmed) {
		n.sawSSE = true
		return normalizeSSELineChunk(payload, trimmed)
	}
	if !allowsBareJSONStreamWrap(n.exitProtocol, n.alt) || n.sawSSE {
		return payload
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return payload
	}
	if json.Valid(trimmed) {
		return wrapSSEDataFrame(trimmed)
	}
	if bytes.IndexByte(trimmed, '\n') < 0 {
		return payload
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	var out bytes.Buffer
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		if (line[0] != '{' && line[0] != '[') || !json.Valid(line) {
			return payload
		}
		out.Write(wrapSSEDataFrame(line))
	}
	if out.Len() == 0 {
		return payload
	}
	return out.Bytes()
}

// normalizeSSELineChunk ensures line-delimited SSE chunks (such as Codex/xAI
// scanner lines emitted without trailing newlines) carry valid line and event
// boundaries for redact.Writer while leaving partial multi-chunk payloads intact.
func normalizeSSELineChunk(payload, trimmed []byte) []byte {
	if bytes.HasSuffix(payload, []byte("\n\n")) || bytes.HasSuffix(payload, []byte("\r\n\r\n")) {
		return payload
	}
	if bytes.IndexByte(trimmed, '\n') < 0 {
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			data := bytes.TrimSpace(trimmed[len("data:"):])
			if bytes.Equal(data, []byte("[DONE]")) || ((len(data) > 0 && (data[0] == '{' || data[0] == '[')) && json.Valid(data)) {
				out := make([]byte, 0, len(trimmed)+2)
				out = append(out, trimmed...)
				out = append(out, '\n', '\n')
				return out
			}
			return payload
		}
		// Single header/comment line (event:, id:, retry:, :) emitted without '\n'.
		out := make([]byte, 0, len(trimmed)+1)
		out = append(out, trimmed...)
		out = append(out, '\n')
		return out
	}
	// Multi-line chunk (e.g. "event: ...\ndata: {...}") missing its blank-line terminator.
	lastNL := bytes.LastIndexByte(trimmed, '\n')
	lastLine := bytes.TrimSpace(trimmed[lastNL+1:])
	if bytes.HasPrefix(lastLine, []byte("data:")) {
		data := bytes.TrimSpace(lastLine[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) || ((len(data) > 0 && (data[0] == '{' || data[0] == '[')) && json.Valid(data)) {
			if bytes.HasSuffix(payload, []byte("\n")) {
				return append(bytes.Clone(payload), '\n')
			}
			return append(bytes.Clone(payload), '\n', '\n')
		}
	}
	return payload
}

func allowsBareJSONStreamWrap(exitProtocol, alt string) bool {
	switch exitProtocol {
	case config.ProtocolOpenAI, config.ProtocolInteractions:
		return true
	case config.ProtocolGemini:
		return strings.TrimSpace(alt) == ""
	default:
		return false
	}
}

func startsWithSSEPrefix(b []byte) bool {
	return bytes.HasPrefix(b, []byte("data:")) ||
		bytes.HasPrefix(b, []byte("event:")) ||
		bytes.HasPrefix(b, []byte(":")) ||
		bytes.HasPrefix(b, []byte("id:")) ||
		bytes.HasPrefix(b, []byte("retry:"))
}

func wrapSSEDataFrame(rawJSON []byte) []byte {
	var data []byte
	if bytes.ContainsAny(rawJSON, "\r\n") {
		var compact bytes.Buffer
		if err := json.Compact(&compact, rawJSON); err == nil {
			data = compact.Bytes()
		}
	}
	if data == nil {
		data = rawJSON
	}
	out := make([]byte, 0, len("data: ")+len(data)+len("\n\n"))
	out = append(out, "data: "...)
	out = append(out, data...)
	out = append(out, "\n\n"...)
	return out
}

func streamResponseHeaders(upstream http.Header, exitProtocol, alt string) http.Header {
	headers := restoredHeaders(upstream)
	if headers == nil {
		headers = make(http.Header)
	}
	ct := headers.Get("Content-Type")
	if ct == "" && !engine.IsBinaryContentType(ct) {
		if !(exitProtocol == config.ProtocolGemini && strings.TrimSpace(alt) != "") {
			headers.Set("Content-Type", "text/event-stream")
		}
	}
	return headers
}

func writerHeadersForStream(headers http.Header, exitProtocol, alt string) http.Header {
	out := make(http.Header)
	if enc := headers.Get("Content-Encoding"); enc != "" {
		out.Set("Content-Encoding", enc)
	}
	ct := headers.Get("Content-Type")
	if engine.IsBinaryContentType(ct) {
		out.Set("Content-Type", ct)
		return out
	}
	if exitProtocol == config.ProtocolGemini && strings.TrimSpace(alt) != "" {
		if strings.Contains(strings.ToLower(ct), "event-stream") {
			out.Set("Content-Type", "text/event-stream")
		} else {
			out.Set("Content-Type", "application/json")
		}
		return out
	}
	out.Set("Content-Type", "text/event-stream")
	return out
}

func restoredHeaders(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	out := headers.Clone()
	out.Del("Content-Length")
	return out
}

type sseBuffer struct {
	bytes.Buffer
	headers http.Header
}

func (b *sseBuffer) Header() http.Header { return b.headers }
func (*sseBuffer) WriteHeader(int)       {}
