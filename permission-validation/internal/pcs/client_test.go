package pcs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_Allow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/permission-check/v1/check", r.URL.Path)
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "Bearer sso-tok", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "req-1", r.Header.Get("X-Request-Id"))

		var body struct {
			ObjectID   string `json:"objectId"`
			ObjectType string `json:"objectType"`
			Permission string `json:"permission"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "doc-1", body.ObjectID)
		require.Equal(t, "document", body.ObjectType)
		require.Equal(t, "edit", body.Permission)
		_, _ = io.WriteString(w, `{"allowed": true}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{
		ObjectID: "doc-1", ObjectType: "document", Permission: "edit",
		SSOToken: "sso-tok", RequestID: "req-1",
	})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, dec)
}

func TestClient_Deny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"allowed": false}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, dec)
}

func TestClient_5xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 503)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
	require.Equal(t, DecisionUnknown, dec)
}

func TestClient_TimeoutIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"allowed": true}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
	require.Equal(t, DecisionUnknown, dec)
}

func TestClient_ContextCancelIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{"allowed": true}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient(srv.URL, 5*time.Second) // long timeout — only ctx cancels
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	dec, err := c.Check(ctx, CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
	require.Equal(t, DecisionUnknown, dec)
}

func TestClient_MalformedResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not-json`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode response")
	require.Equal(t, DecisionUnknown, dec)
}

func TestClient_TrailingSlashEndpointNormalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/permission-check/v1/check", r.URL.Path)
		_, _ = io.WriteString(w, `{"allowed": true}`)
	}))
	defer srv.Close()
	// Note the trailing slash.
	c := NewClient(srv.URL+"/", 500*time.Millisecond)
	_, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.NoError(t, err)
}

func TestClient_OversizedResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBodyBytes+1))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "response body too large")
	require.Equal(t, DecisionUnknown, dec)
}
