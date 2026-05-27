# Client-to-server onboarding example

This example runs a local app handler and a event-adapter sidecar.

1. Start NATS JetStream.
2. Start the fake app: `go run ./examples/onboarding/app.go`
3. Start the sidecar: `go run ./cmd/event-adapter --config ./examples/onboarding/routes.yaml`
4. Publish an event: `./examples/onboarding/publish.sh`

The sidecar forwards CloudEvent `data` to `/events/task-created`, publishes a response CloudEvent to `t.tenant-a.app.task.event.processed`, and acknowledges the original message only after response publish confirmation.

Publisher-supplied backend HTTP headers must be sent in the CloudEvent `dispatchheaders` extension and listed in the route's `dispatch.forwardHeaders` allowlist before the sidecar forwards them to the app handler.

