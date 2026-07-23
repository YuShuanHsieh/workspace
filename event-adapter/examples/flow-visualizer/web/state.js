const PRECEDENCE = { waiting: 0, active: 1, completed: 2, failed: 3 };
const WIRE_STATUS = { active: 'started', completed: 'completed', failed: 'failed' };
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|([+-])(\d{2}):(\d{2}))$/;

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
    || !isValidTimestamp(liveEvent.timestamp)) {
    return null;
  }

  const correlationField = config.source.correlationField;
  if (typeof correlationField !== 'string' || liveEvent[correlationField] !== trace.requestID) return null;

  const mapping = config.eventIndex?.get(liveEvent.event);
  if (!mapping || WIRE_STATUS[mapping.status] !== liveEvent.status) return null;
  if (!isObject(liveEvent.detail) && liveEvent.detail !== undefined) return null;

  const allowed = config.detailFields?.get(mapping.stepId);
  if (!(allowed instanceof Set)) return null;
  const detail = {};
  try {
    for (const field of allowed) {
      if (Object.hasOwn(liveEvent.detail ?? {}, field)) {
        detail[field] = clone(liveEvent.detail[field]);
      }
    }
    const event = { event: liveEvent.event, status: mapping.status, timestamp: liveEvent.timestamp, detail };
    canonicalJSON(event);
    return { stepId: mapping.stepId, event };
  } catch {
    return null;
  }
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
  const leftJSON = canonicalJSON(left);
  const rightJSON = canonicalJSON(right);
  if (leftJSON < rightJSON) return -1;
  if (leftJSON > rightJSON) return 1;
  return 0;
}

function compareTimestamp(left, right) {
  return Date.parse(left) - Date.parse(right);
}

function sameEvent(left, right) {
  return canonicalJSON(left) === canonicalJSON(right);
}

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function clone(value) {
  return structuredClone(value);
}

function isValidTimestamp(value) {
  if (typeof value !== 'string') return false;
  const match = RFC3339.exec(value);
  if (!match) return false;
  const [, year, month, day, hour, minute, second, , offsetHour, offsetMinute] = match;
  const numericYear = Number(year);
  const numericMonth = Number(month);
  const numericDay = Number(day);
  if (numericMonth < 1 || numericMonth > 12 || numericDay < 1 || numericDay > daysInMonth(numericYear, numericMonth)
    || Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59) return false;
  return offsetHour === undefined || (Number(offsetHour) <= 23 && Number(offsetMinute) <= 59);
}

function daysInMonth(year, month) {
  if (month === 2) {
    return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  }
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}

function canonicalJSON(value, ancestors = new Set()) {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') return JSON.stringify(value);
  if (typeof value === 'number') return Number.isFinite(value) ? JSON.stringify(value) : 'null';
  if (value === undefined) return 'null';
  if (typeof value !== 'object') throw new TypeError('value is not JSON serializable');
  if (ancestors.has(value)) throw new TypeError('value must not contain a cycle');

  ancestors.add(value);
  const serialized = Array.isArray(value)
    ? `[${value.map((item) => canonicalJSON(item, ancestors)).join(',')}]`
    : `{${Object.keys(value).sort().filter((key) => value[key] !== undefined).map(
      (key) => `${JSON.stringify(key)}:${canonicalJSON(value[key], ancestors)}`,
    ).join(',')}}`;
  ancestors.delete(value);
  return serialized;
}
