package router

import (
	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

type Matcher struct {
	index map[string]config.RouteConfig
}

func New(routes []config.RouteConfig) *Matcher {
	index := make(map[string]config.RouteConfig, len(routes))
	for _, r := range routes {
		index[r.Match.Type] = r
	}
	return &Matcher{index: index}
}

func (m *Matcher) Match(ev *clevent.Event) (config.RouteConfig, bool) {
	if ev == nil {
		return config.RouteConfig{}, false
	}
	r, ok := m.index[ev.Type()]
	return r, ok
}
