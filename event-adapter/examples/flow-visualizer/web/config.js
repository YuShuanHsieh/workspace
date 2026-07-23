const LANE_OWNERS = new Set(['caller', 'platform', 'application']);
const EVENT_MAPPINGS = [
  { property: 'startWhen', status: 'active' },
  { property: 'completeWhen', status: 'completed' },
  { property: 'failWhen', status: 'failed' },
];

export function normalizeConfig(input) {
  const config = object(input, 'config');
  assertKeys(config, 'config', ['version', 'title', 'comparison', 'source', 'lanes', 'steps']);

  if (config.version !== 1) {
    throw new Error('config.version must be 1');
  }

  const lanes = normalizeLanes(config.lanes);
  const laneByID = new Map(lanes.map((lane) => [lane.id, lane]));
  const { steps, eventIndex, detailFields } = normalizeSteps(config.steps, laneByID);

  return {
    version: 1,
    title: requiredString(config.title, 'config.title'),
    comparison: normalizeComparison(config.comparison),
    source: normalizeSource(config.source),
    lanes,
    steps,
    laneByID,
    eventIndex,
    detailFields,
  };
}

function normalizeComparison(input) {
  const comparison = object(input, 'config.comparison');
  assertKeys(comparison, 'config.comparison', ['beforeLabel', 'before', 'afterLabel', 'after']);
  return {
    beforeLabel: requiredString(comparison.beforeLabel, 'config.comparison.beforeLabel'),
    before: stringList(comparison.before, 'config.comparison.before'),
    afterLabel: requiredString(comparison.afterLabel, 'config.comparison.afterLabel'),
    after: requiredString(comparison.after, 'config.comparison.after'),
  };
}

function normalizeSource(input) {
  const source = object(input, 'config.source');
  assertKeys(source, 'config.source', ['transport', 'url', 'correlationField']);
  if (source.transport !== 'sse') {
    throw new Error('config.source.transport must be sse');
  }
  return {
    transport: 'sse',
    url: requiredString(source.url, 'config.source.url'),
    correlationField: requiredString(source.correlationField, 'config.source.correlationField'),
  };
}

function normalizeLanes(input) {
  const lanes = nonEmptyArray(input, 'config.lanes');
  const laneIDs = new Set();
  return lanes.map((item, index) => {
    const path = `config.lanes[${index}]`;
    const lane = object(item, path);
    assertKeys(lane, path, ['id', 'label', 'owner']);
    const id = requiredString(lane.id, `${path}.id`);
    if (laneIDs.has(id)) {
      throw new Error(`duplicate lane id: ${id}`);
    }
    laneIDs.add(id);
    if (!LANE_OWNERS.has(lane.owner)) {
      throw new Error(`${path}.owner must be caller, platform, or application`);
    }
    return { id, label: requiredString(lane.label, `${path}.label`), owner: lane.owner };
  });
}

function normalizeSteps(input, laneByID) {
  const steps = nonEmptyArray(input, 'config.steps');
  const stepIDs = new Set();
  const orders = new Set();
  const eventIndex = new Map();
  const detailFields = new Map();

  const normalizedSteps = steps.map((item, index) => {
    const path = `config.steps[${index}]`;
    const step = object(item, path);
    assertKeys(step, path, ['id', 'order', 'lane', 'label', 'description', 'detailFields', 'startWhen', 'completeWhen', 'failWhen']);
    const id = requiredString(step.id, `${path}.id`);
    if (stepIDs.has(id)) {
      throw new Error(`duplicate step id: ${id}`);
    }
    stepIDs.add(id);
    if (!Number.isInteger(step.order) || step.order <= 0 || orders.has(step.order)) {
      throw new Error(`${path}.order must be a unique positive integer`);
    }
    orders.add(step.order);
    const lane = requiredString(step.lane, `${path}.lane`);
    const laneConfig = laneByID.get(lane);
    if (!laneConfig) {
      throw new Error(`${path}.lane references unknown lane: ${lane}`);
    }

    const mappings = EVENT_MAPPINGS.flatMap(({ property, status }) => {
      if (step[property] === undefined) return [];
      const mapping = object(step[property], `${path}.${property}`);
      assertKeys(mapping, `${path}.${property}`, ['event']);
      return [{ property, event: requiredString(mapping.event, `${path}.${property}.event`), status }];
    });
    if (mappings.length === 0) {
      throw new Error(`${path} must define an event mapping`);
    }
    for (const { event, status } of mappings) {
      if (eventIndex.has(event)) {
        throw new Error(`duplicate event mapping: ${event}`);
      }
      eventIndex.set(event, { stepId: id, status });
    }

    const fields = step.detailFields === undefined
      ? []
      : stringList(step.detailFields, `${path}.detailFields`);
    detailFields.set(id, new Set(fields));
    const normalized = {
      id,
      order: step.order,
      lane,
      owner: laneConfig.owner,
      label: requiredString(step.label, `${path}.label`),
    };
    if (step.description !== undefined) {
      normalized.description = requiredString(step.description, `${path}.description`);
    }
    if (fields.length > 0) {
      normalized.detailFields = [...fields];
    }
    for (const { property, event } of mappings) {
      normalized[property] = { event };
    }
    return normalized;
  });

  normalizedSteps.sort((left, right) => left.order - right.order);
  return { steps: normalizedSteps, eventIndex, detailFields };
}

function object(value, path) {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${path} must be an object`);
  }
  return value;
}

function assertKeys(value, path, keys) {
  for (const key of Object.keys(value)) {
    if (!keys.includes(key)) {
      throw new Error(`${path}.${key} is not supported`);
    }
  }
}

function requiredString(value, path) {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`${path} must be a non-empty string`);
  }
  return value;
}

function nonEmptyArray(value, path) {
  if (!Array.isArray(value) || value.length === 0) {
    throw new Error(`${path} must be a non-empty array`);
  }
  return value;
}

function stringList(value, path) {
  return nonEmptyArray(value, path).map((item, index) => requiredString(item, `${path}[${index}]`));
}
