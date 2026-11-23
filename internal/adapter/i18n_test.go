package adapter

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestI18nDictionaries(t *testing.T) {
	placeholders := regexp.MustCompile(`\{[^{}]+\}`)
	placeholderSet := func(message string) map[string]bool {
		set := make(map[string]bool)
		for _, match := range placeholders.FindAllString(message, -1) {
			set[match] = true
		}
		return set
	}

	english, chinese := localeMessages[localeEN], localeMessages[localeZHCN]
	if len(english) == 0 {
		t.Fatal("English dictionary is empty")
	}
	for key, en := range english {
		zh, ok := chinese[key]
		if !ok {
			t.Errorf("zh-CN is missing %q", key)
			continue
		}
		if strings.TrimSpace(en) == "" || strings.TrimSpace(zh) == "" {
			t.Errorf("%q has an empty translation (en: %q, zh-CN: %q)", key, en, zh)
		}
		if !maps.Equal(placeholderSet(en), placeholderSet(zh)) {
			t.Errorf("%q has different placeholders (en: %q, zh-CN: %q)", key, en, zh)
		}
	}
	for key := range chinese {
		if _, ok := english[key]; !ok {
			t.Errorf("en is missing %q", key)
		}
	}
}

func TestI18nNormalizeLocale(t *testing.T) {
	for input, want := range map[string]string{
		"en": localeEN, "en-US": localeEN, " EN-gb ": localeEN,
		"zh": localeZHCN, "zh-cn": localeZHCN, "zh_CN": localeZHCN,
		"zh-Hans": localeZHCN, "zh-SG": localeZHCN,
		"": "", "fr-FR": "", "*": "", "english": "",
	} {
		if got, ok := normalizeLocale(input); got != want || ok != (want != "") {
			t.Errorf("normalizeLocale(%q) = (%q, %t); want (%q, %t)", input, got, ok, want, want != "")
		}
	}
}

func TestI18nResolveLocale(t *testing.T) {
	tests := []struct {
		name, query, cookie, accept, want string
	}{
		{name: "query overrides cookie and header", query: "zh-SG", cookie: "en", accept: "en", want: localeZHCN},
		{name: "cookie overrides header", cookie: "zh_CN", accept: "en-US", want: localeZHCN},
		{name: "unsupported query falls through to cookie", query: "fr", cookie: "zh", want: localeZHCN},
		{name: "unsupported cookie falls through to header", cookie: "fr", accept: "zh-Hans", want: localeZHCN},
		{name: "higher weight wins despite appearing later", accept: "en-GB;q=0.3, fr;q=1, zh-CN;q=0.8", want: localeZHCN},
		{name: "unweighted tag defaults to one", accept: "en;q=0.4, zh", want: localeZHCN},
		{name: "equal weights preserve header order", accept: "zh;q=0.9, en;q=0.9", want: localeZHCN},
		{name: "zero weight is ignored", accept: "zh;q=0", want: localeEN},
		{name: "malformed weight is ignored", accept: "zh;q=bad, en;q=0.3", want: localeEN},
		{name: "unsupported languages", query: "fr", cookie: "de", accept: "ja, *;q=0.8", want: localeEN},
		{name: "no preference", want: localeEN},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := pluginapi.ManagementRequest{
				Query:   url.Values{},
				Headers: http.Header{},
			}
			if test.query != "" {
				req.Query.Set("lang", test.query)
			}
			if test.cookie != "" {
				req.Headers.Set("Cookie", "other=ignored; "+langCookieName+"="+test.cookie)
			}
			if test.accept != "" {
				req.Headers.Set("Accept-Language", test.accept)
			}
			if got := resolveLocale(req); got != test.want {
				t.Errorf("resolveLocale() = %q; want %q", got, test.want)
			}
		})
	}
}

func TestI18nTranslator(t *testing.T) {
	zh := newTranslator("zh-Hans")
	if got := zh.Locale(); got != localeZHCN {
		t.Errorf("Locale() = %q; want %q", got, localeZHCN)
	}
	if got := zh.T("health.running"); got != "运行中" {
		t.Errorf("T(health.running) = %q; want 运行中", got)
	}
	if got := zh.Tf("preview.metrics", "count", 3, "ms", 12.5); got != "耗时 12.5 ms · 命中 3 项" {
		t.Errorf("Tf(preview.metrics) = %q; want 耗时 12.5 ms · 命中 3 项", got)
	}
	if got := newTranslator("fr").Locale(); got != localeEN {
		t.Errorf("Locale() = %q for unsupported locale; want %q", got, localeEN)
	}
	if got := (translator{}).Tf("events.restored", "count", 5); got != "Restored 5 placeholders" {
		t.Errorf("Tf(events.restored) with no locale dictionary = %q; want Restored 5 placeholders", got)
	}
	if got := zh.T("missing.key"); got != "missing.key" {
		t.Errorf("T(missing.key) = %q; want missing.key", got)
	}
}

func TestI18nMessagesJSON(t *testing.T) {
	var all map[string]map[string]string
	if err := json.Unmarshal([]byte(allLocalesJSON()), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != len(supportedLocales) {
		t.Fatalf("allLocalesJSON() contains %d locales; want %d", len(all), len(supportedLocales))
	}
	for _, option := range supportedLocales {
		var current map[string]string
		if err := json.Unmarshal([]byte(newTranslator(option.Code).MessagesJSON()), &current); err != nil {
			t.Fatalf("MessagesJSON() for %s: %v", option.Code, err)
		}
		want := localeMessages[option.Code]
		if !maps.Equal(current, want) || !maps.Equal(all[option.Code], want) {
			t.Errorf("JSON messages for %s do not match the embedded dictionary", option.Code)
		}
	}
}
