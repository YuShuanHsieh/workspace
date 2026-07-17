package cloudevent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"event-adapter/internal/config"
)

// ErrorReplyType is the CloudEvent type used for replies the sidecar generates
// itself (parse failures, no matching route) rather than from an app response.
const ErrorReplyType = "io.eventadapter.error.reply"

func BuildResponse(in *Event, route config.RouteConfig, status int, contentType string, body []byte, location string) (*ce.Event, error) {
	if in == nil {
		return nil, fmt.Errorf("response: incoming event is nil")
	}
	out := ce.New()
	out.SetID(deterministicID(in.ID(), route.Name, route.Response.Type, route.Response.Subject))
	out.SetType(route.Response.Type)
	out.SetSource(route.Response.Source)
	out.SetSubject(route.Response.Subject)
	out.SetTime(time.Now().UTC())
	if route.Response.DataSchema != "" {
		out.SetDataSchema(route.Response.DataSchema)
	}
	if err := setHTTPData(&out, contentType, body); err != nil {
		return nil, fmt.Errorf("response: %w", err)
	}
	out.SetExtension("httpstatus", int32(status)) // #nosec G115 -- HTTP status code (100-599) always fits int32
	out.SetExtension("causationid", in.ID())
	if corr, ok := in.Extensions()["correlationid"]; ok {
		out.SetExtension("correlationid", corr)
	}
	if location != "" {
		out.SetExtension("httplocation", location)
	}
	return &out, nil
}

// BuildReply builds a request-reply response CloudEvent from the app's HTTP
// response. Unlike BuildResponse it sets no subject — the reply travels on the
// request's inbox.
func BuildReply(in *Event, reply config.ReplyConfig, routeName string, status int, contentType string, body []byte, location string) (*ce.Event, error) {
	if in == nil {
		return nil, fmt.Errorf("reply: incoming event is nil")
	}
	out := ce.New()
	out.SetID(deterministicID(in.ID(), routeName, reply.Type))
	out.SetType(reply.Type)
	out.SetSource(reply.Source)
	out.SetTime(time.Now().UTC())
	if reply.DataSchema != "" {
		out.SetDataSchema(reply.DataSchema)
	}
	if err := setHTTPData(&out, contentType, body); err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}
	out.SetExtension("httpstatus", int32(status)) // #nosec G115 -- HTTP status code (100-599) always fits int32
	out.SetExtension("causationid", in.ID())
	if corr, ok := in.Extensions()["correlationid"]; ok {
		out.SetExtension("correlationid", corr)
	}
	if location != "" {
		out.SetExtension("httplocation", location)
	}
	return &out, nil
}

// BuildErrorReply builds a self-generated error reply when there is no app
// response to wrap (malformed request, no matching route).
func BuildErrorReply(in *Event, source string, status int, message string) *ce.Event {
	out := ce.New()
	if in != nil {
		out.SetID(deterministicID(in.ID(), source, ErrorReplyType))
	} else {
		out.SetID(deterministicID(message, source, ErrorReplyType))
	}
	out.SetType(ErrorReplyType)
	out.SetSource(source)
	out.SetTime(time.Now().UTC())
	_ = out.SetData("application/json", map[string]string{"error": message})
	out.SetExtension("httpstatus", int32(status)) // #nosec G115 -- HTTP status code (100-599) always fits int32
	if in != nil {
		out.SetExtension("causationid", in.ID())
		if corr, ok := in.Extensions()["correlationid"]; ok {
			out.SetExtension("correlationid", corr)
		}
	}
	return &out
}

func setHTTPData(out *ce.Event, contentType string, body []byte) error {
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
		return fmt.Errorf("set data: %w", err)
	}
	return nil
}

func deterministicID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return "evt_" + hex.EncodeToString(sum[:])
}
