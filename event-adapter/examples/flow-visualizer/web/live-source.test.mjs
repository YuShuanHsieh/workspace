import assert from 'node:assert/strict';
import test from 'node:test';

import { LiveSource } from './live-source.js';

class FakeEventSource {
  static instances = [];

  constructor(url) {
    this.url = url;
    this.readyState = FakeEventSource.CONNECTING;
    this.closed = false;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }
}

FakeEventSource.CONNECTING = 0;
FakeEventSource.OPEN = 1;
FakeEventSource.CLOSED = 2;

function createLiveSource() {
  FakeEventSource.instances = [];
  const calls = [];
  const source = new LiveSource('/live-events', {
    EventSourceClass: FakeEventSource,
    onState: (state) => calls.push(['state', state]),
    onEvent: (event) => calls.push(['event', event]),
    onDiagnostic: (message) => calls.push(['diagnostic', message]),
  });
  return { source, calls };
}

test('connects to the configured URL and reports connection lifecycle in order', () => {
  const { source, calls } = createLiveSource();

  source.connect();
  const eventSource = FakeEventSource.instances[0];
  assert.equal(eventSource.url, '/live-events');
  assert.deepEqual(calls, [['state', 'connecting']]);

  eventSource.onopen();
  eventSource.onerror();
  eventSource.readyState = FakeEventSource.CLOSED;
  eventSource.onerror();

  assert.deepEqual(calls, [
    ['state', 'connecting'],
    ['state', 'live'],
    ['state', 'reconnecting'],
    ['state', 'disconnected'],
  ]);
});

test('delivers parsed SSE events and diagnoses malformed JSON without stopping later events', () => {
  const { source, calls } = createLiveSource();
  source.connect();
  const eventSource = FakeEventSource.instances[0];

  eventSource.onmessage({ data: '{"event":"dispatch.completed","attempt":1}' });
  eventSource.onmessage({ data: '{not json' });
  eventSource.onmessage({ data: '{"event":"reply.completed"}' });

  assert.deepEqual(calls, [
    ['state', 'connecting'],
    ['event', { event: 'dispatch.completed', attempt: 1 }],
    ['diagnostic', 'Received malformed JSON from live event source.'],
    ['event', { event: 'reply.completed' }],
  ]);
});

test('reconnect closes the old source without reporting a disconnection', () => {
  const { source, calls } = createLiveSource();
  source.connect();
  const first = FakeEventSource.instances[0];
  source.connect();
  const second = FakeEventSource.instances[1];

  assert.equal(first.closed, true);
  assert.notEqual(first, second);
  assert.deepEqual(calls, [['state', 'connecting'], ['state', 'connecting']]);
});

test('close reports disconnection by default and supports silent or repeated closes', () => {
  const { source, calls } = createLiveSource();

  source.close();
  source.connect();
  const first = FakeEventSource.instances[0];
  source.close();
  source.close();
  source.connect();
  const second = FakeEventSource.instances[1];
  source.close(false);

  assert.equal(first.closed, true);
  assert.equal(second.closed, true);
  assert.deepEqual(calls, [
    ['state', 'disconnected'],
    ['state', 'connecting'],
    ['state', 'disconnected'],
    ['state', 'disconnected'],
    ['state', 'connecting'],
  ]);
});
