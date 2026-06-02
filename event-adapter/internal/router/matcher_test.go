package router

import (
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

func TestMatchExactSubjectTypeSource(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
	}
	m := New([]config.RouteConfig{route})
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	got, ok := m.Match("t.tenant-a.app.task.event.created", &clevent.Event{Event: &ev})
	if !ok {
		t.Fatal("expected match")
	}
	if got.Name != "task-created" {
		t.Fatalf("unexpected route: %s", got.Name)
	}
}

func TestMatchRejectsWrongSource(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
	}
	m := New([]config.RouteConfig{route})
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("other")
	ev.SetType("com.workspace.task.created")
	_, ok := m.Match("t.tenant-a.app.task.event.created", &clevent.Event{Event: &ev})
	if ok {
		t.Fatal("expected no match")
	}
}

func TestMatchIndexedAcrossManyRoutes(t *testing.T) {
	routes := make([]config.RouteConfig, 0, 100)
	for i := 0; i < 100; i++ {
		name := "route-" + string(rune('a'+i%26))
		routes = append(routes, config.RouteConfig{
			Name: name,
			Match: config.MatchConfig{
				Subject: "t.app." + name + ".created",
				Type:    "type." + name,
				Source:  "src/" + name,
			},
		})
	}
	m := New(routes)
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("src/route-z")
	ev.SetType("type.route-z")
	got, ok := m.Match("t.app.route-z.created", &clevent.Event{Event: &ev})
	if !ok || got.Name != "route-z" {
		t.Fatalf("expected route-z, got ok=%v name=%q", ok, got.Name)
	}
}

func TestMatchIndexedRejectsWrongType(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Subject: "s", Type: "t", Source: "src"},
	}
	m := New([]config.RouteConfig{route})
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("src")
	ev.SetType("WRONG")
	if _, ok := m.Match("s", &clevent.Event{Event: &ev}); ok {
		t.Fatal("expected no match for wrong type")
	}
}
