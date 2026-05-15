package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// RouteConfig is the YAML schema described in phase-1-route-config-schema.md §2.
type RouteConfig struct {
	Version         string      `yaml:"version"`
	AppID           string      `yaml:"appId"`
	DefaultBehavior string      `yaml:"defaultBehavior"`
	Routes          []RouteRule `yaml:"routes"`
}

// RouteRule is one entry in the routes list.
type RouteRule struct {
	Method   string `yaml:"method"`
	Path     string `yaml:"path"`
	Behavior string `yaml:"behavior"`
}

// Parse decodes the YAML bytes into a RouteConfig. Decode errors are returned;
// semantic validation lives in Validate.
func Parse(b []byte) (*RouteConfig, error) {
	rc := &RouteConfig{}
	if err := yaml.Unmarshal(b, rc); err != nil {
		return nil, fmt.Errorf("config: yaml decode: %w", err)
	}
	return rc, nil
}
