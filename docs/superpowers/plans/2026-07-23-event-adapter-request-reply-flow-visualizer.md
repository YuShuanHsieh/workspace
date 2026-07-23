# Event-Adapter Request-Reply Flow Visualizer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a configurable, embeddable web component that turns an existing demo's correlated SSE progress events into the approved live request-reply processing flow.

**Architecture:** A dependency-free ES-module web component owns configuration validation, event correlation, trace state, rendering, and SSE connection state. A small Go preview server converts YAML or JSON configuration to normalized JSON, serves the component on port 8080, and replays a deterministic SSE fixture; production event-adapter behavior remains unchanged.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, browser Web Components, Server-Sent Events, vanilla ES modules/CSS, Node 18+ built-in test runner.

---

## File Structure

Create the feature as a self-contained example:

```text
event-adapter/examples/flow-visualizer/
├── README.md                  integration and PM-demo instructions
├── package.json               dependency-free JavaScript test command
├── request-reply.yaml         approved eight-step preset
├── main.go                    preview HTTP server and command-line flags
├── main_test.go               config endpoint, static assets, and SSE replay tests
├── config.go                  YAML/JSON normalization
├── config_test.go             normalization and malformed-input tests
├── fixtures/
│   └── request-reply.jsonl    deterministic successful live-event trace
└── web/
    ├── index.html             standalone preview shell
    ├── event-flow.css         responsive and accessible presentation
    ├── config.js              normalized configuration validation and indexes
    ├── config.test.mjs        schema and event-mapping tests
    ├── state.js               pure correlated trace reducer
    ├── state.test.mjs         state precedence and ordering tests
    ├── render.js              escaped semantic HTML renderer
    ├── render.test.mjs        content, escaping, and accessibility tests
    ├── live-source.js         injectable EventSource adapter
    ├── live-source.test.mjs   connection and malformed-event tests
    ├── event-flow.js          `<event-flow>` custom element
    └── replay.test.mjs        complete fixture-to-render integration test
```

The browser files stay framework-free and independently importable. The Go
preview server is optional for embedding: an existing demo may serve the files
itself and provide normalized JSON directly.

### Task 1: Configuration Validator and Event Index

**Files:**
- Create: `event-adapter/examples/flow-visualizer/package.json`
- Create: `event-adapter/examples/flow-visualizer/web/config.test.mjs`
- Create: `event-adapter/examples/flow-visualizer/web/config.js`

- [ ] **Step 1: Add the dependency-free JavaScript test command**

```json
{
  "name": "event-adapter-flow-visualizer",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "node --test web/*.test.mjs"
  }
}
```

- [ ] **Step 2: Write failing configuration tests**

```js
// web/config.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { normalizeConfig } from "./config.js";

const valid = {
  version: 1,
  title: "Request-reply",
  comparison: {
    beforeLabel: "Before: every application owns",
    before: ["NATS client", "CloudEvents", "Routing"],
    afterLabel: "After: application owns",
    after: "One HTTP business handler",
  },
  source: {
    transport: "sse",
    url: "/api/demo/events",
    correlationField: "requestId",
  },
  lanes: [
    { id: "adapter", label: "Event-adapter", owner: "platform" },
    { id: "app", label: "Application", owner: "application" },
  ],
  steps: [
    {
      id: "validate",
      order: 1,
      lane: "adapter",
      label: "Validate CloudEvent",
      completeWhen: { event: "adapter.validated" },
      failWhen: { event: "adapter.validation_failed" },
      detailFields: ["eventType"],
    },
  ],
};

test("normalizes and indexes a valid configuration", () => {
  const config = normalizeConfig(valid);
  assert.equal(config.steps[0].owner, "platform");
  assert.deepEqual(config.eventIndex.get("adapter.validated"), {
    stepId: "validate",
    status: "completed",
  });
  assert.equal(config.detailFields.get("validate").has("eventType"), true);
});

for (const [name, mutate, message] of [
  ["version", c => { c.version = 2; }, "version must be 1"],
  ["transport", c => { c.source.transport = "websocket"; }, "transport must be sse"],
  ["source URL", c => { c.source.url = ""; }, "source.url is required"],
  ["correlation", c => { c.source.correlationField = ""; }, "correlationField is required"],
  ["comparison", c => { c.comparison.before = []; }, "comparison.before is required"],
  ["duplicate lane", c => { c.lanes.push({ ...c.lanes[0] }); }, "duplicate lane id"],
  ["unknown lane", c => { c.steps[0].lane = "missing"; }, "unknown lane"],
  ["duplicate step", c => { c.steps.push({ ...c.steps[0], order: 2 }); }, "duplicate step id"],
  ["duplicate order", c => { c.steps.push({ ...c.steps[0], id: "other" }); }, "duplicate step order"],
  ["missing mapping", c => { delete c.steps[0].completeWhen; delete c.steps[0].failWhen; }, "event mapping"],
]) {
  test(`rejects invalid ${name}`, () => {
    const input = structuredClone(valid);
    mutate(input);
    assert.throws(() => normalizeConfig(input), new RegExp(message));
  });
}
```

- [ ] **Step 3: Run the test and confirm the expected failure**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/config.test.mjs
```

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for `web/config.js`.

- [ ] **Step 4: Implement strict normalized configuration**

```js
// web/config.js
const owners = new Set(["caller", "platform", "application"]);
const statuses = [
  ["startWhen", "active"],
  ["completeWhen", "completed"],
  ["failWhen", "failed"],
];

function requiredString(value, name) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${name} is required`);
  }
  return value;
}

function unique(items, field, name) {
  const seen = new Set();
  for (const item of items) {
    const value = item[field];
    if (seen.has(value)) throw new Error(`duplicate ${name}: ${value}`);
    seen.add(value);
  }
}

export function normalizeConfig(input) {
  if (!input || input.version !== 1) throw new Error("version must be 1");
  requiredString(input.title, "title");
  requiredString(input.comparison?.beforeLabel, "comparison.beforeLabel");
  requiredString(input.comparison?.afterLabel, "comparison.afterLabel");
  requiredString(input.comparison?.after, "comparison.after");
  if (!Array.isArray(input.comparison?.before) || input.comparison.before.length === 0) {
    throw new Error("comparison.before is required");
  }
  input.comparison.before.forEach((item, index) => requiredString(item, `comparison.before[${index}]`));
  if (input.source?.transport !== "sse") throw new Error("source.transport must be sse");
  requiredString(input.source.url, "source.url");
  requiredString(input.source.correlationField, "source.correlationField");
  if (!Array.isArray(input.lanes) || input.lanes.length === 0) throw new Error("lanes are required");
  if (!Array.isArray(input.steps) || input.steps.length === 0) throw new Error("steps are required");

  input.lanes.forEach((lane, index) => {
    requiredString(lane.id, `lanes[${index}].id`);
    requiredString(lane.label, `lanes[${index}].label`);
    if (!owners.has(lane.owner)) throw new Error(`invalid owner: ${lane.owner}`);
  });
  unique(input.lanes, "id", "lane id");

  const laneByID = new Map(input.lanes.map(lane => [lane.id, { ...lane }]));
  input.steps.forEach((step, index) => {
    requiredString(step.id, `steps[${index}].id`);
    requiredString(step.label, `steps[${index}].label`);
    if (!Number.isInteger(step.order) || step.order <= 0) throw new Error("step order must be positive");
    if (!laneByID.has(step.lane)) throw new Error(`unknown lane: ${step.lane}`);
    if (!statuses.some(([field]) => step[field]?.event)) throw new Error(`step ${step.id} requires an event mapping`);
  });
  unique(input.steps, "id", "step id");
  unique(input.steps, "order", "step order");

  const eventIndex = new Map();
  const steps = [...input.steps].sort((a, b) => a.order - b.order).map(step => {
    const normalized = { ...step, owner: laneByID.get(step.lane).owner };
    for (const [field, status] of statuses) {
      const event = step[field]?.event;
      if (!event) continue;
      requiredString(event, `${step.id}.${field}.event`);
      if (eventIndex.has(event)) throw new Error(`duplicate event mapping: ${event}`);
      eventIndex.set(event, { stepId: step.id, status });
    }
    return normalized;
  });

  return {
    ...input,
    lanes: input.lanes.map(lane => ({ ...lane })),
    steps,
    laneByID,
    eventIndex,
    detailFields: new Map(steps.map(step => [step.id, new Set(step.detailFields ?? [])])),
  };
}
```

- [ ] **Step 5: Run the configuration tests**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/config.test.mjs
```

Expected: all configuration tests PASS.

- [ ] **Step 6: Commit the configuration unit**

```sh
git add event-adapter/examples/flow-visualizer/package.json \
  event-adapter/examples/flow-visualizer/web/config.js \
  event-adapter/examples/flow-visualizer/web/config.test.mjs
git commit -m "feat(event-adapter): validate flow visualizer config"
```

### Task 2: Correlated Trace State Reducer

**Files:**
- Create: `event-adapter/examples/flow-visualizer/web/state.test.mjs`
- Create: `event-adapter/examples/flow-visualizer/web/state.js`

- [ ] **Step 1: Write reducer tests for correlation and precedence**

```js
// web/state.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { normalizeConfig } from "./config.js";
import { createTrace, reduceLiveEvent } from "./state.js";

const config = normalizeConfig({
  version: 1,
  title: "test",
  comparison: {
    beforeLabel: "Before",
    before: ["NATS client"],
    afterLabel: "After",
    after: "HTTP handler",
  },
  source: { transport: "sse", url: "/events", correlationField: "requestId" },
  lanes: [{ id: "adapter", label: "Adapter", owner: "platform" }],
  steps: [{
    id: "validate", order: 1, lane: "adapter", label: "Validate",
    startWhen: { event: "validate.started" },
    completeWhen: { event: "validate.completed" },
    failWhen: { event: "validate.failed" },
    detailFields: ["type"],
  }],
});

const event = (name, statusTime, extra = {}) => ({
  requestId: "req-1",
  event: name,
  status: name.split(".")[1],
  timestamp: statusTime,
  detail: { type: "com.example.request", secret: "hidden" },
  ...extra,
});

test("advances a matching correlated step and allowlists detail", () => {
  const trace = reduceLiveEvent(
    createTrace(config, "req-1"),
    event("validate.completed", "2026-07-23T10:00:00Z"),
    config,
  );
  assert.equal(trace.steps.get("validate").status, "completed");
  assert.deepEqual(trace.steps.get("validate").detail, { type: "com.example.request" });
});

test("ignores other correlation IDs and unknown events", () => {
  const initial = createTrace(config, "req-1");
  assert.strictEqual(reduceLiveEvent(initial, event("validate.completed", "2026-07-23T10:00:00Z", { requestId: "req-2" }), config), initial);
  assert.strictEqual(reduceLiveEvent(initial, event("unknown", "2026-07-23T10:00:00Z"), config), initial);
});

test("is idempotent and keeps failed over completed over active", () => {
  let trace = createTrace(config, "req-1");
  trace = reduceLiveEvent(trace, event("validate.failed", "2026-07-23T10:00:03Z"), config);
  trace = reduceLiveEvent(trace, event("validate.completed", "2026-07-23T10:00:02Z"), config);
  trace = reduceLiveEvent(trace, event("validate.started", "2026-07-23T10:00:01Z"), config);
  assert.equal(trace.steps.get("validate").status, "failed");
  assert.equal(trace.steps.get("validate").events.length, 3);
  const duplicate = reduceLiveEvent(trace, event("validate.failed", "2026-07-23T10:00:03Z"), config);
  assert.equal(duplicate.steps.get("validate").events.length, 3);
});

test("rejects malformed timestamps without changing the trace", () => {
  const initial = createTrace(config, "req-1");
  assert.strictEqual(reduceLiveEvent(initial, event("validate.completed", "not-a-time"), config), initial);
  assert.strictEqual(reduceLiveEvent(initial, event("validate.completed", "2026-07-23T10:00:00Z", { status: "failed" }), config), initial);
});
```

- [ ] **Step 2: Run the test and confirm the expected failure**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/state.test.mjs
```

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for `web/state.js`.

- [ ] **Step 3: Implement immutable, timestamp-aware reduction**

```js
// web/state.js
const precedence = { waiting: 0, active: 1, completed: 2, failed: 3 };

export function createTrace(config, requestID) {
  const steps = new Map(config.steps.map(step => [
    step.id,
    { status: "waiting", timestamp: null, detail: {}, events: [] },
  ]));
  return { requestID, startedAt: null, updatedAt: null, steps };
}

function publicDetail(detail, allowed) {
  if (!detail || typeof detail !== "object" || Array.isArray(detail)) return {};
  return Object.fromEntries(
    Object.entries(detail).filter(([key]) => allowed.has(key)),
  );
}

export function reduceLiveEvent(trace, liveEvent, config) {
  if (!liveEvent || liveEvent[config.source.correlationField] !== trace.requestID) return trace;
  const mapping = config.eventIndex.get(liveEvent.event);
  if (!mapping) return trace;
  const expectedStatus = { active: "started", completed: "completed", failed: "failed" }[mapping.status];
  if (liveEvent.status !== expectedStatus) return trace;
  const timestamp = Date.parse(liveEvent.timestamp);
  if (!Number.isFinite(timestamp)) return trace;

  const previous = trace.steps.get(mapping.stepId);
  const signature = `${liveEvent.event}\u0000${liveEvent.timestamp}\u0000${JSON.stringify(liveEvent.detail ?? {})}`;
  if (previous.events.some(item => item.signature === signature)) return trace;

  const observed = {
    signature,
    status: mapping.status,
    timestamp,
    detail: publicDetail(liveEvent.detail, config.detailFields.get(mapping.stepId)),
  };
  const events = [...previous.events, observed].sort((a, b) => a.timestamp - b.timestamp);
  const selected = events.reduce((best, item) => (
    precedence[item.status] > precedence[best.status] ||
    (precedence[item.status] === precedence[best.status] && item.timestamp > best.timestamp)
      ? item : best
  ), { status: "waiting", timestamp: -Infinity, detail: {} });

  const steps = new Map(trace.steps);
  steps.set(mapping.stepId, {
    status: selected.status,
    timestamp: new Date(selected.timestamp).toISOString(),
    detail: selected.detail,
    events,
  });
  const observedTimes = [...steps.values()].flatMap(step => step.events.map(item => item.timestamp));
  return {
    ...trace,
    startedAt: new Date(Math.min(...observedTimes)).toISOString(),
    updatedAt: new Date(Math.max(...observedTimes)).toISOString(),
    steps,
  };
}
```

- [ ] **Step 4: Run reducer tests**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/state.test.mjs
```

Expected: all reducer tests PASS.

- [ ] **Step 5: Commit the state unit**

```sh
git add event-adapter/examples/flow-visualizer/web/state.js \
  event-adapter/examples/flow-visualizer/web/state.test.mjs
git commit -m "feat(event-adapter): correlate flow progress events"
```

### Task 3: Semantic and Escaped Flow Renderer

**Files:**
- Create: `event-adapter/examples/flow-visualizer/web/render.test.mjs`
- Create: `event-adapter/examples/flow-visualizer/web/render.js`

- [ ] **Step 1: Write rendering and escaping tests**

```js
// web/render.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { normalizeConfig } from "./config.js";
import { createTrace, reduceLiveEvent } from "./state.js";
import { renderFlow } from "./render.js";

const config = normalizeConfig({
  version: 1,
  title: "Request <reply>",
  comparison: {
    beforeLabel: "Before",
    before: ["NATS <client>", "Routing"],
    afterLabel: "After",
    after: "One HTTP handler",
  },
  source: { transport: "sse", url: "/events", correlationField: "requestId" },
  lanes: [
    { id: "adapter", label: "Event-adapter", owner: "platform" },
    { id: "app", label: "Application", owner: "application" },
  ],
  steps: [
    { id: "route", order: 1, lane: "adapter", label: "Match route", completeWhen: { event: "route.done" }, detailFields: ["route"] },
    { id: "handler", order: 2, lane: "app", label: "Run handler", completeWhen: { event: "handler.done" } },
  ],
});

test("renders lanes, ordered steps, status text, and escaped detail", () => {
  let trace = createTrace(config, "req-1");
  trace = reduceLiveEvent(trace, {
    requestId: "req-1",
    event: "route.done",
    status: "completed",
    timestamp: "2026-07-23T10:00:00Z",
    detail: { route: "<img src=x onerror=alert(1)>" },
  }, config);
  const html = renderFlow(config, trace, "live");
  assert.match(html, /Request &lt;reply&gt;/);
  assert.match(html, /NATS &lt;client&gt;/);
  assert.match(html, /One HTTP handler/);
  assert.match(html, /Event-adapter/);
  assert.match(html, /Application/);
  assert.ok(html.indexOf("Match route") < html.indexOf("Run handler"));
  assert.match(html, /aria-label="Match route: completed"/);
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /&lt;img/);
});

test("renders connection and failed states without color-only meaning", () => {
  let trace = createTrace(config, "req-1");
  trace = reduceLiveEvent(trace, {
    requestId: "req-1",
    event: "route.done",
    status: "failed",
    timestamp: "2026-07-23T10:00:00Z",
  }, {
    ...config,
    eventIndex: new Map([["route.done", { stepId: "route", status: "failed" }]]),
  });
  const html = renderFlow(config, trace, "disconnected");
  assert.match(html, />Disconnected</);
  assert.match(html, /aria-label="Match route: failed"/);
  assert.match(html, />Failed</);
  assert.match(html, />Waiting</);
});
```

- [ ] **Step 2: Run the test and confirm the expected failure**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/render.test.mjs
```

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for `web/render.js`.

- [ ] **Step 3: Implement semantic string rendering**

```js
// web/render.js
const stateText = {
  waiting: "Waiting",
  active: "In progress",
  completed: "Completed",
  failed: "Failed",
};
const stateIcon = { waiting: "○", active: "◌", completed: "✓", failed: "!" };
const connectionText = {
  connecting: "Connecting",
  live: "Live",
  reconnecting: "Reconnecting",
  disconnected: "Disconnected",
};

export function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function renderDetail(detail) {
  const items = Object.entries(detail);
  if (items.length === 0) return "";
  return `<dl class="step-detail">${items.map(([key, value]) =>
    `<div><dt>${escapeHTML(key)}</dt><dd>${escapeHTML(value)}</dd></div>`
  ).join("")}</dl>`;
}

function renderComparison(comparison) {
  const before = comparison.before.map(item => `<li>${escapeHTML(item)}</li>`).join("");
  return `<section class="comparison" aria-label="Responsibility comparison">
    <div class="comparison-before"><h3>${escapeHTML(comparison.beforeLabel)}</h3><ul>${before}</ul></div>
    <div class="comparison-arrow" aria-hidden="true">→</div>
    <div class="comparison-after"><h3>${escapeHTML(comparison.afterLabel)}</h3><p>${escapeHTML(comparison.after)}</p></div>
  </section>`;
}

export function renderFlow(config, trace, connection) {
  const lanes = config.lanes.map(lane => {
    const steps = config.steps.filter(step => step.lane === lane.id).map(step => {
      const state = trace.steps.get(step.id);
      return `<li class="flow-step state-${state.status}" data-owner="${escapeHTML(step.owner)}"
        aria-label="${escapeHTML(step.label)}: ${stateText[state.status]}">
        <span class="step-number">${step.order}</span>
        <span class="step-icon" aria-hidden="true">${stateIcon[state.status]}</span>
        <span class="step-copy"><strong>${escapeHTML(step.label)}</strong>
          <span class="step-status">${stateText[state.status]}</span>
          ${step.description ? `<span>${escapeHTML(step.description)}</span>` : ""}
          ${renderDetail(state.detail)}
        </span>
      </li>`;
    }).join("");
    return `<section class="flow-lane" aria-labelledby="lane-${escapeHTML(lane.id)}">
      <h3 id="lane-${escapeHTML(lane.id)}">${escapeHTML(lane.label)}</h3>
      <ol>${steps}</ol>
    </section>`;
  }).join("");

  const elapsed = trace.startedAt && trace.updatedAt
    ? `${Date.parse(trace.updatedAt) - Date.parse(trace.startedAt)} ms`
    : "Waiting";
  return `<article class="event-flow">
    <header><div><p class="eyebrow">Live request trace</p><h2>${escapeHTML(config.title)}</h2></div>
      <p class="connection state-${escapeHTML(connection)}" role="status">${connectionText[connection] ?? "Disconnected"}</p>
    </header>
    ${renderComparison(config.comparison)}
    <p class="request-id">Request: <code>${escapeHTML(trace.requestID)}</code> · Elapsed: ${elapsed}</p>
    <div class="flow-lanes">${lanes}</div>
  </article>`;
}
```

- [ ] **Step 4: Run rendering tests**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/render.test.mjs
```

Expected: all renderer tests PASS.

- [ ] **Step 5: Commit the renderer**

```sh
git add event-adapter/examples/flow-visualizer/web/render.js \
  event-adapter/examples/flow-visualizer/web/render.test.mjs
git commit -m "feat(event-adapter): render accessible request flow"
```

### Task 4: Injectable SSE Transport

**Files:**
- Create: `event-adapter/examples/flow-visualizer/web/live-source.test.mjs`
- Create: `event-adapter/examples/flow-visualizer/web/live-source.js`

- [ ] **Step 1: Write SSE lifecycle tests with a fake EventSource**

```js
// web/live-source.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { LiveSource } from "./live-source.js";

class FakeEventSource {
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    FakeEventSource.instances.push(this);
  }
  close() { this.closed = true; }
}

test("reports connection lifecycle and parsed events", () => {
  const states = [];
  const events = [];
  const source = new LiveSource("/events", {
    EventSourceClass: FakeEventSource,
    onState: state => states.push(state),
    onEvent: event => events.push(event),
  });
  source.connect();
  const raw = FakeEventSource.instances.at(-1);
  assert.equal(raw.url, "/events");
  raw.readyState = 1;
  raw.onopen();
  raw.onmessage({ data: '{"requestId":"r1","event":"sent"}' });
  assert.deepEqual(states, ["connecting", "live"]);
  assert.equal(events[0].requestId, "r1");
});

test("ignores malformed JSON, reports reconnecting, and closes", () => {
  const states = [];
  const diagnostics = [];
  const source = new LiveSource("/events", {
    EventSourceClass: FakeEventSource,
    onState: state => states.push(state),
    onDiagnostic: message => diagnostics.push(message),
  });
  source.connect();
  const raw = FakeEventSource.instances.at(-1);
  raw.onmessage({ data: "{" });
  raw.readyState = 0;
  raw.onerror();
  source.close();
  assert.deepEqual(states, ["connecting", "reconnecting", "disconnected"]);
  assert.equal(diagnostics.length, 1);
  assert.equal(raw.closed, true);
});
```

- [ ] **Step 2: Run the test and confirm the expected failure**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/live-source.test.mjs
```

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for `web/live-source.js`.

- [ ] **Step 3: Implement the EventSource adapter**

```js
// web/live-source.js
export class LiveSource {
  constructor(url, options = {}) {
    this.url = url;
    this.EventSourceClass = options.EventSourceClass ?? globalThis.EventSource;
    this.onState = options.onState ?? (() => {});
    this.onEvent = options.onEvent ?? (() => {});
    this.onDiagnostic = options.onDiagnostic ?? (() => {});
    this.source = null;
  }

  connect() {
    this.close(false);
    this.onState("connecting");
    this.source = new this.EventSourceClass(this.url);
    this.source.onopen = () => this.onState("live");
    this.source.onmessage = message => {
      try {
        this.onEvent(JSON.parse(message.data));
      } catch {
        this.onDiagnostic("ignored malformed SSE event");
      }
    };
    this.source.onerror = () => {
      this.onState(this.source?.readyState === 2 ? "disconnected" : "reconnecting");
    };
  }

  close(report = true) {
    this.source?.close();
    this.source = null;
    if (report) this.onState("disconnected");
  }
}
```

- [ ] **Step 4: Run SSE transport tests**

Run:

```sh
cd event-adapter
node --test examples/flow-visualizer/web/live-source.test.mjs
```

Expected: all SSE lifecycle tests PASS.

- [ ] **Step 5: Commit the transport**

```sh
git add event-adapter/examples/flow-visualizer/web/live-source.js \
  event-adapter/examples/flow-visualizer/web/live-source.test.mjs
git commit -m "feat(event-adapter): consume flow progress over SSE"
```

### Task 5: Embeddable `<event-flow>` Component and Styling

**Files:**
- Create: `event-adapter/examples/flow-visualizer/web/event-flow.js`
- Create: `event-adapter/examples/flow-visualizer/web/event-flow.css`
- Create: `event-adapter/examples/flow-visualizer/web/index.html`

- [ ] **Step 1: Implement the custom element composition boundary**

```js
// web/event-flow.js
import { normalizeConfig } from "./config.js";
import { createTrace, reduceLiveEvent } from "./state.js";
import { renderFlow, escapeHTML } from "./render.js";
import { LiveSource } from "./live-source.js";

const stylesheetURL = new URL("./event-flow.css", import.meta.url);

export class EventFlowElement extends HTMLElement {
  static observedAttributes = ["config-url", "request-id"];

  constructor() {
    super();
    this.attachShadow({ mode: "open" });
    this.connection = "disconnected";
    this.config = null;
    this.trace = null;
    this.liveSource = null;
  }

  connectedCallback() { this.load(); }
  disconnectedCallback() { this.liveSource?.close(false); }
  attributeChangedCallback(name, oldValue, newValue) {
    if (oldValue !== newValue && this.isConnected) this.load();
  }

  async load() {
    this.liveSource?.close(false);
    const configURL = this.getAttribute("config-url");
    const requestID = this.getAttribute("request-id");
    if (!configURL || !requestID) {
      this.renderError("config-url and request-id are required");
      return;
    }
    try {
      const response = await fetch(configURL);
      if (!response.ok) throw new Error(`configuration request returned ${response.status}`);
      this.config = normalizeConfig(await response.json());
      this.trace = createTrace(this.config, requestID);
      this.connection = "connecting";
      this.render();
      this.liveSource = new LiveSource(this.config.source.url, {
        onState: state => { this.connection = state; this.render(); },
        onEvent: event => {
          this.trace = reduceLiveEvent(this.trace, event, this.config);
          this.render();
        },
        onDiagnostic: message => console.debug(`[event-flow] ${message}`),
      });
      this.liveSource.connect();
    } catch (error) {
      this.renderError(error instanceof Error ? error.message : String(error));
    }
  }

  render() {
    this.shadowRoot.innerHTML = `<link rel="stylesheet" href="${stylesheetURL}">
      ${renderFlow(this.config, this.trace, this.connection)}`;
  }

  renderError(message) {
    this.shadowRoot.innerHTML = `<link rel="stylesheet" href="${stylesheetURL}">
      <p class="setup-error" role="alert"><strong>Flow setup error:</strong> ${escapeHTML(message)}</p>`;
  }
}

if (!customElements.get("event-flow")) {
  customElements.define("event-flow", EventFlowElement);
}
```

- [ ] **Step 2: Add responsive, ownership-aware, reduced-motion CSS**

```css
/* web/event-flow.css */
:host {
  --flow-platform: #2563eb;
  --flow-application: #16a34a;
  --flow-caller: #7c3aed;
  --flow-failed: #dc2626;
  --flow-border: #cbd5e1;
  --flow-surface: #ffffff;
  display: block;
  color: #0f172a;
  font: 16px/1.45 system-ui, sans-serif;
}
* { box-sizing: border-box; }
.event-flow { background: var(--flow-surface); border: 1px solid var(--flow-border); border-radius: 16px; padding: 24px; }
header { display: flex; justify-content: space-between; gap: 16px; align-items: start; }
h2, h3, p { margin-top: 0; }
.eyebrow { margin-bottom: 4px; color: #475569; font-size: .75rem; font-weight: 700; letter-spacing: .08em; text-transform: uppercase; }
.connection { border-radius: 999px; font-weight: 700; padding: 6px 10px; }
.connection.state-live { background: #dcfce7; color: #166534; }
.connection.state-connecting, .connection.state-reconnecting { background: #fef3c7; color: #92400e; }
.connection.state-disconnected { background: #fee2e2; color: #991b1b; }
.request-id { color: #475569; }
.comparison { align-items: stretch; display: grid; gap: 12px; grid-template-columns: 1fr auto 1fr; margin: 20px 0; }
.comparison > div:not(.comparison-arrow) { border-radius: 12px; padding: 16px; }
.comparison h3 { font-size: .9rem; text-transform: uppercase; }
.comparison-before { background: #fef2f2; }
.comparison-before ul { display: flex; flex-wrap: wrap; gap: 6px; list-style: none; margin: 0; padding: 0; }
.comparison-before li { background: white; border: 1px solid #fecaca; border-radius: 6px; padding: 5px 8px; }
.comparison-arrow { align-self: center; font-size: 1.5rem; }
.comparison-after { background: #f0fdf4; }
.flow-lanes { display: grid; gap: 12px; }
.flow-lane { display: grid; grid-template-columns: 150px 1fr; gap: 14px; align-items: start; }
.flow-lane ol { display: grid; gap: 8px; list-style: none; margin: 0; padding: 0; }
.flow-step { align-items: start; border: 1px solid var(--flow-border); border-left: 5px solid #94a3b8; border-radius: 10px; display: grid; gap: 10px; grid-template-columns: 24px 24px 1fr; padding: 12px; }
.flow-step[data-owner="platform"] { border-left-color: var(--flow-platform); }
.flow-step[data-owner="application"] { border-left-color: var(--flow-application); }
.flow-step[data-owner="caller"] { border-left-color: var(--flow-caller); }
.flow-step.state-failed { border-left-color: var(--flow-failed); background: #fef2f2; }
.flow-step.state-completed { background: #f8fafc; }
.step-number, .step-icon, .step-status { font-weight: 700; }
.step-copy, .step-copy > span { display: block; }
.step-status { color: #475569; font-size: .8rem; text-transform: uppercase; }
.step-detail { display: flex; gap: 12px; margin: 6px 0 0; }
.step-detail div { display: flex; gap: 4px; }
.step-detail dt { color: #64748b; }
.step-detail dd { margin: 0; }
.state-active .step-icon { animation: pulse 1s ease-in-out infinite; }
.setup-error { background: #fef2f2; border: 1px solid #fca5a5; border-radius: 10px; color: #991b1b; padding: 16px; }
@keyframes pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .state-active .step-icon { animation: none; } }
@media (max-width: 720px) {
  .event-flow { padding: 16px; }
  header, .flow-lane, .comparison { display: block; }
  .comparison-arrow { margin: 6px; text-align: center; transform: rotate(90deg); }
  .connection { display: inline-block; }
}
```

- [ ] **Step 3: Add the standalone preview shell**

```html
<!-- web/index.html -->
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Event-adapter request flow</title>
  <style>
    body { background:#e2e8f0; margin:0; padding:32px; }
    main { margin:auto; max-width:1200px; }
  </style>
</head>
<body>
  <main>
    <event-flow config-url="/config.json" request-id="req-demo-001"></event-flow>
  </main>
  <script type="module" src="/event-flow.js"></script>
</body>
</html>
```

- [ ] **Step 4: Run all JavaScript tests to catch composition regressions**

Run:

```sh
cd event-adapter
npm --prefix examples/flow-visualizer test
```

Expected: all current JavaScript tests PASS.

- [ ] **Step 5: Commit the component shell**

```sh
git add event-adapter/examples/flow-visualizer/web/event-flow.js \
  event-adapter/examples/flow-visualizer/web/event-flow.css \
  event-adapter/examples/flow-visualizer/web/index.html
git commit -m "feat(event-adapter): add embeddable event flow component"
```

### Task 6: Request-Reply Preset and Full Replay Test

**Files:**
- Create: `event-adapter/examples/flow-visualizer/request-reply.yaml`
- Create: `event-adapter/examples/flow-visualizer/fixtures/request-reply.jsonl`
- Create: `event-adapter/examples/flow-visualizer/web/replay.test.mjs`

- [ ] **Step 1: Add the approved eight-step configuration**

```yaml
version: 1
title: Event-adapter request-reply
comparison:
  beforeLabel: "Before: every application owns"
  before: [NATS client, CloudEvents, Routing, HTTP translation, Reply envelopes, Error mapping]
  afterLabel: "After: application owns"
  after: One local HTTP business handler
source:
  transport: sse
  url: /api/demo/events
  correlationField: requestId
lanes:
  - { id: caller, label: Caller, owner: caller }
  - { id: nats, label: NATS, owner: platform }
  - { id: adapter, label: Event-adapter, owner: platform }
  - { id: application, label: Application, owner: application }
steps:
  - id: request-sent
    order: 1
    lane: caller
    label: Send NATS request
    description: Publish to the request subject with a reply inbox
    completeWhen: { event: demo.request_sent }
    failWhen: { event: demo.request_failed }
    detailFields: [subject]
  - id: responder-selected
    order: 2
    lane: nats
    label: Deliver to one responder
    description: Queue group selects one adapter instance
    completeWhen: { event: nats.responder_selected }
    detailFields: [queueGroup]
  - id: validate
    order: 3
    lane: adapter
    label: Validate CloudEvent
    startWhen: { event: adapter.validation_started }
    completeWhen: { event: adapter.validated }
    failWhen: { event: adapter.validation_failed }
    detailFields: [eventType]
  - id: route
    order: 4
    lane: adapter
    label: Match request route
    completeWhen: { event: adapter.route_matched }
    failWhen: { event: adapter.route_failed }
    detailFields: [route]
  - id: dispatch
    order: 5
    lane: adapter
    label: Dispatch local HTTP
    startWhen: { event: adapter.dispatch_started }
    completeWhen: { event: adapter.dispatch_completed }
    failWhen: { event: adapter.dispatch_failed }
    detailFields: [method, path]
  - id: handler
    order: 6
    lane: application
    label: Run business handler
    startWhen: { event: application.handler_started }
    completeWhen: { event: application.handler_completed }
    failWhen: { event: application.handler_failed }
    detailFields: [httpStatus]
  - id: build-reply
    order: 7
    lane: adapter
    label: Build reply CloudEvent
    completeWhen: { event: adapter.reply_built }
    failWhen: { event: adapter.reply_failed }
    detailFields: [httpStatus]
  - id: reply-received
    order: 8
    lane: caller
    label: Return reply
    completeWhen: { event: demo.reply_received }
    failWhen: { event: demo.reply_failed }
    detailFields: [httpStatus, elapsedMs]
```

- [ ] **Step 2: Add a deterministic JSON Lines trace**

```jsonl
{"requestId":"req-demo-001","event":"demo.request_sent","status":"completed","timestamp":"2026-07-23T10:00:00.000Z","detail":{"subject":"q.orders.request"}}
{"requestId":"req-demo-001","event":"nats.responder_selected","status":"completed","timestamp":"2026-07-23T10:00:00.010Z","detail":{"queueGroup":"order-responders"}}
{"requestId":"req-demo-001","event":"adapter.validation_started","status":"started","timestamp":"2026-07-23T10:00:00.012Z","detail":{}}
{"requestId":"req-demo-001","event":"adapter.validated","status":"completed","timestamp":"2026-07-23T10:00:00.014Z","detail":{"eventType":"com.workspace.order.create.request"}}
{"requestId":"req-demo-001","event":"adapter.route_matched","status":"completed","timestamp":"2026-07-23T10:00:00.016Z","detail":{"route":"order-create"}}
{"requestId":"req-demo-001","event":"adapter.dispatch_started","status":"started","timestamp":"2026-07-23T10:00:00.018Z","detail":{"method":"POST","path":"/requests/orders"}}
{"requestId":"req-demo-001","event":"application.handler_started","status":"started","timestamp":"2026-07-23T10:00:00.020Z","detail":{}}
{"requestId":"req-demo-001","event":"application.handler_completed","status":"completed","timestamp":"2026-07-23T10:00:00.046Z","detail":{"httpStatus":200}}
{"requestId":"req-demo-001","event":"adapter.dispatch_completed","status":"completed","timestamp":"2026-07-23T10:00:00.047Z","detail":{"method":"POST","path":"/requests/orders"}}
{"requestId":"req-demo-001","event":"adapter.reply_built","status":"completed","timestamp":"2026-07-23T10:00:00.049Z","detail":{"httpStatus":200}}
{"requestId":"req-demo-001","event":"demo.reply_received","status":"completed","timestamp":"2026-07-23T10:00:00.052Z","detail":{"httpStatus":200,"elapsedMs":52}}
```

- [ ] **Step 3: Write the end-to-end reducer/render replay test**

```js
// web/replay.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { normalizeConfig } from "./config.js";
import { createTrace, reduceLiveEvent } from "./state.js";
import { renderFlow } from "./render.js";

test("replays the normalized request-reply preset to eight completed steps", async () => {
  const config = normalizeConfig(JSON.parse(
    await fs.readFile(new URL("./generated-request-reply.json", import.meta.url), "utf8"),
  ));
  const events = (await fs.readFile(new URL("../fixtures/request-reply.jsonl", import.meta.url), "utf8"))
    .trim().split("\n").map(line => JSON.parse(line));
  const trace = events.reduce(
    (current, event) => reduceLiveEvent(current, event, config),
    createTrace(config, "req-demo-001"),
  );
  assert.equal([...trace.steps.values()].filter(step => step.status === "completed").length, 8);
  const html = renderFlow(config, trace, "live");
  assert.match(html, /Request: <code>req-demo-001<\/code>/);
  assert.equal((html.match(/state-completed/g) ?? []).length, 8);
  assert.match(html, /elapsedMs/);
});
```

The Go config-normalization test in Task 7 writes
`web/generated-request-reply.json` before JavaScript tests. Add that generated
path to `event-adapter/examples/flow-visualizer/.gitignore` so source control
keeps only the YAML preset.

- [ ] **Step 4: Commit the preset and replay test**

```sh
git add event-adapter/examples/flow-visualizer/request-reply.yaml \
  event-adapter/examples/flow-visualizer/fixtures/request-reply.jsonl \
  event-adapter/examples/flow-visualizer/web/replay.test.mjs \
  event-adapter/examples/flow-visualizer/.gitignore
git commit -m "test(event-adapter): add request flow replay fixture"
```

### Task 7: YAML/JSON Normalization and Preview Server

**Files:**
- Create: `event-adapter/examples/flow-visualizer/config.go`
- Create: `event-adapter/examples/flow-visualizer/config_test.go`
- Create: `event-adapter/examples/flow-visualizer/main.go`
- Create: `event-adapter/examples/flow-visualizer/main_test.go`
- Modify: `event-adapter/examples/flow-visualizer/package.json`

- [ ] **Step 1: Write failing configuration normalization tests**

```go
// config_test.go
package main

import (
	"encoding/json"
	"testing"
)

func TestNormalizeConfigYAMLAndJSON(t *testing.T) {
	t.Parallel()
	yamlInput := []byte("version: 1\ntitle: Demo\nsource:\n  transport: sse\n  url: /events\n  correlationField: requestId\nlanes: []\nsteps: []\n")
	jsonInput := []byte(`{"version":1,"title":"Demo","source":{"transport":"sse","url":"/events","correlationField":"requestId"},"lanes":[],"steps":[]}`)
	gotYAML, err := normalizeConfigBytes("flow.yaml", yamlInput)
	if err != nil { t.Fatal(err) }
	gotJSON, err := normalizeConfigBytes("flow.json", jsonInput)
	if err != nil { t.Fatal(err) }
	var y, j any
	if err := json.Unmarshal(gotYAML, &y); err != nil { t.Fatal(err) }
	if err := json.Unmarshal(gotJSON, &j); err != nil { t.Fatal(err) }
	if string(gotYAML) != string(gotJSON) {
		t.Fatalf("normalized output differs:\nyaml %s\njson %s", gotYAML, gotJSON)
	}
}

func TestNormalizeConfigRejectsUnknownExtensionAndMalformedInput(t *testing.T) {
	t.Parallel()
	if _, err := normalizeConfigBytes("flow.txt", []byte("{}")); err == nil {
		t.Fatal("expected extension error")
	}
	if _, err := normalizeConfigBytes("flow.yaml", []byte(":\n")); err == nil {
		t.Fatal("expected YAML parse error")
	}
}

```

- [ ] **Step 2: Run the Go test and confirm the expected failure**

Run:

```sh
cd event-adapter
go test ./examples/flow-visualizer -run TestNormalizeConfig -v
```

Expected: FAIL because `normalizeConfigBytes` is undefined.

- [ ] **Step 3: Implement YAML/JSON normalization**

```go
// config.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func normalizeConfigBytes(name string, input []byte) ([]byte, error) {
	var value any
	switch filepath.Ext(name) {
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(input))
		decoder.KnownFields(false)
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode YAML: %w", err)
		}
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(input))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
	default:
		return nil, fmt.Errorf("config must use .yaml, .yml, or .json")
	}
	output, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode normalized JSON: %w", err)
	}
	return output, nil
}
```

- [ ] **Step 4: Write failing preview-server tests**

```go
// main_test.go
package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPreviewHandlerServesAssetsConfigAndSSE(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<h1>preview</h1>")},
		"event-flow.js": {Data: []byte("export {};")},
	}
	config := []byte(`{"version":1}`)
	fixture := []byte("{\"requestId\":\"req-demo-001\",\"event\":\"demo.request_sent\"}\n")
	server := httptest.NewServer(newPreviewHandler(assets, config, fixture, 0))
	t.Cleanup(server.Close)

	for path, want := range map[string][2]string{
		"/":                {"text/html", "preview"},
		"/event-flow.js":   {"text/javascript", "export"},
		"/config.json":     {"application/json", `"version":1`},
	} {
		contentType, body := want[0], want[1]
		response, err := http.Get(server.URL + path)
		if err != nil { t.Fatal(err) }
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK { t.Fatalf("%s status = %d", path, response.StatusCode) }
		if !strings.Contains(response.Header.Get("Content-Type"), contentType) { t.Fatalf("%s content type = %q", path, response.Header.Get("Content-Type")) }
		if !strings.Contains(string(data), body) { t.Fatalf("%s body = %q", path, data) }
	}

	response, err := http.Get(server.URL + "/api/demo/events")
	if err != nil { t.Fatal(err) }
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(line, "data: ") { t.Fatalf("SSE line = %q", line) }
}
```

- [ ] **Step 5: Implement the preview handler and command**

```go
// main.go
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/*
var embedded embed.FS

func newPreviewHandler(assets fs.FS, config, fixture []byte, replayDelay time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(config)
	})
	mux.HandleFunc("/api/demo/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok { http.Error(w, "streaming unsupported", http.StatusInternalServerError); return }
		for _, line := range strings.Split(strings.TrimSpace(string(fixture)), "\n") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(replayDelay):
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	})
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return mux
}

func run() error {
	var listen, configPath, fixturePath, normalizeOutput string
	var replayDelay time.Duration
	flag.StringVar(&listen, "listen", "0.0.0.0:8080", "preview listen address")
	flag.StringVar(&configPath, "config", "request-reply.yaml", "YAML or JSON flow config")
	flag.StringVar(&fixturePath, "fixture", "fixtures/request-reply.jsonl", "JSONL SSE replay fixture")
	flag.StringVar(&normalizeOutput, "normalize-output", "", "write normalized JSON and exit")
	flag.DurationVar(&replayDelay, "replay-delay", 350*time.Millisecond, "delay between replayed events")
	flag.Parse()

	input, err := os.ReadFile(configPath)
	if err != nil { return fmt.Errorf("read config: %w", err) }
	config, err := normalizeConfigBytes(configPath, input)
	if err != nil { return err }
	if normalizeOutput != "" {
		if err := os.WriteFile(normalizeOutput, config, 0o600); err != nil {
			return fmt.Errorf("write normalized config: %w", err)
		}
		return nil
	}
	fixture, err := os.ReadFile(fixturePath)
	if err != nil { return fmt.Errorf("read fixture: %w", err) }
	assets, err := fs.Sub(embedded, "web")
	if err != nil { return err }
	log.Printf("flow visualizer listening on http://%s", listen)
	return http.ListenAndServe(listen, newPreviewHandler(assets, config, fixture, replayDelay))
}

func main() {
	if err := run(); err != nil { log.Fatal(err) }
}
```

- [ ] **Step 6: Wire deterministic preset normalization into JavaScript tests**

Update `package.json` to:

```json
{
  "name": "event-adapter-flow-visualizer",
  "private": true,
  "type": "module",
  "scripts": {
    "pretest": "go run . --config request-reply.yaml --normalize-output web/generated-request-reply.json",
    "test": "node --test web/*.test.mjs"
  }
}
```

- [ ] **Step 7: Run Go tests, normalize the preset, and run JavaScript tests**

Run:

```sh
cd event-adapter
go test ./examples/flow-visualizer -v
npm --prefix examples/flow-visualizer test
```

Expected: Go tests PASS; npm's pretest writes the ignored normalized JSON preset;
all JavaScript tests PASS.

- [ ] **Step 8: Commit the preview server**

```sh
git add event-adapter/examples/flow-visualizer/config.go \
  event-adapter/examples/flow-visualizer/config_test.go \
  event-adapter/examples/flow-visualizer/main.go \
  event-adapter/examples/flow-visualizer/main_test.go \
  event-adapter/examples/flow-visualizer/package.json
git commit -m "feat(event-adapter): serve flow visualizer preview"
```

### Task 8: Integration Documentation and Demo Instructions

**Files:**
- Create: `event-adapter/examples/flow-visualizer/README.md`
- Modify: `event-adapter/README.md`

- [ ] **Step 1: Document the existing-demo integration contract**

Add this content to `examples/flow-visualizer/README.md`:

```markdown
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
```

- [ ] **Step 2: Link the example from the event-adapter README**

Add under the request-reply overview in `event-adapter/README.md`:

```markdown
For PM demos, [`examples/flow-visualizer`](examples/flow-visualizer/) provides a
configurable live request-reply processing trace driven by correlated SSE events.
```

- [ ] **Step 3: Verify documentation paths and commands**

Run:

```sh
cd event-adapter
test -f examples/flow-visualizer/README.md
test -f examples/flow-visualizer/web/event-flow.js
go run ./examples/flow-visualizer --listen 127.0.0.1:18080
```

Expected: files exist and the server logs
`flow visualizer listening on http://127.0.0.1:18080`. Stop it with Ctrl-C after
loading `/`, `/config.json`, and `/api/demo/events` successfully.

- [ ] **Step 4: Commit documentation**

```sh
git add event-adapter/examples/flow-visualizer/README.md event-adapter/README.md
git commit -m "docs(event-adapter): explain request flow demo integration"
```

### Task 9: Full Verification

**Files:**
- Modify only files required to fix failures found by the checks below.

- [ ] **Step 1: Format Go files**

Run:

```sh
cd event-adapter
gofmt -w examples/flow-visualizer/*.go
```

Expected: command exits 0.

- [ ] **Step 2: Run all JavaScript tests**

Run:

```sh
cd event-adapter
npm --prefix examples/flow-visualizer test
```

Expected: all configuration, reducer, renderer, transport, and replay tests PASS.

- [ ] **Step 3: Run the module standard check**

Run:

```sh
cd event-adapter
go build ./...
go vet ./...
go test ./...
test -z "$(gofmt -l .)"
```

Expected: all four commands exit 0.

- [ ] **Step 4: Run a live preview smoke test on a non-demo port**

Run:

```sh
cd event-adapter
go run ./examples/flow-visualizer --listen 127.0.0.1:18080
```

In a second terminal:

```sh
curl -fsS http://127.0.0.1:18080/config.json
curl -fsS http://127.0.0.1:18080/ | grep -F '<event-flow'
curl -N --max-time 2 http://127.0.0.1:18080/api/demo/events | grep -F 'data:'
```

Expected: normalized JSON config, the component element, and SSE `data:` lines
are returned. Stop the preview server after the checks.

- [ ] **Step 5: Inspect final scope**

Run:

```sh
git status --short
git diff --stat HEAD~5..HEAD
git log -5 --oneline
```

Expected: only flow-visualizer example files and the README link changed; no
production request-reply packages changed.

- [ ] **Step 6: Commit any verification-only corrections**

If verification required corrections:

```sh
git add event-adapter/examples/flow-visualizer event-adapter/README.md
git commit -m "fix(event-adapter): complete flow visualizer verification"
```

If no files changed, do not create an empty commit.
