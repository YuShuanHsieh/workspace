import { normalizeConfig } from './config.js';
import { LiveSource } from './live-source.js';
import { escapeHTML, renderFlow } from './render.js';
import { createTrace, reduceLiveEvent } from './state.js';

const HTMLElementBase = globalThis.HTMLElement ?? class {};
const stylesheetURL = new URL('./event-flow.css', import.meta.url).href;

export class EventFlowElement extends HTMLElementBase {
  static LiveSourceClass = LiveSource;

  static get observedAttributes() {
    return ['config-url', 'request-id'];
  }

  constructor() {
    super();
    this.shadow = this.attachShadow({ mode: 'open' });
    this.config = null;
    this.trace = null;
    this.source = null;
    this.connection = 'disconnected';
    this.loadVersion = 0;
  }

  connectedCallback() {
    this.load();
  }

  disconnectedCallback() {
    this.loadVersion += 1;
    this.closeSource();
  }

  attributeChangedCallback(name, oldValue, newValue) {
    if (oldValue !== newValue && this.isConnected) this.load();
  }

  async load() {
    const version = ++this.loadVersion;
    this.closeSource();

    const configURL = this.getAttribute('config-url');
    const requestID = this.getAttribute('request-id');
    if (!configURL || !requestID) {
      this.renderSetupError('Both config-url and request-id attributes are required.');
      return;
    }

    let response;
    try {
      response = await fetch(configURL);
    } catch (error) {
      if (this.isCurrent(version)) this.renderSetupError(errorMessage(error, 'Unable to fetch flow configuration.'));
      return;
    }
    if (!this.isCurrent(version)) return;
    if (!response.ok) {
      this.renderSetupError(`Unable to fetch flow configuration (HTTP ${response.status}).`);
      return;
    }

    let rawConfig;
    try {
      rawConfig = await response.json();
    } catch (error) {
      if (this.isCurrent(version)) this.renderSetupError(errorMessage(error, 'Unable to parse flow configuration.'));
      return;
    }
    if (!this.isCurrent(version)) return;

    let config;
    try {
      config = normalizeConfig(rawConfig);
    } catch (error) {
      if (this.isCurrent(version)) this.renderSetupError(errorMessage(error, 'Invalid flow configuration.'));
      return;
    }
    if (!this.isCurrent(version)) return;

    const trace = createTrace(config, requestID);
    this.config = config;
    this.trace = trace;
    this.connection = 'connecting';
    this.render();

    let source;
    try {
      source = new this.constructor.LiveSourceClass(config.source.url, {
        onState: (connection) => {
          if (!this.isCurrent(version) || this.source !== source) return;
          this.connection = connection;
          this.render();
        },
        onEvent: (event) => {
          if (!this.isCurrent(version) || this.source !== source) return;
          this.trace = reduceLiveEvent(this.trace, event, this.config);
          this.render();
        },
        onDiagnostic: (message) => console.debug('event-flow live source:', message),
      });
      if (!this.isCurrent(version)) {
        source.close(false);
        return;
      }
      this.source = source;
    } catch (error) {
      if (source) source.close(false);
      if (this.isCurrent(version)) {
        this.source = null;
        this.renderSetupError(errorMessage(error, 'Unable to create live event source.'));
      }
      return;
    }

    try {
      source.connect();
    } catch (error) {
      this.closeSource();
      if (this.isCurrent(version)) {
        this.renderSetupError(errorMessage(error, 'Unable to connect to live event source.'));
      }
    }
  }

  closeSource() {
    if (this.source) {
      this.source.close(false);
      this.source = null;
    }
  }

  isCurrent(version) {
    return this.isConnected && version === this.loadVersion;
  }

  render() {
    if (this.config && this.trace) this.write(renderFlow(this.config, this.trace, this.connection));
  }

  renderSetupError(message) {
    this.write(`<p class="setup-error" role="alert">${escapeHTML(message)}</p>`);
  }

  write(content) {
    this.shadow.innerHTML = `<link rel="stylesheet" href="${stylesheetURL}">${content}`;
  }
}

function errorMessage(error, fallback) {
  return error instanceof Error && error.message ? error.message : fallback;
}

if (globalThis.customElements && !globalThis.customElements.get('event-flow')) {
  globalThis.customElements.define('event-flow', EventFlowElement);
}
