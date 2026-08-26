import assert from "node:assert/strict";
import test from "node:test";

import { flattenOperationsWork, matchesOperationsResponse, normalizeWorkspaceOperations } from "./operations-state.mjs";

test("operations normalization keeps observed delivery facts separate from reports", () => {
  const value = normalizeWorkspaceOperations({
    version: 1,
    workspace: { id: "workspace", title: "Work\u202e" },
    summary: { agents: 2, staleObservations: 1 },
    work: [{
      id: "root",
      title: "Worker",
      priority: "reported_blocker",
      observation: { state: "started", source: "observed", lease: "stale", attempt: 3 },
      checkpoint: { phase: "blocked", summary: "Waiting for a choice", blocker: "Choose a label", source: "reported" },
      children: [{ id: "child", title: "Reviewer", observation: { state: "queued", source: "observed", lease: "none" } }],
    }],
    truncation: { truncated: true },
  });

  assert.equal(value.workspace.title, "Work");
  assert.deepEqual(value.work[0].observation, { state: "started", source: "observed", lease: "stale", attempt: 3 });
  assert.equal(value.work[0].checkpoint.source, "reported");
  assert.equal(value.work[0].priority, "reported_blocker");
  assert.deepEqual(flattenOperationsWork(value.work).map(({ item, depth }) => [item.id, depth]), [["root", 0], ["child", 1]]);
  assert.equal(value.truncation.truncated, true);
});

test("operations normalization applies bounds without reclassifying server state", () => {
  const value = normalizeWorkspaceOperations({
    version: 1,
    workspace: { id: "workspace", title: "Work" },
    agents: Array.from({ length: 140 }, (_, index) => ({ id: `agent-${index}`, title: `Agent ${index}`, status: "running" })),
    work: Array.from({ length: 70 }, (_, index) => ({ id: `work-${index}`, title: `Work ${index}`, priority: "server_label", observation: { state: "server_state", lease: "server_freshness" } })),
    timeline: Array.from({ length: 140 }, (_, index) => ({ workId: `work-${index}`, label: "Fact" })),
  });
  assert.equal(value.agents.length, 128);
  assert.equal(value.work.length, 64);
  assert.equal(value.timeline.length, 128);
  assert.equal(value.work[0].observation.state, "server_state");
  assert.equal(value.work[0].observation.lease, "server_freshness");
  assert.equal(value.work[0].priority, "server_label");
});

test("operations response fencing accepts only the current workspace generation", () => {
  const current = { activeWorkspaceId: "workspace-b", generation: 4 };
  assert.equal(matchesOperationsResponse(current, { workspaceId: "workspace-b", generation: 4 }), true);
  assert.equal(matchesOperationsResponse(current, { workspaceId: "workspace-a", generation: 4 }), false);
  assert.equal(matchesOperationsResponse(current, { workspaceId: "workspace-b", generation: 3 }), false);
  assert.equal(matchesOperationsResponse(current, { workspaceId: "workspace-b", generation: 4 }, true), false);
});
