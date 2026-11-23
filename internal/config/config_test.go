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

func TestModelGlobAndExcludeModels(t *testing.T) {
	cases := []struct {
		pattern string
		model   string
		want    bool
	}{
		{"claude-*", "claude-sonnet-4", true},
		{"claude-*", "gpt-5", false},
		{"ollama/*", "ollama/qwen2.5:32b", true},
		{"ollama/*", "ollama/a/b", true},
		{"gpt-?-mini", "gpt-4-mini", true},
		{"gpt-?-mini", "gpt-40-mini", false},
		{"*codex*", "gpt-5-codex-test", true},
		{"*", "anything/at/all", true},
		{"", "anything", false},
		{"  claude-*  ", "claude-opus", true},
	}
	for _, tc := range cases {
		if got := MatchModelPattern(tc.pattern, tc.model); got != tc.want {
			t.Errorf("MatchModelPattern(%q, %q) = %t, want %t", tc.pattern, tc.model, got, tc.want)
		}
	}

	// Empty models + exclude_models: protect everything except excluded patterns.
	excludeOnly, err := Parse([]byte(`
exclude_models:
  - "ollama/*"
  - "local-*"
  - "gpt-?-local"
`))
	if err != nil {
		t.Fatalf("parse excludeOnly: %v", err)
	}
	if !excludeOnly.AllowsModel(ProtocolOpenAI, "gpt-5") {
		t.Fatal("expected gpt-5 to be allowed")
	}
	for _, skipped := range []string{"ollama/llama3", "local-qwen", "gpt-4-local"} {
		if excludeOnly.AllowsModel(ProtocolOpenAI, skipped) {
			t.Fatalf("expected %q to be excluded", skipped)
		}
	}

	// Glob models + exclude_models: exclude_models takes precedence.
	combined, err := Parse([]byte(`
models:
  - "claude-*"
  - "gpt-*"
exclude_models:
  - "claude-haiku-*"
  - "gpt-4o-mini"
`))
	if err != nil {
		t.Fatalf("parse combined: %v", err)
	}
	if !combined.AllowsModel(ProtocolClaude, "claude-sonnet-4") {
		t.Fatal("expected claude-sonnet-4 to match claude-*")
	}
	if combined.AllowsModel(ProtocolClaude, "claude-haiku-3.5") {
		t.Fatal("expected exclude_models to override claude-*")
	}
	if combined.AllowsModel(ProtocolOpenAI, "gpt-4o-mini") {
		t.Fatal("expected exact exclude_models to override gpt-*")
	}
	if !combined.AllowsModel(ProtocolOpenAI, "gpt-5") {
		t.Fatal("expected gpt-5 to match gpt-*")
	}
	if combined.AllowsModel(ProtocolGemini, "gemini-2.5-pro") {
		t.Fatal("expected unmatched gemini model to be rejected")
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

func TestPerRoutePolicyOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
secrets: true
personal: true
words: [BASE_SECRET]
actions:
  EMAIL: block
model:
  enabled: false
  min_score: 0.8
policies:
  - name: external-claude
    models: ["claude-*"]
    exclude_models: ["claude-haiku-*"]
    protocols: [anthropic]
    words: [CLAUDE_ONLY_TERM]
    actions:
      EMAIL: warn
      TERM: block
    shape: true
    model:
      enabled: true
      min_score: 0.65
      strict: true
      wait_ms: 5000
  - name: downgrade-email
    models: ["claude-sonnet-*"]
    actions:
      EMAIL: mask
`))
	if err != nil {
		t.Fatalf("parse policies: %v", err)
	}

	// Runner config should become enabled because external-claude enables model.
	if !cfg.EffectiveModelRunnerConfig().Enabled {
		t.Fatal("expected EffectiveModelRunnerConfig().Enabled to be true")
	}

	// Unmatched model gets base config directly.
	baseResolved := cfg.ResolvePolicy(ProtocolOpenAI, "gpt-5")
	if baseResolved != cfg {
		t.Fatal("expected unmatched route to return base config pointer")
	}

	// Excluded model inside policy does not match external-claude.
	haikuResolved := cfg.ResolvePolicy(ProtocolClaude, "claude-haiku-3.5")
	if haikuResolved.Shape || haikuResolved.Model.Enabled {
		t.Fatalf("expected claude-haiku-3.5 to skip external-claude policy, got %+v", haikuResolved)
	}

	// claude-opus-4 matches only external-claude.
	opusResolved := cfg.ResolvePolicy(ProtocolClaude, "claude-opus-4")
	if !opusResolved.Shape || !opusResolved.Model.Enabled || !opusResolved.Model.Strict || opusResolved.Model.EffectiveMinScore() != 0.65 || opusResolved.Model.WaitMS != 5000 {
		t.Fatalf("unexpected opus resolved policy: %+v", opusResolved)
	}
	if len(opusResolved.Words) != 2 || opusResolved.Words[0] != "BASE_SECRET" || opusResolved.Words[1] != "CLAUDE_ONLY_TERM" {
		t.Fatalf("unexpected opus words: %#v", opusResolved.Words)
	}
	if opusResolved.EffectiveActions()["EMAIL"] != "warn" || opusResolved.EffectiveActions()["TERM"] != "block" {
		t.Fatalf("unexpected opus actions: %#v", opusResolved.EffectiveActions())
	}

	// claude-sonnet-4 matches both policies in order; second policy resets EMAIL to mask.
	sonnetResolved := cfg.ResolvePolicy(ProtocolClaude, "claude-sonnet-4")
	if _, hasEmail := sonnetResolved.EffectiveActions()["EMAIL"]; hasEmail {
		t.Fatalf("expected EMAIL action to be reset to mask, got %#v", sonnetResolved.EffectiveActions())
	}
	if sonnetResolved.EffectiveActions()["TERM"] != "block" {
		t.Fatalf("expected TERM=block to carry forward, got %#v", sonnetResolved.EffectiveActions())
	}

	// Validation errors inside policies.
	for _, bad := range []string{
		"policies:\n  - name: bad-proto\n    protocols: [invalid-proto]\n",
		"policies:\n  - name: bad-kind\n    kinds:\n      bogus: true\n",
		"policies:\n  - name: bad-score\n    model:\n      min_score: 0.2\n",
		"policies:\n  - name: bad-wait\n    model:\n      wait_ms: -5\n",
		"policies:\n  - name: bad-action\n    actions:\n      EMAIL: drop\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("expected validation error for %q", bad)
		}
	}
}
