package cloudevent

import "testing"

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
