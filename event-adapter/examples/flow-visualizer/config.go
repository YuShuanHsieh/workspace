// config.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func normalizeConfigBytes(name string, input []byte) ([]byte, error) {
	var value any
	switch filepath.Ext(name) {
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(input))
		decoder.KnownFields(false)
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode YAML: %w", err)
		}
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(input))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
	default:
		return nil, fmt.Errorf("config must use .yaml, .yml, or .json")
	}
	output, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode normalized JSON: %w", err)
	}
	return output, nil
}
