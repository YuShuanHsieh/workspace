import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizeConfig } from './config.js';
import { createTrace, reduceLiveEvent } from './state.js';

function config() {
  return normalizeConfig({
    version: 1,
    title: 'Upload lifecycle',
    comparison: { beforeLabel: 'Before', before: ['Direct'], afterLabel: 'After', after: 'Sidecar' },
    source: { transport: 'sse', url: '/events', correlationField: 'correlationid' },
    lanes: [{ id: 'app', label: 'Application', owner: 'application' }],
    steps: [
      {
        id: 'dispatch', order: 1, lane: 'app', label: 'Dispatch',
        detailFields: ['attempt', 'httpstatus'],
        startWhen: { event: 'dispatch.started' },
        completeWhen: { event: 'dispatch.completed' },
        failWhen: { event: 'dispatch.failed' },
      },
      { id: 'reply', order: 2, lane: 'app', label: 'Reply', completeWhen: { event: 'reply.completed' } },
    ],
  });
}

function event(overrides = {}) {
  return {
    event: 'dispatch.completed', status: 'completed', timestamp: '2026-07-23T10:00:00.000Z',
    correlationid: 'request-1', detail: { attempt: 1, httpstatus: 200, secret: 'omit' },
    ...overrides,
  };
}

test('creates every configured step in the waiting state', () => {
  const trace = createTrace(config(), 'request-1');

  assert.equal(trace.requestID, 'request-1');
  assert.equal(trace.startedAt, null);
  assert.equal(trace.updatedAt, null);
  assert.deepEqual([...trace.steps.entries()], [
    ['dispatch', { status: 'waiting', timestamp: null, detail: {}, events: [] }],
    ['reply', { status: 'waiting', timestamp: null, detail: {}, events: [] }],
  ]);
});

test('records a matching completion with allowlisted details', () => {
  const cfg = config();
  const trace = reduceLiveEvent(createTrace(cfg, 'request-1'), event(), cfg);
  const step = trace.steps.get('dispatch');

  assert.equal(step.status, 'completed');
  assert.equal(step.timestamp, '2026-07-23T10:00:00.000Z');
  assert.deepEqual(step.detail, { attempt: 1, httpstatus: 200 });
  assert.deepEqual(step.events, [{
    event: 'dispatch.completed', status: 'completed', timestamp: '2026-07-23T10:00:00.000Z',
    detail: { attempt: 1, httpstatus: 200 },
  }]);
  assert.equal(trace.startedAt, '2026-07-23T10:00:00.000Z');
  assert.equal(trace.updatedAt, '2026-07-23T10:00:00.000Z');
});

test('handles active, completed, and failed statuses', () => {
  const cfg = config();
  const initial = createTrace(cfg, 'request-1');
  const active = reduceLiveEvent(initial, event({ event: 'dispatch.started', status: 'started' }), cfg);
  const completed = reduceLiveEvent(active, event({ event: 'dispatch.completed', status: 'completed', timestamp: '2026-07-23T10:01:00.000Z' }), cfg);
  const failed = reduceLiveEvent(completed, event({ event: 'dispatch.failed', status: 'failed', timestamp: '2026-07-23T10:02:00.000Z' }), cfg);

  assert.equal(active.steps.get('dispatch').status, 'active');
  assert.equal(completed.steps.get('dispatch').status, 'completed');
  assert.equal(failed.steps.get('dispatch').status, 'failed');
});

test('ignores events for another correlation ID, unknown events, malformed events, and invalid timestamps', () => {
  const cfg = config();
  const trace = createTrace(cfg, 'request-1');

  for (const input of [null, {}, event({ correlationid: 'other' }), event({ event: 'unknown' }), event({ timestamp: 'not-a-date' })]) {
    assert.equal(reduceLiveEvent(trace, input, cfg), trace);
  }
});

test('accepts strict RFC 3339 timestamps and rejects permissive Date.parse formats', () => {
  const cfg = config();
  const trace = createTrace(cfg, 'request-1');
  const valid = reduceLiveEvent(trace, event({ timestamp: '2026-07-23T10:00:00.123+05:30' }), cfg);

  assert.equal(valid.steps.get('dispatch').timestamp, '2026-07-23T10:00:00.123+05:30');
  for (const timestamp of ['2026-07-23', '2026-07-23T10:00:00', 'July 23, 2026', '2026-02-30T10:00:00Z']) {
    assert.equal(reduceLiveEvent(trace, event({ timestamp }), cfg), trace);
  }
});

test('ignores an event whose supplied status does not match its mapping', () => {
  const cfg = config();
  const trace = createTrace(cfg, 'request-1');

  assert.equal(reduceLiveEvent(trace, event({ event: 'dispatch.started', status: 'completed' }), cfg), trace);
  assert.equal(reduceLiveEvent(trace, event({ event: 'dispatch.started', status: 'active' }), cfg), trace);
  assert.equal(reduceLiveEvent(trace, event({ event: 'dispatch.completed', status: 'failed' }), cfg), trace);
  assert.equal(reduceLiveEvent(trace, event({ event: 'dispatch.failed', status: 'active' }), cfg), trace);
});

test('defensively copies allowlisted detail values', () => {
  const cfg = config();
  const input = event({ detail: { attempt: { number: 1 }, httpstatus: 200, secret: { token: 'no' } } });
  const trace = reduceLiveEvent(createTrace(cfg, 'request-1'), input, cfg);

  input.detail.attempt.number = 2;
  input.detail.secret.token = 'changed';
  assert.deepEqual(trace.steps.get('dispatch').detail, { attempt: { number: 1 }, httpstatus: 200 });
  assert.deepEqual(trace.steps.get('dispatch').events[0].detail, { attempt: { number: 1 }, httpstatus: 200 });
});

test('is idempotent for duplicate events and orders observations by timestamp', () => {
  const cfg = config();
  const completed = event();
  const earlierActive = event({ event: 'dispatch.started', status: 'started', timestamp: '2026-07-23T09:00:00.000Z', detail: { attempt: 0 } });
  const once = reduceLiveEvent(createTrace(cfg, 'request-1'), completed, cfg);
  const duplicate = reduceLiveEvent(once, completed, cfg);
  const reconciled = reduceLiveEvent(duplicate, earlierActive, cfg);

  assert.equal(duplicate, once);
  assert.equal(reconciled.steps.get('dispatch').status, 'completed');
  assert.deepEqual(reconciled.steps.get('dispatch').events.map((item) => item.timestamp), [
    '2026-07-23T09:00:00.000Z', '2026-07-23T10:00:00.000Z',
  ]);
  assert.equal(reconciled.startedAt, '2026-07-23T09:00:00.000Z');
  assert.equal(reconciled.updatedAt, '2026-07-23T10:00:00.000Z');
});

test('deduplicates semantically identical details with different property insertion order', () => {
  const cfg = config();
  const first = event({ detail: { attempt: 1, httpstatus: 200 } });
  const reordered = event({ detail: { httpstatus: 200, attempt: 1 } });
  const once = reduceLiveEvent(createTrace(cfg, 'request-1'), first, cfg);
  const duplicate = reduceLiveEvent(once, reordered, cfg);

  assert.equal(duplicate, once);
  assert.equal(once.steps.get('dispatch').events.length, 1);
});

test('uses later timestamps for equal-precedence detail without downgrading confirmed state', () => {
  const cfg = config();
  const first = reduceLiveEvent(createTrace(cfg, 'request-1'), event({ detail: { attempt: 1 } }), cfg);
  const later = reduceLiveEvent(first, event({ timestamp: '2026-07-23T11:00:00.000Z', detail: { attempt: 2 } }), cfg);
  const afterActive = reduceLiveEvent(later, event({ event: 'dispatch.started', status: 'started', timestamp: '2026-07-23T12:00:00.000Z' }), cfg);

  assert.equal(later.steps.get('dispatch').timestamp, '2026-07-23T11:00:00.000Z');
  assert.deepEqual(later.steps.get('dispatch').detail, { attempt: 2 });
  assert.equal(afterActive.steps.get('dispatch').status, 'completed');
  assert.equal(afterActive.steps.get('dispatch').timestamp, '2026-07-23T11:00:00.000Z');
});

test('preserves unaffected step references while reducing multiple steps', () => {
  const cfg = config();
  const initial = createTrace(cfg, 'request-1');
  const dispatch = reduceLiveEvent(initial, event(), cfg);
  const reply = reduceLiveEvent(dispatch, event({ event: 'reply.completed', status: 'completed', timestamp: '2026-07-23T10:01:00.000Z', detail: {} }), cfg);

  assert.notEqual(dispatch, initial);
  assert.equal(dispatch.steps.get('reply'), initial.steps.get('reply'));
  assert.equal(reply.steps.get('dispatch'), dispatch.steps.get('dispatch'));
  assert.equal(reply.steps.get('reply').status, 'completed');
  assert.equal(reply.startedAt, '2026-07-23T10:00:00.000Z');
  assert.equal(reply.updatedAt, '2026-07-23T10:01:00.000Z');
});

test('keeps concurrent request traces isolated', () => {
  const cfg = config();
  const first = createTrace(cfg, 'request-1');
  const second = createTrace(cfg, 'request-2');
  const updatedFirst = reduceLiveEvent(first, event(), cfg);
  const updatedSecond = reduceLiveEvent(second, event({ correlationid: 'request-2', event: 'dispatch.failed', status: 'failed' }), cfg);

  assert.equal(updatedFirst.steps.get('dispatch').status, 'completed');
  assert.equal(updatedSecond.steps.get('dispatch').status, 'failed');
  assert.equal(first.steps.get('dispatch').status, 'waiting');
  assert.equal(second.steps.get('dispatch').status, 'waiting');
});
