package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/redact"
)

const (
	PreviewModeText = "text"
	PreviewModeJSON = "json"
)

// ErrInvalidPreviewRequest indicates a malformed preview mode, JSON body, or policy override.
var ErrInvalidPreviewRequest = errors.New("invalid preview request")

// PreviewRequest describes a dry-run masking request.
type PreviewRequest struct {
	Text     string               `json:"text"`
	Mode     string               `json:"mode,omitempty"`
	Protocol string               `json:"protocol,omitempty"`
	Model    string               `json:"model,omitempty"`
	Override *config.PolicyFields `json:"override,omitempty"`
}

func (e *Engine) Preview(ctx context.Context, text string) (PreviewResult, error) {
	return e.PreviewWithOptions(ctx, PreviewRequest{Text: text})
}

func (e *Engine) PreviewWithOptions(ctx context.Context, req PreviewRequest) (res PreviewResult, err error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = PreviewModeText
	}
	if mode != PreviewModeText && mode != PreviewModeJSON {
		return PreviewResult{}, fmt.Errorf("%w: unsupported mode %q (expected text or json)", ErrInvalidPreviewRequest, req.Mode)
	}
	if mode == PreviewModeJSON && !json.Valid([]byte(req.Text)) {
		return PreviewResult{}, fmt.Errorf("%w: text must be valid JSON when mode=json", ErrInvalidPreviewRequest)
	}

	start := time.Now()
	e.lockKeyPath()
	cfg := e.Config()
	if req.Protocol != "" || req.Model != "" {
		cfg = cfg.ResolvePolicy(req.Protocol, req.Model)
	}
	if req.Override != nil {
		if err := req.Override.Validate(); err != nil {
			return PreviewResult{}, fmt.Errorf("%w: %v", ErrInvalidPreviewRequest, err)
		}
		if cfg != nil && len(cfg.Rules)+len(req.Override.Rules) > redact.MaxRules {
			return PreviewResult{}, fmt.Errorf("%w: combined rules exceed maximum of %d", ErrInvalidPreviewRequest, redact.MaxRules)
		}
		cfg = cfg.ApplyOverride(*req.Override)
	}

	var hits []redact.Hit
	var modelErr error
	blocked := false
	options := e.redactOptions(ctx, cfg, "", func(hit redact.Hit) {
		hits = append(hits, hit)
		if hit.Action == redact.Block {
			blocked = true
		}
	}, &modelErr)

	defer func() {
		if rec := recover(); rec != nil {
			res = PreviewResult{}
			err = fmt.Errorf("redaction preview panic: %v", rec)
		}
	}()

	var masked string
	if mode == PreviewModeJSON {
		out, jerr := previewJSON([]byte(req.Text), options)
		if jerr != nil {
			return PreviewResult{}, fmt.Errorf("%w: %v", ErrInvalidPreviewRequest, jerr)
		}
		masked = string(out)
	} else {
		masked = redact.Preview(req.Text, options)
	}
	if modelErr != nil {
		return PreviewResult{}, modelErr
	}
	return PreviewResult{
		Mode:       mode,
		Blocked:    blocked,
		Masked:     masked,
		Hits:       hits,
		DurationMS: roundMS(float64(time.Since(start).Microseconds()) / 1000.0),
	}, nil
}

// previewKeptKeys mirrors Magpie's redact JSON protocol-metadata allowlist so
// dry-run JSON previews match production MaskJSON field selection without
// mutating Magpie's placeholder store.
var previewKeptKeys = map[string]bool{
	"signature": true, "encrypted_content": true, "data": true, "thoughtSignature": true, "thought_signature": true,
	"model": true, "id": true, "tool_use_id": true, "call_id": true, "item_id": true, "type": true, "role": true,
	"name": true, "namespace": true, "server_label": true, "server_name": true, "previous_response_id": true,
	"prompt_cache_key": true, "media_type": true, "mime_type": true, "mimeType": true, "url": true,
	"image_url": true, "file_id": true, "reasoning_effort": true, "effort": true, "stop_reason": true,
	"finish_reason": true, "status": true, "object": true, "event": true,
}

var previewDefKeys = map[string]bool{
	"tools": true, "functions": true, "tool_choice": true, "toolConfig": true, "tool_config": true,
	"response_format": true, "generationConfig": true, "generation_config": true, "text": true,
}

func isPreviewIDKey(key string) bool {
	return key == "id" || key == "ids" || strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids") ||
		strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "Ids")
}

func isPreviewSealed(obj map[string]any) bool {
	t, _ := obj["type"].(string)
	for _, k := range []string{"signature", "thoughtSignature", "thought_signature", "encrypted_content"} {
		if s, _ := obj[k].(string); s == "" {
			continue
		}
		if k == "thoughtSignature" || k == "thought_signature" ||
			t == "thinking" || t == "reasoning" || strings.HasPrefix(t, "reasoning.") {
			return true
		}
	}
	return false
}

func isPreviewToolArgsKey(objType, parentKey, key string) bool {
	if key == "arguments" {
		return true
	}
	if key == "input" && strings.HasSuffix(objType, "tool_use") {
		return true
	}
	if key == "args" && (parentKey == "functionCall" || parentKey == "function_call") {
		return true
	}
	if key == "response" && (parentKey == "functionResponse" || parentKey == "function_response") {
		return true
	}
	if strings.HasSuffix(objType, "_call") && (key == "input" || key == "action" || key == "operation") {
		return true
	}
	return false
}

func previewJSON(body []byte, opts redact.Options) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	defsOpts := opts
	defsOpts.Model = nil

	var walk func(val any, parentKey, key string, depth int, inArgs, inDefs bool) any
	walk = func(val any, parentKey, key string, depth int, inArgs, inDefs bool) any {
		switch x := val.(type) {
		case map[string]any:
			if !inArgs && isPreviewSealed(x) {
				return x
			}
			objType, _ := x["type"].(string)
			out := make(map[string]any, len(x))
			for k, v := range x {
				childArgs := inArgs || isPreviewToolArgsKey(objType, parentKey, k)
				childDefs := inDefs
				if depth == 0 && previewDefKeys[k] {
					if k != "text" {
						childDefs = true
					} else if _, isObj := v.(map[string]any); isObj {
						childDefs = true
					}
				}
				out[k] = walk(v, k, k, depth+1, childArgs, childDefs)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = walk(item, parentKey, key, depth+1, inArgs, inDefs)
			}
			return out
		case string:
			if strings.HasPrefix(x, "data:") {
				return x
			}
			if inDefs {
				if key != "description" {
					return x
				}
				return redact.Preview(x, defsOpts)
			}
			if !inArgs && (previewKeptKeys[key] || isPreviewIDKey(key)) {
				return x
			}
			return redact.Preview(x, opts)
		default:
			return val
		}
	}

	masked := walk(root, "", "", 0, false, false)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(masked); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
