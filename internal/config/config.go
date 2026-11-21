package config

import (
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	PluginID        = "cpa-reductant"
	DefaultProtocol = "claude"
)

var Version = "dev"

type Config struct {
	Models     []string `yaml:"models"`
	Words      []string `yaml:"words"`
	BlockTerms bool     `yaml:"block_terms"`
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
	if protocol != DefaultProtocol {
		return false
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
			{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Exact model IDs (empty: all Claude models)"},
			{Name: "words", Type: pluginapi.ConfigFieldTypeArray, Description: "Additional literal terms to protect"},
			{Name: "block_terms", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Reject instead of masking configured terms"},
		},
	}
}
