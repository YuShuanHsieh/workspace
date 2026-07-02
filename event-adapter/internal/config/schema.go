package config

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App           AppConfig           `yaml:"app"`
	NATS          NATSConfig          `yaml:"nats"`
	Routes        []RouteConfig       `yaml:"routes"`
	Requests      *RequestsConfig     `yaml:"requests"`
	Observability ObservabilityConfig `yaml:"observability"`
}

// ObservabilityConfig configures the o11y SDK, the health-check HTTP server, and
// the backpressure threshold. Every field is optional; WithDefaults fills in the
// values used when a field is left empty so existing route configs keep working
// without an observability block.
type ObservabilityConfig struct {
	ServiceName           string `yaml:"serviceName"`
	ServiceVersion        string `yaml:"serviceVersion"`
	Environment           string `yaml:"environment"`
	ServiceNamespace      string `yaml:"serviceNamespace"`
	HealthAddr            string `yaml:"healthAddr"`
	MetricsAddr           string `yaml:"metricsAddr"`
	MetricsOTLPEndpoint   string `yaml:"metricsOTLPEndpoint"` // when set, metrics push via OTLP instead of Prometheus pull
	BackpressureThreshold int    `yaml:"backpressureThreshold"`
}

// WithDefaults returns a copy of o with empty fields replaced by their defaults.
func (o ObservabilityConfig) WithDefaults() ObservabilityConfig {
	if o.ServiceName == "" {
		o.ServiceName = "event-adapter"
	}
	if o.ServiceVersion == "" {
		o.ServiceVersion = "0.1.0"
	}
	// Environment is intentionally NOT defaulted: it is deployment-distinguishing
	// and required via Validate, so an unset value fails fast rather than
	// silently mislabeling production telemetry as "testing".
	if o.ServiceNamespace == "" {
		o.ServiceNamespace = "workspace"
	}
	if o.HealthAddr == "" {
		o.HealthAddr = ":8080"
	}
	if o.MetricsAddr == "" {
		o.MetricsAddr = ":8200"
	}
	if o.BackpressureThreshold == 0 {
		o.BackpressureThreshold = 1000
	}
	return o
}

type AppConfig struct {
	ID          string `yaml:"id"`
	HTTPBaseURL string `yaml:"httpBaseURL"`
}

type NATSConfig struct {
	URL               string        `yaml:"url"`
	Stream            string        `yaml:"stream"`
	DurableConsumer   string        `yaml:"durableConsumer"`
	FilterSubject     string        `yaml:"filterSubject"`
	WorkerPoolSize    int           `yaml:"workerPoolSize"`
	FetchBatch        int           `yaml:"fetchBatch"`
	AckWait           time.Duration `yaml:"ackWait"`
	MaxDeliver        int           `yaml:"maxDeliver"`
	MaxAckPending     int           `yaml:"maxAckPending"`
	DefaultDLQSubject string        `yaml:"defaultDLQSubject"`
	CredsFilePath     string        `yaml:"credsFilePath,omitempty"`
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

type RequestMatchConfig struct {
	Type string `yaml:"type"`
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

type RequestsConfig struct {
	Subject        string               `yaml:"subject"`
	QueueGroup     string               `yaml:"queueGroup"`
	WorkerPoolSize int                  `yaml:"workerPoolSize"`
	Routes         []RequestRouteConfig `yaml:"routes"`
}

type RequestRouteConfig struct {
	Name     string             `yaml:"name"`
	Match    RequestMatchConfig `yaml:"match"`
	Dispatch DispatchConfig     `yaml:"dispatch"`
	Reply    ReplyConfig        `yaml:"reply"`
}

type ReplyConfig struct {
	Source     string `yaml:"source"`
	Type       string `yaml:"type"`
	DataSchema string `yaml:"dataschema"`
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
