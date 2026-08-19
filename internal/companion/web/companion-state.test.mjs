import assert from "node:assert/strict";
import test from "node:test";

import {
  draftStorageKey,
  invalidationPlan,
  matchesDetailPage,
  optimisticMessage,
  readAgentDraft,
  removeOptimisticMessage,
  settleOptimisticMessage,
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
