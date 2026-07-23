package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type calls struct {
	mu sync.Mutex
	c  []map[string]any
}

func (c *calls) record(r *http.Request) {
	hdrs := map[string]string{}
	for k, vs := range r.Header {
		hdrs[k] = vs[0]
	}
	c.mu.Lock()
	c.c = append(c.c, map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"headers": hdrs,
	})
	c.mu.Unlock()
}

func main() {
	addr := flag.String("listen", "0.0.0.0:8080", "")
	flag.Parse()

	c := &calls{}

	mux := http.NewServeMux()
	mux.HandleFunc("/_admin/calls", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c.c)
	})
	mux.HandleFunc("/_admin/reset", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		c.c = nil
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "ok %s %s\n", r.Method, r.URL.Path)
	})

	log.Printf("fake-backend: listening on %s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
