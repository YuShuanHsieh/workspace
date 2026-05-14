package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCheckHandler_AllowedUser_Returns200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "alice@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCheckHandler_SecondAllowedUser_Returns200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "bob@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCheckHandler_DeniedUser_Returns403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "mallory@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCheckHandler_MissingHeader_Returns403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCheckHandler_EmptyHeader_Returns403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// Envoy's ext_authz_http with `path_prefix: /check` appends the original
// request path to the prefix — e.g. a request for /hello becomes a POST
// to /check/hello on PCS. Verify the NoRoute catch-all handles this.
func TestCheckHandler_CatchAllPathPrefix_AllowedUser_Returns200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check/hello", nil)
	req.Header.Set("x-workspace-user-id", "alice@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCheckHandler_CatchAllPathPrefix_DeniedUser_Returns403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check/some/nested/path", nil)
	req.Header.Set("x-workspace-user-id", "mallory@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCheckHandler_AllowedUser_SetsIdentityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "alice@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "alice-uid-001", w.Header().Get("X-User-Id"))
	assert.Equal(t, "editor", w.Header().Get("X-User-Role"))
	assert.Equal(t, "documents:read,documents:write", w.Header().Get("X-Allowed-Scopes"))
}

func TestCheckHandler_AllowedSecondUser_HasDifferentIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "bob@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "bob-uid-002", w.Header().Get("X-User-Id"))
	assert.Equal(t, "viewer", w.Header().Get("X-User-Role"))
}

func TestCheckHandler_DeniedUser_NoIdentityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check", nil)
	req.Header.Set("x-workspace-user-id", "mallory@workspace.test")

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, w.Header().Get("X-User-Id"))
	assert.Empty(t, w.Header().Get("X-User-Role"))
}
