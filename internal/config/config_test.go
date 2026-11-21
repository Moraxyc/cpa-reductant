package config

import "testing"

func TestNormalizeAndAllowsModel(t *testing.T) {
	cfg, err := Parse([]byte(`
models: [claude-sonnet-test, gpt-5-test]
words: [SECRET_ALPHA]
block_terms: true
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, proto := range []string{
		ProtocolClaude,
		"anthropic",
		ProtocolOpenAI,
		"chat-completions",
		"openai-chat-completions",
		ProtocolOpenAIResponse,
		"responses",
		"openai-responses",
		ProtocolGemini,
		ProtocolInteractions,
		ProtocolCodex,
		ProtocolAntigravity,
		ProtocolOpenAIImage,
		ProtocolOpenAISpeech,
		ProtocolOpenAIVideo,
	} {
		if !cfg.AllowsModel(proto, "claude-sonnet-test") {
			t.Fatalf("expected protocol %q to be allowed when protocols is empty", proto)
		}
	}
	if cfg.AllowsModel("codex-alpha-search", "claude-sonnet-test") {
		t.Fatal("expected unsupported protocol codex-alpha-search to be rejected")
	}
	if cfg.AllowsModel(ProtocolOpenAI, "unlisted-model") {
		t.Fatal("expected unlisted model to be rejected")
	}

	protoFiltered, err := Parse([]byte(`
protocols: [chat-completions, responses]
`))
	if err != nil {
		t.Fatalf("parse filtered: %v", err)
	}
	if !protoFiltered.AllowsModel(ProtocolOpenAI, "any-model") {
		t.Fatal("expected normalized openai to match chat-completions")
	}
	if !protoFiltered.AllowsModel(ProtocolOpenAIResponse, "any-model") {
		t.Fatal("expected normalized openai-response to match responses")
	}
	if protoFiltered.AllowsModel(ProtocolClaude, "any-model") {
		t.Fatal("expected claude to be filtered out by protocols config")
	}
}

func TestModelConfigParseAndDefaults(t *testing.T) {
	t.Setenv(EnvModelDir, "/tmp/env-guard-dir")
	cfg, err := Parse([]byte(`
model:
  enabled: true
  strict: true
`))
	if err != nil {
		t.Fatalf("parse model config: %v", err)
	}
	if cfg.Model.EffectiveName() != ModelAstrLinkGuard {
		t.Fatalf("name = %q, want %q", cfg.Model.EffectiveName(), ModelAstrLinkGuard)
	}
	if cfg.Model.EffectiveDir() != "/tmp/env-guard-dir" {
		t.Fatalf("dir = %q, want /tmp/env-guard-dir", cfg.Model.EffectiveDir())
	}
	if cfg.Model.EffectiveMinScore() != 0.8 {
		t.Fatalf("min_score = %v, want 0.8", cfg.Model.EffectiveMinScore())
	}
	if cfg.Model.EffectiveWait().Milliseconds() != DefaultModelStrictWaitMS {
		t.Fatalf("wait = %v, want %dms", cfg.Model.EffectiveWait(), DefaultModelStrictWaitMS)
	}
	if !cfg.Model.EffectiveAutoDownload() {
		t.Fatal("expected auto_download to default to true")
	}

	explicit, err := Parse([]byte(`
model:
  enabled: true
  name: astrlink-guard
  dir: /opt/models/guard/
  min_score: 0.65
  wait_ms: 4500
  auto_download: false
  mirror: https://hf-mirror.com
`))
	if err != nil {
		t.Fatalf("parse explicit model config: %v", err)
	}
	if explicit.Model.EffectiveAutoDownload() || explicit.Model.Mirror != "https://hf-mirror.com" {
		t.Fatalf("unexpected download settings: %+v", explicit.Model)
	}
	if explicit.Model.EffectiveDir() != "/opt/models/guard" {
		t.Fatalf("dir = %q, want /opt/models/guard", explicit.Model.EffectiveDir())
	}
	if explicit.Model.EffectiveMinScore() != 0.65 {
		t.Fatalf("min_score = %v, want 0.65", explicit.Model.EffectiveMinScore())
	}
	if explicit.Model.EffectiveWait().Milliseconds() != 4500 {
		t.Fatalf("wait = %v, want 4500ms", explicit.Model.EffectiveWait())
	}

	for _, bad := range []string{
		"model:\n  name: unknown-model\n",
		"model:\n  min_score: 0.4\n",
		"model:\n  min_score: 1.0\n",
		"model:\n  wait_ms: -1\n",
		"model:\n  mirror: not-a-url\n",
		"model:\n  mirror: ftp://example.com\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestRedactionPolicyConfigAndValidation(t *testing.T) {
	t.Setenv(EnvKeyPath, "/tmp/from-env.key")
	cfg, err := Parse([]byte(`
secrets: false
personal: true
kinds:
  ip: true
  home: false
words: [ProjectOrion]
words_any_case: true
words_whole: true
rules:
  - kind: CORP_KEY
    prefix: crp_
  - kind: TICKET
    regex: 'JIRA-\d{4,}'
actions:
  email: warn
  corp_key: block
allow: [ops@example.org]
shape: true
block_terms: true
`))
	if err != nil {
		t.Fatalf("parse policy config: %v", err)
	}
	if cfg.EffectiveSecrets() {
		t.Fatal("expected secrets=false")
	}
	if !cfg.EffectivePersonal() {
		t.Fatal("expected personal=true")
	}
	kinds := cfg.EffectiveKinds()
	if !kinds["ip"] || kinds["home"] {
		t.Fatalf("unexpected kinds: %#v", kinds)
	}
	if !cfg.WordsAnyCase || !cfg.WordsWhole || !cfg.Shape {
		t.Fatalf("expected words_any_case, words_whole, shape to be true: %+v", cfg)
	}
	rules := cfg.EffectiveRules()
	if len(rules) != 2 || rules[0].Kind != "CORP_KEY" || rules[1].Kind != "TICKET" {
		t.Fatalf("unexpected rules: %#v", rules)
	}
	actions := cfg.EffectiveActions()
	if actions["EMAIL"] != "warn" || actions["CORP_KEY"] != "block" || actions["TERM"] != "block" {
		t.Fatalf("unexpected actions: %#v", actions)
	}
	if cfg.EffectiveKeyPath() != "/tmp/from-env.key" {
		t.Fatalf("key_path = %q, want /tmp/from-env.key", cfg.EffectiveKeyPath())
	}

	explicitKeyCfg, err := Parse([]byte("key_path: /var/lib/cpa/reductant.key/\nactions:\n  term: warn\nblock_terms: true\n"))
	if err != nil {
		t.Fatalf("parse explicit key_path: %v", err)
	}
	if explicitKeyCfg.EffectiveKeyPath() != "/var/lib/cpa/reductant.key" {
		t.Fatalf("explicit key_path = %q", explicitKeyCfg.EffectiveKeyPath())
	}
	if explicitKeyCfg.EffectiveActions()["TERM"] != "warn" {
		t.Fatalf("expected explicit actions.TERM=warn to override block_terms, got %#v", explicitKeyCfg.EffectiveActions())
	}

	for _, bad := range []string{
		"kinds:\n  nonexistent_cat: true\n",
		"rules:\n  - kind: BAD\n    prefix: ab\n",
		"rules:\n  - kind: BAD\n    regex: '[unclosed'\n",
		"rules:\n  - kind: BAD\n    prefix: abc_\n    regex: 'abc_[0-9]+'\n",
		"actions:\n  123_INVALID: block\n",
		"actions:\n  EMAIL: drop\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("expected validation error for %q", bad)
		}
	}
}
