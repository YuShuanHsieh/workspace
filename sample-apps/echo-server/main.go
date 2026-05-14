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
		slog.Info("hello request",
			"pod", podName,
			"user", user,
		)
		c.String(http.StatusOK, "hello from "+podName)
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
