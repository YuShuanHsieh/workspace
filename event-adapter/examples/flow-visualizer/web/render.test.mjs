import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizeConfig } from './config.js';
import { createTrace } from './state.js';
import { escapeHTML, renderFlow } from './render.js';

function config(overrides = {}) {
  return normalizeConfig({
    version: 1,
    title: 'Upload lifecycle',
    comparison: {
      beforeLabel: 'Before', before: ['Direct upload', 'Client retry'],
      afterLabel: 'After', after: 'One handler',
    },
    source: { transport: 'sse', url: '/events', correlationField: 'correlationid' },
    lanes: [
      { id: 'caller', label: 'Caller', owner: 'caller' },
      { id: 'platform', label: 'Platform', owner: 'platform' },
      { id: 'app', label: 'Application', owner: 'application' },
    ],
    steps: [
      { id: 'reply', order: 4, lane: 'app', label: 'Return reply', completeWhen: { event: 'reply.completed' } },
      { id: 'request', order: 1, lane: 'caller', label: 'Send request', description: 'The caller starts the flow.', startWhen: { event: 'request.started' } },
      { id: 'publish', order: 2, lane: 'platform', label: 'Publish request', completeWhen: { event: 'publish.completed' } },
      { id: 'dispatch', order: 3, lane: 'app', label: 'Dispatch handler', failWhen: { event: 'dispatch.failed' } },
    ],
    ...overrides,
  });
}

function traceFor(cfg, requestID = 'request-42') {
  const trace = createTrace(cfg, requestID);
  trace.startedAt = '2026-07-23T10:00:00.000Z';
  trace.updatedAt = '2026-07-23T10:00:01.234Z';
  trace.steps.set('request', { status: 'active', detail: { attempt: 2 }, timestamp: null, events: [] });
  trace.steps.set('publish', { status: 'completed', detail: {}, timestamp: null, events: [] });
  trace.steps.set('dispatch', { status: 'failed', detail: { reason: 'timeout' }, timestamp: null, events: [] });
  return trace;
}

test('escapes HTML-sensitive characters', () => {
  assert.equal(escapeHTML(`&<>"'`), '&amp;&lt;&gt;&quot;&#39;');
});

test('renders the comparison, focused request, elapsed time, lane headings, and ordered steps', () => {
  const cfg = config();
  const html = renderFlow(cfg, traceFor(cfg), 'live');

  assert.match(html, /<h1>Upload lifecycle<\/h1>/);
  assert.match(html, /<ul><li>Direct upload<\/li><li>Client retry<\/li><\/ul>/);
  assert.match(html, /Before/);
  assert.match(html, /aria-hidden="true"[^>]*>.*→/);
  assert.match(html, /After/);
  assert.match(html, /One handler/);
  assert.match(html, /Focused request.*request-42/);
  assert.match(html, /1,234 ms/);
  assert.match(html, /role="status"[^>]*>Live</);

  const caller = html.indexOf('id="lane-heading-1"');
  const platform = html.indexOf('id="lane-heading-2"');
  const application = html.indexOf('id="lane-heading-3"');
  assert.ok(caller < platform && platform < application);
  assert.ok(html.indexOf('Send request') < html.indexOf('Publish request'));
  assert.ok(html.indexOf('Publish request') < html.indexOf('Dispatch handler'));
  assert.ok(html.indexOf('Dispatch handler') < html.indexOf('Return reply'));
  assert.match(html, /<h2 id="lane-heading-1"[^>]*>Caller<\/h2>/);
  assert.match(html, /<h2 id="lane-heading-2"[^>]*>Platform<\/h2>/);
  assert.match(html, /<h2 id="lane-heading-3"[^>]*>Application<\/h2>/);
  assert.match(html, /<ol>/);
  for (const step of cfg.steps) {
    const laneIndex = cfg.lanes.findIndex((lane) => lane.id === step.lane) + 1;
    assert.match(html, new RegExp(`<li[^>]*flow-step[^>]*data-owner="${step.owner}"[^>]*data-lane="${step.lane}"[^>]*aria-describedby="lane-heading-${laneIndex}"`));
    assert.match(html, new RegExp(`<span class="step-order">${step.order}\.<\\/span>`));
  }
  assert.match(html, /The caller starts the flow\./);
  assert.match(html, /<dt>attempt<\/dt><dd>2<\/dd>/);
});

test('preserves globally ordered interleaved lanes and uses safe unique lane heading IDs', () => {
  const cfg = config({
    lanes: [
      { id: ' caller <one> ', label: 'Caller lane', owner: 'caller' },
      { id: 'adapter & two', label: 'Adapter lane', owner: 'platform' },
    ],
    steps: [
      { id: 'third', order: 3, lane: ' caller <one> ', label: 'Third caller step', completeWhen: { event: 'third' } },
      { id: 'first', order: 1, lane: ' caller <one> ', label: 'First caller step', startWhen: { event: 'first' } },
      { id: 'second', order: 2, lane: 'adapter & two', label: 'Second adapter step', completeWhen: { event: 'second' } },
    ],
  });
  const html = renderFlow(cfg, createTrace(cfg, 'request-1'), 'live');

  assert.match(html, /<ol><li class="flow-step[^>]*data-lane=" caller &lt;one&gt; "/);
  assert.ok(html.indexOf('First caller step') < html.indexOf('Second adapter step'));
  assert.ok(html.indexOf('Second adapter step') < html.indexOf('Third caller step'));
  assert.equal((html.match(/Caller lane/g) ?? []).length, 3);

  const headingIDs = [...html.matchAll(/<h2 id="(lane-heading-\d+)"/g)].map((match) => match[1]);
  assert.deepEqual(headingIDs, ['lane-heading-1', 'lane-heading-2']);
  assert.equal(new Set(headingIDs).size, headingIDs.length);
  assert.doesNotMatch(html, /id="[^"]*(?:caller|adapter|<|&)[^"]*"/);
  assert.match(html, /data-lane=" caller &lt;one&gt; "[^>]*aria-describedby="lane-heading-1"/);
  assert.match(html, /data-lane="adapter &amp; two"[^>]*aria-describedby="lane-heading-2"/);
});

test('renders every connection and step status with prescribed text, icon, and accessible label', () => {
  const cfg = config();
  const trace = traceFor(cfg);

  for (const [connection, text] of Object.entries({
    connecting: 'Connecting', live: 'Live', reconnecting: 'Reconnecting', disconnected: 'Disconnected',
  })) {
    assert.match(renderFlow(cfg, trace, connection), new RegExp(`role="status"[^>]*>${text}<`));
  }
  assert.match(renderFlow(cfg, trace, 'constructor'), /role="status"[^>]*>Disconnected</);

  trace.steps.set('request', { status: 'constructor', detail: {}, timestamp: null, events: [] });
  const fallback = renderFlow(cfg, trace, 'live');
  assert.match(fallback, /state-waiting[^>]*aria-label="Send request: Waiting"/);
  assert.match(fallback, /<span class="step-status" aria-hidden="true">○<\/span> <span class="step-status-text">Waiting<\/span>/);

  trace.steps.set('request', { status: 'active', detail: {}, timestamp: null, events: [] });
  const html = renderFlow(cfg, trace, 'live');
  for (const [id, expected] of Object.entries({
    request: { state: 'active', text: 'In progress', icon: '◌' },
    publish: { state: 'completed', text: 'Completed', icon: '✓' },
    dispatch: { state: 'failed', text: 'Failed', icon: '!' },
    reply: { state: 'waiting', text: 'Waiting', icon: '○' },
  })) {
    const label = cfg.steps.find((step) => step.id === id).label;
    assert.match(html, new RegExp(`<li[^>]*state-${expected.state}[^>]*aria-label="${label}: ${expected.text}"`));
    assert.match(html, new RegExp(`<span class="step-status" aria-hidden="true">${expected.icon}<\\/span> <span class="step-status-text">${expected.text}<\\/span>`));
  }
});

test('renders waiting elapsed time and escapes config, request, and live detail values', () => {
  const cfg = config({
    title: '<img src=x onerror=alert(1)>',
    comparison: { beforeLabel: '"Before"', before: ["<before>'"], afterLabel: "After'", after: '<after>' },
    lanes: [{ id: 'app', label: '<App>', owner: 'application' }],
    steps: [{ id: 'step', order: 1, lane: 'app', label: '"Step" <img>', description: "It's <safe>", detailFields: ['ignored'], startWhen: { event: 'step.started' } }],
  });
  const trace = createTrace(cfg, 'request-<img onerror="bad">');
  trace.steps.set('step', {
    status: 'active', timestamp: null, events: [], detail: { '<key "quoted">': '<img onerror="bad">' },
  });
  const html = renderFlow(cfg, trace, 'disconnected');

  assert.match(html, /Waiting/);
  assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(html, /&quot;Before&quot;/);
  assert.match(html, /&lt;before&gt;&#39;/);
  assert.match(html, /After&#39;/);
  assert.match(html, /&lt;after&gt;/);
  assert.match(html, /&lt;App&gt;/);
  assert.match(html, /&quot;Step&quot; &lt;img&gt;/);
  assert.match(html, /It&#39;s &lt;safe&gt;/);
  assert.match(html, /request-&lt;img onerror=&quot;bad&quot;&gt;/);
  assert.match(html, /&lt;key &quot;quoted&quot;&gt;/);
  assert.match(html, /&lt;img onerror=&quot;bad&quot;&gt;/);
  assert.doesNotMatch(html, /<img/);
  assert.doesNotMatch(html, /onerror="bad"/);
});
