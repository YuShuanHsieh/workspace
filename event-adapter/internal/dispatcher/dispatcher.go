package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

type Result struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

var ErrNilEvent = errors.New("dispatcher: nil event")

type Dispatcher struct {
	baseURL string
	client  *http.Client
}

func New(baseURL string, client *http.Client) *Dispatcher {
	if client == nil {
		client = http.DefaultClient
	}
	return &Dispatcher{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

func (d *Dispatcher) Dispatch(ctx context.Context, route config.RouteConfig, ev *clevent.Event) (Result, error) {
	if ev == nil || ev.Event == nil {
		return Result{}, ErrNilEvent
	}
	body, err := clevent.JSONDataBytes(ev)
	if err != nil {
		return Result{}, err
	}
	u, err := url.JoinPath(d.baseURL, route.Dispatch.Path)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: build url: %w", err)
	}
	if route.Dispatch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, route.Dispatch.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, route.Dispatch.Method, u, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: create request: %w", err)
	}
	setCloudEventHeaders(req, ev)
	setPublisherHeaders(req, route, ev)
	for k, v := range route.Dispatch.Headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: read response: %w", err)
	}
	return Result{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: respBody}, nil
}

func setCloudEventHeaders(req *http.Request, ev *clevent.Event) {
	req.Header.Set("ce-id", ev.ID())
	req.Header.Set("ce-type", ev.Type())
	req.Header.Set("ce-source", ev.Source())
	req.Header.Set("ce-specversion", ev.SpecVersion())
	req.Header.Set("Idempotency-Key", ev.ID())
	if ev.Subject() != "" {
		req.Header.Set("ce-subject", ev.Subject())
	}
	if !ev.Time().IsZero() {
		req.Header.Set("ce-time", ev.Time().Format("2006-01-02T15:04:05.999999999Z07:00"))
	}
	if ev.DataContentType() != "" {
		req.Header.Set("ce-datacontenttype", ev.DataContentType())
	}
	if ev.DataSchema() != "" {
		req.Header.Set("ce-dataschema", ev.DataSchema())
	}
	for name, value := range ev.Extensions() {
		if strings.EqualFold(name, "dispatchheaders") {
			continue
		}
		req.Header.Set("ce-"+strings.ToLower(name), fmt.Sprint(value))
	}
}

func setPublisherHeaders(req *http.Request, route config.RouteConfig, ev *clevent.Event) {
	if len(ev.DispatchHeaders) == 0 {
		return
	}
	if len(route.Dispatch.ForwardHeaders) == 0 {
		for name, value := range ev.DispatchHeaders {
			if config.IsReservedHeader(name) {
				continue
			}
			req.Header.Set(name, value)
		}
		return
	}
	allowed := map[string]string{}
	for _, name := range route.Dispatch.ForwardHeaders {
		allowed[strings.ToLower(name)] = name
	}
	for name, value := range ev.DispatchHeaders {
		canonical, ok := allowed[strings.ToLower(name)]
		if !ok {
			continue
		}
		if config.IsReservedHeader(canonical) {
			continue
		}
		req.Header.Set(canonical, value)
	}
}
