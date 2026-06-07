package router

import (
	"fmt"
	"strings"
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

func TestMatchByType(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Type: "com.workspace.task.created"},
	}
	m, err := New([]config.RouteConfig{route})
	if err != nil {
		t.Fatal(err)
	}
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	got, ok := m.Match(&clevent.Event{Event: &ev})
	if !ok {
		t.Fatal("expected match")
	}
	if got.Name != "task-created" {
		t.Fatalf("unexpected route: %s", got.Name)
	}
}

func TestMatchIgnoresSource(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Type: "com.workspace.task.created", Source: "workspace/task"},
	}
	m, err := New([]config.RouteConfig{route})
	if err != nil {
		t.Fatal(err)
	}
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("some-other-source")
	ev.SetType("com.workspace.task.created")
	got, ok := m.Match(&clevent.Event{Event: &ev})
	if !ok {
		t.Fatal("expected match: source must be ignored")
	}
	if got.Name != "task-created" {
		t.Fatalf("unexpected route: %s", got.Name)
	}
}

func TestMatchIgnoresSubject(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Type: "com.workspace.task.created", Subject: "t.tenant-a.app.task.event.created"},
	}
	m, err := New([]config.RouteConfig{route})
	if err != nil {
		t.Fatal(err)
	}
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	got, ok := m.Match(&clevent.Event{Event: &ev})
	if !ok {
		t.Fatal("expected match: subject must be ignored")
	}
	if got.Name != "task-created" {
		t.Fatalf("unexpected route: %s", got.Name)
	}
}

func TestMatchIndexedAcrossManyRoutes(t *testing.T) {
	routes := make([]config.RouteConfig, 0, 100)
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("route-%d", i)
		routes = append(routes, config.RouteConfig{
			Name:  name,
			Match: config.MatchConfig{Type: "type." + name},
		})
	}
	m, err := New(routes)
	if err != nil {
		t.Fatal(err)
	}
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("src/route-77")
	ev.SetType("type.route-77")
	got, ok := m.Match(&clevent.Event{Event: &ev})
	if !ok || got.Name != "route-77" {
		t.Fatalf("expected route-77, got ok=%v name=%q", ok, got.Name)
	}
}

func TestMatchIndexedRejectsWrongType(t *testing.T) {
	route := config.RouteConfig{
		Name:  "task-created",
		Match: config.MatchConfig{Type: "t"},
	}
	m, err := New([]config.RouteConfig{route})
	if err != nil {
		t.Fatal(err)
	}
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("src")
	ev.SetType("WRONG")
	if _, ok := m.Match(&clevent.Event{Event: &ev}); ok {
		t.Fatal("expected no match for wrong type")
	}
}

func TestNewRejectsDuplicateType(t *testing.T) {
	routes := []config.RouteConfig{
		{Name: "route-a", Match: config.MatchConfig{Type: "com.workspace.task.created"}},
		{Name: "route-b", Match: config.MatchConfig{Type: "com.workspace.task.created"}},
	}
	_, err := New(routes)
	if err == nil {
		t.Fatal("expected error for duplicate match type")
	}
	if !strings.Contains(err.Error(), "com.workspace.task.created") {
		t.Fatalf("expected error to name the duplicate type, got %v", err)
	}
}

func mustEvent(t *testing.T, s string) *clevent.Event {
	t.Helper()
	ev, err := clevent.Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	return ev
}

func TestRequestMatcher(t *testing.T) {
	routes := []config.RequestRouteConfig{{
		Name:  "upload-presign",
		Match: config.RequestMatchConfig{Type: "com.workspace.uploads.presign.request"},
	}}
	m, err := NewRequests(routes)
	if err != nil {
		t.Fatalf("NewRequests: %v", err)
	}
	ev := mustEvent(t, `{"specversion":"1.0","id":"1","source":"c","type":"com.workspace.uploads.presign.request","data":{}}`)
	r, ok := m.Match(ev)
	if !ok || r.Name != "upload-presign" {
		t.Fatalf("match = %+v ok=%v", r, ok)
	}
	miss := mustEvent(t, `{"specversion":"1.0","id":"2","source":"c","type":"other","data":{}}`)
	if _, ok := m.Match(miss); ok {
		t.Fatal("expected no match for unknown type")
	}
}

func TestNewRequestsRejectsDuplicateType(t *testing.T) {
	routes := []config.RequestRouteConfig{
		{Name: "a", Match: config.RequestMatchConfig{Type: "t"}},
		{Name: "b", Match: config.RequestMatchConfig{Type: "t"}},
	}
	if _, err := NewRequests(routes); err == nil {
		t.Fatal("expected duplicate-type error")
	}
}
