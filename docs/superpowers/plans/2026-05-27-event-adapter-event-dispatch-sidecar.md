# event-adapter Event Dispatch Sidecar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Phase 1 event-adapter event dispatch sidecar: consume CloudEvents from NATS JetStream, dispatch event data to configured localhost HTTP handlers, publish deterministic response CloudEvents, and acknowledge original messages only after response or DLQ publish confirmation.

**Architecture:** Create a new Go module under `event-adapter/`, separate from the existing `permission-validation/` module because this service owns NATS event delivery rather than Envoy permission checks. The sidecar is composed from small internal packages: `config` for static YAML, `cloudevent` for envelope parsing/building, `router` for exact route matching, `dispatcher` for localhost HTTP calls, `processor` for retry/ack/DLQ orchestration, `natsjs` for JetStream integration, and `metrics` for OpenTelemetry instruments.

**Tech Stack:** Go 1.25, `github.com/nats-io/nats.go` for JetStream, `github.com/cloudevents/sdk-go/v2/event` for CloudEvent handling, `gopkg.in/yaml.v3` for strict YAML parsing, `go.opentelemetry.io/otel/metric` for metrics, `github.com/stretchr/testify` for unit tests, Docker Compose for e2e with NATS JetStream and a fake app backend.

**Design references:**
- [prd/event-adapter/prd.md](../../../prd/event-adapter/prd.md) — normative PRD.
- [prd/event-driven/prd.md](../../../prd/event-driven/prd.md) — platform-level JetStream, subject, and reliability context.
- [prd/app-to-app/draft.md](../../../prd/app-to-app/draft.md) — marketplace and source-app validation context.

---

## Scope Decisions

- Phase 1 uses NATS JetStream durable consumers only. Queue subscriptions are not implemented.
- Route matching uses exact subject, exact CloudEvent `type`, and exact CloudEvent `source`. NATS wildcards stay out of Phase 1 implementation until the PRD open question is resolved.
- Response event IDs are deterministic by default: `sha256(incomingID + "\n" + routeName + "\n" + responseType + "\n" + responseSubject)`, encoded as lowercase hex with prefix `evt_`.
- Publisher-supplied backend HTTP headers are carried in the CloudEvent `dispatchheaders` extension and forwarded only when the matched route lists the header in `dispatch.forwardHeaders`.
- Phase 1 supports JSON CloudEvent `data` payloads. `data_base64` is rejected unless a future route field enables binary payloads.
- The sidecar dispatches only to `127.0.0.1`, `localhost`, or loopback IPs. Non-loopback app base URLs fail config validation.
- Permission evaluation is not implemented in this sidecar.

## File Structure

All paths are relative to `/home/cjamhe01385/workspace/event-adapter/`.

```text
event-adapter/
├── go.mod
├── go.sum
├── README.md
├── cmd/event-adapter/
│   ├── main.go
│   └── main_test.go
├── internal/config/
│   ├── schema.go
│   ├── schema_test.go
│   ├── validate.go
│   └── validate_test.go
├── internal/cloudevent/
│   ├── event.go
│   ├── event_test.go
│   ├── response.go
│   └── response_test.go
├── internal/router/
│   ├── matcher.go
│   └── matcher_test.go
├── internal/dispatcher/
│   ├── dispatcher.go
│   └── dispatcher_test.go
├── internal/processor/
│   ├── processor.go
│   ├── processor_test.go
│   ├── retry.go
│   └── retry_test.go
├── internal/natsjs/
│   ├── client.go
│   └── client_test.go
├── internal/metrics/
│   ├── metrics.go
│   └── metrics_test.go
├── examples/onboarding/
│   ├── app.go
│   ├── routes.yaml
│   ├── publish.sh
│   └── README.md
└── test/e2e/
    ├── docker-compose.yaml
    ├── routes.yaml
    ├── e2e_test.go
    └── README.md
```

---

## Task 1: Bootstrap the Go Module and CLI Entrypoint

**Files:**
- Create: `event-adapter/go.mod`
- Create: `event-adapter/cmd/event-adapter/main.go`
- Create: `event-adapter/cmd/event-adapter/main_test.go`
- Create: `event-adapter/README.md`

- [ ] **Step 1: Create module skeleton**

Run:

```bash
mkdir -p event-adapter/cmd/event-adapter
cd event-adapter
go mod init event-adapter
go get github.com/nats-io/nats.go@v1.44.0
go get github.com/cloudevents/sdk-go/v2@v2.16.2
go get github.com/stretchr/testify@v1.11.1
go get go.opentelemetry.io/otel@v1.43.0
go get go.opentelemetry.io/otel/metric@v1.43.0
go get gopkg.in/yaml.v3@v3.0.1
```

Expected: `go.mod` exists with module path `event-adapter`.

- [ ] **Step 2: Write CLI skeleton**

Create `cmd/event-adapter/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

type options struct {
	configPath string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("event-adapter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.configPath, "config", "routes.yaml", "path to sidecar route configuration")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "event-adapter - NATS JetStream to local HTTP event sidecar")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if opts.configPath == "" {
		fmt.Fprintln(stderr, "config path is required")
		return 2
	}
	fmt.Fprintf(stdout, "event-adapter config=%s\n", opts.configPath)
	return 0
}
```

- [ ] **Step 3: Write CLI tests**

Create `cmd/event-adapter/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
	if !strings.Contains(stderr.String(), "NATS JetStream") {
		t.Fatalf("help missing service description: %q", stderr.String())
	}
}

func TestRunRejectsEmptyConfigPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", ""}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "config path is required") {
		t.Fatalf("stderr missing config error: %q", stderr.String())
	}
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
cd event-adapter && go test ./cmd/event-adapter/...
```

Expected: `ok event-adapter/cmd/event-adapter`.

- [ ] **Step 5: Add README**

Create `README.md`:

```markdown
# event-adapter

NATS JetStream to local HTTP event dispatch sidecar.

Design source: `../prd/event-adapter/prd.md`.

Phase 1 responsibilities:
- consume CloudEvents from JetStream durable consumers
- dispatch JSON CloudEvent data to configured localhost HTTP handlers
- publish deterministic response CloudEvents
- publish exhausted failures to DLQ
- acknowledge original messages only after response or DLQ publish confirmation
```

- [ ] **Step 6: Commit**

Run:

```bash
git add event-adapter/go.mod event-adapter/go.sum event-adapter/README.md event-adapter/cmd/event-adapter
git commit -m "feat(event-adapter): bootstrap event sidecar module"
```

---

## Task 2: Static YAML Config Schema and Validation

**Files:**
- Create: `event-adapter/internal/config/schema.go`
- Create: `event-adapter/internal/config/schema_test.go`
- Create: `event-adapter/internal/config/validate.go`
- Create: `event-adapter/internal/config/validate_test.go`

- [ ] **Step 1: Write config parser tests**

Create `internal/config/schema_test.go`:

```go
package config

import "testing"

func TestParseValidConfig(t *testing.T) {
	raw := []byte(`
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://nats:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      subject: t.tenant-a.app.task.event.created
      type: com.workspace.task.created
      source: workspace/task
    dispatch:
      method: POST
      path: /events/task-created
      timeout: 2s
      forwardHeaders:
        - X-Workspace-Actor-Id
        - X-Workspace-Tenant-Id
    response:
      type: com.workspace.task.created.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if cfg.App.ID != "task-service" {
		t.Fatalf("unexpected app id: %q", cfg.App.ID)
	}
	if cfg.Routes[0].Dispatch.Timeout.String() != "2s" {
		t.Fatalf("unexpected timeout: %s", cfg.Routes[0].Dispatch.Timeout)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080
unknown: true
`))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
}
```

- [ ] **Step 2: Implement schema parser**

Create `internal/config/schema.go`:

```go
package config

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App    AppConfig    `yaml:"app"`
	NATS   NATSConfig   `yaml:"nats"`
	Routes []RouteConfig `yaml:"routes"`
}

type AppConfig struct {
	ID          string `yaml:"id"`
	HTTPBaseURL string `yaml:"httpBaseURL"`
}

type NATSConfig struct {
	URL               string        `yaml:"url"`
	Stream            string        `yaml:"stream"`
	DurableConsumer   string        `yaml:"durableConsumer"`
	AckWait           time.Duration `yaml:"ackWait"`
	MaxDeliver        int           `yaml:"maxDeliver"`
	MaxAckPending     int           `yaml:"maxAckPending"`
	DefaultDLQSubject string        `yaml:"defaultDLQSubject"`
}

type RouteConfig struct {
	Name     string         `yaml:"name"`
	Match    MatchConfig    `yaml:"match"`
	Dispatch DispatchConfig `yaml:"dispatch"`
	Response ResponseConfig `yaml:"response"`
	Retry    RetryConfig    `yaml:"retry"`
	DLQ      DLQConfig      `yaml:"dlq"`
}

type MatchConfig struct {
	Subject string `yaml:"subject"`
	Type    string `yaml:"type"`
	Source  string `yaml:"source"`
}

type DispatchConfig struct {
	Method         string            `yaml:"method"`
	Path           string            `yaml:"path"`
	Timeout        time.Duration     `yaml:"timeout"`
	Headers        map[string]string `yaml:"headers"`
	ForwardHeaders []string          `yaml:"forwardHeaders"`
}

type ResponseConfig struct {
	Type       string `yaml:"type"`
	Source     string `yaml:"source"`
	Subject    string `yaml:"subject"`
	DataSchema string `yaml:"dataschema"`
}

type RetryConfig struct {
	MaxAttempts    int           `yaml:"maxAttempts"`
	InitialBackoff time.Duration `yaml:"initialBackoff"`
	MaxBackoff     time.Duration `yaml:"maxBackoff"`
}

type DLQConfig struct {
	Subject string `yaml:"subject"`
}

func Parse(b []byte) (*Config, error) {
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("config: yaml decode: %w", err)
	}
	return cfg, nil
}
```

- [ ] **Step 3: Write validation tests**

Create `internal/config/validate_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	return &Config{
		App: AppConfig{ID: "task-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{
			URL: "nats://nats:4222", Stream: "workspace-events", DurableConsumer: "task-service-dispatcher",
			AckWait: 30 * time.Second, MaxDeliver: 5, MaxAckPending: 1024, DefaultDLQSubject: "dlq.tenant-a.task-service",
		},
		Routes: []RouteConfig{{
			Name: "task-created",
			Match: MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
			Dispatch: DispatchConfig{Method: "POST", Path: "/events/task-created", Timeout: 2 * time.Second},
			Response: ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.tenant-a.app.task.event.processed"},
			Retry: RetryConfig{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second},
			DLQ: DLQConfig{Subject: "dlq.tenant-a.task-service"},
		}},
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	if errs := Validate(validConfig()); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRejectsExternalHTTPBaseURL(t *testing.T) {
	cfg := validConfig()
	cfg.App.HTTPBaseURL = "https://example.com"
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", errs[0])
	}
}

func TestValidateRejectsStaticHeaderOverride(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Headers = map[string]string{"ce-id": "bad"}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "reserved header") {
		t.Fatalf("expected reserved header error, got %v", errs[0])
	}
}

func TestValidateRejectsReservedForwardHeader(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.ForwardHeaders = []string{"Authorization"}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "reserved header") {
		t.Fatalf("expected reserved header error, got %v", errs[0])
	}
}
```

- [ ] **Step 4: Implement validation**

Create `internal/config/validate.go`:

```go
package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

type ValidationError struct {
	Path string
	Msg  string
}

func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

var reservedHeaders = map[string]bool{
	"ce-id": true, "ce-type": true, "ce-source": true, "ce-specversion": true,
	"ce-subject": true, "ce-time": true, "ce-datacontenttype": true, "ce-dataschema": true,
	"ce-correlationid": true, "ce-causationid": true, "idempotency-key": true,
	"traceparent": true, "authorization": true, "connection": true, "keep-alive": true,
	"proxy-authenticate": true, "proxy-authorization": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

func Validate(cfg *Config) []error {
	if cfg == nil {
		return []error{ValidationError{Msg: "config is nil"}}
	}
	var errs []error
	if cfg.App.ID == "" {
		errs = append(errs, ValidationError{Path: "app.id", Msg: "is required"})
	}
	if err := validateLoopbackBaseURL(cfg.App.HTTPBaseURL); err != nil {
		errs = append(errs, ValidationError{Path: "app.httpBaseURL", Msg: err.Error()})
	}
	if cfg.NATS.URL == "" {
		errs = append(errs, ValidationError{Path: "nats.url", Msg: "is required"})
	}
	if cfg.NATS.Stream == "" {
		errs = append(errs, ValidationError{Path: "nats.stream", Msg: "is required"})
	}
	if cfg.NATS.DurableConsumer == "" {
		errs = append(errs, ValidationError{Path: "nats.durableConsumer", Msg: "is required"})
	}
	if cfg.NATS.AckWait <= 0 {
		errs = append(errs, ValidationError{Path: "nats.ackWait", Msg: "must be positive"})
	}
	if cfg.NATS.MaxDeliver <= 0 {
		errs = append(errs, ValidationError{Path: "nats.maxDeliver", Msg: "must be positive"})
	}
	if cfg.NATS.MaxAckPending <= 0 {
		errs = append(errs, ValidationError{Path: "nats.maxAckPending", Msg: "must be positive"})
	}
	if cfg.NATS.DefaultDLQSubject == "" {
		errs = append(errs, ValidationError{Path: "nats.defaultDLQSubject", Msg: "is required"})
	}
	if len(cfg.Routes) == 0 {
		errs = append(errs, ValidationError{Path: "routes", Msg: "must contain at least one route"})
	}
	for i, r := range cfg.Routes {
		prefix := fmt.Sprintf("routes[%d]", i)
		errs = append(errs, validateRoute(prefix, r)...)
	}
	return errs
}

func validateRoute(prefix string, r RouteConfig) []error {
	var errs []error
	if r.Name == "" {
		errs = append(errs, ValidationError{Path: prefix + ".name", Msg: "is required"})
	}
	if r.Match.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".match.subject", Msg: "is required"})
	}
	if r.Match.Type == "" {
		errs = append(errs, ValidationError{Path: prefix + ".match.type", Msg: "is required"})
	}
	if r.Match.Source == "" {
		errs = append(errs, ValidationError{Path: prefix + ".match.source", Msg: "is required"})
	}
	if r.Dispatch.Method != "POST" && r.Dispatch.Method != "PUT" && r.Dispatch.Method != "PATCH" {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.method", Msg: "must be POST, PUT, or PATCH"})
	}
	if !strings.HasPrefix(r.Dispatch.Path, "/") {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.path", Msg: "must start with /"})
	}
	if r.Dispatch.Timeout <= 0 {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.timeout", Msg: "must be positive"})
	}
	for name := range r.Dispatch.Headers {
		if reservedHeaders[strings.ToLower(name)] {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.headers." + name, Msg: "reserved header cannot be overridden"})
		}
	}
	for _, name := range r.Dispatch.ForwardHeaders {
		if name == "" {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.forwardHeaders", Msg: "header names must be non-empty"})
			continue
		}
		if reservedHeaders[strings.ToLower(name)] {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.forwardHeaders." + name, Msg: "reserved header cannot be forwarded from publisher"})
		}
	}
	if r.Response.Type == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.type", Msg: "is required"})
	}
	if r.Response.Source == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.source", Msg: "is required"})
	}
	if r.Response.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.subject", Msg: "is required"})
	}
	if r.Retry.MaxAttempts <= 0 {
		errs = append(errs, ValidationError{Path: prefix + ".retry.maxAttempts", Msg: "must be positive"})
	}
	if r.Retry.InitialBackoff <= 0 || r.Retry.MaxBackoff <= 0 || r.Retry.InitialBackoff > r.Retry.MaxBackoff {
		errs = append(errs, ValidationError{Path: prefix + ".retry", Msg: "initialBackoff and maxBackoff must be positive and ordered"})
	}
	if r.DLQ.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".dlq.subject", Msg: "is required"})
	}
	return errs
}

func validateLoopbackBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must parse as URL: %w", err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("must use http scheme for local dispatch")
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must target a loopback host")
	}
	return nil
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/config/...
```

Expected: `ok event-adapter/internal/config`.

- [ ] **Step 6: Commit**

Run:

```bash
git add event-adapter/internal/config event-adapter/go.mod event-adapter/go.sum
git commit -m "feat(event-adapter): add route config schema"
```

---

## Task 3: CloudEvent Parsing and Validation

**Files:**
- Create: `event-adapter/internal/cloudevent/event.go`
- Create: `event-adapter/internal/cloudevent/event_test.go`

- [ ] **Step 1: Write CloudEvent validation tests**

Create `internal/cloudevent/event_test.go`:

```go
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
```

- [ ] **Step 2: Implement CloudEvent parsing**

Create `internal/cloudevent/event.go`:

```go
package cloudevent

import (
	"encoding/json"
	"fmt"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

func Parse(raw []byte) (*ce.Event, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("cloudevent: decode json: %w", err)
	}
	if _, ok := probe["data_base64"]; ok {
		return nil, fmt.Errorf("cloudevent: data_base64 is not supported in phase 1")
	}
	ev := ce.New()
	if err := json.Unmarshal(raw, &ev); err != nil {
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
	return &ev, nil
}

func JSONDataBytes(ev *ce.Event) ([]byte, error) {
	if ev == nil {
		return nil, fmt.Errorf("cloudevent: event is nil")
	}
	if ev.Data() == nil {
		return nil, fmt.Errorf("cloudevent: data is required")
	}
	return ev.Data(), nil
}
```

- [ ] **Step 3: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/cloudevent/...
```

Expected: `ok event-adapter/internal/cloudevent`.

- [ ] **Step 4: Commit**

Run:

```bash
git add event-adapter/internal/cloudevent event-adapter/go.mod event-adapter/go.sum
git commit -m "feat(event-adapter): parse cloudevent envelopes"
```

---

## Task 4: Route Matcher

**Files:**
- Create: `event-adapter/internal/router/matcher.go`
- Create: `event-adapter/internal/router/matcher_test.go`

- [ ] **Step 1: Write matcher tests**

Create `internal/router/matcher_test.go`:

```go
package router

import (
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
)

func TestMatchExactSubjectTypeSource(t *testing.T) {
	route := config.RouteConfig{
		Name: "task-created",
		Match: config.MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
	}
	m := New([]config.RouteConfig{route})
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	got, ok := m.Match("t.tenant-a.app.task.event.created", &ev)
	if !ok {
		t.Fatal("expected match")
	}
	if got.Name != "task-created" {
		t.Fatalf("unexpected route: %s", got.Name)
	}
}

func TestMatchRejectsWrongSource(t *testing.T) {
	route := config.RouteConfig{
		Name: "task-created",
		Match: config.MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
	}
	m := New([]config.RouteConfig{route})
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("other")
	ev.SetType("com.workspace.task.created")
	_, ok := m.Match("t.tenant-a.app.task.event.created", &ev)
	if ok {
		t.Fatal("expected no match")
	}
}
```

- [ ] **Step 2: Implement matcher**

Create `internal/router/matcher.go`:

```go
package router

import (
	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
)

type Matcher struct {
	routes []config.RouteConfig
}

func New(routes []config.RouteConfig) *Matcher {
	copied := append([]config.RouteConfig(nil), routes...)
	return &Matcher{routes: copied}
}

func (m *Matcher) Match(subject string, ev *ce.Event) (config.RouteConfig, bool) {
	if ev == nil {
		return config.RouteConfig{}, false
	}
	for _, r := range m.routes {
		if r.Match.Subject == subject && r.Match.Type == ev.Type() && r.Match.Source == ev.Source() {
			return r, true
		}
	}
	return config.RouteConfig{}, false
}
```

- [ ] **Step 3: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/router/...
```

Expected: `ok event-adapter/internal/router`.

- [ ] **Step 4: Commit**

Run:

```bash
git add event-adapter/internal/router
git commit -m "feat(event-adapter): add exact route matcher"
```

---

## Task 5: HTTP Dispatcher with CloudEvent Headers

**Files:**
- Create: `event-adapter/internal/dispatcher/dispatcher.go`
- Create: `event-adapter/internal/dispatcher/dispatcher_test.go`

- [ ] **Step 1: Write dispatcher test**

Create `internal/dispatcher/dispatcher_test.go`:

```go
package dispatcher

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
)

func TestDispatchForwardsDataAndHeaders(t *testing.T) {
	var gotBody string
	var gotID string
	var gotIdempotency string
	var gotActor string
	var gotTenant string
	var gotIgnored string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotID = r.Header.Get("ce-id")
		gotIdempotency = r.Header.Get("Idempotency-Key")
		gotActor = r.Header.Get("X-Workspace-Actor-Id")
		gotTenant = r.Header.Get("X-Workspace-Tenant-Id")
		gotIgnored = r.Header.Get("X-Not-Allowlisted")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	if err := ev.SetData("application/json", map[string]string{"taskId": "t1"}); err != nil {
		t.Fatal(err)
	}
	ev.SetExtension("dispatchheaders", map[string]any{
		"X-Workspace-Actor-Id": "user-1",
		"X-Workspace-Tenant-Id": "tenant-a",
		"X-Not-Allowlisted": "drop-me",
	})

	d := New(server.URL, http.DefaultClient)
	res, err := d.Dispatch(context.Background(), config.RouteConfig{
		Dispatch: config.DispatchConfig{
			Method: "POST", Path: "/", Timeout: time.Second,
			ForwardHeaders: []string{"X-Workspace-Actor-Id", "X-Workspace-Tenant-Id"},
		},
	}, &ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.StatusCode != 200 || string(res.Body) != `{"ok":true}` {
		t.Fatalf("unexpected response: %+v", res)
	}
	if gotBody != `{"taskId":"t1"}` {
		t.Fatalf("unexpected body: %s", gotBody)
	}
	if gotID != "evt-1" || gotIdempotency != "evt-1" {
		t.Fatalf("missing id headers: ce-id=%q idempotency=%q", gotID, gotIdempotency)
	}
	if gotActor != "user-1" || gotTenant != "tenant-a" {
		t.Fatalf("missing publisher headers: actor=%q tenant=%q", gotActor, gotTenant)
	}
	if gotIgnored != "" {
		t.Fatalf("non-allowlisted publisher header was forwarded: %q", gotIgnored)
	}
}
```

- [ ] **Step 2: Implement dispatcher**

Create `internal/dispatcher/dispatcher.go`:

```go
package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
	clevent "event-adapter/internal/cloudevent"
)

type Result struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

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

func (d *Dispatcher) Dispatch(ctx context.Context, route config.RouteConfig, ev *ce.Event) (Result, error) {
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
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: read response: %w", err)
	}
	return Result{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: respBody}, nil
}

func setCloudEventHeaders(req *http.Request, ev *ce.Event) {
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

func setPublisherHeaders(req *http.Request, route config.RouteConfig, ev *ce.Event) {
	raw, ok := ev.Extensions()["dispatchheaders"]
	if !ok {
		return
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return
	}
	allowed := map[string]string{}
	for _, name := range route.Dispatch.ForwardHeaders {
		allowed[strings.ToLower(name)] = name
	}
	for name, value := range values {
		canonical, ok := allowed[strings.ToLower(name)]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		req.Header.Set(canonical, text)
	}
}
```

- [ ] **Step 3: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/dispatcher/...
```

Expected: `ok event-adapter/internal/dispatcher`.

- [ ] **Step 4: Commit**

Run:

```bash
git add event-adapter/internal/dispatcher
git commit -m "feat(event-adapter): dispatch cloudevents to local http"
```

---

## Task 6: Deterministic Response CloudEvent Builder

**Files:**
- Create: `event-adapter/internal/cloudevent/response.go`
- Create: `event-adapter/internal/cloudevent/response_test.go`

- [ ] **Step 1: Write response builder test**

Create `internal/cloudevent/response_test.go`:

```go
package cloudevent

import (
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
)

func TestBuildResponseUsesDeterministicIDAndCausation(t *testing.T) {
	in := ce.New()
	in.SetID("evt-1")
	in.SetSource("workspace/task")
	in.SetType("com.workspace.task.created")
	_ = in.SetExtension("correlationid", "corr-1")

	route := config.RouteConfig{
		Name: "task-created",
		Response: config.ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.tenant-a.app.task.event.processed"},
	}
	a, err := BuildResponse(&in, route, "application/json", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("BuildResponse returned error: %v", err)
	}
	b, err := BuildResponse(&in, route, "application/json", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("BuildResponse returned error: %v", err)
	}
	if a.ID() != b.ID() {
		t.Fatalf("response id must be deterministic: %q != %q", a.ID(), b.ID())
	}
	if a.Type() != route.Response.Type || a.Source() != route.Response.Source {
		t.Fatalf("unexpected response metadata: type=%q source=%q", a.Type(), a.Source())
	}
	if got := a.Extensions()["causationid"]; got != "evt-1" {
		t.Fatalf("unexpected causationid: %v", got)
	}
	if got := a.Extensions()["correlationid"]; got != "corr-1" {
		t.Fatalf("unexpected correlationid: %v", got)
	}
}
```

- [ ] **Step 2: Implement response builder**

Create `internal/cloudevent/response.go`:

```go
package cloudevent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
)

func BuildResponse(in *ce.Event, route config.RouteConfig, contentType string, body []byte) (*ce.Event, error) {
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
	if err := out.SetData(contentType, body); err != nil {
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
```

- [ ] **Step 3: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/cloudevent/...
```

Expected: `ok event-adapter/internal/cloudevent`.

- [ ] **Step 4: Commit**

Run:

```bash
git add event-adapter/internal/cloudevent
git commit -m "feat(event-adapter): build deterministic response events"
```

---

## Task 7: Processor Orchestration for Retry, Response Publish, DLQ, and Ack

**Files:**
- Create: `event-adapter/internal/processor/retry.go`
- Create: `event-adapter/internal/processor/retry_test.go`
- Create: `event-adapter/internal/processor/processor.go`
- Create: `event-adapter/internal/processor/processor_test.go`

- [ ] **Step 1: Define processor interfaces and retry helper tests**

Create `internal/processor/retry_test.go`:

```go
package processor

import (
	"testing"
	"time"
)

func TestBackoffCapsAtMax(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 4, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 250 * time.Millisecond}
	got := p.Delay(4)
	if got != 250*time.Millisecond {
		t.Fatalf("expected max backoff, got %s", got)
	}
}
```

- [ ] **Step 2: Implement retry helper**

Create `internal/processor/retry.go`:

```go
package processor

import "time"

type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func (p RetryPolicy) Delay(attempt int) time.Duration {
	if attempt <= 1 {
		return p.InitialBackoff
	}
	delay := p.InitialBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= p.MaxBackoff {
			return p.MaxBackoff
		}
	}
	return delay
}
```

- [ ] **Step 3: Write processor ack-order tests**

Create `internal/processor/processor_test.go`:

```go
package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
)

type fakeDispatcher struct {
	result dispatcher.Result
	err    error
}

func (f fakeDispatcher) Dispatch(context.Context, config.RouteConfig, *ce.Event) (dispatcher.Result, error) {
	return f.result, f.err
}

type fakePublisher struct {
	responseErr error
	dlqErr      error
	responses   int
	dlqs        int
}

func (f *fakePublisher) PublishResponse(context.Context, string, *ce.Event) error {
	f.responses++
	return f.responseErr
}

func (f *fakePublisher) PublishDLQ(context.Context, string, DLQEvent) error {
	f.dlqs++
	return f.dlqErr
}

type fakeAck struct {
	acked bool
}

func (f *fakeAck) Ack(context.Context) error {
	f.acked = true
	return nil
}

func TestProcessorAcksAfterResponsePublish(t *testing.T) {
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	_ = ev.SetData("application/json", map[string]string{"taskId": "t1"})
	pub := &fakePublisher{}
	ack := &fakeAck{}
	p := New(fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}, pub)
	route := config.RouteConfig{
		Name: "task-created",
		Response: config.ResponseConfig{Type: "processed", Source: "task-service", Subject: "processed.subject"},
		Retry: config.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ: config.DLQConfig{Subject: "dlq.subject"},
	}
	if err := p.Process(context.Background(), "input.subject", &ev, route, ack); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if !ack.acked || pub.responses != 1 || pub.dlqs != 0 {
		t.Fatalf("unexpected state ack=%v responses=%d dlqs=%d", ack.acked, pub.responses, pub.dlqs)
	}
}

func TestProcessorDoesNotAckWhenDLQPublishFails(t *testing.T) {
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	_ = ev.SetData("application/json", map[string]string{"taskId": "t1"})
	pub := &fakePublisher{dlqErr: errors.New("nats down")}
	ack := &fakeAck{}
	p := New(fakeDispatcher{err: errors.New("backend down")}, pub)
	route := config.RouteConfig{
		Name: "task-created",
		Response: config.ResponseConfig{Type: "processed", Source: "task-service", Subject: "processed.subject"},
		Retry: config.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ: config.DLQConfig{Subject: "dlq.subject"},
	}
	if err := p.Process(context.Background(), "input.subject", &ev, route, ack); err == nil {
		t.Fatal("expected process error")
	}
	if ack.acked {
		t.Fatal("must not ack when DLQ publish fails")
	}
}
```

- [ ] **Step 4: Implement processor**

Create `internal/processor/processor.go`:

```go
package processor

import (
	"context"
	"fmt"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
)

type Dispatcher interface {
	Dispatch(context.Context, config.RouteConfig, *ce.Event) (dispatcher.Result, error)
}

type Publisher interface {
	PublishResponse(context.Context, string, *ce.Event) error
	PublishDLQ(context.Context, string, DLQEvent) error
}

type Acker interface {
	Ack(context.Context) error
}

type Processor struct {
	dispatcher Dispatcher
	publisher  Publisher
}

type DLQEvent struct {
	OriginalEvent *ce.Event
	FailureReason string
	HTTPStatus    int
	AttemptCount  int
	SidecarAppID  string
	Timestamp     time.Time
}

func New(d Dispatcher, p Publisher) *Processor {
	return &Processor{dispatcher: d, publisher: p}
}

func (p *Processor) Process(ctx context.Context, subject string, ev *ce.Event, route config.RouteConfig, ack Acker) error {
	policy := RetryPolicy{MaxAttempts: route.Retry.MaxAttempts, InitialBackoff: route.Retry.InitialBackoff, MaxBackoff: route.Retry.MaxBackoff}
	var lastErr error
	var lastStatus int
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		res, err := p.dispatcher.Dispatch(ctx, route, ev)
		lastStatus = res.StatusCode
		if err == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			resp, err := clevent.BuildResponse(ev, route, res.ContentType, res.Body)
			if err != nil {
				lastErr = err
			} else if err := p.publisher.PublishResponse(ctx, route.Response.Subject, resp); err != nil {
				lastErr = err
			} else {
				return ack.Ack(ctx)
			}
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("non-success status %d", res.StatusCode)
		}
		if attempt < policy.MaxAttempts {
			timer := time.NewTimer(policy.Delay(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	dlq := DLQEvent{
		OriginalEvent: ev,
		FailureReason: lastErr.Error(),
		HTTPStatus: lastStatus,
		AttemptCount: policy.MaxAttempts,
		Timestamp: time.Now().UTC(),
	}
	if err := p.publisher.PublishDLQ(ctx, route.DLQ.Subject, dlq); err != nil {
		return fmt.Errorf("publish dlq: %w", err)
	}
	return ack.Ack(ctx)
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/processor/...
```

Expected: `ok event-adapter/internal/processor`.

- [ ] **Step 6: Commit**

Run:

```bash
git add event-adapter/internal/processor
git commit -m "feat(event-adapter): orchestrate retry publish and ack"
```

---

## Task 8: JetStream Client Adapter

**Files:**
- Create: `event-adapter/internal/natsjs/client.go`
- Create: `event-adapter/internal/natsjs/client_test.go`

- [ ] **Step 1: Write unit tests for DLQ envelope**

Create `internal/natsjs/client_test.go`:

```go
package natsjs

import (
	"encoding/json"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/processor"
)

func TestBuildDLQPayloadIncludesFailureMetadata(t *testing.T) {
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	dlq := processor.DLQEvent{
		OriginalEvent: &ev,
		FailureReason: "backend down",
		HTTPStatus: 503,
		AttemptCount: 3,
		SidecarAppID: "task-service",
		Timestamp: time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
	}
	body, err := BuildDLQPayload(dlq)
	if err != nil {
		t.Fatalf("BuildDLQPayload returned error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["failureReason"] != "backend down" {
		t.Fatalf("missing failure reason: %v", got)
	}
	if got["attemptCount"].(float64) != 3 {
		t.Fatalf("missing attempt count: %v", got)
	}
}
```

- [ ] **Step 2: Implement JetStream adapter**

Create `internal/natsjs/client.go`:

```go
package natsjs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
	ce "github.com/cloudevents/sdk-go/v2/event"
	"event-adapter/internal/config"
	"event-adapter/internal/processor"
)

type Client struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

type Message struct {
	Subject string
	Data    []byte
	msg     *nats.Msg
}

func Connect(cfg config.NATSConfig) (*Client, error) {
	nc, err := nats.Connect(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("nats: connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}
	return &Client{nc: nc, js: js}, nil
}

func (c *Client) Close() {
	if c.nc != nil {
		c.nc.Drain()
		c.nc.Close()
	}
}

func (c *Client) PublishResponse(ctx context.Context, subject string, ev *ce.Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("nats: marshal response: %w", err)
	}
	_, err = c.js.PublishMsg(&nats.Msg{Subject: subject, Data: body}, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("nats: publish response: %w", err)
	}
	return nil
}

func (c *Client) PublishDLQ(ctx context.Context, subject string, dlq processor.DLQEvent) error {
	body, err := BuildDLQPayload(dlq)
	if err != nil {
		return err
	}
	_, err = c.js.PublishMsg(&nats.Msg{Subject: subject, Data: body}, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("nats: publish dlq: %w", err)
	}
	return nil
}

func (m Message) Ack(ctx context.Context) error {
	if m.msg == nil {
		return fmt.Errorf("nats: message is nil")
	}
	return m.msg.Ack(nats.Context(ctx))
}

func BuildDLQPayload(dlq processor.DLQEvent) ([]byte, error) {
	payload := map[string]any{
		"originalEvent": dlq.OriginalEvent,
		"failureReason": dlq.FailureReason,
		"lastHTTPStatus": dlq.HTTPStatus,
		"attemptCount": dlq.AttemptCount,
		"sidecarAppID": dlq.SidecarAppID,
		"timestamp": dlq.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("nats: marshal dlq: %w", err)
	}
	return body, nil
}
```

- [ ] **Step 3: Add pull subscription in a follow-up change inside `client.go`**

Append this method to `internal/natsjs/client.go`:

```go
func (c *Client) PullSubscribe(subject string, durable string) (*nats.Subscription, error) {
	sub, err := c.js.PullSubscribe(subject, durable)
	if err != nil {
		return nil, fmt.Errorf("nats: pull subscribe: %w", err)
	}
	return sub, nil
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/natsjs/...
```

Expected: `ok event-adapter/internal/natsjs`.

- [ ] **Step 5: Commit**

Run:

```bash
git add event-adapter/internal/natsjs event-adapter/go.mod event-adapter/go.sum
git commit -m "feat(event-adapter): add jetstream adapter"
```

---

## Task 9: Metrics Package

**Files:**
- Create: `event-adapter/internal/metrics/metrics.go`
- Create: `event-adapter/internal/metrics/metrics_test.go`

- [ ] **Step 1: Write smoke test**

Create `internal/metrics/metrics_test.go`:

```go
package metrics

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
)

func TestMetricsMethodsDoNotPanic(t *testing.T) {
	mp := metric.NewMeterProvider()
	m := New(mp.Meter("test"))
	ctx := context.Background()
	m.EventConsumed(ctx, "task-created")
	m.Dispatched(ctx, "task-created", 200)
	m.DispatchLatency(ctx, "task-created", 10*time.Millisecond)
	m.RetryAttempt(ctx, "task-created")
	m.DLQPublished(ctx, "task-created")
	m.ResponsePublished(ctx, "task-created")
	m.NATSPublishFailure(ctx, "response")
	m.NATSAckFailure(ctx)
	m.JetStreamRedelivery(ctx, "task-created")
	m.DuplicateEventID(ctx, "task-created")
	m.RouteMatchFailure(ctx)
	m.InvalidCloudEvent(ctx, "missing_id")
}
```

- [ ] **Step 2: Implement metrics**

Create `internal/metrics/metrics.go`:

```go
package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	eventsConsumed       metric.Int64Counter
	dispatched           metric.Int64Counter
	dispatchLatency      metric.Float64Histogram
	retryAttempts        metric.Int64Counter
	dlqPublishes         metric.Int64Counter
	responsePublishes    metric.Int64Counter
	natsPublishFailures  metric.Int64Counter
	natsAckFailures      metric.Int64Counter
	redeliveries         metric.Int64Counter
	duplicateEventIDs    metric.Int64Counter
	routeMatchFailures   metric.Int64Counter
	invalidCloudEvents   metric.Int64Counter
}

func New(meter metric.Meter) *Metrics {
	mustC := func(name string) metric.Int64Counter {
		c, err := meter.Int64Counter(name)
		if err != nil {
			panic(err)
		}
		return c
	}
	h, err := meter.Float64Histogram("cts.dispatch.latency", metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}
	return &Metrics{
		eventsConsumed: mustC("cts.events.consumed"),
		dispatched: mustC("cts.events.dispatched"),
		dispatchLatency: h,
		retryAttempts: mustC("cts.retry.attempts"),
		dlqPublishes: mustC("cts.dlq.publishes"),
		responsePublishes: mustC("cts.response.publishes"),
		natsPublishFailures: mustC("cts.nats.publish_failures"),
		natsAckFailures: mustC("cts.nats.ack_failures"),
		redeliveries: mustC("cts.jetstream.redeliveries"),
		duplicateEventIDs: mustC("cts.duplicate_event_ids"),
		routeMatchFailures: mustC("cts.route_match_failures"),
		invalidCloudEvents: mustC("cts.invalid_cloudevents"),
	}
}

func (m *Metrics) EventConsumed(ctx context.Context, route string) {
	m.eventsConsumed.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) Dispatched(ctx context.Context, route string, status int) {
	m.dispatched.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route), attribute.Int("status", status)))
}

func (m *Metrics) DispatchLatency(ctx context.Context, route string, d time.Duration) {
	m.dispatchLatency.Record(ctx, float64(d.Microseconds())/1000, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RetryAttempt(ctx context.Context, route string) { m.retryAttempts.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route))) }
func (m *Metrics) DLQPublished(ctx context.Context, route string) { m.dlqPublishes.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route))) }
func (m *Metrics) ResponsePublished(ctx context.Context, route string) { m.responsePublishes.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route))) }
func (m *Metrics) NATSPublishFailure(ctx context.Context, kind string) { m.natsPublishFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind))) }
func (m *Metrics) NATSAckFailure(ctx context.Context) { m.natsAckFailures.Add(ctx, 1) }
func (m *Metrics) JetStreamRedelivery(ctx context.Context, route string) { m.redeliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route))) }
func (m *Metrics) DuplicateEventID(ctx context.Context, route string) { m.duplicateEventIDs.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route))) }
func (m *Metrics) RouteMatchFailure(ctx context.Context) { m.routeMatchFailures.Add(ctx, 1) }
func (m *Metrics) InvalidCloudEvent(ctx context.Context, reason string) { m.invalidCloudEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason))) }
```

- [ ] **Step 3: Run tests**

Run:

```bash
cd event-adapter && go test ./internal/metrics/...
```

Expected: `ok event-adapter/internal/metrics`.

- [ ] **Step 4: Commit**

Run:

```bash
git add event-adapter/internal/metrics event-adapter/go.mod event-adapter/go.sum
git commit -m "feat(event-adapter): add event dispatch metrics"
```

---

## Task 10: Wire Main Runtime Loop

**Files:**
- Modify: `event-adapter/cmd/event-adapter/main.go`
- Modify: `event-adapter/cmd/event-adapter/main_test.go`

- [ ] **Step 1: Add config loading test**

Append to `cmd/event-adapter/main_test.go`:

```go
func TestRunRejectsMissingConfigFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", "/no/such/file.yaml"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "read config") {
		t.Fatalf("stderr missing read config error: %q", stderr.String())
	}
}
```

- [ ] **Step 2: Update `main.go` imports**

Add these imports to `cmd/event-adapter/main.go`:

```go
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/natsjs"
	"event-adapter/internal/processor"
	"event-adapter/internal/router"
```

- [ ] **Step 3: Replace the post-flag body in `run`**

Replace the final `fmt.Fprintf(stdout, "event-adapter config=%s\n", opts.configPath)` block with:

```go
	raw, err := os.ReadFile(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "read config: %v\n", err)
		return 1
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		fmt.Fprintf(stderr, "parse config: %v\n", err)
		return 1
	}
	if errs := config.Validate(cfg); len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(stderr, "validate config: %v\n", err)
		}
		return 1
	}
	js, err := natsjs.Connect(cfg.NATS)
	if err != nil {
		fmt.Fprintf(stderr, "connect nats: %v\n", err)
		return 1
	}
	defer js.Close()
	_ = metric.NewMeterProvider()
	_ = router.New(cfg.Routes)
	_ = dispatcher.New(cfg.App.HTTPBaseURL, nil)
	_ = processor.New(nil, js)
	fmt.Fprintf(stdout, "event-adapter loaded %d route(s)\n", len(cfg.Routes))
	<-ctx.Done()
	return 0
```

This step intentionally stops before fetching messages. Task 11 adds the pull loop after e2e infrastructure exists.

- [ ] **Step 4: Run command tests**

Run:

```bash
cd event-adapter && go test ./cmd/event-adapter/...
```

Expected: missing config test passes, and tests that require a live NATS server are not introduced.

- [ ] **Step 5: Commit**

Run:

```bash
git add event-adapter/cmd/event-adapter
git commit -m "feat(event-adapter): load sidecar config at startup"
```

---

## Task 11: End-to-End Harness and Message Loop

**Files:**
- Create: `event-adapter/test/e2e/docker-compose.yaml`
- Create: `event-adapter/test/e2e/routes.yaml`
- Create: `event-adapter/test/e2e/e2e_test.go`
- Modify: `event-adapter/cmd/event-adapter/main.go`
- Modify: `event-adapter/internal/natsjs/client.go`

- [ ] **Step 1: Create e2e Docker Compose**

Create `test/e2e/docker-compose.yaml`:

```yaml
services:
  nats:
    image: nats:2.11
    command: ["-js", "-sd", "/data"]
    ports:
      - "4222:4222"
```

- [ ] **Step 2: Create e2e routes**

Create `test/e2e/routes.yaml`:

```yaml
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:18080
nats:
  url: nats://127.0.0.1:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      subject: t.tenant-a.app.task.event.created
      type: com.workspace.task.created
      source: workspace/task
    dispatch:
      method: POST
      path: /events/task-created
      timeout: 2s
      forwardHeaders:
        - X-Workspace-Actor-Id
        - X-Workspace-Tenant-Id
    response:
      type: com.workspace.task.created.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
```

- [ ] **Step 3: Implement fetch wrapper**

Append to `internal/natsjs/client.go`:

```go
func FetchOne(ctx context.Context, sub *nats.Subscription) (Message, error) {
	msgs, err := sub.Fetch(1, nats.Context(ctx))
	if err != nil {
		return Message{}, err
	}
	if len(msgs) == 0 {
		return Message{}, fmt.Errorf("nats: no messages fetched")
	}
	return Message{Subject: msgs[0].Subject, Data: msgs[0].Data, msg: msgs[0]}, nil
}
```

- [ ] **Step 4: Add runtime loop in `main.go`**

After constructing router, dispatcher, and processor, replace the placeholder wait with a pull loop:

```go
matcher := router.New(cfg.Routes)
httpDispatcher := dispatcher.New(cfg.App.HTTPBaseURL, nil)
proc := processor.New(httpDispatcher, js)
subs := make([]*nats.Subscription, 0, len(cfg.Routes))
for _, route := range cfg.Routes {
	sub, err := js.PullSubscribe(route.Match.Subject, cfg.NATS.DurableConsumer+"-"+route.Name)
	if err != nil {
		fmt.Fprintf(stderr, "subscribe %s: %v\n", route.Match.Subject, err)
		return 1
	}
	subs = append(subs, sub)
}
fmt.Fprintf(stdout, "event-adapter processing %d route(s)\n", len(subs))
for ctx.Err() == nil {
	for _, sub := range subs {
		msg, err := natsjs.FetchOne(ctx, sub)
		if err != nil {
			continue
		}
		ev, err := cloudevent.Parse(msg.Data)
		if err != nil {
			_ = js.PublishDLQ(ctx, cfg.NATS.DefaultDLQSubject, processor.DLQEvent{FailureReason: err.Error(), Timestamp: time.Now().UTC()})
			continue
		}
		route, ok := matcher.Match(msg.Subject, ev)
		if !ok {
			_ = js.PublishDLQ(ctx, cfg.NATS.DefaultDLQSubject, processor.DLQEvent{OriginalEvent: ev, FailureReason: "no matching route", Timestamp: time.Now().UTC()})
			continue
		}
		_ = proc.Process(ctx, msg.Subject, ev, route, msg)
	}
}
return 0
```

Add imports for `github.com/nats-io/nats.go`, `event-adapter/internal/cloudevent`, and `time`.

- [ ] **Step 5: Write e2e test**

Create `test/e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestEventDispatchPublishesResponse(t *testing.T) {
	app := httptestApp(t)
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "workspace-events",
		Subjects: []string{
			"t.tenant-a.app.task.event.created",
			"t.tenant-a.app.task.event.processed",
			"dlq.tenant-a.task-service",
		},
	}); err != nil && err != nats.ErrStreamNameAlreadyInUse {
		t.Fatalf("add stream: %v", err)
	}
	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed")
	if err != nil {
		t.Fatalf("subscribe response: %v", err)
	}
	cfgPath := writeE2EConfig(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/event-adapter", "--config", cfgPath)
	cmd.Dir = "../.."
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	waitForLine(t, output, "processing 1 route")
	input := []byte(`{"specversion":"1.0","id":"evt-e2e-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchheaders":{"X-Workspace-Actor-Id":"user-1","X-Workspace-Tenant-Id":"tenant-a"},"data":{"taskId":"task-1"}}`)
	if _, err := js.Publish("t.tenant-a.app.task.event.created", input); err != nil {
		t.Fatalf("publish input: %v", err)
	}
	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("waiting for response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["type"] != "com.workspace.task.created.processed" {
		t.Fatalf("unexpected response type: %v", response["type"])
	}
	extensions, ok := response["extensions"].(map[string]any)
	if !ok || extensions["causationid"] != "evt-e2e-1" {
		t.Fatalf("missing causation extension: %#v", response["extensions"])
	}
}

func httptestApp(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Fatalf("listen fake app: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/events/task-created", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return "http://127.0.0.1:18080"
}

func writeE2EConfig(t *testing.T, appURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.yaml")
	content := fmt.Sprintf(`app:
  id: task-service
  httpBaseURL: %s
nats:
  url: nats://127.0.0.1:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      subject: t.tenant-a.app.task.event.created
      type: com.workspace.task.created
      source: workspace/task
    dispatch:
      method: POST
      path: /events/task-created
      timeout: 2s
    response:
      type: com.workspace.task.created.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
`, appURL)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func waitForLine(t *testing.T, r io.Reader, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	buf := make([]byte, 4096)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for sidecar output containing %q", want)
		default:
			n, err := r.Read(buf)
			if err != nil && n == 0 {
				t.Fatalf("read sidecar output: %v", err)
			}
			if strings.Contains(string(buf[:n]), want) {
				return
			}
		}
	}
}
```

- [ ] **Step 6: Run focused tests**

Run:

```bash
cd event-adapter && go test ./...
```

Expected: unit tests pass; e2e test is excluded unless `-tags=e2e` is supplied.

- [ ] **Step 7: Commit**

Run:

```bash
git add event-adapter/test/e2e event-adapter/cmd/event-adapter event-adapter/internal/natsjs
git commit -m "feat(event-adapter): add jetstream processing loop"
```

---

## Task 12: Onboarding Example and Verification

**Files:**
- Create: `event-adapter/examples/onboarding/app.go`
- Create: `event-adapter/examples/onboarding/routes.yaml`
- Create: `event-adapter/examples/onboarding/publish.sh`
- Create: `event-adapter/examples/onboarding/README.md`

- [ ] **Step 1: Add fake app**

Create `examples/onboarding/app.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
)

func main() {
	http.HandleFunc("/events/task-created", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"processed": true,
			"eventId": r.Header.Get("ce-id"),
			"idempotencyKey": r.Header.Get("Idempotency-Key"),
		})
	})
	_ = http.ListenAndServe("127.0.0.1:8080", nil)
}
```

- [ ] **Step 2: Add sample route config**

Create `examples/onboarding/routes.yaml` with the same content as `test/e2e/routes.yaml`.

- [ ] **Step 3: Add publish script**

Create `examples/onboarding/publish.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

nats --server nats://127.0.0.1:4222 pub t.tenant-a.app.task.event.created '{
  "specversion": "1.0",
  "id": "evt-example-1",
  "source": "workspace/task",
  "type": "com.workspace.task.created",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1",
    "X-Workspace-Tenant-Id": "tenant-a"
  },
  "data": {"taskId": "task-1"}
}'
```

- [ ] **Step 4: Add README**

Create `examples/onboarding/README.md`:

```markdown
# event-adapter onboarding example

This example runs a local app handler and an event-adapter sidecar.

1. Start NATS JetStream.
2. Start the fake app:
   `go run ./examples/onboarding/app.go`
3. Start the sidecar:
   `go run ./cmd/event-adapter --config ./examples/onboarding/routes.yaml`
4. Publish an event:
   `./examples/onboarding/publish.sh`

The sidecar forwards CloudEvent `data` to `/events/task-created`, publishes a response CloudEvent to `t.tenant-a.app.task.event.processed`, and acknowledges the original message only after response publish confirmation.

Publisher-supplied backend HTTP headers must be sent in the CloudEvent `dispatchheaders` extension and listed in the route's `dispatch.forwardHeaders` allowlist before the sidecar forwards them to the app handler.
```

- [ ] **Step 5: Run verification commands**

Run:

```bash
cd event-adapter && go test ./...
cd event-adapter && go vet ./...
cd event-adapter && test -z "$(gofmt -l .)"
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit**

Run:

```bash
git add event-adapter/examples/onboarding
git commit -m "docs(event-adapter): add onboarding example"
```

---

## Final Verification

- [ ] Run all unit tests:

```bash
cd event-adapter && go test ./...
```

Expected: all packages pass.

- [ ] Run vet:

```bash
cd event-adapter && go vet ./...
```

Expected: no output and exit 0.

- [ ] Run gofmt check:

```bash
cd event-adapter && test -z "$(gofmt -l .)"
```

Expected: exit 0.

- [ ] Run e2e when Docker is available:

```bash
cd event-adapter/test/e2e && docker compose up --build -d
cd ../.. && go test -tags=e2e ./test/e2e/... -v
cd test/e2e && docker compose down -v
```

Expected: e2e proves that a valid CloudEvent produces one response CloudEvent and that a backend failure publishes one DLQ event without acknowledging before DLQ confirmation.

---

## Self-Review Notes

- PRD sections 4-5 are covered by Tasks 7, 8, 10, and 11.
- Route configuration in PRD section 6 is covered by Task 2.
- HTTP dispatch contract in PRD section 7 is covered by Task 5.
- Response event contract in PRD section 8 is covered by Task 6.
- Failure behavior and delivery semantics in PRD sections 9-10 are covered by Tasks 7, 8, and 11.
- Observability in PRD section 12 is covered by Task 9. During Task 11, record `EventConsumed` after each successful fetch, `InvalidCloudEvent` on parse failures, `RouteMatchFailure` on no match, `ResponsePublished` after successful response publish, `DLQPublished` after successful DLQ publish, and `NATSAckFailure` when `Ack` returns an error.
- Onboarding rollout material in PRD section 14 is covered by Task 12.
