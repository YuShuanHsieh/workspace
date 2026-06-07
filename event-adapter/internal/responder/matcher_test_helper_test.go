package responder

import (
	"event-adapter/internal/config"
	"event-adapter/internal/router"
)

func newTestMatcher() (*router.RequestMatcher, error) {
	return router.NewRequests([]config.RequestRouteConfig{{
		Name:  "upload-presign",
		Match: config.MatchConfig{Type: "com.x.request"},
	}})
}
