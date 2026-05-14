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
