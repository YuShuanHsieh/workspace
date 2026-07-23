const CLOSED = 2;
const noop = () => {};

export class LiveSource {
  constructor(url, options = {}) {
    this.url = url;
    this.EventSourceClass = options.EventSourceClass ?? globalThis.EventSource;
    this.onState = options.onState ?? noop;
    this.onEvent = options.onEvent ?? noop;
    this.onDiagnostic = options.onDiagnostic ?? noop;
    this.source = null;
  }

  connect() {
    this.close(false);
    this.onState('connecting');

    const source = new this.EventSourceClass(this.url);
    this.source = source;
    source.onopen = () => {
      if (this.source === source) this.onState('live');
    };
    source.onmessage = (message) => {
      if (this.source !== source) return;
      let event;
      try {
        event = JSON.parse(message.data);
      } catch {
        this.onDiagnostic('Received malformed JSON from live event source.');
        return;
      }
      this.onEvent(event);
    };
    source.onerror = () => {
      if (this.source === source) {
        this.onState(source.readyState === CLOSED ? 'disconnected' : 'reconnecting');
      }
    };
  }

  close(report = true) {
    if (this.source) {
      this.source.close();
      this.source = null;
    }
    if (report) this.onState('disconnected');
  }
}
