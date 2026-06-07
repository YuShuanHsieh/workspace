package config

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App      AppConfig       `yaml:"app"`
	NATS     NATSConfig      `yaml:"nats"`
	Routes   []RouteConfig   `yaml:"routes"`
	Requests *RequestsConfig `yaml:"requests"`
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

type RequestsConfig struct {
	Subject        string               `yaml:"subject"`
	QueueGroup     string               `yaml:"queueGroup"`
	WorkerPoolSize int                  `yaml:"workerPoolSize"`
	Routes         []RequestRouteConfig `yaml:"routes"`
}

type RequestRouteConfig struct {
	Name     string         `yaml:"name"`
	Match    MatchConfig    `yaml:"match"`
	Dispatch DispatchConfig `yaml:"dispatch"`
	Reply    ReplyConfig    `yaml:"reply"`
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
