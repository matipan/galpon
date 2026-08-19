import test from "node:test";
import assert from "node:assert/strict";
import { reduceTimeline } from "./timeline-state.mjs";

function event(seq, kind, values = {}) {
  return {
    seq,
    eventId: `event-${seq}`,
    kind,
    createdAt: `2026-08-17T12:00:${String(seq).padStart(2, "0")}Z`,
    ...values,
  };
}

test("agent lifecycle boundaries do not appear in discussion", () => {
  const result = reduceTimeline([
    event(1, "agent_start"),
    event(2, "user_message", { role: "user", content: "Ship it" }),
    event(3, "agent_end"),
    event(4, "agent_settled"),
  ]);

  assert.deepEqual(result.map((item) => item.content), ["Ship it"]);
});

test("assistant turns with no visible text do not leave empty avatar rows", () => {
  const result = reduceTimeline([
    event(1, "assistant_message_start", { role: "assistant" }),
    event(2, "assistant_message_end", { role: "assistant" }),
    event(3, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
  ]);

  assert.deepEqual(result.map((item) => item.role), ["tools"]);
});

test("standalone newline-only assistant endings do not create empty rows", () => {
  const result = reduceTimeline([
    event(1, "assistant_message_end", { role: "assistant", content: " \n\n  " }),
  ]);

  assert.deepEqual(result, []);
});

test("meaningful agent failures remain visible", () => {
  const result = reduceTimeline([
    event(1, "agent_failed", { content: "The test environment stopped", state: "failed" }),
  ]);

  assert.deepEqual(result.map((item) => item.content), ["The test environment stopped"]);
});

test("tool phases stay in durable order around assistant text", () => {
  const result = reduceTimeline([
    event(1, "user_message", { role: "user", content: "Check and fix it" }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1", content: '{"path":"app.mjs"}' }),
    event(3, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "read-1", content: "source", state: "completed" }),
    event(4, "assistant_message_start", { role: "assistant" }),
    event(5, "assistant_text_delta", { role: "assistant", content: "\n\nI found the issue.", isDelta: true }),
    event(6, "tool_execution_start", { role: "tool", toolName: "edit", toolCallId: "edit-1", content: '{"path":"app.mjs"}' }),
    event(7, "tool_execution_end", { role: "tool", toolName: "edit", toolCallId: "edit-1", content: "updated", state: "completed" }),
  ]);

  const groups = result.filter((item) => item.role === "tools");
  assert.equal(groups.length, 2);
  assert.deepEqual(result.map((item) => item.role), ["user", "tools", "assistant", "tools"]);
  assert.equal(result.find((item) => item.role === "assistant").content, "I found the issue.");
  assert.deepEqual(groups.map((group) => group.tools.map((tool) => tool.toolName)), [["read"], ["edit"]]);
  assert.equal(groups[0].state, "completed");
  assert.equal(groups[1].tools[0].output, "updated");
});

test("a new user turn starts a new work group", () => {
  const result = reduceTimeline([
    event(1, "user_message", { role: "user", content: "First" }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(3, "user_message", { role: "user", content: "Second" }),
    event(4, "tool_execution_start", { role: "tool", toolName: "bash", toolCallId: "bash-1" }),
  ]);

  assert.equal(result.filter((item) => item.role === "tools").length, 2);
});

test("a prompt stays before its actions and final answer", () => {
  const result = reduceTimeline([
    event(1, "delivery_completed", { role: "user", content: "Inspect it" }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(3, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(4, "assistant_message_end", { role: "assistant", content: "Done" }),
  ]);

  assert.deepEqual(result.map((item) => item.role), ["user", "tools", "assistant"]);
});

test("reused tool call IDs cannot create empty action groups", () => {
  const result = reduceTimeline([
    event(1, "user_message", { role: "user", content: "First" }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "same" }),
    event(3, "user_message", { role: "user", content: "Second" }),
    event(4, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "same" }),
  ]);

  assert.deepEqual(result.filter((item) => item.role === "tools").map((item) => item.tools.length), [1, 1]);
});

test("assistant reasoning is not part of the Companion timeline", () => {
  const result = reduceTimeline([
    event(1, "user_message", { role: "user", content: "Check it" }),
    event(2, "assistant_message_start", { role: "assistant" }),
    event(3, "assistant_reasoning_start", { role: "assistant" }),
    event(4, "assistant_reasoning_delta", { role: "assistant", content: "Inspect files", isDelta: true }),
    event(5, "assistant_reasoning_end", { role: "assistant", content: "Inspect files" }),
    event(6, "assistant_text_delta", { role: "assistant", content: "I found it", isDelta: true }),
  ]);

  assert.deepEqual(result.map((item) => item.role), ["user", "assistant"]);
  assert.equal(result[1].content, "I found it");
});

test("extending a live timeline does not move an existing tool group", () => {
  const first = reduceTimeline([
    event(1, "user_message", { role: "user", content: "Check it" }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
  ]);
  const extended = reduceTimeline([
    event(1, "user_message", { role: "user", content: "Check it" }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(3, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(4, "assistant_text_delta", { role: "assistant", content: "One result", isDelta: true }),
    event(5, "tool_execution_start", { role: "tool", toolName: "bash", toolCallId: "bash-1" }),
  ]);

  assert.deepEqual(first.map((item) => item.id), extended.slice(0, first.length).map((item) => item.id));
  assert.deepEqual(extended.map((item) => item.role), ["user", "tools", "assistant", "tools"]);
});
