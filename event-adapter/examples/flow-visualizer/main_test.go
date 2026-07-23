// main_test.go
package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPreviewHandlerServesAssetsConfigAndSSE(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<h1>preview</h1>")},
		"event-flow.js": {Data: []byte("export {};")},
	}
	config := []byte(`{"version":1}`)
	fixture := []byte("{\"requestId\":\"req-demo-001\",\"event\":\"demo.request_sent\"}\n")
	server := httptest.NewServer(newPreviewHandler(assets, config, fixture, 0))
	t.Cleanup(server.Close)

	for path, want := range map[string][2]string{
		"/":              {"text/html", "preview"},
		"/event-flow.js": {"text/javascript", "export"},
		"/config.json":   {"application/json", `"version":1`},
	} {
		contentType, body := want[0], want[1]
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		if !strings.Contains(response.Header.Get("Content-Type"), contentType) {
			t.Fatalf("%s content type = %q", path, response.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(data), body) {
			t.Fatalf("%s body = %q", path, data)
		}
	}

	response, err := http.Get(server.URL + "/api/demo/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("SSE line = %q", line)
	}
}
