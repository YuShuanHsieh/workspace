const CONNECTION_LABELS = {
  connecting: 'Connecting',
  live: 'Live',
  reconnecting: 'Reconnecting',
  disconnected: 'Disconnected',
};

const STEP_STATUS = {
  waiting: { icon: '○', label: 'Waiting' },
  active: { icon: '◌', label: 'In progress' },
  completed: { icon: '✓', label: 'Completed' },
  failed: { icon: '!', label: 'Failed' },
};

export function escapeHTML(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

export function renderFlow(config, trace, connection) {
  const connectionState = CONNECTION_LABELS[connection] ? connection : 'disconnected';
  const connectionLabel = CONNECTION_LABELS[connectionState];
  const elapsed = renderElapsed(trace.startedAt, trace.updatedAt);
  const lanes = config.lanes.map((lane) => renderLane(lane, config.steps, trace.steps)).join('');

  return `<main class="flow-visualizer">
  <h1>${escapeHTML(config.title)}</h1>
  <p class="connection state-${connectionState}" role="status">${connectionLabel}</p>
  <section class="flow-comparison" aria-label="Flow comparison">
    <div class="comparison-before"><h2>${escapeHTML(config.comparison.beforeLabel)}</h2><ul>${config.comparison.before.map((item) => `<li>${escapeHTML(item)}</li>`).join('')}</ul></div>
    <span class="comparison-arrow" aria-hidden="true">→</span>
    <div class="comparison-after"><h2>${escapeHTML(config.comparison.afterLabel)}</h2><p>${escapeHTML(config.comparison.after)}</p></div>
  </section>
  <p class="focused-request">Focused request: <code>${escapeHTML(trace.requestID)}</code></p>
  <p class="elapsed">Elapsed: ${elapsed}</p>
  <div class="flow-lanes">${lanes}</div>
</main>`;
}

function renderLane(lane, steps, traceSteps) {
  const laneSteps = steps.filter((step) => step.lane === lane.id);
  const headingID = `lane-${escapeHTML(lane.id)}-heading`;
  return `<section class="flow-lane" data-owner="${escapeHTML(lane.owner)}" aria-labelledby="${headingID}">
  <h2 id="${headingID}">${escapeHTML(lane.label)}</h2>
  <ol>${laneSteps.map((step) => renderStep(step, traceSteps.get(step.id))).join('')}</ol>
</section>`;
}

function renderStep(step, state = {}) {
  const statusName = STEP_STATUS[state.status] ? state.status : 'waiting';
  const status = STEP_STATUS[statusName];
  const description = step.description === undefined ? '' : `<p class="step-description">${escapeHTML(step.description)}</p>`;
  const details = renderDetails(state.detail);
  return `<li class="flow-step state-${statusName}" data-owner="${escapeHTML(step.owner)}" aria-label="${escapeHTML(step.label)}: ${status.label}">
  <span class="step-status" aria-hidden="true">${status.icon}</span> <span class="step-status-text">${status.label}</span>
  <span class="step-order">${step.order}.</span> <span class="step-label">${escapeHTML(step.label)}</span>${description}${details}
</li>`;
}

function renderDetails(detail) {
  if (detail === null || typeof detail !== 'object' || Array.isArray(detail)) return '';
  const entries = Object.entries(detail);
  if (entries.length === 0) return '';
  return `<dl class="step-detail">${entries.map(([key, value]) => `<dt>${escapeHTML(key)}</dt><dd>${escapeHTML(detailValue(value))}</dd>`).join('')}</dl>`;
}

function detailValue(value) {
  if (typeof value === 'string') return value;
  if (value === undefined) return '';
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function renderElapsed(startedAt, updatedAt) {
  if (typeof startedAt !== 'string' || typeof updatedAt !== 'string') return 'Waiting';
  const elapsed = Date.parse(updatedAt) - Date.parse(startedAt);
  if (!Number.isFinite(elapsed)) return 'Waiting';
  return `${Math.max(0, elapsed).toLocaleString('en-US')} ms`;
}
