export class APIError extends Error {
  constructor(message, status = 0, payload = null) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.payload = payload;
  }
}

export class CompanionAPI {
  constructor({
    basePath = "/api/v1",
    fetchImpl = globalThis.fetch?.bind(globalThis),
    eventSourceFactory = (url) => new EventSource(url),
  } = {}) {
    if (!fetchImpl) throw new Error("A fetch implementation is required");
    this.basePath = basePath.replace(/\/$/, "");
    this.fetchImpl = fetchImpl;
    this.eventSourceFactory = eventSourceFactory;
  }

  async bootstrap({ signal } = {}) {
    return this.request("/bootstrap", { signal });
  }

  async agent(id, { signal } = {}) {
    return this.request(`/agents/${encodeURIComponent(id)}`, { signal });
  }

  async sendMessage(id, prompt, idempotencyKey, { signal } = {}) {
    return this.request(`/agents/${encodeURIComponent(id)}/messages`, {
      method: "POST",
      signal,
      idempotencyKey,
      body: { prompt },
    });
  }

  async createAgent(input, idempotencyKey, { signal } = {}) {
    return this.request("/agents", {
      method: "POST",
      signal,
      idempotencyKey,
      body: input,
    });
  }

  subscribe(after, handlers = {}) {
    const query = new URLSearchParams();
    if (Number.isFinite(Number(after))) query.set("after", String(after));
    const source = this.eventSourceFactory(`${this.basePath}/events?${query}`);

    source.onopen = () => handlers.onState?.("online");
    source.onerror = (error) => handlers.onState?.("reconnecting", error);
    const handleEvent = (event) => {
      let value = {};
      try {
        value = event.data ? JSON.parse(event.data) : {};
      } catch {
        value = { content: event.data };
      }
      const eventSequence = Number(event.lastEventId || value.seq || value.cursor || after || 0);
      handlers.onEvent?.({ ...value, seq: eventSequence });
    };
    source.onmessage = handleEvent;
    source.addEventListener?.("invalidate", handleEvent);
    source.addEventListener?.("event", handleEvent);
    source.addEventListener?.("invalidation", handleEvent);

    return () => source.close();
  }

  async request(path, { method = "GET", body, signal, idempotencyKey } = {}) {
    const headers = new Headers({ Accept: "application/json" });
    const options = { method, headers, signal, credentials: "same-origin" };

    if (body !== undefined) {
      headers.set("Content-Type", "application/json");
      options.body = JSON.stringify(body);
    }
    if (idempotencyKey) headers.set("Idempotency-Key", idempotencyKey);

    let response;
    try {
      response = await this.fetchImpl(`${this.basePath}${path}`, options);
    } catch (error) {
      if (error?.name === "AbortError") throw error;
      throw new APIError("The Galpón host could not be reached.", 0, error);
    }

    const contentType = response.headers?.get?.("content-type") || "";
    let payload = null;
    if (response.status !== 204) {
      try {
        payload = contentType.includes("application/json")
          ? await response.json()
          : await response.text();
      } catch {
        payload = null;
      }
    }

    if (!response.ok) {
      const message = payload && typeof payload === "object" && payload.error
        ? String(payload.error)
        : typeof payload === "string" && payload.trim()
          ? payload.trim()
          : `Galpón returned HTTP ${response.status}.`;
      throw new APIError(message, response.status, payload);
    }

    return payload ?? {};
  }
}

export function newIdempotencyKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const random = Math.random().toString(36).slice(2);
  return `${Date.now().toString(36)}-${random}`;
}
