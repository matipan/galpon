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
  });
  const fresh = detail({
    timeline: [{ seq: 0, eventId: "delivery:new:prompt" }],
    hasMore: true,
    messageHasMore: true,
    messageBefore: "2.new",
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
  });

  const merged = mergeRefreshedDetail(previous, fresh);
  assert.deepEqual(merged.timeline.map((event) => event.eventId), ["event-2", "event-7", "delivery:new:prompt"]);
  assert.equal(merged.before, 2);
  assert.equal(merged.messageBefore, "10.new");
  assert.equal(merged.hasMore, true);
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
