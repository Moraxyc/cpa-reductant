package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type HostCaller interface {
	Call(method string, request, reply any) error
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
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
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
		return map[string]any{
			"schema_version": pluginabi.SchemaVersion,
			"metadata":       config.Metadata(),
			"capabilities": map[string]any{
				"model_router":            true,
				"executor":                true,
				"executor_model_scope":    "static",
				"executor_input_formats":  []string{config.DefaultProtocol},
				"executor_output_formats": []string{config.DefaultProtocol},
			},
		}, nil
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return nil, nil
	case pluginabi.MethodExecutorIdentifier:
		return map[string]string{"identifier": config.PluginID}, nil
	case pluginabi.MethodModelRoute:
		var request struct {
			SourceFormat   string
			RequestedModel string
		}
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}
		if !p.engine.Config().AllowsModel(request.SourceFormat, request.RequestedModel) {
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
			go p.forwardStream(masked, request.StreamID)
			return map[string]any{"headers": http.Header{"Content-Type": {"text/event-stream"}}}, nil
		}
		var reply pluginapi.HostModelExecutionResponse
		if err := p.host.Call(pluginabi.MethodHostModelExecute, masked, &reply); err != nil {
			return nil, err
		}
		return pluginapi.ExecutorResponse{
			Payload: p.engine.RestoreJSON(reply.Body),
			Headers: restoredHeaders(reply.Headers),
		}, nil
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
		return modelRequest{}, err
	}
	callerScope, _ := request.Metadata[pluginapi.CallerScopeMetadataKey].(string)
	return modelRequest{
		HostCallbackID: request.HostCallbackID,
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: config.DefaultProtocol,
			ExitProtocol:  config.DefaultProtocol,
			Model:         request.Model,
			Stream:        stream,
			Body:          masked,
			Headers:       request.Headers,
			Query:         request.Query,
			Alt:           request.Alt,
			CallerScope:   callerScope,
		},
	}, nil
}

func (p *Plugin) forwardStream(request modelRequest, outputID string) {
	update := streamUpdate{StreamID: outputID}
	if err := p.restoreStream(request, outputID); err != nil {
		update.Error = err.Error()
	}
	_ = p.host.Call(pluginabi.MethodHostStreamClose, update, nil)
}

func (p *Plugin) restoreStream(request modelRequest, outputID string) error {
	var upstream pluginapi.HostModelStreamResponse
	if err := p.host.Call(pluginabi.MethodHostModelExecuteStream, request, &upstream); err != nil {
		return err
	}
	stream := pluginapi.HostModelStreamCloseRequest{StreamID: upstream.StreamID}
	defer func() { _ = p.host.Call(pluginabi.MethodHostStreamClose, stream, nil) }()

	buffer := &sseBuffer{headers: http.Header{"Content-Type": {"text/event-stream"}}}
	writer := p.engine.NewStreamWriter(buffer)
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
			if _, err := writer.Write(chunk.Payload); err != nil {
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

func restoredHeaders(headers http.Header) http.Header {
	headers.Del("Content-Length")
	return headers
}

type sseBuffer struct {
	bytes.Buffer
	headers http.Header
}

func (b *sseBuffer) Header() http.Header { return b.headers }
func (*sseBuffer) WriteHeader(int)       {}
