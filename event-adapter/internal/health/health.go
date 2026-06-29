// Package health provides liveness/readiness HTTP endpoints and a heartbeat
// primitive used to detect a stalled (deadlocked) event loop.
//
//   - /ready reports whether the service can accept traffic: the NATS
//     connection must be established. Healthy → 200 {"ready":true};
//     NATS down → 503 {"ready":false,"reason":"nats connection failure"}.
//   - /live reports whether the process is alive and the event loop is not
//     wedged: the most recent heartbeat must be recent. Healthy → 200;
//     stale → 503.
package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// Heartbeat is a monotonic timestamp updated by a long-running loop. A stale
// heartbeat signals the loop is deadlocked or wedged.
type Heartbeat struct {
	lastNano atomic.Int64
}

// Beat records the current time as the latest heartbeat.
func (h *Heartbeat) Beat() {
	if h == nil {
		return
	}
	h.lastNano.Store(time.Now().UnixNano())
}

// Healthy reports whether the heartbeat is within maxAge. A heartbeat that has
// never been recorded is treated as healthy, so services that run no consumer
// loop (request-reply only) are not reported dead.
func (h *Heartbeat) Healthy(maxAge time.Duration) bool {
	if h == nil {
		return true
	}
	last := h.lastNano.Load()
	if last == 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) <= maxAge
}

// Checker answers the readiness and liveness probes.
type Checker struct {
	// NATSConnected reports whether the NATS connection is established.
	NATSConnected func() bool
	// Heartbeat is the event-loop heartbeat consulted by the liveness probe.
	Heartbeat *Heartbeat
	// MaxHeartbeatAge is how old the heartbeat may be before /live fails.
	MaxHeartbeatAge time.Duration
}

// Ready handles GET /ready.
func (c *Checker) Ready(w http.ResponseWriter, _ *http.Request) {
	if c.NATSConnected != nil && c.NATSConnected() {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"ready":  false,
		"reason": "nats connection failure",
	})
}

// Live handles GET /live.
func (c *Checker) Live(w http.ResponseWriter, _ *http.Request) {
	if c.Heartbeat.Healthy(c.MaxHeartbeatAge) {
		writeJSON(w, http.StatusOK, map[string]any{"alive": true})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"alive":  false,
		"reason": "event loop heartbeat stale",
	})
}

// Handler returns an http.Handler serving /ready and /live.
func (c *Checker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", c.Ready)
	mux.HandleFunc("/live", c.Live)
	return mux
}

// NewServer builds an http.Server serving the health endpoints on addr.
func NewServer(addr string, c *Checker) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           c.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func writeJSON(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
