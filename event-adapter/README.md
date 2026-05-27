# event-adapter

NATS JetStream to local HTTP event dispatch sidecar.

Design source: `../prd/event-adapter/prd.md`.

Phase 1 responsibilities:

- consume CloudEvents from JetStream durable consumers
- dispatch JSON CloudEvent data to configured localhost HTTP handlers
- publish deterministic response CloudEvents
- publish exhausted failures to DLQ
- acknowledge original messages only after response or DLQ publish confirmation

