package cloudevent

import (
	"encoding/json"
	"fmt"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

type Event struct {
	*ce.Event
	DispatchHeaders map[string]string
	DispatchCookies map[string]string
}

func (e *Event) MarshalJSON() ([]byte, error) {
	if e == nil || e.Event == nil {
		return []byte("null"), nil
	}
	return json.Marshal(e.Event)
}

func Parse(raw []byte) (*Event, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("cloudevent: decode json: %w", err)
	}
	if _, ok := probe["data_base64"]; ok {
		return nil, fmt.Errorf("cloudevent: data_base64 is not supported in phase 1")
	}
	dispatchHeaders, err := parseDispatchHeaders(probe["dispatchheaders"])
	if err != nil {
		return nil, err
	}
	delete(probe, "dispatchheaders")
	dispatchCookies, err := parseDispatchCookies(probe["dispatchcookies"])
	if err != nil {
		return nil, err
	}
	delete(probe, "dispatchcookies")
	cleaned, err := json.Marshal(probe)
	if err != nil {
		return nil, fmt.Errorf("cloudevent: clean envelope: %w", err)
	}
	ev := ce.New()
	if err := json.Unmarshal(cleaned, &ev); err != nil {
		return nil, fmt.Errorf("cloudevent: decode envelope: %w", err)
	}
	if ev.ID() == "" {
		return nil, fmt.Errorf("cloudevent: id is required")
	}
	if ev.Source() == "" {
		return nil, fmt.Errorf("cloudevent: source is required")
	}
	if ev.SpecVersion() == "" {
		return nil, fmt.Errorf("cloudevent: specversion is required")
	}
	if ev.Type() == "" {
		return nil, fmt.Errorf("cloudevent: type is required")
	}
	if ev.Data() == nil {
		return nil, fmt.Errorf("cloudevent: data is required")
	}
	return &Event{Event: &ev, DispatchHeaders: dispatchHeaders, DispatchCookies: dispatchCookies}, nil
}

func JSONDataBytes(ev *Event) ([]byte, error) {
	if ev == nil {
		return nil, fmt.Errorf("cloudevent: event is nil")
	}
	if ev.Data() == nil {
		return nil, fmt.Errorf("cloudevent: data is required")
	}
	return ev.Data(), nil
}

func parseDispatchHeaders(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("cloudevent: dispatchheaders must be a string-valued object: %w", err)
	}
	return values, nil
}

func parseDispatchCookies(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("cloudevent: dispatchcookies must be a string-valued object: %w", err)
	}
	return values, nil
}
