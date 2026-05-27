package cloudevent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"client-to-server/internal/config"
)

func BuildResponse(in *Event, route config.RouteConfig, contentType string, body []byte) (*ce.Event, error) {
	if in == nil {
		return nil, fmt.Errorf("response: incoming event is nil")
	}
	out := ce.New()
	out.SetID(deterministicResponseID(in.ID(), route))
	out.SetType(route.Response.Type)
	out.SetSource(route.Response.Source)
	out.SetTime(time.Now().UTC())
	if route.Response.DataSchema != "" {
		out.SetDataSchema(route.Response.DataSchema)
	}
	if contentType == "" {
		contentType = "application/json"
	}
	data := any(string(body))
	if strings.Contains(strings.ToLower(contentType), "json") && len(body) > 0 {
		var raw any
		if err := json.Unmarshal(body, &raw); err == nil {
			data = raw
		}
	}
	if err := out.SetData(contentType, data); err != nil {
		return nil, fmt.Errorf("response: set data: %w", err)
	}
	out.SetExtension("causationid", in.ID())
	if corr, ok := in.Extensions()["correlationid"]; ok {
		out.SetExtension("correlationid", corr)
	}
	return &out, nil
}

func deterministicResponseID(incomingID string, route config.RouteConfig) string {
	sum := sha256.Sum256([]byte(incomingID + "\n" + route.Name + "\n" + route.Response.Type + "\n" + route.Response.Subject))
	return "evt_" + hex.EncodeToString(sum[:])
}
