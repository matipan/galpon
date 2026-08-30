import { audioMessageRequestTimeoutMilliseconds } from "./audio-policy.mjs";

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
    requestTimeoutMs = 15_000,
  } = {}) {
    if (!fetchImpl) throw new Error("A fetch implementation is required");
    this.basePath = basePath.replace(/\/$/, "");
    this.fetchImpl = fetchImpl;
    this.eventSourceFactory = eventSourceFactory;
    this.requestTimeoutMs = requestTimeoutMs;
  }

  async bootstrap({ signal } = {}) {
    return this.request("/bootstrap", { signal });
  }

  async agentOperations(id, { signal } = {}) {
    return this.request(`/agents/${encodeURIComponent(id)}/operations`, { signal });
  }

  async agent(id, { signal, before, after, messageBefore } = {}) {
    const query = new URLSearchParams();
    if (Number(before) > 0) query.set("before", String(before));
    if (Number(after) > 0) query.set("after", String(after));
    if (messageBefore) query.set("messageBefore", String(messageBefore));
    const suffix = query.size ? `?${query}` : "";
    return this.request(`/agents/${encodeURIComponent(id)}${suffix}`, { signal });
  }

  async sendMessage(id, prompt, idempotencyKey, { signal, images = [] } = {}) {
    const selectedImages = Array.isArray(images) ? images : [];
    if (selectedImages.length) {
      const form = new FormData();
      form.append("prompt", prompt);
      for (const image of selectedImages) form.append("images", image, image.name || "image");
      return this.request(`/agents/${encodeURIComponent(id)}/messages`, {
        method: "POST",
        signal,
        idempotencyKey,
        timeoutMs: 60_000,
        rawBody: form,
      });
    }
    return this.request(`/agents/${encodeURIComponent(id)}/messages`, {
      method: "POST",
      signal,
      idempotencyKey,
      timeoutMs: 30_000,
      body: { prompt },
    });
  }

  async sendAudioMessage(id, audio, language, idempotencyKey, { signal, images = [] } = {}) {
    const form = new FormData();
    form.append("audio", audio, audioFileName(audio.type));
    form.append("language", language);
    for (const image of Array.isArray(images) ? images : []) form.append("images", image, image.name || "image");
    return this.request(`/agents/${encodeURIComponent(id)}/audio-messages`, {
      method: "POST",
      signal,
      idempotencyKey,
      timeoutMs: audioMessageRequestTimeoutMilliseconds,
      rawBody: form,
    });
  }

  async createAgent(input, idempotencyKey, { signal } = {}) {
    return this.request("/agents", {
      method: "POST",
      signal,
      idempotencyKey,
      timeoutMs: 60_000,
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
    source.addEventListener?.("reset", handleEvent);

    return () => source.close();
  }

  async request(path, { method = "GET", body, rawBody, signal, idempotencyKey, timeoutMs = this.requestTimeoutMs } = {}) {
    const headers = new Headers({ Accept: "application/json" });
    const timeoutController = new AbortController();
    let timedOut = false;
    const abortFromCaller = () => timeoutController.abort(signal?.reason);
    if (signal?.aborted) abortFromCaller();
    else signal?.addEventListener?.("abort", abortFromCaller, { once: true });
    const timer = Number(timeoutMs) > 0 ? setTimeout(() => {
      timedOut = true;
      timeoutController.abort();
    }, Number(timeoutMs)) : null;
    const options = { method, headers, signal: timeoutController.signal, credentials: "same-origin" };
    const cleanup = () => {
      if (timer) clearTimeout(timer);
      signal?.removeEventListener?.("abort", abortFromCaller);
    };

    if (rawBody !== undefined) {
      options.body = rawBody;
    } else if (body !== undefined) {
      headers.set("Content-Type", "application/json");
      options.body = JSON.stringify(body);
    }
    if (idempotencyKey) headers.set("Idempotency-Key", idempotencyKey);

    let response;
    try {
      response = await this.fetchImpl(`${this.basePath}${path}`, options);
    } catch (error) {
      cleanup();
      if (timedOut) throw new APIError("The Galpón request timed out. Try again.", 0, error);
      if (signal?.aborted || error?.name === "AbortError") throw error;
      throw new APIError("The Galpón host could not be reached.", 0, error);
    }

    const contentType = response.headers?.get?.("content-type") || "";
    let payload = null;
    if (response.status !== 204) {
      try {
        payload = contentType.includes("application/json")
          ? await response.json()
          : await response.text();
      } catch (error) {
        if (timedOut) {
          cleanup();
          throw new APIError("The Galpón request timed out. Try again.", 0, error);
        }
        if (signal?.aborted || error?.name === "AbortError") {
          cleanup();
          throw error;
        }
        payload = null;
      }
    }

    if (!response.ok) {
      const message = payload && typeof payload === "object" && payload.error
        ? String(payload.error)
        : typeof payload === "string" && payload.trim()
          ? payload.trim()
          : `Galpón returned HTTP ${response.status}.`;
      cleanup();
      throw new APIError(message, response.status, payload);
    }

    cleanup();
    return payload ?? {};
  }
}

function audioFileName(contentType = "") {
  if (contentType.includes("mp4")) return "message.m4a";
  if (contentType.includes("ogg")) return "message.ogg";
  return "message.webm";
}

export function mutationAttempt(current, payload) {
  const fingerprint = JSON.stringify(payload);
  if (current?.fingerprint === fingerprint) return current;
  return { fingerprint, key: newIdempotencyKey() };
}

export function isDefiniteMutationRejection(error) {
  const status = Number(error?.status || 0);
  return status >= 400 && status < 500 && status !== 409;
}

export function newIdempotencyKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const random = Math.random().toString(36).slice(2);
  return `${Date.now().toString(36)}-${random}`;
}
