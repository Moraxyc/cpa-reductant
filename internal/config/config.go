package config

import (
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	PluginID = "cpa-reductant"

	// Protocol identifiers matching CLIProxyAPI/sdk/translator & internal/constant.
	ProtocolClaude         = "claude"
	ProtocolOpenAI         = "openai"
	ProtocolOpenAIResponse = "openai-response"
	ProtocolGemini         = "gemini"
	ProtocolInteractions   = "interactions"
	ProtocolCodex          = "codex"
	ProtocolAntigravity    = "antigravity"
	ProtocolOpenAIImage    = "openai-image"
	ProtocolOpenAISpeech   = "openai-speech"
	ProtocolOpenAIVideo    = "openai-video"

	DefaultProtocol = ProtocolClaude
)

var Version = "dev"

var supportedProtocols = []string{
	ProtocolClaude,
	ProtocolOpenAI,
	ProtocolOpenAIResponse,
	ProtocolGemini,
	ProtocolInteractions,
	ProtocolCodex,
	ProtocolAntigravity,
	ProtocolOpenAIImage,
	ProtocolOpenAISpeech,
	ProtocolOpenAIVideo,
}

type Config struct {
	Models     []string `yaml:"models"`
	Protocols  []string `yaml:"protocols"`
	Words      []string `yaml:"words"`
	BlockTerms bool     `yaml:"block_terms"`
}

func SupportedProtocols() []string {
	return slices.Clone(supportedProtocols)
}

func NormalizeProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none":
		return ""
	case "anthropic":
		return ProtocolClaude
	case "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return ProtocolOpenAI
	case "responses", "openai-responses", "openai_responses":
		return ProtocolOpenAIResponse
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func IsSupportedProtocol(protocol string) bool {
	return slices.Contains(supportedProtocols, NormalizeProtocol(protocol))
}

func Parse(rawYAML []byte) (*Config, error) {
	var cfg Config
	if len(rawYAML) > 0 {
		if err := yaml.Unmarshal(rawYAML, &cfg); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}

func (c *Config) AllowsModel(protocol, model string) bool {
	normalized := NormalizeProtocol(protocol)
	if !IsSupportedProtocol(normalized) {
		return false
	}
	if c != nil && len(c.Protocols) > 0 {
		matched := false
		for _, allowed := range c.Protocols {
			if NormalizeProtocol(allowed) == normalized {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if c == nil || len(c.Models) == 0 {
		return true
	}
	return slices.Contains(c.Models, model)
}

func Metadata() pluginapi.Metadata {
	return pluginapi.Metadata{
		Name:             PluginID,
		Version:          Version,
		Author:           "moraxyc",
		GitHubRepository: "https://github.com/moraxyc/cpa-reductant",
		ConfigFields: []pluginapi.ConfigField{
			{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Exact model IDs (empty: all models)"},
			{Name: "protocols", Type: pluginapi.ConfigFieldTypeArray, Description: "Exact protocol formats (empty: all supported CPA formats)"},
			{Name: "words", Type: pluginapi.ConfigFieldTypeArray, Description: "Additional literal terms to protect"},
			{Name: "block_terms", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Reject instead of masking configured terms"},
		},
	}
}
