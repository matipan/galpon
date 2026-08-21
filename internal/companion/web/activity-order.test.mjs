import assert from "node:assert/strict";
import test from "node:test";

import { agentTreeActivity, orderTopLevelAgentsByActivity } from "./activity-order.mjs";

const agent = (id, updatedAt, delegatedAgents = []) => ({ id, title: id, updatedAt, delegatedAgents });
const workspace = (id, agents) => ({ id, title: id, agents });
const agentIDs = (entries) => entries.map((entry) => entry.agent.id);
const ids = (values) => values.map((value) => value.id);

test("activity creates one global top-level order and keeps delegated agents nested", () => {
  const workspaces = [
    workspace("first-workspace", [
      agent("parent", 100, [agent("new-child", 500), agent("old-child", 200)]),
      agent("old-top-level", 50),
    ]),
    workspace("second-workspace", [agent("middle", 400)]),
  ];

  const ordered = orderTopLevelAgentsByActivity(workspaces, [], { recompute: true });

  assert.deepEqual(agentIDs(ordered), ["parent", "middle", "old-top-level"]);
  assert.deepEqual(ids(ordered[0].agent.delegatedAgents), ["new-child", "old-child"]);
  assert.equal(agentTreeActivity(ordered[0].agent), 500);
  assert.equal(ordered[1].workspace.id, "second-workspace");
});

test("live refresh freezes known global rows and inserts new agents by activity", () => {
  const initial = [
    workspace("first-workspace", [agent("first", 300), agent("third", 100)]),
    workspace("second-workspace", [agent("second", 200)]),
  ];
  const previous = orderTopLevelAgentsByActivity(initial, [], { recompute: true });
  const refreshed = [
    workspace("first-workspace", [agent("first", 300), agent("third", 600), agent("newest", 700)]),
    workspace("second-workspace", [agent("second", 200), agent("between", 250)]),
  ];

  const ordered = orderTopLevelAgentsByActivity(refreshed, previous);

  assert.deepEqual(agentIDs(ordered), ["newest", "first", "between", "second", "third"]);
});

test("list navigation rebuilds the complete global activity order", () => {
  const initial = [
    workspace("first-workspace", [agent("first", 300), agent("second", 200)]),
    workspace("second-workspace", [agent("third", 100)]),
  ];
  const previous = orderTopLevelAgentsByActivity(initial, [], { recompute: true });
  const refreshed = [
    workspace("first-workspace", [agent("first", 300), agent("second", 200)]),
    workspace("second-workspace", [agent("third", 600)]),
  ];
  const stable = orderTopLevelAgentsByActivity(refreshed, previous);
  const navigated = orderTopLevelAgentsByActivity(refreshed, stable, { recompute: true });

  assert.deepEqual(agentIDs(stable), ["first", "second", "third"]);
  assert.deepEqual(agentIDs(navigated), ["third", "first", "second"]);
});
