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

test("compaction shows one boundary without the reason, summary, or attachments", () => {
  const events = [
    event(1, "assistant_text_delta", { role: "assistant", content: "Before compaction", isDelta: true }),
    event(2, "compaction_start", { content: "internal-start-reason" }),
    event(3, "compaction_end", {
      content: "internal-compaction-summary",
      images: [{ url: "/api/v1/images/internal-compaction-image" }],
    }),
    event(4, "assistant_text_delta", { role: "assistant", content: "After compaction", isDelta: true }),
  ];
  const starting = reduceTimeline(events.slice(0, 2));
  assert.deepEqual(starting.map((item) => item.content), ["Before compaction"]);
  const result = reduceTimeline(events);
  assert.deepEqual(result.map((item) => item.kind), ["message", "compaction", "message"]);
  assert.equal(result[0].content, "Before compaction");
  assert.equal(result[2].content, "After compaction");
  assert.equal(result[1].createdAt, events[2].createdAt);
  assert.doesNotMatch(JSON.stringify(result), /internal-/);
  assert.equal(events[2].content, "internal-compaction-summary");
});

test("historical compaction without a start or summary separates tool groups", () => {
  const result = reduceTimeline([
    event(1, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(2, "compaction_end"),
    event(3, "tool_execution_start", { role: "tool", toolName: "bash", toolCallId: "bash-1" }),
  ]);
  assert.deepEqual(result.map((item) => item.kind), ["tool_group", "compaction", "tool_group"]);
  assert.equal(result[1].id, "event-2");
});

test("message and tool images stay attached to their timeline items", () => {
  const userImage = { id: "one", url: "/api/v1/images/one", mimeType: "image/png", name: "screen.png" };
  const toolImage = { id: "two", url: "/api/v1/images/two", mimeType: "image/webp" };
  const result = reduceTimeline([
    event(1, "user_message", { role: "user", images: [userImage] }),
    event(2, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(3, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "read-1", images: [toolImage] }),
    event(4, "assistant_message_end", { role: "assistant", images: [userImage] }),
  ]);

  const normalizedUserImage = { ...userImage, width: 0, height: 0 };
  const normalizedToolImage = { ...toolImage, name: "", width: 0, height: 0 };
  assert.deepEqual(result[0].images, [normalizedUserImage]);
  assert.deepEqual(result[1].tools[0].images, [normalizedToolImage]);
  assert.deepEqual(result[2].images, [normalizedUserImage]);
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

test("meaningful agent failures remain visible and end a work group", () => {
  const result = reduceTimeline([
    event(1, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(2, "agent_failed", { content: "The test environment stopped", state: "failed" }),
    event(3, "tool_execution_start", { role: "tool", toolName: "bash", toolCallId: "bash-1" }),
  ]);

  assert.deepEqual(result.map((item) => item.role), ["tools", "system", "tools"]);
  assert.equal(result[1].content, "The test environment stopped");
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

test("hidden assistant protocol boundaries keep one work group", () => {
  const result = reduceTimeline([
    event(1, "user_message", { role: "user", content: "Check it" }),
    event(2, "assistant_message_start", { role: "assistant" }),
    event(3, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
    event(4, "tool_execution_end", { role: "tool", toolName: "read", toolCallId: "read-1", state: "completed" }),
    event(5, "assistant_message_end", { role: "assistant" }),
    event(6, "assistant_message_start", { role: "assistant" }),
    event(7, "tool_execution_start", { role: "tool", toolName: "edit", toolCallId: "edit-1" }),
    event(8, "tool_execution_end", { role: "tool", toolName: "edit", toolCallId: "edit-1", state: "completed" }),
    event(9, "assistant_message_end", { role: "assistant" }),
  ]);

  const prefix = reduceTimeline([
    event(1, "user_message", { role: "user", content: "Check it" }),
    event(2, "assistant_message_start", { role: "assistant" }),
    event(3, "tool_execution_start", { role: "tool", toolName: "read", toolCallId: "read-1" }),
  ]);
  const groups = result.filter((item) => item.role === "tools");
  assert.equal(groups.length, 1);
  assert.equal(groups[0].id, prefix.find((item) => item.role === "tools").id);
  assert.deepEqual(groups[0].tools.map((tool) => tool.toolName), ["read", "edit"]);
  assert.equal(groups[0].state, "completed");
});

test("agent deliveries are distinct from user messages", () => {
  const result = reduceTimeline([
    event(1, "delivery_queued", { eventId: "direct", role: "user", content: "Phone feedback" }),
    event(1, "delivery_completed", {
      eventId: "bot",
      role: "user",
      content: "Review complete",
      isAgentDelivery: true,
      deliveryKind: "result",
      deliverySenderTitle: "Parity reviewer",
    }),
  ]);

  assert.deepEqual(result.map((item) => item.role), ["user", "delivery"]);
  assert.deepEqual(result.map((item) => item.seq), [1, 1]);
  assert.equal(result[1].deliveryKind, "result");
  assert.equal(result[1].deliverySenderTitle, "Parity reviewer");
  assert.equal(result[1].state, "completed");
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
