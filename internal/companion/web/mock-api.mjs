const pause = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

const now = Date.now();
let cursor = 28;
let nextAgent = 4;
const subscribers = new Set();
const timers = new Set();

const repositories = [
  { id: "repository-galpon", title: "Galpon" },
  { id: "repository-pi", title: "Pi coding agent" },
];

const workspaces = [
  {
    id: "workspace-galpon",
    title: "Galpon",
    agents: [
      {
        id: "agent-captain",
        title: "Mobile companion",
        role: "coordinator",
        status: "running",
        canCopyPlacement: true,
        lastActivity: "Running isolated frontend tests",
        updatedAt: new Date(now - 38_000).toISOString(),
        delegatedAgents: [{
          id: "agent-worker",
          workspaceId: "workspace-galpon",
          workspaceTitle: "Galpon",
          title: "Background test runner",
          role: "tester",
          status: "running",
          canCopyPlacement: true,
          lastActivity: "Running browser tests",
          updatedAt: new Date(now - 22_000).toISOString(),
        }],
      },
      {
        id: "agent-reviewer",
        title: "Security reviewer",
        role: "reviewer",
        status: "idle",
        canCopyPlacement: true,
        lastActivity: "Review complete",
        updatedAt: new Date(now - 19 * 60_000).toISOString(),
      },
    ],
  },
  {
    id: "workspace-runtime",
    title: "Runtime reliability",
    agents: [
      {
        id: "agent-investigator",
        title: "Reconnect investigator",
        role: "investigator",
        status: "failed",
        canCopyPlacement: false,
        lastActivity: "Needs desktop action",
        updatedAt: new Date(now - 7 * 60_000).toISOString(),
      },
    ],
  },
];

const timelines = new Map([
  ["agent-captain", [
    event(1, "user_message", {
      role: "user",
      content: "Build the phone companion without touching the live Galpon session. Use an isolated state directory and mock model endpoint.",
      createdAt: now - 12 * 60_000,
    }),
    event(2, "assistant_message_start", {
      role: "assistant",
      content: "",
      createdAt: now - 11.8 * 60_000,
    }),
    event(3, "assistant_text_delta", {
      role: "assistant",
      content: "I will first inspect the existing theme and service boundaries. ",
      isDelta: true,
      createdAt: now - 11.7 * 60_000,
    }),
    event(4, "assistant_text_delta", {
      role: "assistant",
      content: "All development and tests will use separate state and renderer sessions.",
      isDelta: true,
      createdAt: now - 11.6 * 60_000,
    }),
    event(5, "tool_execution_start", {
      role: "tool",
      toolName: "read",
      toolCallId: "tool-theme",
      content: '{"path":"internal/tui/theme.go"}',
      createdAt: now - 11.4 * 60_000,
    }),
    event(6, "tool_execution_end", {
      role: "tool",
      toolName: "read",
      toolCallId: "tool-theme",
      content: "Tokyo Night Moon\nBackground #222436\nSurface #1e2030\nStatus #7aa2f7",
      isDelta: false,
      state: "completed",
      createdAt: now - 11.2 * 60_000,
    }),
    event(7, "assistant_text_delta", {
      role: "assistant",
      content: "\n\nThe interface can use the same flat prompt, result, and blue status bands without copying the terminal layout.",
      isDelta: true,
      createdAt: now - 10.8 * 60_000,
    }),
    event(8, "assistant_message_end", {
      role: "assistant",
      content: "",
      state: "completed",
      createdAt: now - 10.6 * 60_000,
    }),
    event(90, "agent_start", { createdAt: now - 40_000 }),
    event(10, "tool_execution_start", {
      role: "tool",
      toolName: "bash",
      toolCallId: "tool-test",
      content: "node --test internal/companion/web/api.test.mjs",
      createdAt: now - 33_000,
    }),
    event(11, "tool_execution_update", {
      role: "tool",
      toolName: "bash",
      toolCallId: "tool-test",
      content: "TAP version 13\n",
      isDelta: true,
      state: "running",
      createdAt: now - 29_000,
    }),
    event(91, "tool_execution_start", {
      role: "tool",
      toolName: "read",
      toolCallId: "tool-read-api",
      content: '{"path":"internal/companion/web/api.mjs"}',
      createdAt: now - 24_000,
    }),
    event(92, "tool_execution_end", {
      role: "tool",
      toolName: "read",
      toolCallId: "tool-read-api",
      content: "export class CompanionAPI {}",
      state: "completed",
      createdAt: now - 21_000,
    }),
    event(93, "agent_end", { createdAt: now - 18_000 }),
    event(94, "agent_settled", { createdAt: now - 17_000 }),
  ]],
  ["agent-reviewer", [
    event(12, "user_message", {
      role: "user",
      content: "Review the proposed phone boundary and list security risks.",
      createdAt: now - 24 * 60_000,
    }),
    event(13, "assistant_message_start", { role: "assistant", createdAt: now - 23.8 * 60_000 }),
    event(14, "assistant_text_delta", {
      role: "assistant",
      content: "The narrow boundary is sound. Keep the browser API separate from the local daemon API, escape all transcript text, and bind through Tailscale instead of a public interface. See the [safety guide](https://example.test/safety).",
      isDelta: true,
      createdAt: now - 21 * 60_000,
    }),
    event(15, "assistant_message_end", {
      role: "assistant",
      state: "completed",
      createdAt: now - 19 * 60_000,
    }),
  ]],
  ["agent-investigator", [
    event(16, "tool_execution_start", {
      role: "tool",
      toolName: "bash",
      toolCallId: "tool-reconnect",
      content: "run isolated reconnect check",
      createdAt: now - 8 * 60_000,
    }),
    event(17, "tool_execution_end", {
      role: "tool",
      toolName: "bash",
      toolCallId: "tool-reconnect",
      content: "mock endpoint closed before the check completed",
      isError: true,
      createdAt: now - 7.5 * 60_000,
    }),
    event(18, "agent_failed", {
      content: "The isolated mock endpoint stopped before the reconnect check completed.",
      state: "failed",
      createdAt: now - 7 * 60_000,
    }),
  ]],
]);

function event(seq, kind, fields = {}) {
  return {
    seq,
    eventId: `mock-event-${seq}`,
    kind,
    role: "",
    content: "",
    toolName: "",
    toolCallId: "",
    isDelta: false,
    createdAt: new Date(fields.createdAt ?? now).toISOString(),
    ...fields,
  };
}

function clone(value) {
  return structuredClone(value);
}

function findAgent(id) {
  const findInTree = (agents) => {
    for (const agent of agents) {
      if (agent.id === id) return agent;
      const child = findInTree(agent.delegatedAgents || []);
      if (child) return child;
    }
    return null;
  };
  for (const workspace of workspaces) {
    const agent = findInTree(workspace.agents);
    if (agent) return { workspace, agent };
  }
  return null;
}

function publish(value) {
  const sequence = Number(value.seq || 0) || cursor + 1;
  cursor = Math.max(cursor, sequence);
  const durable = { ...value, seq: sequence, eventId: value.eventId || `mock-event-${sequence}` };
  for (const subscriber of subscribers) subscriber.onEvent?.(clone(durable));
}

function schedule(callback, delay) {
  const timer = setTimeout(() => {
    timers.delete(timer);
    callback();
  }, delay);
  timers.add(timer);
}

export class MockCompanionAPI {
  async bootstrap() {
    await pause(240);
    return clone({ cursor, audioMessages: true, repositories, workspaces });
  }

  async agent(id) {
    await pause(180);
    const found = findAgent(id);
    if (!found) throw new Error("Agent not found");
    return clone({
      cursor,
      agent: {
        id: found.agent.id,
        title: found.agent.title,
        role: found.agent.role,
        status: found.agent.status,
        workspaceId: found.workspace.id,
        workspaceTitle: found.workspace.title,
      },
      timeline: timelines.get(id) || [],
      hasMore: false,
      conversationHasMore: false,
      messageHasMore: false,
      before: 0,
      messageBefore: "",
      mirroredDeliveryResponses: [],
      messagePageIds: [],
      delegatedAgents: found.agent.delegatedAgents || [],
    });
  }

  async sendMessage(id, prompt, idempotencyKey) {
    await pause(280);
    const found = findAgent(id);
    if (!found) throw new Error("Agent not found");
    if (!prompt.trim()) throw new Error("Feedback is required");

    const sequence = ++cursor;
    const messageId = `mock-delivery-${idempotencyKey}`;
    const item = event(sequence, "delivery_queued", {
      id: messageId,
      eventId: `delivery:${messageId}:prompt`,
      clientRequestId: idempotencyKey,
      role: "user",
      content: prompt.trim(),
      state: "queued",
      status: "queued",
      createdAt: Date.now(),
    });
    timelines.set(id, [...(timelines.get(id) || []), item]);
    found.agent.status = "running";
    found.agent.lastActivity = "Reading new feedback";
    found.agent.updatedAt = item.createdAt;
    publish(item);

    schedule(() => {
      const start = event(++cursor, "assistant_message_start", {
        role: "assistant",
        state: "running",
        createdAt: Date.now(),
      });
      timelines.get(id).push(start);
      publish(start);
    }, 550);

    schedule(() => {
      const delta = event(++cursor, "assistant_text_delta", {
        role: "assistant",
        content: "I received the additional feedback. I will apply it at the next safe point in this turn.",
        isDelta: true,
        state: "running",
        createdAt: Date.now(),
      });
      timelines.get(id).push(delta);
      publish(delta);
    }, 1_050);

    schedule(() => {
      const end = event(++cursor, "assistant_message_end", {
        role: "assistant",
        state: "completed",
        createdAt: Date.now(),
      });
      timelines.get(id).push(end);
      found.agent.status = "idle";
      found.agent.lastActivity = "Feedback applied";
      found.agent.updatedAt = end.createdAt;
      publish(end);
    }, 1_650);

    return clone({ message: item, delivery: "queued" });
  }

  async sendAudioMessage(id, _audio, language, idempotencyKey) {
    const transcript = language === "es"
      ? "Este es un mensaje de voz simulado y transcrito."
      : "This is a transcribed mock voice message.";
    return this.sendMessage(id, transcript, idempotencyKey);
  }

  async createAgent(input, idempotencyKey) {
    await pause(450);
    const workspace = workspaces.find((value) => value.id === input.workspaceId);
    if (!workspace) throw new Error("The workspace is no longer available");
    if (input.sourceAgentId && !findAgent(input.sourceAgentId)) throw new Error("The source agent is no longer available");
    if (!input.sourceAgentId && !input.repositoryIds?.some((id) => repositories.some((repository) => repository.id === id))) {
      throw new Error("Choose a repository");
    }

    const created = {
      id: `mock-created-${nextAgent++}`,
      title: input.title.trim(),
      role: input.role?.trim() || "",
      status: "starting",
      canCopyPlacement: true,
      lastActivity: "Preparing private setup",
      updatedAt: new Date().toISOString(),
    };
    workspace.agents.push(created);
    const first = event(++cursor, "user_message", {
      eventId: `mock-launch-${idempotencyKey}`,
      role: "user",
      content: input.prompt.trim(),
      state: "queued",
      createdAt: Date.now(),
    });
    timelines.set(created.id, [first]);
    publish({ kind: "agent_created", agentId: created.id, workspaceId: workspace.id });

    schedule(() => {
      created.status = "running";
      created.lastActivity = "Starting first turn";
      created.updatedAt = new Date().toISOString();
      publish({ kind: "agent_running", agentId: created.id, workspaceId: workspace.id });
    }, 900);

    return clone({
      agent: {
        id: created.id,
        title: created.title,
        role: created.role,
        status: created.status,
        workspaceId: workspace.id,
        workspaceTitle: workspace.title,
      },
      message: first,
    });
  }

  subscribe(_after, handlers = {}) {
    subscribers.add(handlers);
    queueMicrotask(() => handlers.onState?.("online"));
    return () => subscribers.delete(handlers);
  }
}
