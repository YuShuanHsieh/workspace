package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return m
}

func TestReadyHealthyReturns200(t *testing.T) {
	c := &Checker{NATSConnected: func() bool { return true }, Heartbeat: &Heartbeat{}}
	rec := httptest.NewRecorder()
	c.Ready(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := decode(t, rec.Body.Bytes()); body["ready"] != true {
		t.Fatalf("body = %v, want ready:true", body)
	}
}

func TestReadyNATSDownReturns503WithReason(t *testing.T) {
	c := &Checker{NATSConnected: func() bool { return false }, Heartbeat: &Heartbeat{}}
	rec := httptest.NewRecorder()
	c.Ready(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := decode(t, rec.Body.Bytes())
	if body["ready"] != false {
		t.Fatalf("body = %v, want ready:false", body)
	}
	if body["reason"] != "nats connection failure" {
		t.Fatalf("reason = %v, want nats connection failure", body["reason"])
	}
}

func TestLiveFreshHeartbeatReturns200(t *testing.T) {
	hb := &Heartbeat{}
	hb.Beat()
	c := &Checker{Heartbeat: hb, MaxHeartbeatAge: time.Minute}
	rec := httptest.NewRecorder()
	c.Live(rec, httptest.NewRequest(http.MethodGet, "/live", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestLiveStaleHeartbeatReturns503(t *testing.T) {
	hb := &Heartbeat{}
	hb.lastNano.Store(time.Now().Add(-5 * time.Minute).UnixNano())
	c := &Checker{Heartbeat: hb, MaxHeartbeatAge: time.Minute}
	rec := httptest.NewRecorder()
	c.Live(rec, httptest.NewRequest(http.MethodGet, "/live", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := decode(t, rec.Body.Bytes()); body["alive"] != false {
		t.Fatalf("body = %v, want alive:false", body)
	}
}

func TestHeartbeatNeverBeatenIsHealthy(t *testing.T) {
	// A consumer-less (request-reply only) service never beats; it must still be
	// considered alive.
	if !(&Heartbeat{}).Healthy(time.Second) {
		t.Fatal("unbeaten heartbeat should be healthy")
	}
}

func TestNilHeartbeatHealthy(t *testing.T) {
	var hb *Heartbeat
	if !hb.Healthy(time.Second) {
		t.Fatal("nil heartbeat should report healthy")
	}
	hb.Beat() // must not panic
}
