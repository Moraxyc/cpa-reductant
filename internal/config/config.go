package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/yetone/magpie/pkg/guard"
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

	ModelAstrLinkGuard = "astrlink-guard"
	EnvModelDir        = "CPA_REDUCTANT_MODEL_DIR"

	DefaultModelWaitMS       = 2000
	DefaultModelStrictWaitMS = 15000
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

type ModelConfig struct {
	Enabled  bool    `yaml:"enabled"`
	Name     string  `yaml:"name"`
	Dir      string  `yaml:"dir"`
	MinScore float64 `yaml:"min_score"`
	Strict   bool    `yaml:"strict"`
	WaitMS   int     `yaml:"wait_ms"`
}

func (m ModelConfig) EffectiveName() string {
	name := strings.ToLower(strings.TrimSpace(m.Name))
	if name == "" {
		return ModelAstrLinkGuard
	}
	return name
}

func (m ModelConfig) EffectiveDir() string {
	if dir := strings.TrimSpace(m.Dir); dir != "" {
		return filepath.Clean(dir)
	}
	if envDir := strings.TrimSpace(os.Getenv(EnvModelDir)); envDir != "" {
		return filepath.Clean(envDir)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		return ""
	}
	return filepath.Join(cacheDir, PluginID, "models", m.EffectiveName())
}

func (m ModelConfig) EffectiveMinScore() float64 {
	if m.MinScore >= guard.Floor && m.MinScore < 1 {
		return m.MinScore
	}
	return guard.MinScore
}

func (m ModelConfig) EffectiveWait() time.Duration {
	if m.WaitMS > 0 {
		return time.Duration(m.WaitMS) * time.Millisecond
	}
	if m.Strict {
		return DefaultModelStrictWaitMS * time.Millisecond
	}
	return DefaultModelWaitMS * time.Millisecond
}

func (m ModelConfig) Validate() error {
	if name := m.EffectiveName(); name != ModelAstrLinkGuard {
		return fmt.Errorf("unsupported model %q (only %q is supported)", m.Name, ModelAstrLinkGuard)
	}
	if m.MinScore != 0 && (m.MinScore < guard.Floor || m.MinScore >= 1) {
		return fmt.Errorf("model.min_score must be in [%.1f, 1.0), got %g", guard.Floor, m.MinScore)
	}
	if m.WaitMS < 0 {
		return fmt.Errorf("model.wait_ms must be >= 0, got %d", m.WaitMS)
	}
	return nil
}

type Config struct {
	Models     []string    `yaml:"models"`
	Protocols  []string    `yaml:"protocols"`
	Words      []string    `yaml:"words"`
	BlockTerms bool        `yaml:"block_terms"`
	Model      ModelConfig `yaml:"model"`
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
	if err := cfg.Model.Validate(); err != nil {
		return nil, err
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
			{Name: "model", Type: pluginapi.ConfigFieldTypeObject, Description: "Local PII detection model configuration (enabled, name, dir, min_score, strict, wait_ms)"},
		},
	}
}
