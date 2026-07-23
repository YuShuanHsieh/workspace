import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizeConfig } from './config.js';

function validConfig() {
  return {
    version: 1,
    title: 'Upload lifecycle',
    comparison: {
      beforeLabel: 'Before',
      before: ['Caller uploads directly'],
      afterLabel: 'After',
      after: 'One HTTP business handler',
    },
    source: {
      transport: 'sse',
      url: '/events',
      correlationField: 'correlationid',
    },
    lanes: [
      { id: 'caller', label: 'Caller', owner: 'caller' },
      { id: 'platform', label: 'Platform', owner: 'platform' },
      { id: 'app', label: 'Application', owner: 'application' },
    ],
    steps: [
      {
        id: 'response',
        order: 2,
        lane: 'app',
        label: 'Return response',
        description: 'The app returns its reply.',
        detailFields: ['correlationid', 'httpstatus'],
        completeWhen: { event: 'com.workspace.upload.completed' },
      },
      {
        id: 'request',
        order: 1,
        lane: 'caller',
        label: 'Request upload',
        startWhen: { event: 'com.workspace.upload.requested' },
      },
    ],
  };
}

test('normalizes a valid version-1 config into indexed flow data', () => {
  const config = normalizeConfig(validConfig());

  assert.equal(config.title, 'Upload lifecycle');
  assert.deepEqual(config.steps.map((step) => step.id), ['request', 'response']);
  assert.equal(config.steps[0].owner, 'caller');
  assert.equal(config.steps[1].owner, 'application');
  assert.equal(config.laneByID.get('platform').label, 'Platform');
  assert.deepEqual(config.eventIndex.get('com.workspace.upload.requested'), {
    stepId: 'request', status: 'active',
  });
  assert.deepEqual(config.eventIndex.get('com.workspace.upload.completed'), {
    stepId: 'response', status: 'completed',
  });
  assert.deepEqual([...config.detailFields.get('response')], ['correlationid', 'httpstatus']);
  assert.notEqual(config.steps, validConfig().steps);
});

test('rejects unsupported version', () => {
  const config = validConfig();
  config.version = 2;
  assert.throws(() => normalizeConfig(config));
});

test('rejects unsupported source transport', () => {
  const config = validConfig();
  config.source.transport = 'websocket';
  assert.throws(() => normalizeConfig(config));
});

test('rejects a source without URL', () => {
  const config = validConfig();
  delete config.source.url;
  assert.throws(() => normalizeConfig(config));
});

test('rejects a source without correlation field', () => {
  const config = validConfig();
  delete config.source.correlationField;
  assert.throws(() => normalizeConfig(config));
});

test('rejects invalid or empty before comparison', () => {
  const invalid = validConfig();
  invalid.comparison.before = [];
  assert.throws(() => normalizeConfig(invalid));

  const empty = validConfig();
  empty.comparison.before = [''];
  assert.throws(() => normalizeConfig(empty));
});

test('rejects missing, empty, or non-string after comparison', () => {
  const missing = validConfig();
  delete missing.comparison.after;
  assert.throws(() => normalizeConfig(missing));

  const empty = validConfig();
  empty.comparison.after = '';
  assert.throws(() => normalizeConfig(empty));

  const nonString = validConfig();
  nonString.comparison.after = ['One HTTP business handler'];
  assert.throws(() => normalizeConfig(nonString));
});

test('rejects duplicate lane ID', () => {
  const config = validConfig();
  config.lanes.push({ id: 'caller', label: 'Another caller', owner: 'caller' });
  assert.throws(() => normalizeConfig(config));
});

test('rejects invalid lane owner', () => {
  const config = validConfig();
  config.lanes[0].owner = 'operator';
  assert.throws(() => normalizeConfig(config));
});

test('rejects a step that references an unknown lane', () => {
  const config = validConfig();
  config.steps[0].lane = 'unknown';
  assert.throws(() => normalizeConfig(config));
});

test('rejects duplicate step ID', () => {
  const config = validConfig();
  config.steps[1].id = 'response';
  assert.throws(() => normalizeConfig(config));
});

test('rejects duplicate or nonpositive step order', () => {
  const duplicate = validConfig();
  duplicate.steps[1].order = 2;
  assert.throws(() => normalizeConfig(duplicate));

  const nonpositive = validConfig();
  nonpositive.steps[1].order = 0;
  assert.throws(() => normalizeConfig(nonpositive));
});

test('rejects a step without an event mapping', () => {
  const config = validConfig();
  delete config.steps[1].startWhen;
  assert.throws(() => normalizeConfig(config));
});

test('rejects duplicate event mapping', () => {
  const config = validConfig();
  config.steps[0].completeWhen.event = 'com.workspace.upload.requested';
  assert.throws(() => normalizeConfig(config));
});
