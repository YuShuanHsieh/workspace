package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
)

var allowList = map[string]struct{}{
	"alice@workspace.test": {},
	"bob@workspace.test":   {},
}

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	r.POST("/check", func(c *gin.Context) {
		user := c.GetHeader("x-workspace-user-id")
		decision := "deny"
		status := http.StatusForbidden
		if _, ok := allowList[user]; ok && user != "" {
			decision = "allow"
			status = http.StatusOK
		}
		slog.Info("decision",
			"user", user,
			"decision", decision,
			"ts", time.Now().UTC().Format(time.RFC3339Nano),
		)
		c.Status(status)
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
	slog.Info("pcs starting", "port", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
}
