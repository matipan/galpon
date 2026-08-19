import assert from "node:assert/strict";
import test from "node:test";

import { APIError, CompanionAPI, isDefiniteMutationRejection, mutationAttempt } from "./api.mjs";

function jsonResponse(value, init = {}) {
  return new Response(JSON.stringify(value), {
    status: init.status || 200,
    headers: { "content-type": "application/json", ...(init.headers || {}) },
  });
}

test("bootstrap and agent reads use isolated companion endpoints", async () => {
  const calls = [];
  const api = new CompanionAPI({
    basePath: "/test/api/v1/",
    fetchImpl: async (url, options) => {
      calls.push({ url, options });
      return jsonResponse({ cursor: 7 });
    },
  });

  assert.deepEqual(await api.bootstrap(), { cursor: 7 });
  await api.agent("agent/with space");
  await api.agent("agent/with space", { before: 42, messageBefore: "17.message-id" });

  assert.equal(calls[0].url, "/test/api/v1/bootstrap");
  assert.equal(calls[0].options.method, "GET");
  assert.equal(calls[1].url, "/test/api/v1/agents/agent%2Fwith%20space");
  assert.equal(calls[1].options.credentials, "same-origin");
  assert.equal(calls[2].url, "/test/api/v1/agents/agent%2Fwith%20space?before=42&messageBefore=17.message-id");
});

test("logical mutation retries keep one idempotency key", () => {
  const first = mutationAttempt(null, { agentId: "agent", prompt: "continue" });
  const retry = mutationAttempt(first, { agentId: "agent", prompt: "continue" });
  const changed = mutationAttempt(retry, { agentId: "agent", prompt: "different" });

  assert.equal(retry.key, first.key);
  assert.notEqual(changed.key, first.key);
  assert.equal(isDefiniteMutationRejection(new APIError("offline", 0)), false);
  assert.equal(isDefiniteMutationRejection(new APIError("uncertain", 409)), false);
  assert.equal(isDefiniteMutationRejection(new APIError("invalid", 422)), true);
});

test("message send preserves prompt and idempotency key", async () => {
  let call;
  const api = new CompanionAPI({
    fetchImpl: async (url, options) => {
      call = { url, options };
      return jsonResponse({ delivery: "queued" });
    },
  });

  const result = await api.sendMessage("agent-a", "Use the existing helper", "message-key");

  assert.deepEqual(result, { delivery: "queued" });
  assert.equal(call.url, "/api/v1/agents/agent-a/messages");
  assert.equal(call.options.method, "POST");
  assert.equal(call.options.headers.get("Idempotency-Key"), "message-key");
  assert.equal(call.options.headers.get("Content-Type"), "application/json");
  assert.deepEqual(JSON.parse(call.options.body), { prompt: "Use the existing helper" });
});

test("audio message send uses multipart data and keeps the idempotency key", async () => {
  let call;
  const api = new CompanionAPI({
    fetchImpl: async (url, options) => {
      call = { url, options };
      return jsonResponse({ message: { status: "queued" }, transcript: "Check the tests" });
    },
  });
  const audio = new Blob(["recording"], { type: "audio/webm" });

  const result = await api.sendAudioMessage("agent-a", audio, "es", "audio-key");

  assert.equal(result.transcript, "Check the tests");
  assert.equal(call.url, "/api/v1/agents/agent-a/audio-messages");
  assert.equal(call.options.method, "POST");
  assert.equal(call.options.headers.get("Idempotency-Key"), "audio-key");
  assert.equal(call.options.headers.get("Content-Type"), null);
  assert.equal(call.options.body.get("audio").type, "audio/webm");
  assert.equal(call.options.body.get("language"), "es");
});

test("agent launch sends the selected workspace and repositories", async () => {
  let body;
  let key;
  const api = new CompanionAPI({
    fetchImpl: async (_url, options) => {
      body = JSON.parse(options.body);
      key = options.headers.get("Idempotency-Key");
      return jsonResponse({ agent: { id: "created" } });
    },
  });
  const input = {
    workspaceId: "workspace",
    repositoryIds: ["primary", "secondary"],
    title: "Investigator",
    role: "reviewer",
    prompt: "Inspect the failure",
  };

  await api.createAgent(input, "launch-key");

  assert.deepEqual(body, input);
  assert.equal(key, "launch-key");
  assert.deepEqual(Object.keys(body).sort(), ["prompt", "repositoryIds", "role", "title", "workspaceId"]);
});

test("API failures return safe server messages", async () => {
  const api = new CompanionAPI({
    fetchImpl: async () => jsonResponse({ error: "Source agent is not eligible" }, { status: 409 }),
  });

  await assert.rejects(
    api.createAgent({}, "key"),
    (error) => error instanceof APIError
      && error.status === 409
      && error.message === "Source agent is not eligible",
  );
});

test("requests have a bounded timeout with a safe retry message", async () => {
  const api = new CompanionAPI({
    requestTimeoutMs: 5,
    fetchImpl: async (_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
    }),
  });

  await assert.rejects(
    api.bootstrap(),
    (error) => error instanceof APIError
      && error.status === 0
      && error.message === "The Galpón request timed out. Try again.",
  );
});

test("network failures do not include transport internals in the user message", async () => {
  const api = new CompanionAPI({
    fetchImpl: async () => {
      throw new Error("connect ECONNREFUSED /live/galpon.sock");
    },
  });

  await assert.rejects(
    api.bootstrap(),
    (error) => error instanceof APIError
      && error.status === 0
      && error.message === "The Galpón host could not be reached.",
  );
});

test("SSE accepts default and named invalidation events", () => {
  const listeners = new Map();
  const source = {
    addEventListener(name, listener) { listeners.set(name, listener); },
    closeCalled: false,
    close() { this.closeCalled = true; },
  };
  const received = [];
  const states = [];
  const api = new CompanionAPI({
    fetchImpl: async () => jsonResponse({}),
    eventSourceFactory(url) {
      assert.equal(url, "/api/v1/events?after=12");
      return source;
    },
  });

  const close = api.subscribe(12, {
    onEvent: (event) => received.push(event),
    onState: (value) => states.push(value),
  });
  source.onopen();
  source.onmessage({ data: '{"kind":"agent_running"}', lastEventId: "13" });
  listeners.get("invalidate")({ data: '{"cursor":14}', lastEventId: "" });
  listeners.get("reset")({ data: '{"sequence":15}', lastEventId: "15" });
  source.onerror(new Error("lost"));
  close();

  assert.deepEqual(states, ["online", "reconnecting"]);
  assert.deepEqual(received.map((event) => event.seq), [13, 14, 15]);
  assert.equal(source.closeCalled, true);
});
