package cloudevent

import (
	"strings"
	"testing"
)

func TestParseJSONCloudEvent(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.ID() != "evt-1" {
		t.Fatalf("unexpected id: %s", ev.ID())
	}
	body, err := JSONDataBytes(ev)
	if err != nil {
		t.Fatalf("JSONDataBytes returned error: %v", err)
	}
	if string(body) != `{"taskId":"t1"}` {
		t.Fatalf("unexpected data: %s", body)
	}
}

func TestParseRejectsMissingRequiredField(t *testing.T) {
	_, err := Parse([]byte(`{"specversion":"1.0","source":"workspace/task","type":"com.workspace.task.created","data":{}}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestParseRejectsBase64Data(t *testing.T) {
	_, err := Parse([]byte(`{"specversion":"1.0","id":"evt-1","source":"workspace/task","type":"com.workspace.task.created","data_base64":"aGVsbG8="}`))
	if err == nil {
		t.Fatal("expected data_base64 rejection")
	}
}

func TestParseExtractsAndStripsDispatchCookies(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-c1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchcookies":{"session":"abc123","csrf-token":"xyz789"},"data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.DispatchCookies["session"] != "abc123" || ev.DispatchCookies["csrf-token"] != "xyz789" {
		t.Fatalf("unexpected cookies: %#v", ev.DispatchCookies)
	}
	if _, ok := ev.Extensions()["dispatchcookies"]; ok {
		t.Fatal("dispatchcookies leaked into CloudEvent extensions")
	}
}

func TestParseDispatchCookiesAbsentIsNil(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-c2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.DispatchCookies != nil {
		t.Fatalf("expected nil cookies, got %#v", ev.DispatchCookies)
	}
}

func TestParseRejectsNonStringDispatchCookies(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-c3","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchcookies":{"session":123},"data":{"taskId":"t1"}}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected error for non-string-valued dispatchcookies")
	}
}

func TestParseExtractsAndStripsDirectDispatchMetadata(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-dd1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchmethod":"DELETE","dispatchpath":"/orders/ord-456?hard=true","data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.DispatchMethod != "DELETE" {
		t.Fatalf("unexpected dispatch method: %q", ev.DispatchMethod)
	}
	if ev.DispatchPath != "/orders/ord-456?hard=true" {
		t.Fatalf("unexpected dispatch path: %q", ev.DispatchPath)
	}
	for _, name := range []string{"dispatchmethod", "dispatchpath"} {
		if _, ok := ev.Extensions()[name]; ok {
			t.Fatalf("%s leaked into CloudEvent extensions", name)
		}
	}
}

func TestParseDirectDispatchMetadataAbsentIsEmpty(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-dd2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.DispatchMethod != "" {
		t.Fatalf("expected empty dispatch method, got %q", ev.DispatchMethod)
	}
	if ev.DispatchPath != "" {
		t.Fatalf("expected empty dispatch path, got %q", ev.DispatchPath)
	}
}

func TestParseRejectsNonStringDirectDispatchMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "method", field: "dispatchmethod", value: `123`},
		{name: "path", field: "dispatchpath", value: `{"order":"ord-456"}`},
		{name: "null method", field: "dispatchmethod", value: `null`},
		{name: "null path", field: "dispatchpath", value: `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"specversion":"1.0","id":"evt-dd3","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","` + tc.field + `":` + tc.value + `,"data":{"taskId":"t1"}}`)
			_, err := Parse(raw)
			if err == nil {
				t.Fatalf("expected error for non-string %s", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), "must be a string") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
