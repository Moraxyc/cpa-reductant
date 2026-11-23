package adapter

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	localeEN       = "en"
	localeZHCN     = "zh-CN"
	langCookieName = "cpa_reductant_lang"
)

type localeOption struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

var supportedLocales = []localeOption{
	{Code: localeEN, Label: "English"},
	{Code: localeZHCN, Label: "简体中文"},
}

//go:embed assets/locales/*.json
var localeFiles embed.FS

var localeMessages = make(map[string]map[string]string, len(supportedLocales))

func init() {
	for _, option := range supportedLocales {
		path := "assets/locales/" + option.Code + ".json"
		data, err := localeFiles.ReadFile(path)
		if err != nil {
			panic(err)
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			panic(fmt.Errorf("parse %s: %w", path, err))
		}
		localeMessages[option.Code] = messages
	}
}

func normalizeLocale(raw string) (string, bool) {
	primary, _, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(raw), "_", "-"), "-")
	switch strings.ToLower(primary) {
	case "en":
		return localeEN, true
	case "zh":
		return localeZHCN, true
	default:
		return "", false
	}
}

func resolveLocale(req pluginapi.ManagementRequest) string {
	if locale, ok := normalizeLocale(req.Query.Get("lang")); ok {
		return locale
	}
	if cookie, err := (&http.Request{Header: req.Headers}).Cookie(langCookieName); err == nil {
		if locale, ok := normalizeLocale(cookie.Value); ok {
			return locale
		}
	}

	bestLocale, bestWeight := localeEN, 0.0
	for _, preference := range strings.Split(strings.Join(req.Headers.Values("Accept-Language"), ","), ",") {
		tag, param, hasParam := strings.Cut(preference, ";")
		locale, ok := normalizeLocale(tag)
		if !ok {
			continue
		}
		weight := 1.0
		if hasParam {
			value, ok := strings.CutPrefix(strings.TrimSpace(param), "q=")
			if !ok {
				continue
			}
			var err error
			weight, err = strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
		}
		if weight > bestWeight && weight <= 1 {
			bestLocale, bestWeight = locale, weight
		}
	}
	return bestLocale
}

type translator struct {
	locale string
}

func newTranslator(locale string) translator {
	if normalized, ok := normalizeLocale(locale); ok {
		return translator{locale: normalized}
	}
	return translator{locale: localeEN}
}

func (tr translator) T(key string) string {
	if message, ok := localeMessages[tr.locale][key]; ok {
		return message
	}
	if message, ok := localeMessages[localeEN][key]; ok {
		return message
	}
	return key
}

func (tr translator) Tf(key string, kv ...any) string {
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(kv[i])+"}", fmt.Sprint(kv[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(tr.T(key))
}

func (tr translator) Locale() string {
	return tr.locale
}

func (tr translator) MessagesJSON() template.JS {
	data, _ := json.Marshal(localeMessages[tr.locale])
	return template.JS(data)
}

func allLocalesJSON() string {
	data, _ := json.Marshal(localeMessages)
	return string(data)
}

func supportedLocalesJSON() string {
	data, _ := json.Marshal(supportedLocales)
	return string(data)
}
