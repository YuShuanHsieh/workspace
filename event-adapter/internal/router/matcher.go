package router

import (
	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

type matchKey struct {
	subject string
	typ     string
	source  string
}

type Matcher struct {
	index map[matchKey]config.RouteConfig
}

func New(routes []config.RouteConfig) *Matcher {
	index := make(map[matchKey]config.RouteConfig, len(routes))
	for _, r := range routes {
		index[matchKey{subject: r.Match.Subject, typ: r.Match.Type, source: r.Match.Source}] = r
	}
	return &Matcher{index: index}
}

func (m *Matcher) Match(subject string, ev *clevent.Event) (config.RouteConfig, bool) {
	if ev == nil {
		return config.RouteConfig{}, false
	}
	r, ok := m.index[matchKey{subject: subject, typ: ev.Type(), source: ev.Source()}]
	return r, ok
}
