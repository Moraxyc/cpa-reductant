package adapter

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
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

func TestI18nTemplateKeysExist(t *testing.T) {
	english := localeMessages[localeEN]
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.Tf?\s+"([a-z0-9_.]+)"`),
		regexp.MustCompile(`\b(?:t|textHTML|monoText)\(\s*'([a-z0-9_.]+)'\s*[,)]`),
		regexp.MustCompile(`data-i18n(?:-[a-z]+)?="([a-z0-9_.]+)"`),
	}
	for name, content := range map[string]string{
		"dashboard.html": dashboardHTML,
		"portal.html":    portalHTML,
	} {
		for _, pat := range patterns {
			for _, m := range pat.FindAllStringSubmatch(content, -1) {
				key := m[1]
				if _, ok := english[key]; !ok {
					t.Errorf("%s references unknown i18n key %q", name, key)
				}
			}
		}
	}
}

func TestManagementUII18n(t *testing.T) {
	p := NewPlugin(engine.New(nil), &fakeHost{})
	defer p.Shutdown()

	if _, err := p.Dispatch(pluginabi.MethodPluginRegister, mustJSON(t, map[string]any{
		"config_yaml": []byte("words: [SYNTHETIC-I18N-SECRET]\n"),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Default dashboard renders in English without leftover template errors or Han characters.
	enRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/cpa-reductant/ui",
	}))
	if err != nil {
		t.Fatalf("dashboard en: %v", err)
	}
	enHTML := string(enRaw.(pluginapi.ManagementResponse).Body)
	for _, want := range []string{`<html lang="en">`, `<title>cpa-reductant Status Dashboard</title>`, `Requests processed`, `id="lang-select"`} {
		if !strings.Contains(enHTML, want) {
			t.Errorf("English dashboard missing %q", want)
		}
	}
	for _, bad := range []string{`/*__DASHBOARD_CSS__*/`, `ZgotmplZ`} {
		if strings.Contains(enHTML, bad) {
			t.Errorf("English dashboard contains broken marker %q", bad)
		}
	}

	// Query ?lang=zh-CN renders Chinese and propagates lang in filter/refresh links.
	zhRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/cpa-reductant/ui",
		Query:  url.Values{"lang": {"zh-CN"}},
	}))
	if err != nil {
		t.Fatalf("dashboard zh-CN: %v", err)
	}
	zhHTML := string(zhRaw.(pluginapi.ManagementResponse).Body)
	for _, want := range []string{`<html lang="zh-CN">`, `<title>cpa-reductant 状态监控</title>`, `请求处理`, `action=mask&amp;lang=zh-CN`, `refresh=5&amp;lang=zh-CN`} {
		if !strings.Contains(zhHTML, want) {
			t.Errorf("zh-CN dashboard missing %q", want)
		}
	}

	// Cookie and Accept-Language negotiation work on SSR dashboard.
	cookieRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management/cpa-reductant/ui",
		Headers: http.Header{"Cookie": {langCookieName + "=zh-CN"}},
	}))
	if err != nil {
		t.Fatalf("dashboard cookie: %v", err)
	}
	if !strings.Contains(string(cookieRaw.(pluginapi.ManagementResponse).Body), `<html lang="zh-CN">`) {
		t.Errorf("expected Cookie to select zh-CN")
	}

	// Portal injects full i18n bundle, resolves initial lang from Accept-Language, and replaces all markers.
	portalRaw, err := p.Dispatch(pluginabi.MethodManagementHandle, mustJSON(t, pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/resource/plugins/cpa-reductant/ui",
		Headers: http.Header{"Accept-Language": {"zh-CN,zh;q=0.9,en;q=0.8"}},
	}))
	if err != nil {
		t.Fatalf("portal zh-CN: %v", err)
	}
	portalBody := string(portalRaw.(pluginapi.ManagementResponse).Body)
	for _, want := range []string{`<html lang="zh-CN">`, `"Requests processed"`, `"请求处理"`, `"简体中文"`, `class="select lang-select"`} {
		if !strings.Contains(portalBody, want) {
			t.Errorf("portal HTML missing %q", want)
		}
	}
	for _, bad := range []string{`/*__DASHBOARD_CSS__*/`, `/*__I18N_BUNDLE__*/`, `/*__I18N_LOCALES__*/`, `__INITIAL_LANG__`, `__LANG_COOKIE__`, `SYNTHETIC-I18N-SECRET`} {
		if strings.Contains(portalBody, bad) {
			t.Errorf("portal HTML contains unexpected marker or secret %q", bad)
		}
	}
}
