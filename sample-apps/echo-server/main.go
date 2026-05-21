package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/hello", func(c *gin.Context) {
		podName := os.Getenv("POD_NAME")
		if podName == "" {
			podName = "unknown"
		}
		user := c.GetHeader("x-workspace-user-id")
		userId := c.GetHeader("X-User-Id")
		role := c.GetHeader("X-User-Role")
		scopes := c.GetHeader("X-Allowed-Scopes")

		slog.Info("hello request",
			"pod", podName,
			"user", user,
			"injected_user_id", userId,
			"injected_role", role,
			"injected_scopes", scopes,
		)

		body := "hello from " + podName
		if userId != "" {
			body += " (uid=" + userId + " role=" + role + ")"
		}
		c.String(http.StatusOK, body)
	})

	// Catch-all: echo back method, path, and request headers as JSON.
	// Used by the kind-demo-ext_proc demos (routes /anything and /healthz),
	// and by anyone who points curl at an arbitrary path to see what the
	// upstream actually receives after the sidecar mutates headers.
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"method":  c.Request.Method,
			"path":    c.Request.URL.Path,
			"headers": c.Request.Header,
		})
	})

	return r
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	r := newRouter()
	slog.Info("echo-server starting", "port", port, "pod", os.Getenv("POD_NAME"))
	if err := http.ListenAndServe(":"+port, r); err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
}
