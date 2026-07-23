// main.go
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/*
var embedded embed.FS

func newPreviewHandler(assets fs.FS, config, fixture []byte, replayDelay time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(config)
	})
	mux.HandleFunc("/api/demo/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(fixture)), "\n") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(replayDelay):
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	})
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return mux
}

func run() error {
	var listen, configPath, fixturePath, normalizeOutput string
	var replayDelay time.Duration
	flag.StringVar(&listen, "listen", "0.0.0.0:8080", "preview listen address")
	flag.StringVar(&configPath, "config", "request-reply.yaml", "YAML or JSON flow config")
	flag.StringVar(&fixturePath, "fixture", "fixtures/request-reply.jsonl", "JSONL SSE replay fixture")
	flag.StringVar(&normalizeOutput, "normalize-output", "", "write normalized JSON and exit")
	flag.DurationVar(&replayDelay, "replay-delay", 350*time.Millisecond, "delay between replayed events")
	flag.Parse()

	input, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	config, err := normalizeConfigBytes(configPath, input)
	if err != nil {
		return err
	}
	if normalizeOutput != "" {
		if err := os.WriteFile(normalizeOutput, config, 0o600); err != nil {
			return fmt.Errorf("write normalized config: %w", err)
		}
		return nil
	}
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	assets, err := fs.Sub(embedded, "web")
	if err != nil {
		return err
	}
	log.Printf("flow visualizer listening on http://%s", listen)
	return http.ListenAndServe(listen, newPreviewHandler(assets, config, fixture, replayDelay))
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
