package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/yetone/magpie/pkg/guard"
	"github.com/yetone/magpie/pkg/redact"
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
	EnvKeyPath         = "CPA_REDUCTANT_KEY_PATH"

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
	Enabled      bool    `yaml:"enabled"`
	Name         string  `yaml:"name"`
	Dir          string  `yaml:"dir"`
	MinScore     float64 `yaml:"min_score"`
	Strict       bool    `yaml:"strict"`
	WaitMS       int     `yaml:"wait_ms"`
	AutoDownload bool    `yaml:"auto_download"`
	Mirror       string  `yaml:"mirror"`
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
	if mirror := strings.TrimSpace(m.Mirror); mirror != "" {
		u, err := url.Parse(mirror)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("model.mirror must be a valid http/https URL, got %q", m.Mirror)
		}
	}
	return nil
}

type RuleConfig struct {
	Kind   string `yaml:"kind"`
	Prefix string `yaml:"prefix"`
	Regex  string `yaml:"regex"`
}

type Config struct {
	Models       []string          `yaml:"models"`
	Protocols    []string          `yaml:"protocols"`
	Secrets      *bool             `yaml:"secrets"`
	Personal     *bool             `yaml:"personal"`
	Kinds        map[string]bool   `yaml:"kinds"`
	Words        []string          `yaml:"words"`
	WordsAnyCase bool              `yaml:"words_any_case"`
	WordsWhole   bool              `yaml:"words_whole"`
	Rules        []RuleConfig      `yaml:"rules"`
	Actions      map[string]string `yaml:"actions"`
	Allow        []string          `yaml:"allow"`
	Shape        bool              `yaml:"shape"`
	BlockTerms   bool              `yaml:"block_terms"`
	KeyPath      string            `yaml:"key_path"`
	Model        ModelConfig       `yaml:"model"`
}

func SupportedProtocols() []string {
	return slices.Clone(supportedProtocols)
}

func SupportedKinds() []string {
	out := make([]string, 0, len(redact.Categories))
	for _, c := range redact.Categories {
		out = append(out, c.ID)
	}
	return out
}

func isValidKindCategory(id string) bool {
	for _, c := range redact.Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

func (c *Config) EffectiveSecrets() bool {
	if c == nil || c.Secrets == nil {
		return true
	}
	return *c.Secrets
}

func (c *Config) EffectivePersonal() bool {
	if c == nil || c.Personal == nil {
		return true
	}
	return *c.Personal
}

func (c *Config) EffectiveKinds() map[string]bool {
	if c == nil || len(c.Kinds) == 0 {
		return nil
	}
	out := make(map[string]bool, len(c.Kinds))
	for k, v := range c.Kinds {
		norm := strings.ToLower(strings.TrimSpace(k))
		if norm != "" {
			out[norm] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *Config) EffectiveRules() []redact.Rule {
	if c == nil || len(c.Rules) == 0 {
		return nil
	}
	raw := make([]redact.Rule, 0, len(c.Rules))
	for _, r := range c.Rules {
		raw = append(raw, redact.Rule{
			Kind:   r.Kind,
			Prefix: r.Prefix,
			Regex:  r.Regex,
		})
	}
	checked, err := redact.CheckRules(raw)
	if err != nil {
		return nil
	}
	return checked
}

func (c *Config) EffectiveActions() map[string]string {
	if c == nil {
		return nil
	}
	out := make(map[string]string)
	hasTERM := false
	for k, v := range c.Actions {
		kind := strings.ToUpper(strings.TrimSpace(k))
		if kind == "" {
			continue
		}
		if kind == "TERM" {
			hasTERM = true
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case redact.Warn:
			out[kind] = redact.Warn
		case redact.Block:
			out[kind] = redact.Block
		}
	}
	if c.BlockTerms && !hasTERM {
		out["TERM"] = redact.Block
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *Config) EffectiveKeyPath() string {
	if c != nil {
		if kp := strings.TrimSpace(c.KeyPath); kp != "" {
			return filepath.Clean(kp)
		}
	}
	if envKP := strings.TrimSpace(os.Getenv(EnvKeyPath)); envKP != "" {
		return filepath.Clean(envKP)
	}
	return ""
}

func (c *Config) validate() error {
	if err := c.Model.Validate(); err != nil {
		return err
	}
	for k := range c.Kinds {
		norm := strings.ToLower(strings.TrimSpace(k))
		if !isValidKindCategory(norm) {
			return fmt.Errorf("unsupported personal data category %q (supported: %s)", k, strings.Join(SupportedKinds(), ", "))
		}
	}
	if len(c.Rules) > 0 {
		raw := make([]redact.Rule, 0, len(c.Rules))
		for _, r := range c.Rules {
			raw = append(raw, redact.Rule{
				Kind:   r.Kind,
				Prefix: r.Prefix,
				Regex:  r.Regex,
			})
		}
		if _, err := redact.CheckRules(raw); err != nil {
			return err
		}
	}
	for k, v := range c.Actions {
		kind := strings.ToUpper(strings.TrimSpace(k))
		if !redact.KindName(kind) {
			return fmt.Errorf("invalid action kind %q (must match placeholder kind format, e.g. API_KEY, TERM)", k)
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "mask", redact.Warn, redact.Block:
		default:
			return fmt.Errorf("unsupported action %q for %s (expected mask, warn, or block)", v, kind)
		}
	}
	return nil
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
	if err := cfg.validate(); err != nil {
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
			{Name: "secrets", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Mask secrets such as API keys, private keys, tokens, and passwords (default: true)"},
			{Name: "personal", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Mask built-in personal data categories (default: true)"},
			{Name: "kinds", Type: pluginapi.ConfigFieldTypeObject, Description: "Per-category personal data toggles (ssn, passport, iban, birthday, mac, serial, home, userhost, ip, bucket)"},
			{Name: "words", Type: pluginapi.ConfigFieldTypeArray, Description: "Additional literal terms to protect"},
			{Name: "words_any_case", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Match configured words case-insensitively"},
			{Name: "words_whole", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Match configured words only on word boundaries"},
			{Name: "rules", Type: pluginapi.ConfigFieldTypeArray, Description: "Custom masking rules ({kind, prefix | regex})"},
			{Name: "actions", Type: pluginapi.ConfigFieldTypeObject, Description: "Per-kind policy overrides (kind -> mask | warn | block)"},
			{Name: "allow", Type: pluginapi.ConfigFieldTypeArray, Description: "Literal values allowed unmasked (overridden by words)"},
			{Name: "shape", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Use format-preserving synthetic stand-ins instead of {{...}} placeholders"},
			{Name: "block_terms", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Reject instead of masking configured terms"},
			{Name: "key_path", Type: pluginapi.ConfigFieldTypeString, Description: "Path to persist the 32-byte placeholder HMAC key across restarts"},
			{Name: "model", Type: pluginapi.ConfigFieldTypeObject, Description: "Local PII detection model configuration (enabled, name, dir, min_score, strict, wait_ms, auto_download, mirror)"},
		},
	}
}
