# Event-Adapter Request-Reply Flow Visualizer Design

**Date:** 2026-07-23
**Status:** Approved for implementation planning
**Audience:** Product managers, platform engineers, and application teams

## 1. Purpose

Add a configurable, embeddable visualization to an existing event-adapter demo.
The visualization explains, in real time, which request-reply processing step is
active and which responsibilities move from application code into the platform
sidecar.

The visualizer does not replace the existing demo or trigger requests itself.
It consumes progress events emitted by the demo and maps them to presentation
steps through configuration.

The primary presentation is a five-minute PM demo. It must make this product
change obvious:

- Before event-adapter adoption, every application implements NATS connectivity,
  CloudEvent handling, routing, HTTP translation, response envelopes, and error
  handling.
- After adoption, the application exposes an ordinary local HTTP business
  handler while the sidecar owns the integration mechanics.

## 2. Scope

### Included

- An embeddable `<event-flow>` web component.
- A standalone preview page for developing and validating configurations.
- A request-reply preset containing the eight approved processing steps.
- A configurable Server-Sent Events (SSE) source.
- Correlation of progress events by request ID.
- Waiting, active, completed, and failed visual states.
- Configurable lanes, step labels, ownership colors, event mappings, and display
  details.
- Responsive rendering suitable for a projected PM presentation.
- Deterministic replay fixtures and a real-stream integration check.

### Excluded

- Replacing or reimplementing the existing demo trigger.
- Changing event-adapter request-reply behavior.
- JetStream acknowledgement, retry, response publication, or DLQ visualization.
- Inferring per-request progress from aggregate Prometheus metrics.
- Persisting completed runs or providing a historical trace explorer.
- Coupling the component to a specific business payload.
- Adding an outbound publish/request API to event-adapter.

## 3. PM Demo Story

The visualization presents one page with three layers.

### 3.1 Product contrast

The top section shows a concise before-and-after comparison:

- **Before:** the app owns the NATS client, CloudEvent parsing, routing, response
  construction, error mapping, and observability plumbing.
- **After:** the app owns one HTTP business handler; the sidecar owns the
  platform integration mechanics.

Platform-owned stages use blue. The application-owned business handler uses
green. The colors reinforce the responsibility boundary throughout the live
trace.

### 3.2 Live request trace

The center section renders the configured request-reply steps. As the existing
demo runs, progress events update the matching steps for one correlated request.
Each completed step may show its timestamp, elapsed time, and configured detail
such as route name, event type, HTTP path, or response status.

### 3.3 Outcome

The final section shows the real reply status, correlation ID, total elapsed
time, and a short takeaway: the app implemented business logic while the
platform delivered the request-reply mechanics.

The recommended five-minute pacing is:

1. 0:00–1:15 — explain duplicated application infrastructure.
2. 1:15–2:15 — show the sidecar responsibility boundary.
3. 2:15–3:45 — trigger the existing demo and narrate the live steps.
4. 3:45–4:30 — inspect the real response and correlation ID.
5. 4:30–5:00 — restate the product value.

## 4. Request-Reply Preset

The default preset contains these eight steps:

| Step | Lane | Owner | Meaning |
|---|---|---|---|
| 1. Send NATS request | Caller | Caller | The caller sends a core-NATS request with a reply inbox. |
| 2. Deliver to one responder | NATS | Platform | The queue group selects one event-adapter instance. |
| 3. Validate CloudEvent | Event-adapter | Platform | The request envelope is parsed and validated. |
| 4. Match request route | Event-adapter | Platform | The CloudEvent `type` selects a configured request route. |
| 5. Dispatch local HTTP | Event-adapter | Platform | The request data is sent to the configured local HTTP handler. |
| 6. Run business handler | Application | Application | The application performs its business action and returns HTTP. |
| 7. Build reply CloudEvent | Event-adapter | Platform | The HTTP response becomes a reply CloudEvent with status and correlation metadata. |
| 8. Return reply | NATS → Caller | Platform | The adapter publishes to the reply inbox and the caller receives the result. |

The preset must not display JetStream semantics. Request-reply is synchronous
and has no acknowledgement, durable retry, or DLQ flow.

For errors, the visualization may show the request-reply status mapping:

- Invalid request or permanent path-template failure: `400`
- No matching route: `404`
- Local application unreachable or other dispatch transport failure: `502`
- Local application timeout: `504`

Every observable request-reply outcome is shown as a reply rather than as a
retry or DLQ transition.

## 5. Component Architecture

The solution has three independent parts:

1. **Existing demo:** triggers the real request-reply flow and emits neutral
   progress events. It contains no visualization layout logic.
2. **Flow configuration:** defines the event source, correlation field, lanes,
   steps, ownership, labels, and event-to-step mappings.
3. **`<event-flow>` component:** loads configuration, consumes live events,
   groups them by correlation ID, calculates step state, and renders the flow.

The component can be embedded in the existing demo or loaded by the standalone
preview page. Its public integration inputs are the configuration URL and,
optionally, the request ID to focus.

The first implementation supports SSE. Additional transports such as WebSocket
or direct NATS WebSocket subscription are not part of the initial scope; they
can be added behind the same source interface later.

## 6. Configuration Model

Configuration may be authored as YAML or JSON. The preview/build path must
normalize it into one validated in-memory model before rendering.

Example:

```yaml
version: 1
title: Event-adapter request-reply

source:
  transport: sse
  url: /api/demo/events
  correlationField: requestId

lanes:
  - id: caller
    label: Caller
    owner: caller
  - id: nats
    label: NATS
    owner: platform
  - id: adapter
    label: Event-adapter
    owner: platform
  - id: application
    label: Application
    owner: application

steps:
  - id: validate
    order: 3
    lane: adapter
    label: Validate CloudEvent
    description: Parse and validate the request envelope
    completeWhen:
      event: adapter.validated
    detailFields:
      - eventType
```

Validation must reject:

- Unsupported configuration versions or transports.
- Missing source URL or correlation field.
- Duplicate lane or step IDs.
- Steps referencing unknown lanes.
- Duplicate or non-positive step order values.
- Missing event mappings.
- Invalid owner or visual-state values.

Configuration errors appear as a clear setup error. The component must not open
the live stream when configuration is invalid.

## 7. Live-Event Contract

The existing demo emits one event per observed stage transition:

```json
{
  "requestId": "req-8f21",
  "event": "adapter.route_matched",
  "status": "completed",
  "timestamp": "2026-07-23T10:15:22.381Z",
  "detail": {
    "route": "upload-presign"
  }
}
```

Required fields:

- The configured correlation field, `requestId` in the preset.
- `event`: stable event name used by a step mapping.
- `status`: `started`, `completed`, or `failed`.
- `timestamp`: RFC 3339 timestamp.

`detail` is optional and must be treated as display data only. Configuration
selects which detail fields may be rendered. Raw HTML from event data must never
be inserted into the page.

The visualizer does not require the business payload. Event names describe
progress, not application data.

## 8. State and Correlation Rules

For each request ID, every configured step begins in `waiting`.

- A matching `started` event changes the step to `active`.
- A matching `completed` event changes the step to `completed`.
- A matching `failed` event changes the step to `failed`.
- A failed step leaves later waiting steps unchanged.
- Unknown event names are ignored and may be reported to the browser console in
  debug mode.
- Events for other request IDs do not affect the focused trace.
- Duplicate events are idempotent.
- When events arrive out of order, the component stores them and reconciles the
  displayed state using their timestamps and state precedence.
- A missing event leaves the corresponding step waiting. The component never
  fabricates completion to make a flow look successful.

State precedence for the same step is `failed` over `completed` over `active`
over `waiting`. A later event may update timestamps and detail without reducing
the state precedence. Resetting or replaying a request creates a fresh trace
rather than mutating a completed trace backwards.

## 9. Connection and Error Behavior

The component displays its event-source state: connecting, live, reconnecting,
or disconnected.

- The "Live" badge appears only while the SSE connection is open.
- Native SSE reconnection is allowed. Replayed duplicate events remain safe.
- A malformed event is ignored and surfaced in debug diagnostics.
- A configuration error replaces the flow with a readable setup error.
- A disconnected stream does not clear already confirmed steps.
- A step-level `failed` event turns that step red and renders an allowlisted
  plain-language detail when configured.
- A configurable inactivity timeout may show "waiting for live event"; it must
  not mark a step failed without a failure event.

## 10. Visual and Accessibility Requirements

- The layout must clearly separate caller, NATS, event-adapter, and application
  lanes.
- Step number and text must communicate state without relying on color alone.
- Platform ownership is blue and application ownership is green by default;
  themes may override colors in configuration or CSS custom properties.
- Waiting, active, completed, and failed states require distinct icons and
  accessible labels.
- The active step receives motion only when reduced-motion is not requested.
- Keyboard and screen-reader users must be able to inspect every step and its
  current status.
- The layout must remain legible on a typical laptop and projected 16:9 display.
- On narrow screens, lanes stack vertically without changing processing order.

## 11. Verification

### 11.1 Configuration tests

- Accept the complete request-reply preset.
- Reject each invalid condition listed in section 6.
- Verify YAML and JSON normalize to the same model.

### 11.2 State reducer tests

- Complete all eight steps for one request ID.
- Keep concurrent request IDs isolated.
- Apply started, completed, and failed transitions.
- Handle duplicate and out-of-order events deterministically.
- Ignore unknown and malformed events without corrupting state.
- Leave missing stages waiting.
- Prevent a lower-precedence event from reversing a confirmed state.

### 11.3 Rendering and accessibility tests

- Render the approved lanes, ownership distinction, and step order.
- Render connection and failure states.
- Escape all event-provided detail.
- Verify keyboard inspection, semantic status labels, reduced motion, and the
  stacked narrow-screen layout.

### 11.4 Replay and integration tests

- Use a deterministic request-reply event fixture to demonstrate the full flow
  without timing instability.
- Connect the component to the existing demo's real SSE stream and confirm that
  a request ID advances only when matching live events arrive.
- Confirm the displayed response status and correlation ID match the real demo
  result.

## 12. Implementation Boundary

Implementation work is limited to the visualizer, its configuration preset,
event contract documentation, and the smallest integration required for the
existing demo to emit the contract.

The event-adapter's production request-reply behavior must remain unchanged.
If the existing demo cannot currently observe an internal stage, that stage
must remain waiting until the demo or its telemetry bridge emits an explicit
event. Aggregate counters such as request totals and latency histograms are not
sufficient evidence for an individual request stage.
