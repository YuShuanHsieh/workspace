package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type call struct {
	target string
	url    string
	user   string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	docsAPI := envOrDefault("DOCUMENTS_API_URL", "http://documents-api.documents.svc.cluster.local:8080/hello")
	docsSearch := envOrDefault("DOCUMENTS_SEARCH_URL", "http://documents-search.documents.svc.cluster.local:8080/hello")
	wikiAPI := envOrDefault("WIKI_API_URL", "http://wiki-api.wiki.svc.cluster.local:8080/hello")

	allow := envOrDefault("ALLOW_USER", "alice@workspace.test")
	deny := envOrDefault("DENY_USER", "mallory@workspace.test")
	sleep := 2 * time.Second

	calls := []call{
		{"documents-api", docsAPI, allow},
		{"documents-api", docsAPI, deny},
		{"documents-search", docsSearch, allow},
		{"documents-search", docsSearch, deny},
		{"wiki-api", wikiAPI, allow},
		{"wiki-api", wikiAPI, deny},
	}

	client := &http.Client{Timeout: 5 * time.Second}
	slog.Info("dashboard-client starting")

	for {
		for _, c := range calls {
			doCall(client, c)
			time.Sleep(sleep)
		}
	}
}

func doCall(client *http.Client, c call) {
	req, err := http.NewRequest(http.MethodGet, c.url, nil)
	if err != nil {
		slog.Error("build request failed", "target", c.target, "err", err)
		return
	}
	req.Header.Set("x-workspace-user-id", c.user)
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("call failed", "target", c.target, "user", c.user, "err", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	slog.Info("call result",
		"target", c.target,
		"user", c.user,
		"status", resp.StatusCode,
		"body", string(body),
	)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
