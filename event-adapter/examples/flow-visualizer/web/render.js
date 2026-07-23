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
  const connectionState = Object.hasOwn(CONNECTION_LABELS, connection) ? connection : 'disconnected';
  const connectionLabel = CONNECTION_LABELS[connectionState];
  const elapsed = renderElapsed(trace.startedAt, trace.updatedAt);
  const laneByID = new Map(config.lanes.map((lane, index) => [lane.id, { ...lane, index: index + 1 }]));
  const laneHeadings = config.lanes.map(renderLaneHeading).join('');
  const steps = config.steps.map((step) => renderStep(step, trace.steps.get(step.id), laneByID.get(step.lane))).join('');

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
  <section class="flow-lanes" aria-label="Flow lanes">
    <div class="flow-lane-headings">${laneHeadings}</div>
    <ol>${steps}</ol>
  </section>
</main>`;
}

function renderLaneHeading(lane, index) {
  return `<h2 id="lane-heading-${index + 1}" class="flow-lane-heading">${escapeHTML(lane.label)}</h2>`;
}

function renderStep(step, state = {}, lane) {
  const statusName = state !== null && Object.hasOwn(STEP_STATUS, state.status) ? state.status : 'waiting';
  const status = STEP_STATUS[statusName];
  const description = step.description === undefined ? '' : `<p class="step-description">${escapeHTML(step.description)}</p>`;
  const details = renderDetails(state.detail);
  const headingID = `lane-heading-${lane.index}`;
  return `<li class="flow-step state-${statusName}" data-owner="${escapeHTML(step.owner)}" data-lane="${escapeHTML(step.lane)}" aria-label="${escapeHTML(step.label)}: ${status.label}" aria-describedby="${headingID}">
  <span class="step-lane">${escapeHTML(lane.label)}</span>
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
