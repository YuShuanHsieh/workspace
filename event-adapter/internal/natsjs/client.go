package natsjs

import (
	"context"
	"encoding/json"
	"fmt"

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
		_ = c.nc.Drain()
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

func (c *Client) PullSubscribe(subject string, durable string) (*nats.Subscription, error) {
	sub, err := c.js.PullSubscribe(subject, durable)
	if err != nil {
		return nil, fmt.Errorf("nats: pull subscribe: %w", err)
	}
	return sub, nil
}

func (m Message) Ack(ctx context.Context) error {
	if m.msg == nil {
		return fmt.Errorf("nats: message is nil")
	}
	return m.msg.Ack(nats.Context(ctx))
}

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
