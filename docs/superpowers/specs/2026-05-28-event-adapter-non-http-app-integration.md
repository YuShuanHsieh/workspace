# event-adapter Integration Options for Non-HTTP Apps — Exploration

**Status:** exploratory / options analysis, not an approved design
**Date:** 2026-05-28
**Authors:** brainstormed with Claude
**Related:** [`prd/event-adapter/prd.md`](../../../prd/event-adapter/prd.md), [`prd/event-adapter/app-developer-guide.md`](../../../prd/event-adapter/app-developer-guide.md), [`prd/app-to-app/draft.md`](../../../prd/app-to-app/draft.md), [`docs/superpowers/specs/2026-05-27-app-to-app-design.md`](./2026-05-27-app-to-app-design.md)

---

## 1. Motivation

The current `event-adapter` sidecar assumes the colocated application server is **HTTP-native and synchronous**: PRD §7 binds dispatch to `method + path` over loopback HTTP, with one HTTP response per inbound CloudEvent and JSON-only `data` payloads. This fits a clean and useful slice of apps — stateless CRUD-style services — but breaks down for apps with different transports (WebSocket, gRPC, SSE), different cardinality (streaming, long-running jobs), or different control flow (pull-based, app-initiated publish).

This document captures the option space we explored for one specific instance of that mismatch — **a backend whose external client interface is WebSocket** — and then generalizes to the broader set of use cases that event-adapter cannot serve as-written. The intent is to **inform a future decision**, not to mandate one. Each option below ends with the condition under which it is the right choice.

## 2. Scope

### In scope

- Integration options when the colocated backend does not expose HTTP request/response endpoints.
- Architecture sketches for sibling/alternative sidecar patterns (WS Edge Gateway, NATS proxy, inverted-dispatch broker).
- A catalogue of use cases the current event-adapter cannot meet, grouped by the type of mismatch.
- Selection guidance: when to add a sibling sidecar vs. when to push complexity into the app vs. when to use an existing primitive (e.g. NATS leaf nodes).

### Out of scope

- Detailed PRDs for any of the sibling sidecars proposed here. Each one that gets adopted will need its own spec.
- Modifying the existing event-adapter contract (PRD §7) to be protocol-pluggable. The exploration concludes that "extend the sidecar" is generally the *least* attractive direction.
- Scheduling, capacity planning, security review, or rollout sequencing.

## 3. The triggering question

> The current event-adapter only handles backends that expose HTTP. If a backend's external interface is WebSocket (or it has no HTTP surface at all), how should it participate in the platform's event-driven flows?

This question fans out into several distinct design problems depending on **who the "client" is** and **what the WS connection is for**:

1. **The backend speaks WS to its end-users**, but apps wanting to invoke it via the platform are other apps publishing CloudEvents to NATS. → §4.
2. **End-user WS clients should themselves flow through NATS**, so every inbound interaction (whether from a human user or another app) reaches the backend via the event bus. → §5.
3. **The backend has no HTTP stack at all** and needs to participate without one being added. → §6.

Sections 4–6 are independent answers to those distinct framings. Section 7 zooms out to alternative sidecar architectures suggested by the discussion, and §8 catalogues the broader set of mismatches.

## 4. WS-native backend, app-to-app traffic on NATS

Three options were considered for letting a WebSocket-native backend participate in NATS-delivered app-to-app event flows.

### 4.1 Option A — HTTP shim inside the same backend *(recommended for one-off case)*

The backend keeps its WebSocket endpoint for end-users **and** adds a thin set of HTTP handlers (e.g. `POST /events/<name>`) that the sidecar dispatches to. Those handlers call the same internal services that WS message handlers already call.

- **Pros:** No sidecar changes, no extra process, fits the current PRD as-written. Forces business logic to be transport-agnostic, which is a healthy refactor.
- **Cons:** Backend gains a second listener (a second port or a second mux on the same port). Some refactor effort if WS handler logic is tangled with framing today.
- **Wrong when:** the backend is closed-source / off-the-shelf and cannot be modified.

#### 4.1.1 Data flow

Two independent paths share the same backend process; the WS path does not touch NATS.

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Pod: backend-service                                 │
│                                                                              │
│  ┌─────────────────────────────────┐    ┌─────────────────────────────┐     │
│  │       Backend container         │    │   event-adapter sidecar     │     │
│  │                                 │    │                             │     │
│  │  :8443  WS endpoint  ◄──────────┼────┼──── (no involvement)        │     │
│  │         /ws                     │    │                             │     │
│  │             │                   │    │                             │     │
│  │             ▼                   │    │                             │     │
│  │  ┌─────────────────────────┐    │    │                             │     │
│  │  │   Shared business logic │    │    │                             │     │
│  │  │   (taskService, etc.)   │◄───┼────┼──── HTTP POST /events/...   │     │
│  │  └─────────────────────────┘    │    │     localhost loopback      │     │
│  │             ▲                   │    │           ▲                 │     │
│  │             │                   │    │           │                 │     │
│  │  :8080  HTTP endpoint           │    │           │                 │     │
│  │         /events/task-created    │    │           │                 │     │
│  │                                 │    │           │                 │     │
│  └─────────────────────────────────┘    │           │                 │     │
│                                         │   subscribe   publish       │     │
│                                         └───────┼───────────┼─────────┘     │
└─────────────────────────────────────────────────┼───────────┼───────────────┘
                                                  │           │
                                                  ▼           ▲
                                          ┌──────────────────────────┐
                                          │       NATS bus           │
                                          └──────────────────────────┘
```

**Flow 1 — End-user WebSocket (unchanged):** client opens WS, sends frames, backend WS handler calls `taskService.Create(...)`, writes WS reply. NATS not involved.

**Flow 2 — App-to-app via NATS:** publisher app publishes CloudEvent → JetStream → sidecar → `POST http://127.0.0.1:8080/events/task-created` with body = CloudEvent `data`, `ce-*` headers, `Idempotency-Key` = `ce-id` (PRD §7) → backend HTTP handler short-circuits if already processed, calls the same `taskService.Create(...)`, returns `200 OK` JSON → sidecar wraps response, publishes response CloudEvent (PRD §8) → sidecar acks JetStream message.

The two flows meet only at the shared business-logic layer. Idempotency is enforced there (or below), since the same logical operation can arrive on either path. Authorization context is extracted per-transport: WS handler from the WS session, HTTP handler from `dispatchheaders` (forwarded `X-Workspace-Actor-Id`, etc.) per PRD §7. Persistence, validation, side-effects live below both transports.

### 4.2 Option B — HTTP→WS bridge as a second container

A small process accepts HTTP from the sidecar on loopback and translates each request into a WS frame to the backend (also on loopback), waits for a correlated response frame, and returns it as the HTTP response.

- **Pros:** Backend code untouched.
- **Cons:** You now have to invent: a WS message envelope, request↔response correlation IDs, timeouts on a persistent connection, reconnect behavior, mapping WS errors → HTTP status. That's a real piece of software, not a shim. Three containers in the pod instead of two.
- **Right when:** the backend genuinely cannot be changed, or multiple WS backends already share an identical framing protocol you can reuse.

### 4.3 Option C — Native WS dispatch in event-adapter

Extend the sidecar so a route can dispatch over a localhost WebSocket instead of HTTP. The sidecar opens/maintains the WS connection, sends each CloudEvent as a frame, waits for a correlated response frame, then publishes the response CloudEvent as today.

#### 4.3.1 Why per-route mapping does not translate cleanly

WebSocket is **connection-oriented**, not request-oriented. The HTTP concept of `method + path` per event has no direct WS analogue.

| Concept | HTTP today | What WS needs |
|---|---|---|
| Where you connect | not separate from path — every dispatch reopens (or pools) `httpBaseURL` + `dispatch.path` | one-time `wsConnectPath` (handshake URL), separate from per-event routing |
| What identifies the operation | `dispatch.method` + `dispatch.path` | a field inside the frame envelope (e.g. `messageType` or `op`) |
| Request body | HTTP body | a `payload` field in the envelope |
| CloudEvent metadata | `ce-*` headers | a `headers` / `attributes` object in the envelope |
| Status | HTTP status code | a `status` field in the envelope |
| Request↔response pairing | implicit on the connection | explicit `correlationId` in every frame |
| Per-call timeout | per HTTP request | per-frame timer waiting for matching `correlationId` |
| Connection lifecycle | none — each dispatch independent | reconnect, backoff, queueing during reconnect, pong/keepalive |

Sketch of route config:

```yaml
app:
  id: task-service
  ws:
    baseURL: ws://127.0.0.1:8080
    connectPath: /ws
    subprotocol: workspace.events.v1
    pingInterval: 15s
    reconnectBackoff: 1s..30s

routes:
  - name: task-created
    match: { subject: ..., type: ..., source: ... }
    dispatch:
      protocol: ws
      messageType: task.created     # the per-event "path" lives here
      timeout: 2s
    response:
      type: com.workspace.task.created.processed
      subject: t.tenant-a.app.task.event.processed
```

Sketch of wire frames:

```json
// sidecar → backend
{ "kind": "request",  "correlationId": "01J...", "op": "task.created",
  "ceId": "evt-123", "ceType": "com.workspace.task.created",
  "ceSource": "workspace/task",
  "headers": { "Idempotency-Key": "evt-123", "traceparent": "..." },
  "payload": { "taskId": "task-456", ... } }

// backend → sidecar
{ "kind": "response", "correlationId": "01J...", "status": 200,
  "headers": { "content-type": "application/json" },
  "payload": { "taskId": "task-456", "status": "processed" } }
```

#### 4.3.2 Hard problems this introduces

| Problem | Why it's new |
|---|---|
| **Reconnect during in-flight work** | If the WS conn dies with N events in flight, none of them got responses. JetStream still has them unacked, so they redeliver — but only because you held off acking. |
| **Head-of-line blocking** | One slow handler on the backend stalls every other event behind it on the same WS conn. Likely needs a *pool* of WS connections per route or per app. |
| **Backpressure** | HTTP has natural per-request flow control. WS doesn't. Sidecar needs explicit max-in-flight; otherwise a stuck backend = unbounded memory growth. |
| **Ordering vs. concurrency** | Decide whether frames are processed in order per-connection or freely. JetStream redelivery + WS reordering can produce surprising sequences. |
| **Draining on shutdown** | Sidecar SIGTERM with N in-flight frames: wait for responses, send a "close" frame, or abort and rely on JetStream redelivery? Must pick and document. |
| **Versioning the envelope** | The frame schema is now a wire contract between sidecar and every backend. Adding a field is a coordinated rollout. |
| **What the backend must implement** | WS endpoint speaking the subprotocol, frame dispatcher, correlation-aware response writer, ping/pong. That's a small framework you have to provide (and maintain) in every backend language. |

- **Pros:** First-class, configurable per route, no extra process.
- **Cons:** Largest change. The dispatch contract in PRD §7 becomes protocol-pluggable; §8/§9/§10 need WS variants. Sidecar gains real state (connections, in-flight map, reconnect).
- **Right when:** several apps in the platform are WS-only and the platform wants a uniform feature, not a per-app shim. Treat it as a real protocol project: separate PRD, versioned envelope, per-language SDK.

### 4.4 Recommendation for §4 framing

**Option A** is the default. The event-adapter PRD deliberately keeps the dispatch contract narrow; adding an HTTP endpoint for the sidecar's traffic preserves that simplicity. Most teams in this pattern end up here.

Option B is rarely worth it. Option C is justified only when the backend population is genuinely WS-only at platform scale.

## 5. End-user WS clients flowing through NATS

If the goal is that **end-user WebSocket traffic itself should flow through the event bus** (not just app-to-app), the answer is **outside the event-adapter scope**. It's a separate platform component: a **WebSocket Edge Gateway** that sits in front of NATS, accepts external WS connections, authenticates users, and translates WS messages ↔ CloudEvents.

```text
WS client ──WS──> [WS Edge Gateway] ──NATS publish──>  NATS
                         ▲                              │
                         │                              │ delivered to subscribed sidecar
                         │                              ▼
                         │                       [event-adapter]
                         │                              │
                         │                              ▼
                         │                       [backend HTTP handler]
                         │                              │
                         │                              ▼
                         │                       [event-adapter wraps response]
                         └──NATS subscribe──────────── NATS publish ────────┘
```

Two viable patterns for how the gateway gets responses back to the right WS session:

### 5.1 Per-request reply-to inbox *(recommended for request/response)*

For each inbound WS message, the gateway:

- Generates a correlation ID.
- Creates an ephemeral NATS inbox subject scoped to its own pod (e.g. `_INBOX.<gateway-pod>.<corr-id>`).
- Publishes the CloudEvent with a `replyto` extension carrying that inbox subject.
- Subscribes (ephemeral) to the inbox for the response, with a timeout.

The event-adapter sidecar gains a **small new feature**: if the inbound CloudEvent has a `replyto` extension, publish the response CloudEvent to that subject *in addition to* (or instead of) the route's configured `response.subject`. Everything else in the sidecar stays the same.

- **Pros:** Clean request/response mapping. Native NATS pattern. Response naturally lands on the pod holding the session — no cross-pod routing.
- **Cons:** Doesn't, by itself, handle server-pushed events. Sidecar PRD §8 needs an addendum.

### 5.2 Per-session subject subscription

The gateway, on each WS connect, subscribes to a subject pattern keyed to the session (e.g. `ws.session.<session-id>.>` or `ws.user.<user-id>.>`). Inbound WS messages are published with that session/user ID in the CloudEvent so any backend's response (or any platform-pushed event) reaching that subject pattern flows to the right pod and out the WS connection.

- **Pros:** Naturally supports server-pushed events alongside request/response. Symmetric design.
- **Cons:** More subject-routing discipline platform-wide. Need either deterministic session-id-based subjects (gateway pod owns the namespace) or a session→pod directory.

### 5.3 Recommendation for §5 framing

Start with §5.1. It's the smallest delta to the existing event-adapter (one new extension, one new publish target) and covers the request/response case the current PRD already models. Add §5.2's subject-subscription mechanism later, only when server-pushed events to connected users become a requirement.

The WS Edge Gateway is **not** part of the event-adapter — it's a new component with its own PRD. The event-adapter just learns to honor `replyto`.

## 6. Inverted dispatch — sidecar as local broker / NATS proxy

A different architecture surfaced during the exploration: instead of the sidecar *pushing* dispatches to the app, the app *pulls* events from the sidecar through a long-lived connection. The sidecar becomes the local edge of the event bus.

### 6.1 Data flow

```text
                NATS bus
                   ▲ │
        subscribe  │ │  publish (response / outbound)
                   │ ▼
         ┌─────────────────────────────────────┐
         │  Sidecar (local broker / proxy)     │
         │  - JetStream consumer               │
         │  - Per-subscription buffer + flow   │
         │  - Local broker listener (WS/gRPC)  │
         │  - Upstream NATS creds + scope ACL  │
         └────────────────┬────────────────────┘
                          │   loopback
              WS/gRPC stream or native NATS protocol
                          │
         ┌────────────────▼────────────────────┐
         │  Backend app                        │
         │  - Sidecar client                   │
         │  - SUBSCRIBE / DELIVER / ACK / NACK │
         │  - PUBLISH outbound events          │
         │  - Shares biz logic with WS path    │
         └─────────────────────────────────────┘
```

### 6.2 What changes vs. the current event-adapter

| Concern | Current (push HTTP) | Inverted (pull stream) |
|---|---|---|
| Who drives | Sidecar | App |
| Sidecar state | Stateless per dispatch | Stateful per subscription (buffer + credit + in-flight) |
| App's "handler" | HTTP route | Subscriber loop reading framed messages |
| Ack semantics | Implicit (HTTP 2xx) | Explicit `ACK`/`NACK` frames |
| Outbound publish | Phase-1 non-goal (PRD §3) | Falls out naturally — same stream carries `PUBLISH` |
| Flow control | HTTP per-request | Explicit credit / pull |
| Backend prerequisite | Any HTTP framework | A small sidecar-client SDK (in every language) |

### 6.3 Primary motivation: credential isolation

The strongest reason to invert dispatch is **operational**: the sidecar holds the real NATS credentials, the app does not. App config becomes `NATS_URL=nats://127.0.0.1:4222`; no TLS material, no rotation logic. Credentials live only in the sidecar (mounted secret / IRSA-style identity / SPIFFE). Scope is enforced at the sidecar (subject allowlist). Rotation: platform restarts/refreshes the sidecar; app process untouched.

### 6.4 Two implementation paths

**6.4.1 NATS leaf node as the sidecar *(recommended starting point)*.** NATS already has a primitive for this. A *leaf node* is a NATS server that connects upstream as an authenticated client and exposes a local listener (with separate, relaxed auth on loopback). What you build: a sidecar container image running `nats-server` with leaf-node config rendered from app route policy. What you don't build: protocol, client SDKs, auth, reconnect, JetStream semantics, observability — all maintained by the NATS team. Cost: commit to the NATS wire protocol and leaf-node semantics; apps must use a NATS client library.

**6.4.2 Custom proxy.** Write a sidecar that terminates NATS protocol on the local listener, applies its own policies, and re-issues operations upstream. Only justified if you need something leaf nodes can't express — e.g. wire-level CloudEvent envelope enforcement, event-marketplace schema validation, identity stamping from pod identity, per-message audit pipeline integration. The app-to-app PRD's "must validate before publishing" requirement is the strongest candidate driver.

### 6.5 CloudEvents structured ↔ binary mode at the boundary

In either implementation path, the sidecar can do one useful, well-specified transformation: convert upstream **structured-mode** CloudEvents (single JSON blob containing envelope + `data`) into local **binary-mode** delivery (`data` as the message body, attributes as NATS message headers). The app then sees raw business data as the payload and can read `ce-*` metadata from headers if it wants.

```text
Upstream NATS (structured CloudEvent)            Local NATS (binary CloudEvent)
┌─────────────────────────────────┐              ┌─────────────────────────────────┐
│ payload: {                      │              │ headers:                        │
│   "specversion": "1.0",         │              │   ce-id, ce-type, ce-source,    │
│   "id": "evt-123",              │   ─────►     │   Idempotency-Key, traceparent  │
│   "type": "...",                │              │ payload:                        │
│   "source": "...",              │              │   { "taskId": "t-456", ... }    │
│   "data": { "taskId": ... }     │              │                                 │
│ }                               │              │                                 │
└─────────────────────────────────┘              └─────────────────────────────────┘
```

**Strongly prefer binary mode over stripping attributes entirely.** Fully stripping metadata looks simpler but quietly breaks the platform's delivery contracts: idempotency (`ce-id` per PRD §10), trace propagation (`traceparent` per PRD §12), and provenance (`ce-source`, app-to-app §4–5). The metadata costs nothing on the wire.

**Practical implication for §6.4.1:** leaf nodes alone don't transform message format — they forward as-is. So either (a) standardize on binary CloudEvents everywhere on the bus and a leaf-node sidecar Just Works, or (b) accept structured-mode publishers and write a small in-process transformer in front of the leaf node. Option (a) is cleaner and worth enforcing as a platform convention.

### 6.6 When to choose §6 over §4

| | Option A (HTTP shim) | Option C (push WS) | §6 (pull stream / proxy) | NATS-as-sidecar (§6.4.1) |
|---|---|---|---|---|
| Sidecar complexity | Smallest | Large | Large | None (use NATS) |
| New wire protocol | None | Yes | Yes | None |
| App-side complexity | One HTTP handler per route | WS dispatcher loop | Subscriber SDK | NATS client (mature) |
| Outbound publish | Separate problem | Separate problem | Built-in | Built-in |
| Subject/route enforcement | Sidecar config | Sidecar config | Sidecar config | NATS permissions |
| Fits WS-native backend | Awkward (adds HTTP) | Natural | Natural | Natural |
| Maintenance burden | Low | Very high | High | Low (NATS team owns it) |

The right question to commit before going further: **does leaf-node-with-subject-ACLs satisfy the platform's enforcement requirements, or do you need wire-level CloudEvent/schema validation?** If yes to leaf nodes, the design is small. If no, you're building a real proxy and it deserves its own PRD.

## 7. Use cases the current event-adapter cannot serve

Beyond WebSocket, several common web-app patterns don't fit the current event-adapter contract. Grouping by the type of mismatch makes the gaps easier to reason about.

### 7.1 Protocol mismatch — app doesn't speak HTTP request/response

| Use case | Why it doesn't fit |
|---|---|
| **WebSocket** (§4–6) | Connection-oriented; no per-message path; needs in-frame `op` + correlation IDs. |
| **Server-Sent Events (SSE)** | Server holds the connection open and streams; HTTP response never "completes" in the way the sidecar timeout expects. |
| **gRPC (unary or streaming)** | Different framing (HTTP/2 + protobuf), different status model, different metadata. Streaming variants have the same cardinality problem as SSE. |
| **GraphQL subscriptions** | A single subscription operation produces an open-ended stream of payloads; doesn't map to "one event in → one response out". |
| **WebRTC signaling / media** | Out of scope for any request/response sidecar. Real-time media is peer-to-peer with negotiation, not event-driven RPC. |
| **MQTT-only IoT devices** | App speaks MQTT to devices, not HTTP. Needs a protocol bridge, not an HTTP dispatcher. |

### 7.2 Cardinality mismatch — one event ≠ one synchronous HTTP response

| Use case | Why it breaks |
|---|---|
| **Long-running async jobs** (video transcode, ML inference, report generation, large ETL) | Handler can't return a meaningful result within the route timeout. Returning "accepted" immediately publishes a misleading "success" response CloudEvent before the work is done. Needs *acknowledge-now, callback-later* — not modeled by PRD §8. |
| **Streaming / chunked responses** (token-by-token LLM output, progress updates, incremental search results) | One inbound event should produce a *sequence* of response events over time. PRD §8 wraps exactly one HTTP response body as exactly one response CloudEvent. |
| **Batched / windowed processing** (analytics aggregation, rate-limit accounting, tumbling-window stats) | App wants a batch of events together (every N events or T seconds). The sidecar delivers one at a time. |
| **Saga / multi-event transactions** | App needs to coordinate state across several related events. Per-event HTTP dispatch has no place to express "these events are part of one logical unit of work". |

### 7.3 Direction mismatch — app needs to initiate

| Use case | Why it breaks |
|---|---|
| **Outbound publishing from the app** | PRD §3 explicitly Phase-1 non-goal. Apps doing app-to-app need to publish back, and today they have to add a NATS client (defeating any credential-isolation story). |
| **App-driven backpressure / pull consumption** | HTTP push has no clean "slow down" signal beyond 429. Apps with bursty load or external rate limits need pull-based consumption — i.e. §6's inverted dispatch. |
| **Replay / catch-up from a specific point** | App needs to start consuming from sequence N or timestamp T (post-outage, post-correction, bootstrap of a new read model). Sidecar's durable consumer is fixed by static config; the app has no API to control it. |
| **Dynamic / per-tenant subscriptions** | Multi-tenant SaaS needs to subscribe to new subjects at runtime. Current sidecar routes are static YAML loaded at startup. |

### 7.4 Origin mismatch — events don't come from internal NATS

| Use case | Why it breaks |
|---|---|
| **External webhooks** (Stripe, GitHub, Twilio, Slack callbacks) | App needs to *receive* events from outside the platform. event-adapter consumes from NATS only. Needs an inverse sidecar (HTTP-in → NATS-publish). |
| **Browser-originated events** (clicks, form submits as platform events) | A browser has no sidecar; it can't speak NATS. Needs an edge gateway (§5's WS/HTTP Edge Gateway). |
| **Scheduled / cron-driven work** | "Run this handler every hour" is not an event from NATS. Needs a cron sidecar or scheduler that publishes events. |

### 7.5 Payload mismatch — non-JSON or large data

| Use case | Why it breaks |
|---|---|
| **Binary payloads** (images, audio frames, file uploads, PDFs) | PRD §7 calls out JSON-only for Phase 1; binary / `data_base64` is rejected. |
| **Large payloads** (multi-MB documents, ML feature vectors) | NATS messages have practical size limits; CloudEvents should carry a reference (claim-check pattern) rather than the data itself. No built-in "fetch the real payload from object storage" support. |
| **Multipart / form uploads** | `multipart/form-data` (file + fields) has no natural CloudEvent shape. Needs upload-then-event-with-pointer, not modeled. |

### 7.6 Semantics mismatch — guarantees the platform doesn't provide

| Use case | Why it breaks |
|---|---|
| **Exactly-once business effects** (payments, financial postings, inventory deduction) | PRD §10 is explicitly at-least-once. Apps that can't tolerate duplicates need a distributed transaction layer or strict end-to-end idempotency the platform doesn't enforce. |
| **Strict ordering** (per-aggregate event order, per-user causal order) | Current PRD doesn't promise per-key ordering. Some apps (CQRS read models, ledger replays) break without it. |
| **Session-affine routing** ("all events for user X go to instance N because instance N holds their session") | Sidecar's local dispatch is per-pod; cross-pod affinity isn't modeled. Required for in-memory session apps, sticky LLM contexts, stateful game servers. |

## 8. Suggested platform direction

Most gaps in §7 are not reasons to *extend* event-adapter — they're reasons for **separate platform products** that share the same event bus.

| Mismatch | Suggested platform component |
|---|---|
| WebSocket end-user clients | WS Edge Gateway (§5) |
| Outbound publish + WS-native apps + credential isolation | NATS-proxy sidecar (§6) |
| Long-running / streaming / batched work | An *async response* contract: app sends N response events over time with the original `correlationId`/`causationid`; PRD addendum to §8 |
| Webhooks from external SaaS | Webhook ingress sidecar (HTTP-in → NATS publish) |
| Browser / mobile direct integration | Same Edge Gateway as §5, with auth |
| Cron / time-driven | Scheduler service that publishes events |
| Binary / large payloads | Claim-check pattern + object-store integration, plus binary-mode CloudEvent support |
| Exactly-once / strict ordering | Out of scope for the bus; document explicitly and steer apps to per-aggregate streams + idempotency |

The honest reading: `event-adapter` is the right primitive for **stateless, synchronous, JSON, inbound-only, HTTP-handler-style** workloads. That covers a real and useful slice (most CRUD-style services), but the platform needs at least two or three sibling sidecars plus an edge gateway to cover the realistic spread of web-app patterns. Trying to make event-adapter cover all of them would turn it into a kitchen-sink product with branching semantics, which is exactly what the current PRD's tight scope is protecting against.

## 9. Decisions deferred

This document is not a commitment. The following decisions are explicitly left open and should be made before any of §5, §6, or §8's sibling components are built:

1. **For §5 (Edge Gateway):** is request/response (§5.1) enough for v1, or is server-pushed event delivery (§5.2) needed at launch?
2. **For §6 (NATS proxy):** is subject-permission enforcement at a leaf node (§6.4.1) sufficient, or is wire-level CloudEvent/schema validation required (§6.4.2)?
3. **For §6.5:** can the platform mandate binary-mode CloudEvents on NATS, eliminating the need for in-sidecar format conversion?
4. **For §7.2 / §8:** does the async-response contract become a Phase-2 of event-adapter (adding §8 addendum) or a separate orchestration product?
5. **For §4.3 (native WS dispatch):** is the WS-only backend population large enough to justify building this, or is Option A's HTTP shim acceptable forever?

Each "yes" to a sibling-product question implies a separate PRD, separate ownership, and separate rollout. The current event-adapter PRD should remain narrow.
