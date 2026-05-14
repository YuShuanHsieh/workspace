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

// checkHandler authorizes a request based on the x-workspace-user-id header.
// Returns 200 if the header value is in allowList, 403 otherwise.
func checkHandler(c *gin.Context) {
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
}

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// Bare /check matches when Envoy ext_authz_http is configured without a
	// path_prefix (or with path_prefix that exactly equals /check + empty original path).
	r.POST("/check", checkHandler)

	// Catch-all: Envoy's ext_authz_http with `path_prefix: /check` appends the
	// original request path to the prefix, e.g. POST /check/hello. Route those
	// through the same decision logic — the prefix is a fixed marker, not a
	// real path component.
	r.NoRoute(checkHandler)

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
