import assert from "node:assert/strict";
import test from "node:test";

import { flattenOperationsWork, matchesOperationsResponse, normalizeAgentOperations } from "./operations-state.mjs";

test("agent operations keeps received and delegated work in current-first sections", () => {
  const value = normalizeAgentOperations({
    version: 2,
    agent: { id: "worker", title: "Work\u202eer", status: "running" },
    workspace: { id: "workspace", title: "Work" },
    summary: { received: 1, delegated: 2, current: 2, needsAttention: 1, results: 1, failures: 1 },
    current: [
      { id: "received", title: "Received task", direction: "received", observation: { state: "started", lease: "fresh", attempt: 2 } },
      { id: "delegated", title: "Delegated task", direction: "delegated", observation: { state: "waiting", lease: "none" } },
    ],
    attention: [
      { id: "failure", title: "Failed review", direction: "delegated", observation: { state: "failed", lease: "none" }, result: { stage: "delivery_failed", label: "Delivery failed", source: "observed" } },
    ],
    recentResults: [
      { id: "failure", title: "Failed review", direction: "delegated", observation: { state: "failed", lease: "none" } },
    ],
    recentCoordination: [{ workId: "failure", workTitle: "Failed review", targetTitle: "Reviewer", kind: "result", label: "failed", source: "observed", createdAt: 5 }],
    directOperations: [{ title: "Direct Pi work", state: "waiting", source: "observed", lease: "none", count: 2, observedAt: 8, operationId: "private" }],
    truncation: { truncated: true, recentResultsOmitted: 3 },
  });

  assert.equal(value.agent.title, "Worker");
  assert.deepEqual(value.current.map((item) => item.direction), ["received", "delegated"]);
  assert.equal(value.attention[0].result.stage, "delivery_failed");
  assert.deepEqual(flattenOperationsWork(value).map(({ item, section }) => [item.id, section]), [
    ["received", "Current"], ["delegated", "Current"], ["failure", "Attention"],
  ]);
  assert.deepEqual(value.directOperations, [{ title: "Direct Pi work", state: "waiting", source: "observed", lease: "none", count: 2, observedAt: 8 }]);
  assert.equal(value.recentCoordination[0].label, "failed");
  assert.equal(value.truncation.recentResultsOmitted, 3);
});

test("agent operations applies section bounds without reclassifying state", () => {
  const value = normalizeAgentOperations({
    version: 1,
    agent: { id: "agent", title: "Agent" },
    current: Array.from({ length: 70 }, (_, index) => ({ id: `current-${index}`, direction: "received", observation: { state: "server_state", lease: "server_lease" } })),
    attention: Array.from({ length: 40 }, (_, index) => ({ id: `attention-${index}`, direction: "delegated", observation: { state: "failed" } })),
    recentResults: Array.from({ length: 40 }, (_, index) => ({ id: `result-${index}`, direction: "delegated", observation: { state: "completed" } })),
  });
  assert.equal(value.current.length, 64);
  assert.equal(value.attention.length, 32);
  assert.equal(value.recentResults.length, 32);
  assert.equal(value.current[0].observation.state, "server_state");
  assert.equal(value.current[0].observation.lease, "server_lease");
});

test("operations response fencing accepts only the current agent generation", () => {
  const current = { activeAgentId: "agent-b", generation: 4 };
  assert.equal(matchesOperationsResponse(current, { agentId: "agent-b", generation: 4 }), true);
  assert.equal(matchesOperationsResponse(current, { agentId: "agent-a", generation: 4 }), false);
  assert.equal(matchesOperationsResponse(current, { agentId: "agent-b", generation: 3 }), false);
  assert.equal(matchesOperationsResponse(current, { agentId: "agent-b", generation: 4 }, true), false);
});
