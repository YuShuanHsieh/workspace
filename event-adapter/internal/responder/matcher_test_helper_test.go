package responder

import (
	"time"

	"event-adapter/internal/config"
	"event-adapter/internal/router"
)

func newTestMatcher() (*router.RequestMatcher, error) {
	return router.NewRequests([]config.RequestRouteConfig{{
		Name:     "upload-presign",
		Match:    config.RequestMatchConfig{Type: "com.x.request"},
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/r", Timeout: time.Second},
		Reply:    config.ReplyConfig{Source: "upload-service", Type: "com.x.reply"},
	}})
}

func newEmptyTestMatcher() (*router.RequestMatcher, error) {
	return router.NewRequests(nil)
}
