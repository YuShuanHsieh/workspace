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

function assertInvalid(input, message) {
  assert.throws(() => normalizeConfig(input), message);
}

test('normalizes a valid version-1 config into indexed flow data', () => {
  const input = validConfig();
  const config = normalizeConfig(input);

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
  assert.notEqual(config, input);
  assert.notEqual(config.comparison, input.comparison);
  assert.notEqual(config.comparison.before, input.comparison.before);
  assert.notEqual(config.source, input.source);
  assert.notEqual(config.lanes, input.lanes);
  assert.notEqual(config.lanes[0], input.lanes[0]);
  assert.notEqual(config.steps, input.steps);
  assert.notEqual(config.steps[1], input.steps[0]);
  assert.notEqual(config.steps[1].completeWhen, input.steps[0].completeWhen);

  input.comparison.before[0] = 'Mutated before';
  input.source.url = '/mutated';
  input.lanes[2].label = 'Mutated app';
  input.steps[0].detailFields[0] = 'mutated-field';
  input.steps[0].completeWhen.event = 'com.workspace.upload.mutated';

  assert.deepEqual(config.comparison.before, ['Caller uploads directly']);
  assert.equal(config.source.url, '/events');
  assert.equal(config.laneByID.get('app').label, 'Application');
  assert.deepEqual([...config.detailFields.get('response')], ['correlationid', 'httpstatus']);
  assert.deepEqual(config.eventIndex.get('com.workspace.upload.completed'), {
    stepId: 'response', status: 'completed',
  });
  assert.equal(config.eventIndex.has('com.workspace.upload.mutated'), false);
});

test('rejects unsupported version', () => {
  const config = validConfig();
  config.version = 2;
  assertInvalid(config, /config\.version must be 1/);
});

test('rejects unsupported source transport', () => {
  const config = validConfig();
  config.source.transport = 'websocket';
  assertInvalid(config, /config\.source\.transport must be sse/);
});

test('rejects a source without URL', () => {
  const config = validConfig();
  delete config.source.url;
  assertInvalid(config, /config\.source\.url must be a non-empty string/);
});

test('rejects a source without correlation field', () => {
  const config = validConfig();
  delete config.source.correlationField;
  assertInvalid(config, /config\.source\.correlationField must be a non-empty string/);
});

test('rejects invalid or empty before comparison', () => {
  const invalid = validConfig();
  invalid.comparison.before = [];
  assertInvalid(invalid, /config\.comparison\.before must be a non-empty array/);

  const empty = validConfig();
  empty.comparison.before = [''];
  assertInvalid(empty, /config\.comparison\.before\[0\] must be a non-empty string/);
});

test('rejects missing, empty, or non-string after comparison', () => {
  const missing = validConfig();
  delete missing.comparison.after;
  assertInvalid(missing, /config\.comparison\.after must be a non-empty string/);

  const empty = validConfig();
  empty.comparison.after = '';
  assertInvalid(empty, /config\.comparison\.after must be a non-empty string/);

  const nonString = validConfig();
  nonString.comparison.after = ['One HTTP business handler'];
  assertInvalid(nonString, /config\.comparison\.after must be a non-empty string/);
});

test('rejects duplicate lane ID', () => {
  const config = validConfig();
  config.lanes.push({ id: 'caller', label: 'Another caller', owner: 'caller' });
  assertInvalid(config, /duplicate lane id: caller/);
});

test('rejects invalid lane owner', () => {
  const config = validConfig();
  config.lanes[0].owner = 'operator';
  assertInvalid(config, /config\.lanes\[0\]\.owner must be caller, platform, or application/);
});

test('rejects a step that references an unknown lane', () => {
  const config = validConfig();
  config.steps[0].lane = 'unknown';
  assertInvalid(config, /config\.steps\[0\]\.lane references unknown lane: unknown/);
});

test('rejects duplicate step ID', () => {
  const config = validConfig();
  config.steps[1].id = 'response';
  assertInvalid(config, /duplicate step id: response/);
});

test('rejects duplicate or nonpositive step order', () => {
  const duplicate = validConfig();
  duplicate.steps[1].order = 2;
  assertInvalid(duplicate, /config\.steps\[1\]\.order must be a unique positive integer/);

  const nonpositive = validConfig();
  nonpositive.steps[1].order = 0;
  assertInvalid(nonpositive, /config\.steps\[1\]\.order must be a unique positive integer/);
});

test('rejects a step without an event mapping', () => {
  const config = validConfig();
  delete config.steps[1].startWhen;
  assertInvalid(config, /config\.steps\[1\] must define an event mapping/);
});

test('rejects duplicate event mapping', () => {
  const config = validConfig();
  config.steps[0].completeWhen.event = 'com.workspace.upload.requested';
  assertInvalid(config, /duplicate event mapping: com\.workspace\.upload\.requested/);
});

test('rejects unsupported keys and malformed objects', () => {
  const unsupported = validConfig();
  unsupported.unexpected = true;
  assertInvalid(unsupported, /config\.unexpected is not supported/);

  const nestedUnsupported = validConfig();
  nestedUnsupported.source.extra = true;
  assertInvalid(nestedUnsupported, /config\.source\.extra is not supported/);

  assertInvalid(null, /config must be an object/);

  const malformedComparison = validConfig();
  malformedComparison.comparison = [];
  assertInvalid(malformedComparison, /config\.comparison must be an object/);

  const malformedSource = validConfig();
  malformedSource.source = null;
  assertInvalid(malformedSource, /config\.source must be an object/);
});

test('rejects empty lane and step collections and invalid required strings', () => {
  const noLanes = validConfig();
  noLanes.lanes = [];
  assertInvalid(noLanes, /config\.lanes must be a non-empty array/);

  const noSteps = validConfig();
  noSteps.steps = [];
  assertInvalid(noSteps, /config\.steps must be a non-empty array/);

  const invalidLane = validConfig();
  invalidLane.lanes[0].label = '';
  assertInvalid(invalidLane, /config\.lanes\[0\]\.label must be a non-empty string/);

  const invalidStep = validConfig();
  invalidStep.steps[0].id = '';
  assertInvalid(invalidStep, /config\.steps\[0\]\.id must be a non-empty string/);
});

test('rejects non-integer orders and malformed mappings and detail fields', () => {
  const fractionalOrder = validConfig();
  fractionalOrder.steps[0].order = 1.5;
  assertInvalid(fractionalOrder, /config\.steps\[0\]\.order must be a unique positive integer/);

  const malformedMapping = validConfig();
  malformedMapping.steps[1].startWhen = 'com.workspace.upload.requested';
  assertInvalid(malformedMapping, /config\.steps\[1\]\.startWhen must be an object/);

  const malformedDetails = validConfig();
  malformedDetails.steps[0].detailFields = 'correlationid';
  assertInvalid(malformedDetails, /config\.steps\[0\]\.detailFields must be a non-empty array/);
});

test('indexes failWhen events as failed', () => {
  const input = validConfig();
  input.steps[1].failWhen = { event: 'com.workspace.upload.failed' };

  const config = normalizeConfig(input);

  assert.deepEqual(config.eventIndex.get('com.workspace.upload.failed'), {
    stepId: 'request', status: 'failed',
  });
});
