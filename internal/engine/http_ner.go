package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/guard"
)

const maxHTTPNERResponseBytes = 4 << 20 // 4 MiB safety cap

type contextSpanFinder interface {
	FindContext(ctx context.Context, text string) ([]guard.Span, error)
}

type httpNERFinder struct {
	endpoint string
	format   string
	language string
	minScore float64
	headers  map[string]string
	client   *http.Client
}

func newHTTPNERFinder(cfg config.ModelConfig) *httpNERFinder {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	hdrs := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		if strings.TrimSpace(k) != "" {
			hdrs[k] = v
		}
	}
	return &httpNERFinder{
		endpoint: strings.TrimSpace(cfg.Endpoint),
		format:   cfg.EffectiveFormat(),
		language: cfg.EffectiveLanguage(),
		minScore: cfg.EffectiveMinScore(),
		headers:  hdrs,
		client:   &http.Client{Transport: tr},
	}
}

func (f *httpNERFinder) Find(text string) []guard.Span {
	spans, _ := f.FindContext(context.Background(), text)
	return spans
}

func (f *httpNERFinder) FindContext(ctx context.Context, text string) ([]guard.Span, error) {
	if text == "" {
		return nil, nil
	}
	var payload []byte
	var err error
	if f.format == config.ModelFormatPresidio {
		payload, err = json.Marshal(map[string]any{
			"text":            text,
			"language":        f.language,
			"score_threshold": f.minScore,
		})
	} else {
		payload, err = json.Marshal(map[string]any{
			"text": text,
		})
	}
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.PluginID+"/"+config.Version)
	for k, v := range f.headers {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPNERResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http ner endpoint returned HTTP %s", resp.Status)
	}
	if f.format == config.ModelFormatPresidio {
		return parsePresidioSpans(text, body)
	}
	return parseGenericNERSpans(text, body)
}

type rawNERSpan struct {
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Label      string  `json:"label"`
	EntityType string  `json:"entity_type"`
	Score      float64 `json:"score"`
}

func parseGenericNERSpans(text string, body []byte) ([]guard.Span, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var raw []rawNERSpan
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, fmt.Errorf("decode generic ner array: %w", err)
		}
	} else {
		var wrapper struct {
			Spans   []rawNERSpan `json:"spans"`
			Results []rawNERSpan `json:"results"`
		}
		if err := json.Unmarshal(trimmed, &wrapper); err != nil {
			return nil, fmt.Errorf("decode generic ner response: %w", err)
		}
		if len(wrapper.Spans) > 0 {
			raw = wrapper.Spans
		} else {
			raw = wrapper.Results
		}
	}
	out := make([]guard.Span, 0, len(raw))
	for _, r := range raw {
		label := strings.TrimSpace(r.Label)
		if label == "" {
			label = mapPresidioEntity(r.EntityType)
		}
		if label == "" || r.Start < 0 || r.End > len(text) || r.Start >= r.End {
			continue
		}
		score := r.Score
		if score == 0 {
			score = 1.0
		}
		out = append(out, guard.Span{
			Start: r.Start,
			End:   r.End,
			Label: label,
			Score: score,
		})
	}
	return out, nil
}

func parsePresidioSpans(text string, body []byte) ([]guard.Span, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var raw []rawNERSpan
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, fmt.Errorf("decode presidio response: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	runeOffsets := buildRuneByteOffsets(text)
	runeCount := len(runeOffsets) - 1
	out := make([]guard.Span, 0, len(raw))
	for _, r := range raw {
		if r.Start < 0 || r.End > runeCount || r.Start >= r.End {
			continue
		}
		label := mapPresidioEntity(r.EntityType)
		if label == "" {
			label = strings.TrimSpace(r.Label)
		}
		if label == "" {
			continue
		}
		score := r.Score
		if score == 0 {
			score = 1.0
		}
		out = append(out, guard.Span{
			Start: runeOffsets[r.Start],
			End:   runeOffsets[r.End],
			Label: label,
			Score: score,
		})
	}
	return out, nil
}

// buildRuneByteOffsets maps rune index i (0..runeCount) to UTF-8 byte offset in s.
func buildRuneByteOffsets(s string) []int {
	offsets := make([]int, 0, utf8.RuneCountInString(s)+1)
	for i := range s {
		offsets = append(offsets, i)
	}
	offsets = append(offsets, len(s))
	return offsets
}

func mapPresidioEntity(entity string) string {
	switch strings.ToUpper(strings.TrimSpace(entity)) {
	case "PERSON":
		return "private_person"
	case "LOCATION", "ADDRESS":
		return "private_address"
	case "PHONE_NUMBER":
		return "private_phone"
	case "EMAIL_ADDRESS":
		return "private_email"
	case "CREDIT_CARD", "IBAN_CODE", "US_BANK_NUMBER", "US_SSN", "US_PASSPORT", "CRYPTO":
		return "account_number"
	case "IP_ADDRESS":
		return "ip_address"
	case "URL":
		return "private_url"
	case "DATE_TIME":
		return "private_date"
	case "":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(entity))
	}
}
