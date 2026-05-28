# event-adapter onboarding example

This example runs a local app handler and an event-adapter sidecar.

1. Start NATS JetStream.
2. Start the fake app: `go run ./examples/onboarding/app.go`
3. Start the sidecar: `go run ./cmd/event-adapter --config ./examples/onboarding/routes.yaml`
4. Publish an event: `./examples/onboarding/publish.sh`

The sidecar forwards CloudEvent `data` to `/events/task-created`, publishes a response CloudEvent to `t.tenant-a.app.task.event.processed`, and acknowledges the original message only after response publish confirmation.

Publisher-supplied backend HTTP headers should be sent in the CloudEvent `dispatchheaders` extension. By default the sidecar forwards every key in `dispatchheaders` to the app handler, except reserved names (CloudEvent metadata, `Idempotency-Key`, `Authorization`, trace context (`traceparent`), hop-by-hop). Set `dispatch.forwardHeaders` on a route only when you need to restrict forwarding to a specific allowlist.
