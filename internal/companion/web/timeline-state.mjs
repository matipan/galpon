const hiddenLifecycleKinds = new Set([
  "agent_start",
  "agent_end",
  "agent_settled",
]);

export function reduceTimeline(source) {
  // The server owns timeline order. Never sort or move an existing item while
  // live events arrive.
  const events = [...(Array.isArray(source) ? source : [])]
    .filter((value) => value && typeof value === "object");
  const items = [];
  let activeToolGroup = null;
  let activeTools = new Map();
  let assistant = null;
  let lastAssistant = null;
  let assistantSegments = [];

  for (const raw of events) {
    const event = {
      seq: Number(raw.seq || 0),
      eventId: String(raw.eventId || `event-${raw.seq || items.length}`),
      kind: String(raw.kind || "event"),
      role: String(raw.role || ""),
      content: raw.content == null ? "" : String(raw.content),
      toolName: String(raw.toolName || ""),
      toolCallId: String(raw.toolCallId || ""),
      isDelta: raw.isDelta === true,
      isError: raw.isError === true,
      state: String(raw.state || ""),
      createdAt: raw.createdAt || "",
    };
    const kind = event.kind.toLocaleLowerCase();

    if (hiddenLifecycleKinds.has(kind)) continue;
    // Reasoning is private model work. It is not part of the Companion chat.
    if (kind.startsWith("assistant_reasoning_")) continue;

    if (kind.startsWith("delivery_") && event.role === "user") {
      const delivery = messageItem(event, "user");
      delivery.state = kind.slice("delivery_".length);
      items.push(delivery);
      activeToolGroup = null;
      activeTools = new Map();
      assistant = null;
      assistantSegments = [];
      continue;
    }

    if (kind === "user_message" || (kind === "message" && event.role === "user")) {
      items.push(messageItem(event, "user"));
      activeToolGroup = null;
      activeTools = new Map();
      assistant = null;
      assistantSegments = [];
      continue;
    }

    if (kind === "assistant_message_start") {
      // An empty placeholder appears and then disappears when a tool starts.
      // Wait for visible assistant text instead.
      assistant = null;
      assistantSegments = [];
      activeToolGroup = null;
      continue;
    }

    if (kind.includes("text_delta") && (event.role === "assistant" || kind.startsWith("assistant"))) {
      // Visible assistant text ends one contiguous tool phase. A later tool gets
      // a new group instead of moving the previous group below this text.
      activeToolGroup = null;
      if (!assistant) {
        assistant = messageItem(event, "assistant");
        assistant.state = event.state || "running";
        lastAssistant = assistant;
        assistantSegments.push(assistant);
        items.push(assistant);
      } else {
        applyContent(assistant, event);
        updateItem(assistant, event);
      }
      continue;
    }

    if (kind === "assistant_message_end") {
      activeToolGroup = null;
      if (!assistant && event.content) {
        const standalone = messageItem(event, "assistant");
        if (standalone.content.trim()) {
          assistant = standalone;
          lastAssistant = assistant;
          assistantSegments.push(assistant);
          items.push(assistant);
        }
      } else if (assistant) {
        applyContent(assistant, event);
        updateItem(assistant, event);
      }
      const finalState = event.isError ? "failed" : event.state || "completed";
      for (const segment of assistantSegments) segment.state = finalState;
      if (!assistantSegments.length && lastAssistant) lastAssistant.state = finalState;
      for (const segment of assistantSegments.filter((value) => !value.content.trim())) {
        const index = items.indexOf(segment);
        if (index >= 0) items.splice(index, 1);
        if (lastAssistant === segment) lastAssistant = null;
      }
      assistant = null;
      assistantSegments = [];
      continue;
    }

    if ((kind === "assistant_message" || kind === "message") && event.role === "assistant") {
      activeToolGroup = null;
      lastAssistant = messageItem(event, "assistant");
      items.push(lastAssistant);
      assistant = null;
      assistantSegments = [];
      continue;
    }

    if (kind.startsWith("tool_execution_") || kind === "tool_call" || kind === "tool_result" || event.role === "tool") {
      if (kind.endsWith("start") || kind === "tool_call") assistant = null;
      const key = event.toolCallId || `tool-${event.eventId}`;
      let record = activeTools.get(key);
      if (!record) {
        if (!activeToolGroup) {
          activeToolGroup = {
            id: `tool-group-${event.eventId}`,
            seq: event.seq,
            kind: "tool_group",
            role: "tools",
            tools: [],
            state: "running",
            createdAt: event.createdAt,
            updatedAt: event.createdAt,
          };
          items.push(activeToolGroup);
        }
        const tool = {
          id: key,
          toolName: event.toolName || "Tool",
          toolCallId: event.toolCallId,
          input: "",
          output: "",
          state: event.state || (kind.endsWith("start") ? "running" : ""),
          createdAt: event.createdAt,
          updatedAt: event.createdAt,
        };
        record = { tool, group: activeToolGroup };
        activeTools.set(key, record);
        activeToolGroup.tools.push(tool);
      }
      const { tool, group } = record;
      tool.toolName = event.toolName || tool.toolName;
      tool.updatedAt = event.createdAt || tool.updatedAt;
      tool.state = event.state || tool.state;
      if (kind.endsWith("start") || kind === "tool_call") {
        tool.input = event.content;
        if (!tool.state) tool.state = "running";
      } else {
        if (event.content) tool.output = event.isDelta ? tool.output + event.content : event.content;
        if (kind.endsWith("end") || kind === "tool_result") {
          tool.state = event.isError ? "failed" : event.state || "completed";
        }
      }
      group.updatedAt = tool.updatedAt;
      group.state = groupState(group.tools);
      continue;
    }

    if (event.content || event.state || kind.startsWith("agent_")) {
      items.push({
        id: event.eventId,
        seq: event.seq,
        kind: event.kind,
        role: event.role || "system",
        content: event.content || humanizeKind(event.kind),
        state: event.state,
        createdAt: event.createdAt,
        updatedAt: event.createdAt,
      });
    }
  }
  return items;
}

function messageItem(event, role) {
  return {
    id: event.eventId,
    seq: event.seq,
    kind: "message",
    role,
    content: role === "assistant" ? event.content.replace(/^(?:\r?\n)+/, "") : event.content,
    state: event.state,
    createdAt: event.createdAt,
    updatedAt: event.createdAt,
  };
}

function applyContent(item, event) {
  if (!event.content) return;
  const content = item.role === "assistant" && !item.content
    ? event.content.replace(/^(?:\r?\n)+/, "")
    : event.content;
  item.content = event.isDelta || !item.content ? item.content + content : content;
}

function updateItem(item, event) {
  item.updatedAt = event.createdAt || item.updatedAt;
  item.state = event.state || item.state;
}

function groupState(tools) {
  if (tools.some((tool) => tool.state === "failed")) return "failed";
  if (tools.some((tool) => !tool.state || tool.state === "running")) return "running";
  return "completed";
}

function humanizeKind(value) {
  return String(value || "Activity").replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toLocaleUpperCase());
}
