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
