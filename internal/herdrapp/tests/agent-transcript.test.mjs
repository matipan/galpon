import test from "node:test";
import assert from "node:assert/strict";
import { applyEvent, conversationSegments, createTranscript, loadSnapshot } from "../mods/agent/transcript.js";

const text = (text) => ({ type: "text", text });
const thinking = { type: "thinking", thinking: "Check the result." };
const call = (id) => ({ type: "toolCall", id, name: "bash", arguments: { command: "check-job" } });
const assistant = (content, stopReason, timestamp) => ({ role: "assistant", content, stopReason, timestamp });
const result = (id, timestamp) => ({ role: "toolResult", toolCallId: id, toolName: "bash", content: [text("Job failed")], timestamp });
const answer = ["I found the cause.", "May I apply the fix?"];
const followUp = "Another job failed for the same reason. I still need your approval.";
const messages = [
  { role: "user", content: [text("Find the cause. Ask before you change anything.")], timestamp: 1000 },
  assistant([thinking, text("I will check the job."), call("first")], "toolUse", 2000),
  result("first", 3000),
  assistant([thinking, ...answer.map(text)], "stop", 4000),
  { role: "custom", customType: "galpon-background", display: true, content: "A deployment failed.",
    details: { notices: [{ kind: "monitor", status: "running", description: "Deployments", lines: ["Job failed"] }] }, timestamp: 4029 },
  assistant([thinking, text("I will check the next job."), call("second")], "toolUse", 5000),
  result("second", 6000),
  assistant([thinking, text(followUp)], "stop", 7000),
];

function append(t, message) {
  applyEvent(t, { type: "message_start", message });
  applyEvent(t, { type: "message_end", message });
}

function visibleText(t) {
  return conversationSegments(t.items)
    .filter((segment) => segment.kind === "item" && segment.item.kind === "text")
    .map((segment) => segment.item.text);
}

// Turn summaries depend on the run timing. This projection covers the
// conversation text that is visible and the content inside folded logs.
function layout(t) {
  return conversationSegments(t.items).filter((segment) => segment.item?.kind !== "turn")
    .map((segment) => segment.kind === "work"
      ? { work: segment.items.map((item) => item.text ?? item.name) }
      : { kind: segment.item.kind, text: segment.item.text });
}

test("a completed answer stays visible while a monitor continues the live run", () => {
  const t = createTranscript();
  applyEvent(t, { type: "agent_start" });
  messages.slice(0, 4).forEach((message) => append(t, message));
  assert.deepEqual(visibleText(t), answer);

  append(t, messages[4]);
  assert.deepEqual(visibleText(t), answer);

  const continuation = messages[5];
  applyEvent(t, { type: "message_start", message: { role: "assistant" } });
  applyEvent(t, { type: "message_update", assistantMessageEvent: { type: "thinking_start", contentIndex: 0 } });
  applyEvent(t, { type: "message_update", assistantMessageEvent: { type: "thinking_delta", contentIndex: 0, delta: thinking.thinking } });
  assert.deepEqual(visibleText(t), answer);
  applyEvent(t, { type: "message_end", message: continuation });
  assert.deepEqual(visibleText(t), answer);

  messages.slice(6).forEach((message) => append(t, message));
  applyEvent(t, { type: "agent_settled" });
  assert.deepEqual(visibleText(t), [...answer, followUp]);
});

test("answers and work logs have the same layout live, from history, and after reconnecting", () => {
  const live = createTranscript();
  applyEvent(live, { type: "agent_start" });
  messages.forEach((message) => append(live, message));
  applyEvent(live, { type: "agent_settled" });

  const history = loadSnapshot(createTranscript(), { messages, streaming: false });
  assert.deepEqual(visibleText(history), [...answer, followUp]);
  assert.deepEqual(layout(live), layout(history));

  for (const cut of [4, 5, 7]) {
    const reconnected = loadSnapshot(createTranscript(), { messages: messages.slice(0, cut), streaming: true });
    messages.slice(cut).forEach((message) => append(reconnected, message));
    applyEvent(reconnected, { type: "agent_settled" });
    assert.deepEqual(layout(reconnected), layout(history), `reconnect after message ${cut}`);
  }
});

test("tool commentary stays in work logs and streamed answers keep their text once complete", () => {
  const t = createTranscript();
  applyEvent(t, { type: "agent_start" });
  messages.slice(0, 3).forEach((message) => append(t, message));
  applyEvent(t, { type: "message_start", message: { role: "assistant" } });
  applyEvent(t, { type: "message_update", assistantMessageEvent: { type: "text_start", contentIndex: 0 } });
  applyEvent(t, { type: "message_update", assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: "May I " } });
  applyEvent(t, { type: "message_update", assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: "apply the fix?" } });
  assert.deepEqual(visibleText(t), [answer[1]]);
  applyEvent(t, { type: "message_end", message: assistant([text(answer[1])], "stop", 4000) });
  messages.slice(4).forEach((message) => append(t, message));
  applyEvent(t, { type: "agent_settled" });

  assert.deepEqual(visibleText(t), [answer[1], followUp]);
  const logs = conversationSegments(t.items).filter((segment) => segment.kind === "work");
  assert.deepEqual(logs.flatMap((segment) => segment.items.filter((item) => item.kind === "text").map((item) => item.text)),
    ["I will check the job.", "I will check the next job."]);
  assert.deepEqual(logs.flatMap((segment) => segment.items.filter((item) => item.kind === "tool").map((item) => item.callId)),
    ["first", "second"]);
});
