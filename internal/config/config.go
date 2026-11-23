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
	DefaultScopeTTLSeconds   = 86400
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
	AutoDownload *bool   `yaml:"auto_download"`
	Mirror       string  `yaml:"mirror"`
}

func (m ModelConfig) EffectiveAutoDownload() bool {
	if m.AutoDownload == nil {
		return true
	}
	return *m.AutoDownload
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
	Kind   string `yaml:"kind" json:"kind"`
	Prefix string `yaml:"prefix" json:"prefix,omitempty"`
	Regex  string `yaml:"regex" json:"regex,omitempty"`
}

type ModelPolicyOverride struct {
	Enabled  *bool    `yaml:"enabled" json:"enabled,omitempty"`
	MinScore *float64 `yaml:"min_score" json:"min_score,omitempty"`
	Strict   *bool    `yaml:"strict" json:"strict,omitempty"`
	WaitMS   *int     `yaml:"wait_ms" json:"wait_ms,omitempty"`
}

func (m *ModelPolicyOverride) Validate() error {
	if m == nil {
		return nil
	}
	if m.MinScore != nil && *m.MinScore != 0 && (*m.MinScore < guard.Floor || *m.MinScore >= 1) {
		return fmt.Errorf("policy model.min_score must be in [%.1f, 1.0), got %g", guard.Floor, *m.MinScore)
	}
	if m.WaitMS != nil && *m.WaitMS < 0 {
		return fmt.Errorf("policy model.wait_ms must be >= 0, got %d", *m.WaitMS)
	}
	return nil
}

type PolicyFields struct {
	Secrets      *bool                `yaml:"secrets" json:"secrets,omitempty"`
	Personal     *bool                `yaml:"personal" json:"personal,omitempty"`
	Kinds        map[string]bool      `yaml:"kinds" json:"kinds,omitempty"`
	Words        []string             `yaml:"words" json:"words,omitempty"`
	WordsAnyCase *bool                `yaml:"words_any_case" json:"words_any_case,omitempty"`
	WordsWhole   *bool                `yaml:"words_whole" json:"words_whole,omitempty"`
	Rules        []RuleConfig         `yaml:"rules" json:"rules,omitempty"`
	Actions      map[string]string    `yaml:"actions" json:"actions,omitempty"`
	Allow        []string             `yaml:"allow" json:"allow,omitempty"`
	Shape        *bool                `yaml:"shape" json:"shape,omitempty"`
	BlockTerms   *bool                `yaml:"block_terms" json:"block_terms,omitempty"`
	Model        *ModelPolicyOverride `yaml:"model" json:"model,omitempty"`
}

func (p PolicyFields) Validate() error {
	if err := p.Model.Validate(); err != nil {
		return err
	}
	if err := validateKinds(p.Kinds); err != nil {
		return err
	}
	if err := validateRules(p.Rules); err != nil {
		return err
	}
	if err := validateActions(p.Actions); err != nil {
		return err
	}
	return nil
}

type PolicyOverride struct {
	Name          string   `yaml:"name" json:"name,omitempty"`
	Models        []string `yaml:"models" json:"models,omitempty"`
	ExcludeModels []string `yaml:"exclude_models" json:"exclude_models,omitempty"`
	Protocols     []string `yaml:"protocols" json:"protocols,omitempty"`
	PolicyFields  `yaml:",inline"`
}

func (p PolicyOverride) Matches(protocol, model string) bool {
	normalized := NormalizeProtocol(protocol)
	if len(p.Protocols) > 0 && !matchesAnyProtocol(p.Protocols, normalized) {
		return false
	}
	if len(p.ExcludeModels) > 0 && matchesAnyModelPattern(p.ExcludeModels, model) {
		return false
	}
	if len(p.Models) == 0 {
		return true
	}
	return matchesAnyModelPattern(p.Models, model)
}

type Config struct {
	Models        []string          `yaml:"models"`
	ExcludeModels []string          `yaml:"exclude_models"`
	Protocols     []string          `yaml:"protocols"`
	Secrets       *bool             `yaml:"secrets"`
	Personal      *bool             `yaml:"personal"`
	Kinds         map[string]bool   `yaml:"kinds"`
	Words         []string          `yaml:"words"`
	WordsAnyCase  bool              `yaml:"words_any_case"`
	WordsWhole    bool              `yaml:"words_whole"`
	Rules         []RuleConfig      `yaml:"rules"`
	Actions       map[string]string `yaml:"actions"`
	Allow         []string          `yaml:"allow"`
	Shape         bool              `yaml:"shape"`
	BlockTerms    bool              `yaml:"block_terms"`
	KeyPath         string            `yaml:"key_path"`
	ScopeIsolation  bool              `yaml:"scope_isolation"`
	ScopeTTLSeconds int               `yaml:"scope_ttl_seconds"`
	Model           ModelConfig       `yaml:"model"`
	Policies        []PolicyOverride  `yaml:"policies"`
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

func validateKinds(kinds map[string]bool) error {
	for k := range kinds {
		norm := strings.ToLower(strings.TrimSpace(k))
		if !isValidKindCategory(norm) {
			return fmt.Errorf("unsupported personal data category %q (supported: %s)", k, strings.Join(SupportedKinds(), ", "))
		}
	}
	return nil
}

func validateRules(rules []RuleConfig) error {
	if len(rules) == 0 {
		return nil
	}
	raw := make([]redact.Rule, 0, len(rules))
	for _, r := range rules {
		raw = append(raw, redact.Rule{
			Kind:   r.Kind,
			Prefix: r.Prefix,
			Regex:  r.Regex,
		})
	}
	_, err := redact.CheckRules(raw)
	return err
}

func validateActions(actions map[string]string) error {
	for k, v := range actions {
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

func (c *Config) EffectiveScopeTTL() time.Duration {
	if c != nil && c.ScopeTTLSeconds > 0 {
		return time.Duration(c.ScopeTTLSeconds) * time.Second
	}
	return DefaultScopeTTLSeconds * time.Second
}

func (c *Config) validate() error {
	if c.ScopeTTLSeconds < 0 {
		return fmt.Errorf("scope_ttl_seconds must be >= 0, got %d", c.ScopeTTLSeconds)
	}
	if err := c.Model.Validate(); err != nil {
		return err
	}
	if err := validateKinds(c.Kinds); err != nil {
		return err
	}
	if err := validateRules(c.Rules); err != nil {
		return err
	}
	if err := validateActions(c.Actions); err != nil {
		return err
	}
	for i, pol := range c.Policies {
		label := strings.TrimSpace(pol.Name)
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if err := pol.Validate(); err != nil {
			return fmt.Errorf("policies[%s]: %w", label, err)
		}
		for _, proto := range pol.Protocols {
			if !IsSupportedProtocol(proto) {
				return fmt.Errorf("policies[%s]: unsupported protocol %q", label, proto)
			}
		}
		if len(c.Rules)+len(pol.Rules) > redact.MaxRules {
			return fmt.Errorf("policies[%s]: combined rules exceed maximum of %d", label, redact.MaxRules)
		}
	}
	return nil
}

// EffectiveModelRunnerConfig returns the ModelConfig used to initialize the
// shared model runner. If any per-route policy enables the local model, the
// runner is marked enabled so weights can be prepared even when the global
// default is disabled.
func (c *Config) EffectiveModelRunnerConfig() ModelConfig {
	if c == nil {
		return ModelConfig{}
	}
	mc := c.Model
	if !mc.Enabled {
		for _, pol := range c.Policies {
			if pol.Model != nil && pol.Model.Enabled != nil && *pol.Model.Enabled {
				mc.Enabled = true
				break
			}
		}
	}
	return mc
}

// ApplyOverride returns a copy of c with the given PolicyFields merged on top.
// Pointer fields override when non-nil; Kinds and Actions merge by key (an
// explicit "mask" action overrides a base warn/block action); Words, Rules, and
// Allow append to the base lists.
func (c *Config) ApplyOverride(ov PolicyFields) *Config {
	var base Config
	if c != nil {
		base = *c
	}
	out := base
	out.Models = slices.Clone(base.Models)
	out.ExcludeModels = slices.Clone(base.ExcludeModels)
	out.Protocols = slices.Clone(base.Protocols)
	out.Words = slices.Clone(base.Words)
	out.Rules = slices.Clone(base.Rules)
	out.Allow = slices.Clone(base.Allow)
	out.Policies = nil

	if len(base.Kinds) > 0 {
		out.Kinds = make(map[string]bool, len(base.Kinds)+len(ov.Kinds))
		for k, v := range base.Kinds {
			out.Kinds[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	if len(base.Actions) > 0 {
		out.Actions = make(map[string]string, len(base.Actions)+len(ov.Actions))
		for k, v := range base.Actions {
			out.Actions[strings.ToUpper(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
		}
	}

	if ov.Secrets != nil {
		v := *ov.Secrets
		out.Secrets = &v
	}
	if ov.Personal != nil {
		v := *ov.Personal
		out.Personal = &v
	}
	if len(ov.Kinds) > 0 {
		if out.Kinds == nil {
			out.Kinds = make(map[string]bool, len(ov.Kinds))
		}
		for k, v := range ov.Kinds {
			norm := strings.ToLower(strings.TrimSpace(k))
			if norm != "" {
				out.Kinds[norm] = v
			}
		}
	}
	if len(ov.Words) > 0 {
		out.Words = append(out.Words, ov.Words...)
	}
	if ov.WordsAnyCase != nil {
		out.WordsAnyCase = *ov.WordsAnyCase
	}
	if ov.WordsWhole != nil {
		out.WordsWhole = *ov.WordsWhole
	}
	if len(ov.Rules) > 0 {
		out.Rules = append(out.Rules, ov.Rules...)
	}
	if len(ov.Actions) > 0 {
		if out.Actions == nil {
			out.Actions = make(map[string]string, len(ov.Actions))
		}
		for k, v := range ov.Actions {
			kind := strings.ToUpper(strings.TrimSpace(k))
			if kind == "" {
				continue
			}
			act := strings.ToLower(strings.TrimSpace(v))
			if act == "" || act == "mask" {
				out.Actions[kind] = "mask"
			} else {
				out.Actions[kind] = act
			}
		}
	}
	if len(ov.Allow) > 0 {
		out.Allow = append(out.Allow, ov.Allow...)
	}
	if ov.Shape != nil {
		out.Shape = *ov.Shape
	}
	if ov.BlockTerms != nil {
		out.BlockTerms = *ov.BlockTerms
	}
	if ov.Model != nil {
		if ov.Model.Enabled != nil {
			out.Model.Enabled = *ov.Model.Enabled
		}
		if ov.Model.MinScore != nil {
			out.Model.MinScore = *ov.Model.MinScore
		}
		if ov.Model.Strict != nil {
			out.Model.Strict = *ov.Model.Strict
		}
		if ov.Model.WaitMS != nil {
			out.Model.WaitMS = *ov.Model.WaitMS
		}
	}
	return &out
}

// ResolvePolicy returns the effective Config for the given (protocol, model)
// after applying all matching PolicyOverride entries in declaration order.
// Returns c directly when no overrides match.
func (c *Config) ResolvePolicy(protocol, model string) *Config {
	if c == nil || len(c.Policies) == 0 {
		return c
	}
	resolved := c
	for _, pol := range c.Policies {
		if pol.Matches(protocol, model) {
			resolved = resolved.ApplyOverride(pol.PolicyFields)
		}
	}
	return resolved
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

// MatchModelPattern matches a model ID against a wildcard pattern supporting
// '*' (zero or more runes, including '/') and '?' (any single rune).
func MatchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == model
	}
	p := []rune(pattern)
	s := []rune(model)
	pi, si := 0, 0
	starIdx, matchIdx := -1, 0
	for si < len(s) {
		if pi < len(p) && (p[pi] == '?' || p[pi] == s[si]) {
			pi++
			si++
		} else if pi < len(p) && p[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
		} else if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
		} else {
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func matchesAnyModelPattern(patterns []string, model string) bool {
	for _, pat := range patterns {
		if MatchModelPattern(pat, model) {
			return true
		}
	}
	return false
}

func matchesAnyProtocol(protocols []string, normalized string) bool {
	for _, allowed := range protocols {
		if NormalizeProtocol(allowed) == normalized {
			return true
		}
	}
	return false
}

func (c *Config) AllowsModel(protocol, model string) bool {
	normalized := NormalizeProtocol(protocol)
	if !IsSupportedProtocol(normalized) {
		return false
	}
	if c != nil && len(c.Protocols) > 0 && !matchesAnyProtocol(c.Protocols, normalized) {
		return false
	}
	if c != nil && len(c.ExcludeModels) > 0 && matchesAnyModelPattern(c.ExcludeModels, model) {
		return false
	}
	if c == nil || len(c.Models) == 0 {
		return true
	}
	return matchesAnyModelPattern(c.Models, model)
}

func Metadata() pluginapi.Metadata {
	return pluginapi.Metadata{
		Name:             PluginID,
		Version:          Version,
		Author:           "moraxyc",
		GitHubRepository: "https://github.com/moraxyc/cpa-reductant",
		ConfigFields: []pluginapi.ConfigField{
			{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Target LLM model IDs or glob patterns (*, ?) to redact (empty: all models; unrelated to local PII model)"},
			{Name: "exclude_models", Type: pluginapi.ConfigFieldTypeArray, Description: "LLM model IDs or glob patterns (*, ?) to skip even when matched by models"},
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
			{Name: "scope_isolation", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Isolate placeholder restoration per CPA CallerScope to prevent cross-tenant placeholder oracle leaks (default: false)"},
			{Name: "scope_ttl_seconds", Type: pluginapi.ConfigFieldTypeNumber, Description: "Retention TTL in seconds for per-CallerScope tracked placeholders when scope_isolation is enabled (default: 86400)"},
			{Name: "model", Type: pluginapi.ConfigFieldTypeObject, Description: "Local PII NER model (astrlink-guard): set enabled=true (auto-downloads by default unless auto_download=false); fields: enabled, auto_download, name, dir, min_score, strict, wait_ms, mirror"},
			{Name: "policies", Type: pluginapi.ConfigFieldTypeArray, Description: "Per-model / per-protocol policy overrides matched in order ({name, models, exclude_models, protocols, secrets, personal, kinds, words, rules, actions, allow, shape, block_terms, model})"},
		},
	}
}
