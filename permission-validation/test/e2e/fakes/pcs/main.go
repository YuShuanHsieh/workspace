package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type rule struct {
	Allowed bool `json:"allowed"`
}

type fixture struct {
	mu    sync.Mutex
	rules map[string]rule
	calls []map[string]string
}

func (f *fixture) check(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		ObjectID   string `json:"objectId"`
		ObjectType string `json:"objectType"`
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	auth := r.Header.Get("Authorization")
	f.mu.Lock()
	f.calls = append(f.calls, map[string]string{
		"objectId":      req.ObjectID,
		"objectType":    req.ObjectType,
		"permission":    req.Permission,
		"authorization": auth,
	})
	key := req.ObjectID + "|" + req.ObjectType + "|" + req.Permission
	r2, ok := f.rules[key]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no rule for "+key, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r2)
}

func (f *fixture) loadFromEnv() {
	// PCS_RULES="doc-1|document|edit=true,doc-2|document|edit=false"
	for _, kv := range strings.Split(os.Getenv("PCS_RULES"), ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.LastIndex(kv, "=")
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		f.rules[key] = rule{Allowed: val == "true"}
	}
}

func main() {
	addr := flag.String("listen", "0.0.0.0:9000", "")
	flag.Parse()

	f := &fixture{rules: map[string]rule{}}
	f.loadFromEnv()

	mux := http.NewServeMux()
	mux.HandleFunc("/permission-check/v1/check", f.check)
	mux.HandleFunc("/_admin/rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		var in map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		for k, v := range in {
			f.rules[k] = rule{Allowed: v}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/_admin/calls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.calls)
	})
	mux.HandleFunc("/_admin/reset", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = nil
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("fake-pcs: listening on %s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
