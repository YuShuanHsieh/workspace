package main

import (
	"bytes"
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level mock-app configuration.
type Config struct {
	Addr     string    `yaml:"addr"`
	Handlers []Handler `yaml:"handlers"`
}

// Handler declares a single HTTP endpoint, its required headers and cookies, and the fixed response.
type Handler struct {
	Method         string          `yaml:"method"`
	Path           string          `yaml:"path"`
	RequireHeaders []string        `yaml:"requireHeaders"`
	RequireCookies []string        `yaml:"requireCookies"`
	Response       HandlerResponse `yaml:"response"`
}

// HandlerResponse is the fixed response the mock app returns.
type HandlerResponse struct {
	Status      int    `yaml:"status"`
	ContentType string `yaml:"contentType"`
	Body        string `yaml:"body"`
	Location    string `yaml:"location"`
	EchoPath    bool   `yaml:"echoPath"`
}

func main() {
	configPath := flag.String("config", "mock-app.yaml", "path to handler config file")
	addrOverride := flag.String("addr", "", "override bind address from config")
	flag.Parse()

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}

	cfg.Addr = cmp.Or(*addrOverride, cfg.Addr, "0.0.0.0:18080")

	mux := http.NewServeMux()
	for _, h := range cfg.Handlers {
		pattern := fmt.Sprintf("%s %s", strings.ToUpper(h.Method), h.Path)
		mux.HandleFunc(pattern, makeHandler(h))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r, nil)
		http.Error(w, "no handler for "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})

	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("mock-app listening on %s with %d handler(s)", cfg.Addr, len(cfg.Handlers))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("mock-app shutting down")
	if err := srv.Shutdown(context.Background()); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func makeHandler(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		logRequest(r, body)

		for _, name := range h.RequireHeaders {
			if r.Header.Get(name) == "" {
				msg := fmt.Sprintf("missing required header: %s", name)
				log.Printf("  ✗ %s", msg)
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}

		for _, name := range h.RequireCookies {
			if _, err := r.Cookie(name); err != nil {
				msg := fmt.Sprintf("missing required cookie: %s", name)
				log.Printf("  ✗ %s", msg)
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}

		if h.Response.ContentType != "" {
			w.Header().Set("Content-Type", h.Response.ContentType)
		}
		if h.Response.Location != "" {
			w.Header().Set("Location", h.Response.Location)
		}
		status := cmp.Or(h.Response.Status, http.StatusOK)
		w.WriteHeader(status)
		switch {
		case h.Response.EchoPath:
			_, _ = fmt.Fprintf(w, `{"path":"%s"}`, r.URL.Path)
		case h.Response.Body != "":
			_, _ = fmt.Fprint(w, h.Response.Body)
		}
		log.Printf("  ← %d %s", status, http.StatusText(status))
	}
}

func logRequest(r *http.Request, body []byte) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "→ %s %s\n", r.Method, r.URL.Path)
	for name, vals := range r.Header {
		if isSensitiveHeader(name) {
			fmt.Fprintf(&b, "  %s: [REDACTED]\n", name)
			continue
		}
		fmt.Fprintf(&b, "  %s: %s\n", name, strings.Join(vals, ", "))
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "  body: %s\n", body)
	}
	log.Print(b.String())
}

func isSensitiveHeader(name string) bool {
	switch {
	case strings.EqualFold(name, "Cookie"),
		strings.EqualFold(name, "Set-Cookie"),
		strings.EqualFold(name, "Authorization"):
		return true
	default:
		return false
	}
}
