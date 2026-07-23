import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const originalGlobals = {
  HTMLElement: globalThis.HTMLElement,
  customElements: globalThis.customElements,
  fetch: globalThis.fetch,
};

class FakeHTMLElement {
  constructor() {
    this.attributes = new Map();
    this.isConnected = false;
    this.shadowRoot = null;
  }

  attachShadow() {
    this.shadowRoot = { innerHTML: '' };
    return this.shadowRoot;
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  setAttribute(name, value) {
    const previous = this.getAttribute(name);
    this.attributes.set(name, String(value));
    if (this.constructor.observedAttributes?.includes(name)) {
      this.attributeChangedCallback(name, previous, String(value));
    }
  }
}

function installDOM() {
  const definitions = new Map();
  globalThis.HTMLElement = FakeHTMLElement;
  globalThis.customElements = {
    define: (name, constructor) => definitions.set(name, constructor),
    get: (name) => definitions.get(name),
  };
  return definitions;
}

function validConfig(sourceURL = '/live-events') {
  return {
    version: 1,
    title: 'Request flow',
    comparison: {
      beforeLabel: 'Before', before: ['Caller manages retries'], afterLabel: 'After', after: 'The platform dispatches it',
    },
    source: { transport: 'sse', url: sourceURL, correlationField: 'requestid' },
    lanes: [
      { id: 'caller', label: 'Caller', owner: 'caller' },
      { id: 'platform', label: 'Platform', owner: 'platform' },
      { id: 'application', label: 'Application', owner: 'application' },
    ],
    steps: [
      { id: 'request', order: 1, lane: 'caller', label: 'Send request', startWhen: { event: 'request.started' } },
      { id: 'dispatch', order: 2, lane: 'application', label: 'Dispatch handler', completeWhen: { event: 'dispatch.completed' } },
    ],
  };
}

function deferred() {
  let resolve;
  const promise = new Promise((result) => { resolve = result; });
  return { promise, resolve };
}

async function loadComponent() {
  installDOM();
  return import(`./event-flow.js?test=${Math.random()}`);
}

function createSourceClass() {
  const instances = [];
  class FakeLiveSource {
    constructor(url, options) {
      this.url = url;
      this.options = options;
      this.closed = false;
      instances.push(this);
    }

    connect() {
      this.options.onState('connecting');
    }

    close() {
      this.closed = true;
    }
  }
  return { FakeLiveSource, instances };
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

test.after(() => {
  Object.assign(globalThis, originalGlobals);
});

test('renders a setup alert when required attributes are missing', async () => {
  const { EventFlowElement } = await loadComponent();
  const element = new EventFlowElement();
  element.isConnected = true;
  element.connectedCallback();

  assert.match(element.shadowRoot.innerHTML, /class="setup-error" role="alert"/);
  assert.match(element.shadowRoot.innerHTML, /config-url and request-id/);
});

test('fetches, normalizes, traces, and connects to the configured live source', async () => {
  const { EventFlowElement } = await loadComponent();
  const { FakeLiveSource, instances } = createSourceClass();
  EventFlowElement.LiveSourceClass = FakeLiveSource;
  globalThis.fetch = async (url) => ({ ok: true, json: async () => validConfig('/events?stream=flow') });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'req-42');
  element.isConnected = true;
  element.connectedCallback();
  await flush();

  assert.equal(instances.length, 1);
  assert.equal(instances[0].url, '/events?stream=flow');
  assert.equal(element.trace.requestID, 'req-42');
  assert.match(element.shadowRoot.innerHTML, /Request flow/);
  assert.match(element.shadowRoot.innerHTML, /Connecting/);
});

test('rerenders when the live source reports a connection state', async () => {
  const { EventFlowElement } = await loadComponent();
  const { FakeLiveSource, instances } = createSourceClass();
  EventFlowElement.LiveSourceClass = FakeLiveSource;
  globalThis.fetch = async () => ({ ok: true, json: async () => validConfig() });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'req-42');
  element.isConnected = true;
  element.connectedCallback();
  await flush();
  instances[0].options.onState('live');

  assert.match(element.shadowRoot.innerHTML, /role="status"[^>]*>Live</);
});

test('reduces live events and rerenders the mapped step', async () => {
  const { EventFlowElement } = await loadComponent();
  const { FakeLiveSource, instances } = createSourceClass();
  EventFlowElement.LiveSourceClass = FakeLiveSource;
  globalThis.fetch = async () => ({ ok: true, json: async () => validConfig() });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'req-42');
  element.isConnected = true;
  element.connectedCallback();
  await flush();
  instances[0].options.onEvent({
    event: 'dispatch.completed', status: 'completed', requestid: 'req-42', timestamp: '2026-07-23T12:00:00Z',
  });

  assert.match(element.shadowRoot.innerHTML, /Dispatch handler: Completed/);
});

test('reloads for a changed request ID, closing the previous source and resetting trace state', async () => {
  const { EventFlowElement } = await loadComponent();
  const { FakeLiveSource, instances } = createSourceClass();
  EventFlowElement.LiveSourceClass = FakeLiveSource;
  globalThis.fetch = async () => ({ ok: true, json: async () => validConfig() });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'first');
  element.isConnected = true;
  element.connectedCallback();
  await flush();
  instances[0].options.onEvent({ event: 'dispatch.completed', status: 'completed', requestid: 'first', timestamp: '2026-07-23T12:00:00Z' });
  element.setAttribute('request-id', 'second');
  await flush();

  assert.equal(instances.length, 2);
  assert.equal(instances[0].closed, true);
  assert.equal(element.trace.requestID, 'second');
  assert.equal(element.trace.steps.get('dispatch').status, 'waiting');
});

test('disconnect closes the source and invalidates its pending load', async () => {
  const { EventFlowElement } = await loadComponent();
  const { FakeLiveSource, instances } = createSourceClass();
  EventFlowElement.LiveSourceClass = FakeLiveSource;
  const pending = deferred();
  globalThis.fetch = () => pending.promise;
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'req-42');
  element.isConnected = true;
  element.connectedCallback();
  element.disconnectedCallback();
  element.isConnected = false;
  pending.resolve({ ok: true, json: async () => validConfig() });
  await flush();

  assert.equal(instances.length, 0);
  assert.equal(element.source, null);
});

test('does not let a stale config fetch replace a newer attribute set', async () => {
  const { EventFlowElement } = await loadComponent();
  const { FakeLiveSource, instances } = createSourceClass();
  EventFlowElement.LiveSourceClass = FakeLiveSource;
  const first = deferred();
  globalThis.fetch = (url) => url === '/first.json'
    ? first.promise
    : Promise.resolve({ ok: true, json: async () => validConfig('/second-events') });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/first.json');
  element.setAttribute('request-id', 'first');
  element.isConnected = true;
  element.connectedCallback();
  element.setAttribute('config-url', '/second.json');
  element.setAttribute('request-id', 'second');
  await flush();
  first.resolve({ ok: true, json: async () => validConfig('/first-events') });
  await flush();

  assert.equal(instances.length, 1);
  assert.equal(instances[0].url, '/second-events');
  assert.equal(element.trace.requestID, 'second');
});

test('renders an escaped setup alert for a failed config fetch', async () => {
  const { EventFlowElement } = await loadComponent();
  globalThis.fetch = async () => ({ ok: false, status: '<503>' });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'req-42');
  element.isConnected = true;
  element.connectedCallback();
  await flush();

  assert.match(element.shadowRoot.innerHTML, /class="setup-error" role="alert"/);
  assert.match(element.shadowRoot.innerHTML, /&lt;503&gt;/);
  assert.equal(element.source, null);
});

test('closes a source that fails to connect and renders its escaped setup error', async () => {
  const { EventFlowElement } = await loadComponent();
  let failedSource;
  class FailingLiveSource {
    constructor() {
      this.closed = false;
      failedSource = this;
    }

    connect() {
      throw new Error('live source <unavailable>');
    }

    close() {
      this.closed = true;
    }
  }
  EventFlowElement.LiveSourceClass = FailingLiveSource;
  globalThis.fetch = async () => ({ ok: true, json: async () => validConfig() });
  const element = new EventFlowElement();
  element.setAttribute('config-url', '/config.json');
  element.setAttribute('request-id', 'req-42');
  element.isConnected = true;

  await element.load();

  assert.equal(failedSource.closed, true);
  assert.equal(element.source, null);
  assert.match(element.shadowRoot.innerHTML, /class="setup-error" role="alert"/);
  assert.match(element.shadowRoot.innerHTML, /live source &lt;unavailable&gt;/);
});

test('keeps step status text visibly available in the stylesheet', async () => {
  const css = await readFile(new URL('./event-flow.css', import.meta.url), 'utf8');
  const rule = css.match(/\.step-status-text\s*\{([^}]*)\}/)?.[1];

  assert.ok(rule, 'expected a .step-status-text CSS rule');
  assert.doesNotMatch(rule, /clip(?:-path)?\s*:/);
  assert.doesNotMatch(rule, /position\s*:\s*absolute/);
  assert.doesNotMatch(rule, /height\s*:\s*1px/);
});

test('registers once when custom elements are available and does not require them', async () => {
  const definitions = installDOM();
  const first = await import(`./event-flow.js?registration-first=${Math.random()}`);
  const second = await import(`./event-flow.js?registration-second=${Math.random()}`);
  assert.equal(definitions.get('event-flow'), first.EventFlowElement);
  assert.equal(definitions.get('event-flow'), first.EventFlowElement);
  assert.notEqual(second.EventFlowElement, first.EventFlowElement);

  delete globalThis.customElements;
  await import(`./event-flow.js?registration-without=${Math.random()}`);
});
