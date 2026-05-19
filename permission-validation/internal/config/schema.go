package config

import (
	"bytes"
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

// Parse decodes the YAML bytes into a RouteConfig with strict-mode decoding:
// unknown fields are rejected so typos in keys surface as decode errors instead
// of unmarshalling to zero values. Semantic validation lives in Validate.
func Parse(b []byte) (*RouteConfig, error) {
	rc := &RouteConfig{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(rc); err != nil {
		return nil, fmt.Errorf("config: yaml decode: %w", err)
	}
	return rc, nil
}
