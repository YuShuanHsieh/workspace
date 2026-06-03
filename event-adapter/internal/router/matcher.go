package router

import (
	"fmt"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

type Matcher struct {
	index map[string]config.RouteConfig
}

func New(routes []config.RouteConfig) (*Matcher, error) {
	index := make(map[string]config.RouteConfig, len(routes))
	for _, r := range routes {
		if existing, ok := index[r.Match.Type]; ok {
			return nil, fmt.Errorf("duplicate match type %q for routes %q and %q", r.Match.Type, existing.Name, r.Name)
		}
		index[r.Match.Type] = r
	}
	return &Matcher{index: index}, nil
}

func (m *Matcher) Match(ev *clevent.Event) (config.RouteConfig, bool) {
	if ev == nil {
		return config.RouteConfig{}, false
	}
	r, ok := m.index[ev.Type()]
	return r, ok
}
