package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestHelloHandler_ReturnsHelloFromPodName(t *testing.T) {
	t.Setenv("POD_NAME", "echo-server-xyz-abc")
	gin.SetMode(gin.TestMode)

	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "hello from echo-server-xyz-abc", w.Body.String())
}

func TestHelloHandler_EmptyPodName_StillReturns200(t *testing.T) {
	t.Setenv("POD_NAME", "")
	gin.SetMode(gin.TestMode)

	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "hello from unknown", w.Body.String())
}

func TestHelloHandler_WithInjectedIdentity_IncludesItInBody(t *testing.T) {
	t.Setenv("POD_NAME", "documents-api-xyz")
	gin.SetMode(gin.TestMode)

	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	req.Header.Set("X-User-Id", "alice-uid-001")
	req.Header.Set("X-User-Role", "editor")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "hello from documents-api-xyz (uid=alice-uid-001 role=editor)", w.Body.String())
}

func TestCatchAll_AnyPath_Returns200WithEchoedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("X-Auth-Context", "doc-1:document:edit")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"path":"/anything"`)
	assert.Contains(t, w.Body.String(), `"X-Auth-Context":["doc-1:document:edit"]`)
}

func TestCatchAll_Healthz_Returns200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"path":"/healthz"`)
}
