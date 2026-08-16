import assert from "node:assert/strict";
import test from "node:test";

import { APIError, CompanionAPI } from "./api.mjs";

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

  assert.equal(calls[0].url, "/test/api/v1/bootstrap");
  assert.equal(calls[0].options.method, "GET");
  assert.equal(calls[1].url, "/test/api/v1/agents/agent%2Fwith%20space");
  assert.equal(calls[1].options.credentials, "same-origin");
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

test("agent launch sends only the narrow source setup contract", async () => {
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
    sourceAgentId: "source",
    title: "Investigator",
    role: "reviewer",
    prompt: "Inspect the failure",
  };

  await api.createAgent(input, "launch-key");

  assert.deepEqual(body, input);
  assert.equal(key, "launch-key");
  assert.deepEqual(Object.keys(body).sort(), ["prompt", "role", "sourceAgentId", "title"]);
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
  source.onerror(new Error("lost"));
  close();

  assert.deepEqual(states, ["online", "reconnecting"]);
  assert.deepEqual(received.map((event) => event.seq), [13, 14]);
  assert.equal(source.closeCalled, true);
});
