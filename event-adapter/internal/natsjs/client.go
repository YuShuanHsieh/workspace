package natsjs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/nats-io/nats.go"

	"event-adapter/internal/config"
	"event-adapter/internal/processor"
)

type Client struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

type Message struct {
	Subject      string
	Data         []byte
	NumDelivered uint64
	msg          *nats.Msg
}

// buildOpts builds the nats.Option slice used by Connect based on the supplied
// configuration. It is extracted from Connect so it can be unit-tested without
// requiring a real NATS server.
func buildOpts(cfg config.NATSConfig, extra ...nats.Option) []nats.Option {
	var opts []nats.Option
	// Add credentials file auth if configured.
	if cfg.CredsFilePath != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFilePath))
	}
	// Caller-supplied options (e.g. dynamic UserJWT auth, reconnect tuning).
	opts = append(opts, extra...)
	return opts
}

func Connect(cfg config.NATSConfig, extra ...nats.Option) (*Client, error) {
	nc, err := nats.Connect(cfg.URL, buildOpts(cfg, extra...)...)
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
		_ = c.nc.Drain()
		c.nc.Close()
	}
}

// IsConnected reports whether the underlying NATS connection is currently
// established. Used by the /ready health check.
func (c *Client) IsConnected() bool {
	return c.nc != nil && c.nc.IsConnected()
}

// ForceReconnect triggers a reconnect to the server. The dynamic-credentials
// refresh loop calls this after minting a fresh JWT so NATS re-invokes the
// UserJWT callback and picks up the new credentials.
func (c *Client) ForceReconnect() error {
	if c.nc == nil {
		return fmt.Errorf("nats: no connection")
	}
	return c.nc.ForceReconnect()
}

// ConsumerPending returns the number of messages the JetStream consumer has not
// yet delivered (NumPending). Used for the backpressure decision. (The
// pending-backlog metric is exported from NATS/JetStream directly, not here.)
func (c *Client) ConsumerPending(stream, durable string) (int64, error) {
	if c.js == nil {
		return 0, fmt.Errorf("nats: jetstream context is nil")
	}
	ci, err := c.js.ConsumerInfo(stream, durable)
	if err != nil {
		return 0, fmt.Errorf("nats: consumer info %s/%s: %w", stream, durable, err)
	}
	return int64(ci.NumPending), nil // #nosec G115 -- NATS pending count, never exceeds int64
}

func (c *Client) PublishResponse(ctx context.Context, subject string, ev *ce.Event) error {
	if ev == nil {
		return fmt.Errorf("nats: publish response: nil event")
	}
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

func (c *Client) SubscribeWildcard(cfg config.NATSConfig) (*nats.Subscription, error) {
	sub, err := c.js.PullSubscribe(
		cfg.FilterSubject,
		cfg.DurableConsumer,
		nats.BindStream(cfg.Stream),
		nats.AckExplicit(),
		nats.ManualAck(),
		nats.AckWait(cfg.AckWait),
		nats.MaxAckPending(cfg.MaxAckPending),
		nats.MaxDeliver(cfg.MaxDeliver),
	)
	if err != nil {
		return nil, fmt.Errorf("nats: wildcard subscribe: %w", err)
	}
	return sub, nil
}

func (m Message) Ack(ctx context.Context) error {
	if m.msg == nil {
		return fmt.Errorf("nats: message is nil")
	}
	return m.msg.Ack(nats.Context(ctx))
}

func (m Message) Nak(ctx context.Context, delay time.Duration) error {
	if m.msg == nil {
		return fmt.Errorf("nats: message is nil")
	}
	return m.msg.NakWithDelay(delay, nats.Context(ctx))
}

func (m Message) Deliveries() uint64 {
	return m.NumDelivered
}

func FetchBatch(ctx context.Context, sub *nats.Subscription, batch int) ([]Message, error) {
	if sub == nil {
		return nil, fmt.Errorf("nats: subscription is nil")
	}
	raw, err := sub.Fetch(batch, nats.Context(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(raw))
	for _, m := range raw {
		var delivered uint64
		if md, mErr := m.Metadata(); mErr == nil {
			delivered = md.NumDelivered
		}
		out = append(out, Message{Subject: m.Subject, Data: m.Data, NumDelivered: delivered, msg: m})
	}
	return out, nil
}

// RequestMsg is a core-NATS request delivered to the responder. Respond is a
// function field (not a method) so responder unit tests can capture the reply
// bytes without a live NATS connection.
type RequestMsg struct {
	Subject string
	ReplyTo string
	Data    []byte
	Respond func([]byte) error
}

// SubscribeRequests subscribes to subject within queue group queue and invokes h
// for each request. Uses core NATS (no JetStream): request-reply is transient.
func (c *Client) SubscribeRequests(subject, queue string, h func(RequestMsg)) (*nats.Subscription, error) {
	sub, err := c.nc.QueueSubscribe(subject, queue, func(m *nats.Msg) {
		h(RequestMsg{
			Subject: m.Subject,
			ReplyTo: m.Reply,
			Data:    m.Data,
			Respond: m.Respond,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("nats: subscribe requests %q: %w", subject, err)
	}
	return sub, nil
}

func BuildDLQPayload(dlq processor.DLQEvent) ([]byte, error) {
	payload := map[string]any{
		"originalEvent":  dlq.OriginalEvent,
		"failureReason":  dlq.FailureReason,
		"lastHTTPStatus": dlq.HTTPStatus,
		"attemptCount":   dlq.AttemptCount,
		"sidecarAppID":   dlq.SidecarAppID,
		"timestamp":      dlq.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("nats: marshal dlq: %w", err)
	}
	return body, nil
}
