const PRECEDENCE = { waiting: 0, active: 1, completed: 2, failed: 3 };

export function createTrace(config, requestID) {
  return {
    requestID,
    startedAt: null,
    updatedAt: null,
    steps: new Map(config.steps.map((step) => [step.id, {
      status: 'waiting',
      timestamp: null,
      detail: {},
      events: [],
    }])),
  };
}

export function reduceLiveEvent(trace, liveEvent, config) {
  const observation = normalizeObservation(trace, liveEvent, config);
  if (!observation) return trace;

  const previousStep = trace.steps.get(observation.stepId);
  if (!previousStep) return trace;

  const events = insertObservation(previousStep.events, observation.event);
  if (events === previousStep.events) return trace;

  const step = deriveStep(events);
  const steps = new Map(trace.steps);
  steps.set(observation.stepId, step);
  const { startedAt, updatedAt } = traceBounds(steps);
  return { ...trace, startedAt, updatedAt, steps };
}

function normalizeObservation(trace, liveEvent, config) {
  if (!isObject(liveEvent) || !isObject(config) || !isObject(config.source)
    || typeof liveEvent.event !== 'string' || typeof liveEvent.status !== 'string'
    || typeof liveEvent.timestamp !== 'string' || Number.isNaN(Date.parse(liveEvent.timestamp))) {
    return null;
  }

  const correlationField = config.source.correlationField;
  if (typeof correlationField !== 'string' || liveEvent[correlationField] !== trace.requestID) return null;

  const mapping = config.eventIndex?.get(liveEvent.event);
  if (!mapping || mapping.status !== liveEvent.status || !PRECEDENCE[liveEvent.status]) return null;
  if (!isObject(liveEvent.detail) && liveEvent.detail !== undefined) return null;

  const allowed = config.detailFields?.get(mapping.stepId);
  if (!(allowed instanceof Set)) return null;
  const detail = {};
  for (const field of allowed) {
    if (Object.hasOwn(liveEvent.detail ?? {}, field)) {
      detail[field] = clone(liveEvent.detail[field]);
    }
  }

  return {
    stepId: mapping.stepId,
    event: { event: liveEvent.event, status: liveEvent.status, timestamp: liveEvent.timestamp, detail },
  };
}

function insertObservation(events, observation) {
  if (events.some((event) => sameEvent(event, observation))) return events;
  return [...events, observation].sort(compareEvents);
}

function deriveStep(events) {
  let selected = events[0];
  for (const event of events.slice(1)) {
    if (PRECEDENCE[event.status] > PRECEDENCE[selected.status]
      || (PRECEDENCE[event.status] === PRECEDENCE[selected.status]
        && compareEvents(event, selected) > 0)) {
      selected = event;
    }
  }
  return {
    status: selected.status,
    timestamp: selected.timestamp,
    detail: clone(selected.detail),
    events,
  };
}

function traceBounds(steps) {
  const timestamps = [];
  for (const step of steps.values()) {
    for (const event of step.events) timestamps.push(event.timestamp);
  }
  timestamps.sort(compareTimestamp);
  return { startedAt: timestamps[0] ?? null, updatedAt: timestamps.at(-1) ?? null };
}

function compareEvents(left, right) {
  const timestamp = compareTimestamp(left.timestamp, right.timestamp);
  if (timestamp !== 0) return timestamp;
  return JSON.stringify(left).localeCompare(JSON.stringify(right));
}

function compareTimestamp(left, right) {
  return Date.parse(left) - Date.parse(right);
}

function sameEvent(left, right) {
  return JSON.stringify(left) === JSON.stringify(right);
}

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function clone(value) {
  return structuredClone(value);
}
