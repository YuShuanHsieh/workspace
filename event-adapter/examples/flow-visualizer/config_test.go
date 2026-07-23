// config_test.go
package main

import (
	"encoding/json"
	"testing"
)

func TestNormalizeConfigYAMLAndJSON(t *testing.T) {
	t.Parallel()
	yamlInput := []byte("version: 1\ntitle: Demo\nsource:\n  transport: sse\n  url: /events\n  correlationField: requestId\nlanes: []\nsteps: []\n")
	jsonInput := []byte(`{"version":1,"title":"Demo","source":{"transport":"sse","url":"/events","correlationField":"requestId"},"lanes":[],"steps":[]}`)
	gotYAML, err := normalizeConfigBytes("flow.yaml", yamlInput)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := normalizeConfigBytes("flow.json", jsonInput)
	if err != nil {
		t.Fatal(err)
	}
	var y, j any
	if err := json.Unmarshal(gotYAML, &y); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotJSON, &j); err != nil {
		t.Fatal(err)
	}
	if string(gotYAML) != string(gotJSON) {
		t.Fatalf("normalized output differs:\nyaml %s\njson %s", gotYAML, gotJSON)
	}
}

func TestNormalizeConfigRejectsUnknownExtensionAndMalformedInput(t *testing.T) {
	t.Parallel()
	if _, err := normalizeConfigBytes("flow.txt", []byte("{}")); err == nil {
		t.Fatal("expected extension error")
	}
	if _, err := normalizeConfigBytes("flow.yaml", []byte(":\n")); err == nil {
		t.Fatal("expected YAML parse error")
	}
}
