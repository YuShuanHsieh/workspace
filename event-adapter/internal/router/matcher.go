package router

import (
	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

type Matcher struct {
	routes []config.RouteConfig
}

func New(routes []config.RouteConfig) *Matcher {
	copied := append([]config.RouteConfig(nil), routes...)
	return &Matcher{routes: copied}
}

func (m *Matcher) Match(subject string, ev *clevent.Event) (config.RouteConfig, bool) {
	if ev == nil {
		return config.RouteConfig{}, false
	}
	for _, r := range m.routes {
		if r.Match.Subject == subject && r.Match.Type == ev.Type() && r.Match.Source == ev.Source() {
			return r, true
		}
	}
	return config.RouteConfig{}, false
}
