import test from "node:test";
import assert from "node:assert/strict";
import { countWork, normalizeWorkItems } from "./work-state.mjs";

test("work projection keeps observed and reported facts distinct", () => {
  const [item] = normalizeWorkItems([{
    id: "work",
    title: "Verifier",
    runtimeId: "must-not-leak",
    sessionId: "must-not-leak",
    path: "/must/not/leak",
    observation: { state: "started", source: "observed", lease: "stale" },
    checkpoint: { phase: "blocked", summary: "Waiting for user input", blocker: "Choose one option", source: "reported", reportedAt: 10 },
    children: [{ id: "child", title: "Child", observation: { state: "failed", source: "observed", lease: "none" } }],
  }]);
  assert.equal(item.observation.source, "observed");
  assert.equal(item.observation.lease, "stale");
  assert.equal(item.checkpoint.source, "reported");
  assert.equal(item.checkpoint.blocker, "Choose one option");
  assert.equal(item.children[0].observation.state, "failed");
  assert.equal(countWork([item]), 2);
  assert.equal("runtimeId" in item, false);
  assert.equal("sessionId" in item, false);
  assert.equal("path" in item, false);
});

test("work projection bounds regions and normalizes invalid lifecycle data", () => {
  const values = Array.from({ length: 140 }, (_, index) => ({
    id: `work-${index}`,
    title: "x".repeat(300),
    observation: { state: "private-thinking", lease: "stuck" },
    checkpoint: {
      source: "reported", phase: "working", summary: "s".repeat(300),
      milestones: Array.from({ length: 12 }, () => ({ label: "milestone", state: "pending" })),
      counts: Array.from({ length: 12 }, () => ({ label: "tests", completed: 1, total: 2 })),
    },
  }));
  const normalized = normalizeWorkItems(values);
  assert.equal(normalized.length, 128);
  assert.equal(normalized[0].title.length, 240);
  assert.equal(normalized[0].observation.state, "failed");
  assert.equal(normalized[0].observation.lease, "none");
  assert.equal(normalized[0].checkpoint.summary.length, 240);
  assert.equal(normalized[0].checkpoint.milestones.length, 8);
  assert.equal(normalized[0].checkpoint.counts.length, 8);
});
