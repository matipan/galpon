import test from "node:test";
import assert from "node:assert/strict";
import { agentCountText, launchIsReady } from "./presentation.mjs";

test("filtered agent counts retain the complete list context", () => {
  assert.equal(agentCountText({ visible: 0, total: 103, query: "review" }), "0 MATCHES · 103 AGENTS");
  assert.equal(agentCountText({ visible: 0, total: 103, filter: "attention" }), "0 NEED YOU · 103 AGENTS");
  assert.equal(agentCountText({ visible: 2, total: 103, filter: "active" }), "2 ACTIVE · 103 AGENTS");
  assert.equal(agentCountText({ visible: 1, total: 1 }), "1 AGENT");
});

test("launch readiness requires explicit valid choices and non-empty text", () => {
  const base = {
    workspaceId: "workspace",
    startMode: "repository",
    repositoryId: "repository",
    sourceAgentId: "",
    title: "Audit worker",
    prompt: "Run the audit",
  };
  assert.equal(launchIsReady(base), true);
  assert.equal(launchIsReady({ ...base, workspaceId: "" }), false);
  assert.equal(launchIsReady({ ...base, repositoryId: "" }), false);
  assert.equal(launchIsReady({ ...base, title: "  " }), false);
  assert.equal(launchIsReady({ ...base, prompt: "" }), false);
  assert.equal(launchIsReady({ ...base, startMode: "agent", repositoryId: "", sourceAgentId: "source" }), true);
  assert.equal(launchIsReady({ ...base, startMode: "agent", repositoryId: "", sourceAgentId: "" }), false);
  assert.equal(launchIsReady({ ...base, startMode: "directory", repositoryId: "", sourceAgentId: "" }), true);
  assert.equal(launchIsReady({ ...base, startMode: "unknown", repositoryId: "", sourceAgentId: "" }), false);
});
