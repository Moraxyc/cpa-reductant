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
