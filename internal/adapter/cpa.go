package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	managementBasePath    = "/v0/management"
	managementStatusPath  = "/cpa-reductant/status"
	managementPreviewPath = "/cpa-reductant/preview"
)

// requestPathMetadataKey matches CLIProxyAPI/sdk/cliproxy/executor.RequestPathMetadataKey.
const requestPathMetadataKey = "request_path"

type HostCaller interface {
	Call(method string, request, reply any) error
}

type routeRequest struct {
	SourceFormat   string         `json:"SourceFormat"`
	RequestedModel string         `json:"RequestedModel"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

type executorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id"`
	StreamID       string `json:"stream_id"`
}

type modelRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id"`
}

type streamUpdate struct {
	StreamID    string           `json:"stream_id"`
	Payload     []byte           `json:"payload,omitempty"`
	Error       string           `json:"error,omitempty"`
	ErrorDetail *pluginabi.Error `json:"error_detail,omitempty"`
}

type Plugin struct {
	engine *engine.Engine
	host   HostCaller
}

func NewPlugin(eng *engine.Engine, host HostCaller) *Plugin {
	return &Plugin{
		engine: eng,
		host:   host,
	}
}

func (p *Plugin) Shutdown() {
	if p != nil && p.engine != nil {
		p.engine.Close()
	}
}

func (p *Plugin) Dispatch(method string, input []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
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
				"model_router":            true,
				"executor":                true,
				"executor_model_scope":    "static",
				"executor_input_formats":  protocols,
				"executor_output_formats": protocols,
				"management_api":          true,
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
		if !p.engine.Config().AllowsModel(protocol, request.RequestedModel) || shouldSkipRoute(protocol, request) {
			return pluginapi.ModelRouteResponse{}, nil
		}
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
		}, nil
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
				return nil, fmt.Errorf("missing executor stream ID")
			}
			var upstream pluginapi.HostModelStreamResponse
			if err := p.host.Call(pluginabi.MethodHostModelExecuteStream, masked, &upstream); err != nil {
				return nil, err
			}
			headers := streamResponseHeaders(upstream.Headers, masked.ExitProtocol, masked.Alt)
			go p.forwardStream(upstream.StreamID, request.StreamID, masked.ExitProtocol, masked.Alt, headers)
			return map[string]any{"headers": headers}, nil
		}
		var reply pluginapi.HostModelExecutionResponse
		if err := p.host.Call(pluginabi.MethodHostModelExecute, masked, &reply); err != nil {
			return nil, err
		}
		return pluginapi.ExecutorResponse{
			Payload: p.engine.RestoreResponse(masked.ExitProtocol, reply.Headers.Get("Content-Type"), reply.Body),
			Headers: restoredHeaders(reply.Headers),
		}, nil
	case pluginabi.MethodManagementRegister:
		return pluginapi.ManagementRegistrationResponse{
			Routes: []pluginapi.ManagementRoute{
				{
					Method:      http.MethodGet,
					Path:        managementStatusPath,
					Description: "Inspect cpa-reductant redaction policy, hit statistics, and local model status",
				},
				{
					Method:      http.MethodPost,
					Path:        managementPreviewPath,
					Description: "Preview redaction on sample text using the active policy without storing placeholders",
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
	path := normalizeManagementPath(req.Path)
	switch {
	case method == http.MethodGet && path == managementStatusPath:
		return jsonManagementResponse(http.StatusOK, p.engine.Status())
	case method == http.MethodPost && path == managementPreviewPath:
		var body struct {
			Text string `json:"text"`
		}
		if len(bytes.TrimSpace(req.Body)) == 0 {
			return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": "request body is required"})
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		}
		res, err := p.engine.Preview(context.Background(), body.Text)
		if err != nil {
			if errors.Is(err, engine.ErrModelUnavailable) {
				return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			}
			return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return jsonManagementResponse(http.StatusOK, res)
	default:
		return jsonManagementResponse(http.StatusNotFound, map[string]string{"error": "route not found"})
	}
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

func shouldSkipRoute(protocol string, request routeRequest) bool {
	if reqPath, _ := request.Metadata[requestPathMetadataKey].(string); strings.HasSuffix(strings.TrimSpace(reqPath), "/count_tokens") {
		return true
	}
	if protocol == config.ProtocolInteractions && isInteractionsAgentRequest(request.Body) {
		return true
	}
	if protocol == config.ProtocolGemini && hasJSONKey(request.Body, "generateContentRequest") {
		return true
	}
	return false
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

func hasJSONKey(body []byte, key string) bool {
	if len(body) == 0 {
		return false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	raw, ok := payload[key]
	return ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (p *Plugin) prepareModelRequest(request executorRequest, stream bool) (modelRequest, error) {
	body := request.OriginalRequest
	if len(body) == 0 {
		body = request.Payload
	}
	if !json.Valid(body) {
		return modelRequest{}, pluginabi.NewError("invalid_json", "expected a JSON body", http.StatusBadRequest)
	}
	masked, err := p.engine.MaskJSON(request.Model, body)
	if err != nil {
		if errors.Is(err, engine.ErrBlocked) {
			return modelRequest{}, pluginabi.NewError("redaction_blocked", err.Error(), http.StatusBadRequest)
		}
		if errors.Is(err, engine.ErrModelUnavailable) {
			return modelRequest{}, pluginabi.NewError(pluginabi.ErrorCodeRequestScoped, err.Error(), http.StatusServiceUnavailable)
		}
		return modelRequest{}, err
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
	reqPath, _ := request.Metadata[requestPathMetadataKey].(string)
	return modelRequest{
		HostCallbackID: request.HostCallbackID,
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

func (p *Plugin) forwardStream(upstreamID, outputID, exitProtocol, alt string, headers http.Header) {
	update := streamUpdate{StreamID: outputID}
	if err := p.restoreStream(upstreamID, outputID, exitProtocol, alt, headers); err != nil {
		update.Error = err.Error()
		var abiErr *pluginabi.Error
		if errors.As(err, &abiErr) && abiErr != nil {
			update.ErrorDetail = abiErr
		}
	}
	_ = p.host.Call(pluginabi.MethodHostStreamClose, update, nil)
}

func (p *Plugin) restoreStream(upstreamID, outputID, exitProtocol, alt string, headers http.Header) error {
	stream := pluginapi.HostModelStreamCloseRequest{StreamID: upstreamID}
	defer func() { _ = p.host.Call(pluginabi.MethodHostModelStreamClose, stream, nil) }()

	buffer := &sseBuffer{headers: writerHeadersForStream(headers, exitProtocol, alt)}
	writer := p.engine.NewStreamWriter(buffer)
	normalizer := streamPayloadNormalizer{
		exitProtocol: exitProtocol,
		alt:          alt,
		contentType:  headers.Get("Content-Type"),
	}
	for {
		var chunk pluginapi.HostModelStreamReadResponse
		if err := p.host.Call(
			pluginabi.MethodHostModelStreamRead,
			pluginapi.HostModelStreamReadRequest{StreamID: stream.StreamID},
			&chunk,
		); err != nil {
			return err
		}
		if chunk.Error != "" {
			return fmt.Errorf("upstream stream: %s", chunk.Error)
		}
		if len(chunk.Payload) != 0 {
			payload := normalizer.Normalize(chunk.Payload)
			if _, err := writer.Write(payload); err != nil {
				return err
			}
		}
		if chunk.Done {
			writer.Finish()
		}
		if buffer.Len() > 0 {
			if err := p.host.Call(
				pluginabi.MethodHostStreamEmit,
				streamUpdate{StreamID: outputID, Payload: buffer.Bytes()},
				nil,
			); err != nil {
				return err
			}
			buffer.Reset()
		}
		if chunk.Done {
			return nil
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
