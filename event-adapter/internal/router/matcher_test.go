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
