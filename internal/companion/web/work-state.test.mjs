import test from "node:test";
import assert from "node:assert/strict";
import { countWork, normalizeWorkItems, summarizeWork } from "./work-state.mjs";

test("work projection keeps observed and reported facts distinct", () => {
  const [item] = normalizeWorkItems([{
    id: "work",
    title: "Verifier",
    runtimeId: "must-not-leak",
    sessionId: "must-not-leak",
    path: "/must/not/leak",
    observation: { state: "started", source: "observed", lease: "stale", observedAt: 7, leaseObservedAt: 8, freshnessAt: 9 },
    activity: { category: "tool: read", status: "completed", source: "observed", observedAt: 9 },
    checkpoint: { phase: "blocked", summary: "Waiting for user input", blocker: "Choose one option", source: "reported", reportedAt: 10 },
    children: [{ id: "child", title: "Child", observation: { state: "failed", source: "observed", lease: "none" } }],
  }]);
  assert.equal(item.observation.source, "observed");
  assert.equal(item.observation.lease, "stale");
  assert.equal(item.observation.leaseObservedAt, 8);
  assert.deepEqual(item.activity, { category: "tool: read", status: "completed", source: "observed", observedAt: 9 });
  assert.equal(item.checkpoint.source, "reported");
  assert.equal(item.checkpoint.blocker, "Choose one option");
  assert.equal(item.children[0].observation.state, "failed");
  assert.equal(countWork([item]), 2);
  assert.deepEqual(summarizeWork([item]), { total: 2, active: 1, attention: 2, completed: 0, stale: 1 });
  assert.equal("runtimeId" in item, false);
  assert.equal("sessionId" in item, false);
  assert.equal("path" in item, false);
});

test("work projection keeps non-current reports historical", () => {
  const [item] = normalizeWorkItems([{
    id: "stale",
    title: "Verifier",
    observation: { state: "started", source: "observed", lease: "stale" },
    timeline: [{ kind: "checkpoint", label: "Prior safe report", source: "reported", createdAt: 10 }],
  }]);
  assert.equal(item.checkpoint, null);
  assert.deepEqual(item.historicalReport, { summary: "Prior safe report", source: "reported", reportedAt: 10, current: false });
  assert.equal(summarizeWork([item]).attention, 0);
});

test("work projection rejects unsafe observed activity labels", () => {
  const [item] = normalizeWorkItems([{
    id: "work",
    title: "Verifier",
    observation: { state: "started", source: "observed", lease: "fresh" },
    activity: { category: "tool: read private path and secret", status: "started", source: "observed", observedAt: 10 },
  }]);
  assert.equal(item.activity, null);
  assert.equal(JSON.stringify(item).includes("private path"), false);
});

test("work projection bounds regions and normalizes invalid lifecycle data", () => {
  const values = Array.from({ length: 140 }, (_, index) => ({
    id: `work-${index}`,
    title: "x".repeat(300),
    observation: { state: "private-thinking", lease: "stuck" },
    checkpoint: {
      source: "reported", phase: "working", summary: "s".repeat(300),
      milestones: Array.from({ length: 12 }, (_, index) => ({ label: "milestone", state: index === 0 ? "secret" : "pending" })),
      counts: Array.from({ length: 12 }, (_, index) => ({ label: "tests", completed: index === 0 ? 9 : 1, total: index === 0 ? 2 : 2 })),
    },
  }));
  const normalized = normalizeWorkItems(values);
  assert.equal(normalized.length, 128);
  assert.equal(normalized[0].title.length, 240);
  assert.equal(normalized[0].observation.state, "failed");
  assert.equal(normalized[0].observation.lease, "none");
  assert.equal(normalized[0].checkpoint.summary.length, 240);
  assert.equal(normalized[0].checkpoint.milestones.length, 8);
  assert.equal(normalized[0].checkpoint.milestones[0].state, "pending");
  assert.equal(normalized[0].checkpoint.counts.length, 8);
  assert.deepEqual(normalized[0].checkpoint.counts[0], { label: "tests", completed: 2, total: 2 });
});

test("work summary counts attention once and follows the bounded nested projection", () => {
  const [item] = normalizeWorkItems([{
    title: "Parent",
    observation: { state: "completed", lease: "fresh" },
    children: [{
      title: "Child",
      observation: { state: "started", lease: "stale" },
      checkpoint: { source: "reported", blocker: "Choose a release", milestones: [], counts: [] },
    }],
  }]);
  assert.deepEqual(summarizeWork([item]), { total: 2, active: 1, attention: 1, completed: 1, stale: 1 });
});
