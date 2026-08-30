import assert from "node:assert/strict";
import test from "node:test";

import {
  createAgentDraftStore,
  draftStorageKey,
  invalidationPlan,
  isConversationNearEnd,
  matchesDetailPage,
  optimisticMessage,
  readAgentDraft,
  reconcileOptimisticMessages,
  removeOptimisticMessage,
  settleOptimisticMessage,
  shouldSubmitComposerKey,
  writeAgentDraft,
} from "./companion-state.mjs";

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
    values,
  };
}

test("feedback drafts are isolated by agent and empty drafts are removed", () => {
  const storage = memoryStorage();
  writeAgentDraft(storage, "one", "First draft");
  writeAgentDraft(storage, "two", "Second draft");

  assert.equal(readAgentDraft(storage, "one"), "First draft");
  assert.equal(readAgentDraft(storage, "two"), "Second draft");
  writeAgentDraft(storage, "one", "");
  assert.equal(readAgentDraft(storage, "one"), "");
  assert.equal(storage.values.has(draftStorageKey("one")), false);
});

test("the draft store keeps per-agent values when browser storage fails", () => {
  const storage = {
    getItem: () => { throw new Error("unavailable"); },
    setItem: () => { throw new Error("unavailable"); },
    removeItem: () => { throw new Error("unavailable"); },
  };
  const drafts = createAgentDraftStore(storage);
  drafts.write("one", "First draft");
  drafts.write("two", "Second draft");

  assert.equal(drafts.read("one"), "First draft");
  assert.equal(drafts.read("two"), "Second draft");
  drafts.write("one", "");
  assert.equal(drafts.read("one"), "");
});

test("only Ctrl-Enter outside IME composition requests composer submission", () => {
  assert.equal(shouldSubmitComposerKey({ key: "Enter", ctrlKey: true }), true);
  assert.equal(shouldSubmitComposerKey({ key: "Enter" }), false);
  assert.equal(shouldSubmitComposerKey({ key: "Enter", ctrlKey: true, shiftKey: true }), false);
  assert.equal(shouldSubmitComposerKey({ key: "Enter", ctrlKey: true, metaKey: true }), false);
  assert.equal(shouldSubmitComposerKey({ key: "Enter", ctrlKey: true, isComposing: true }), false);
  assert.equal(shouldSubmitComposerKey({ key: "Enter", ctrlKey: true, keyCode: 229 }), false);
});

test("conversation proximity uses a bounded distance from the end", () => {
  assert.equal(isConversationNearEnd({ scrollTop: 780, clientHeight: 100, scrollHeight: 1000 }), true);
  assert.equal(isConversationNearEnd({ scrollTop: 779, clientHeight: 100, scrollHeight: 1000 }), false);
  assert.equal(isConversationNearEnd({ scrollTop: 0, clientHeight: 500, scrollHeight: 400 }), true);
});

test("scoped invalidations refresh only the matching open detail", () => {
  const selected = { id: "agent-a", workspaceId: "workspace-a" };
  assert.deepEqual(invalidationPlan([{ agentId: "agent-b", workspaceId: "workspace-b" }], selected), {
    bootstrap: true,
    detail: false,
  });
  assert.equal(invalidationPlan([{ agentId: "agent-a" }], selected).detail, true);
  assert.equal(invalidationPlan([{ workspaceId: "workspace-a" }], selected).detail, true);
  assert.equal(invalidationPlan([{}], selected).detail, true);
  assert.deepEqual(invalidationPlan([{ retryScope: "bootstrap" }], selected), { bootstrap: true, detail: false });
  assert.deepEqual(invalidationPlan([{ retryScope: "detail", agentId: "agent-a" }], selected), { bootstrap: false, detail: true });
});

test("older detail pages require the same visit generation and cursors", () => {
  const current = {
    agent: { id: "agent-a" },
    conversationHasMore: true,
    before: 40,
    messageHasMore: true,
    messageBefore: "cursor",
  };
  const request = { agentId: "agent-a", generation: 3, before: 40, messageBefore: "cursor" };
  assert.equal(matchesDetailPage(current, request, 3), true);
  assert.equal(matchesDetailPage(current, request, 4), false);
  assert.equal(matchesDetailPage({ ...current, before: 20 }, request, 3), false);
  assert.equal(matchesDetailPage({ ...current, agent: { id: "agent-b" } }, request, 3), false);
});

test("an optimistic message is replaced by the top-level send response", () => {
  const pending = optimisticMessage("Check this", "key", 10);
  assert.equal(pending.state, "sending");
  const settled = settleOptimisticMessage([pending], "key", {
    id: "message-id",
    status: "delivered",
    createdAt: 12,
  });
  assert.equal(settled[0].eventId, "delivery:message-id:prompt");
  assert.equal(settled[0].state, "delivered");
  assert.deepEqual(removeOptimisticMessage([pending], "key"), []);
});

test("optimistic image metadata is replaced by durable image metadata", () => {
  const local = [{ url: "blob:local", mimeType: "image/png", name: "screen.png" }];
  const durable = [{ id: "image-id", url: "/api/v1/images/image-id", mimeType: "image/png", name: "screen.png" }];
  const pending = optimisticMessage("", "image-key", local, 10);
  const settled = settleOptimisticMessage([pending], "image-key", {
    message: { id: "message-id", status: "queued", images: durable },
  });

  assert.deepEqual(pending.images, local);
  assert.deepEqual(settled[0].images, durable);
});

test("an uncertain send is removed when its durable request ID appears", () => {
  const pending = optimisticMessage("Check this", "request-key", 10);
  const overlay = new Map([["request-key", settleOptimisticMessage([pending], "request-key", { status: "pending" })[0]]]);
  const timeline = [{
    eventId: "delivery:message-id:prompt",
    clientRequestId: "request-key",
    kind: "delivery_queued",
  }];

  assert.equal(reconcileOptimisticMessages(overlay, timeline).size, 0);
});
