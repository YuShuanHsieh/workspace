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
// On allow, sets identity headers so Envoy can inject them into the original
// request before forwarding to the upstream app (request mutation via
// authorization_response.allowed_upstream_headers in the EnvoyFilter).
func checkHandler(c *gin.Context) {
	user := c.GetHeader("x-workspace-user-id")
	decision := "deny"
	status := http.StatusForbidden
	if _, ok := allowList[user]; ok && user != "" {
		decision = "allow"
		status = http.StatusOK
		// Mutation: enrich the original request with identity/role/scopes
		// before Envoy forwards it to the upstream app. Envoy reads these
		// from PCS's response headers and (when configured via
		// authorization_response.allowed_upstream_headers in the EnvoyFilter)
		// appends them to the original request.
		identity := identityFor(user)
		c.Header("X-User-Id", identity.id)
		c.Header("X-User-Role", identity.role)
		c.Header("X-Allowed-Scopes", identity.scopes)
	}
	slog.Info("decision",
		"user", user,
		"decision", decision,
		"ts", time.Now().UTC().Format(time.RFC3339Nano),
	)
	c.Status(status)
}

type principal struct {
	id     string
	role   string
	scopes string
}

func identityFor(user string) principal {
	switch user {
	case "alice@workspace.test":
		return principal{id: "alice-uid-001", role: "editor", scopes: "documents:read,documents:write"}
	case "bob@workspace.test":
		return principal{id: "bob-uid-002", role: "viewer", scopes: "documents:read"}
	}
	return principal{}
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
