import test from "node:test";
import assert from "node:assert/strict";
import { mergeOlderDetail, mergeRefreshedDetail } from "./detail-state.mjs";

function detail(overrides = {}) {
  return {
    agent: { id: "agent" },
    timeline: [],
    hasMore: false,
    conversationHasMore: false,
    messageHasMore: false,
    before: 0,
    messageBefore: "",
    mirroredDeliveryResponses: [],
    messagePageIds: [],
    ...overrides,
  };
}

test("refresh replaces a transitional fallback with its mirrored response", () => {
  const previous = detail({
    timeline: [{ seq: 0, eventId: "delivery:message:response", kind: "assistant_message_end", content: "done" }],
    messageBefore: "10.message",
  });
  const fresh = detail({
    timeline: [{ seq: 7, eventId: "event-7", kind: "assistant_message_end", content: "done\n" }],
    before: 7,
    mirroredDeliveryResponses: ["message"],
  });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["event-7"]);
});

test("refresh keeps loaded positive-sequence history by its seq field", () => {
  const previous = detail({
    timeline: [{ seq: 2, eventId: "event-2" }, { seq: 7, eventId: "event-7" }],
    hasMore: true,
    conversationHasMore: true,
    before: 2,
  });
  const fresh = detail({ timeline: [{ seq: 7, eventId: "event-7" }, { seq: 8, eventId: "event-8" }], before: 7 });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["event-2", "event-7", "event-8"]);
  assert.equal(merged.before, 2);
  assert.equal(merged.conversationHasMore, true);
});

test("refresh preserves loaded message-only history when pages overlap", () => {
  const previous = detail({
    timeline: [
      { seq: 0, eventId: "delivery:old:prompt" },
      { seq: 0, eventId: "delivery:new:prompt" },
    ],
    messageBefore: "1.old",
    messagePageIds: ["old", "new"],
  });
  const fresh = detail({
    timeline: [{ seq: 0, eventId: "delivery:new:prompt" }],
    hasMore: true,
    messageHasMore: true,
    messageBefore: "2.new",
    messagePageIds: ["new"],
  });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["delivery:old:prompt", "delivery:new:prompt"]);
  assert.equal(merged.messageBefore, "1.old");
  assert.equal(merged.messageHasMore, false);
});

test("refresh keeps new message paging state independently from overlapping real history", () => {
  const previous = detail({
    timeline: [{ seq: 2, eventId: "event-2" }, { seq: 7, eventId: "event-7" }],
    before: 2,
  });
  const fresh = detail({
    timeline: [{ seq: 7, eventId: "event-7" }, { seq: 0, eventId: "delivery:new:prompt" }],
    hasMore: true,
    messageHasMore: true,
    before: 7,
    messageBefore: "10.new",
    messagePageIds: ["new"],
  });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["event-2", "event-7", "delivery:new:prompt"]);
  assert.equal(merged.before, 2);
  assert.equal(merged.messageBefore, "10.new");
  assert.equal(merged.hasMore, true);
});

test("a represented prompt does not prove durable message-page overlap", () => {
  const previous = detail({
    timeline: [{ eventId: "delivery:represented:prompt" }, { seq: 7, eventId: "event-7" }],
    before: 7,
  });
  const fresh = detail({
    timeline: [
      { eventId: "delivery:represented:prompt" },
      { seq: 7, eventId: "event-7" },
      { eventId: "delivery:newest:prompt" },
    ],
    hasMore: true,
    messageHasMore: true,
    before: 7,
    messageBefore: "10.newest",
    messagePageIds: ["newest"],
  });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.equal(merged.messageHasMore, true);
  assert.equal(merged.messageBefore, "10.newest");
  assert.equal(merged.hasMore, true);
});

test("refresh keeps a local send at the tail until its durable row arrives", () => {
  const previous = detail({
    timeline: [
      { seq: 7, eventId: "event-7" },
      { seq: 0, eventId: "optimistic:key", localOnly: true, content: "new message" },
    ],
  });
  const fresh = detail({ timeline: [{ seq: 8, eventId: "event-8" }] });

  const pending = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(pending.timeline.map((event) => event.eventId), ["event-8", "optimistic:key"]);

  const durable = mergeRefreshedDetail(pending, detail({ timeline: [{ seq: 9, eventId: "optimistic:key" }] }));
  assert.deepEqual(durable.timeline.map((event) => event.eventId), ["optimistic:key"]);
});

test("refresh replaces history when the new page has no overlap", () => {
  const previous = detail({
    timeline: [{ seq: 2, eventId: "event-2" }, { seq: 3, eventId: "event-3" }],
    hasMore: true,
    conversationHasMore: true,
    before: 2,
  });
  const fresh = detail({ timeline: [{ seq: 200, eventId: "event-200" }], before: 200 });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["event-200"]);
  assert.equal(merged.before, 200);
});

test("older pages keep all unanchored message rows after durable events", () => {
  const current = detail({
    timeline: [{ seq: 9, eventId: "event-9" }, { seq: 0, eventId: "delivery:new:prompt" }],
    hasMore: true,
    conversationHasMore: true,
    messageHasMore: true,
    before: 9,
    messageBefore: "9.new",
  });
  const older = detail({
    timeline: [{ seq: 2, eventId: "event-2" }, { seq: 0, eventId: "delivery:old:prompt" }],
    before: 2,
    messageBefore: "2.old",
  });

  const merged = mergeOlderDetail(current, older);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), [
    "event-2", "event-9", "delivery:old:prompt", "delivery:new:prompt",
  ]);
});

test("older page retracts a fallback already present in the current view", () => {
  const current = detail({
    timeline: [{ eventId: "delivery:message:response" }, { seq: 9, eventId: "event-9" }],
    hasMore: true,
    conversationHasMore: true,
    before: 9,
  });
  const older = detail({
    timeline: [{ seq: 2, eventId: "event-2" }],
    before: 2,
    mirroredDeliveryResponses: ["message"],
  });

  const merged = mergeOlderDetail(current, older);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["event-2", "event-9"]);
});
