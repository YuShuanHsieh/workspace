# Request-reply flow visualizer

This example adds a configurable live processing trace to an existing
event-adapter demo. It does not trigger NATS requests and does not change the
sidecar.

## Embed

Serve `web/` from the existing demo, expose the normalized flow config, and add:

```html
<event-flow
  config-url="/demo/request-reply-flow.json"
  request-id="req-8f21">
</event-flow>
<script type="module" src="/demo/event-flow.js"></script>
```

Update `request-id` to the ID returned by the existing demo trigger. The
configured SSE endpoint must emit:

```json
{
  "requestId": "req-8f21",
  "event": "adapter.route_matched",
  "status": "completed",
  "timestamp": "2026-07-23T10:15:22.381Z",
  "detail": { "route": "upload-presign" }
}
```

Only `detailFields` listed for the mapped step are displayed.

## Local preview

```sh
cd event-adapter/examples/flow-visualizer
go run . --listen 0.0.0.0:8080
```

Open `http://<server-ip>:8080`. The preview replays a deterministic successful
request-reply trace. Use `--config` and `--fixture` to load another flow.

## Live-state rules

- Unknown events and other request IDs are ignored.
- Missing events leave a step waiting.
- A failed event turns its mapped step red; later waiting steps remain waiting.
- Duplicate events are idempotent.
- The Live badge appears only while SSE is connected.
