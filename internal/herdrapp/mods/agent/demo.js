// demo.js: a pretend host for a demo tab (tab props {demo: true}). It answers
// the requests of the view as host/host.mjs does: /state, /snapshot, /events,
// /rpc, /ui and /galpon/rename. It runs no Pi, calls no model and changes
// nothing in Galpon. The view draws its events with the same code as a real
// session, so a demo shows every part of the view.
//
// A prompt plays a short scripted run. Words in the prompt pick the script:
// "delegate" or "review" delegates to a pretend agent, "ask" or "choose"
// opens a question, "background" or "watch" starts a background command,
// "fail" shows a failed tool and a retry. Any other prompt plays a coding run:
// thinking, todos, a read, a search, an edit, a test run and an answer.

const MODELS = [
  { id: "claude-opus-5-5", name: "Claude Opus 5.5", provider: "anthropic", reasoning: true, input: ["text", "image"], contextWindow: 1000000, maxTokens: 64000, cost: { input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25 } },
  { id: "claude-sonnet-5", name: "Claude Sonnet 5", provider: "anthropic", reasoning: true, input: ["text", "image"], contextWindow: 1000000, maxTokens: 64000, cost: { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 } },
  { id: "claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "anthropic", reasoning: true, input: ["text", "image"], contextWindow: 200000, maxTokens: 32000, cost: { input: 1, output: 5, cacheRead: 0.1, cacheWrite: 1.25 } },
  { id: "gpt-6-1-sol", name: "GPT-6.1 Sol", provider: "openai-codex", reasoning: true, input: ["text", "image"], contextWindow: 400000, maxTokens: 128000, cost: { input: 1.25, output: 10, cacheRead: 0.125, cacheWrite: 0 } },
  { id: "gpt-6-1-mini", name: "GPT-6.1 mini", provider: "openai-codex", reasoning: true, input: ["text"], contextWindow: 400000, maxTokens: 128000, cost: { input: 0.25, output: 2, cacheRead: 0.025, cacheWrite: 0 } },
  { id: "gemini-3-5-pro", name: "Gemini 3.5 Pro", provider: "google", reasoning: true, input: ["text", "image"], contextWindow: 2000000, maxTokens: 64000, cost: { input: 2, output: 12, cacheRead: 0.2, cacheWrite: 0 } },
  { id: "kimi-k3", name: "Kimi K3", provider: "moonshot", reasoning: false, input: ["text"], contextWindow: 256000, maxTokens: 32000, cost: { input: 0.6, output: 2.5, cacheRead: 0.15, cacheWrite: 0 } },
];
const LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh"];
const COMMANDS = [
  { name: "review", description: "Review the changes of this branch", source: "prompt" },
  { name: "skill:release-notes", description: "Write release notes from the merged pull requests", source: "skill" },
  { name: "pick", description: "Ask for a color", source: "extension" },
];
const FOOTER = "Delivery instructions: Address every delivery in this batch. Your final assistant text is the durable result for this batch. State what you completed, the main result, and any error or remaining work. Do not use galpon_send_agent to return a result for a current delivery. Galpón sends your final text to the requester when this turn settles.";
const RESULT_FOOTER = "Continue the original task from the saved conversation. Use these results, then give the final result for that task.";

let ids = 0;
const nextId = (prefix) => `${prefix}_${Date.now().toString(36)}${(++ids).toString(36)}`;

// usage gives the usage of one model call at the prices of the model.
function usage(model, input, output, cacheRead = 0) {
  const price = model.cost ?? {};
  const cost = {
    input: (input * (price.input ?? 0)) / 1e6,
    output: (output * (price.output ?? 0)) / 1e6,
    cacheRead: (cacheRead * (price.cacheRead ?? 0)) / 1e6,
    cacheWrite: 0,
  };
  cost.total = cost.input + cost.output + cost.cacheRead;
  return { input, output, cacheRead, cacheWrite: 0, totalTokens: input + output + cacheRead, cost };
}

const text = (value) => ({ type: "text", text: value });
const userMessage = (value, at) => ({ role: "user", content: [text(value)], timestamp: at });
const custom = (customType, value, at, details) => ({ role: "custom", customType, content: value, display: true, details: details ?? null, timestamp: at });
const toolResult = (call, output, at, { details, isError = false } = {}) => ({
  role: "toolResult", toolCallId: call.id, toolName: call.name, content: [text(output)], details: details ?? null, isError, timestamp: at,
});

const README_DIFF = [
  "@@ -12,9 +12,11 @@ func (r *Runner) deliver(ctx context.Context, msg Message) error {",
  " \tr.mu.Lock()",
  "-\tif r.waiting {",
  "-\t\treturn r.queue(msg)",
  "-\t}",
  "+\t// A delegation can answer while the agent still asks the person.",
  "+\t// Queue the delivery until the question has an answer.",
  "+\tif r.waiting || r.asking {",
  "+\t\treturn r.queue(msg)",
  "+\t}",
  " \tdefer r.mu.Unlock()",
  " \treturn r.send(ctx, msg)",
].join("\n");

const FIRST_ANSWER = [
  "The test was flaky because a **delivery** could arrive while the agent still waited for an answer from the person.",
  "",
  "### What I changed",
  "",
  "1. `deliver` now queues a message while the agent asks a question.",
  "2. The queue drains when the answer arrives.",
  "3. The e2e test waits for the question before it delegates.",
  "",
  "```go",
  "if r.waiting || r.asking {",
  "\treturn r.queue(msg)",
  "}",
  "```",
  "",
  "| Run | Result |",
  "| --- | --- |",
  "| `go test ./internal/runtime` | pass |",
  "| `go test ./e2e -run Codex -count=20` | 20/20 pass |",
  "",
  "> The fix needs no change in the daemon API.",
].join("\n");

// initialMessages is the conversation that the demo tab shows first.
function initialMessages(model) {
  const at = Date.now() - 52 * 60000;
  const m = [];
  const step = (minutes) => at + minutes * 60000;
  m.push(userMessage("The Codex e2e test fails one run in ten. Find out why and fix it.", step(0)));
  const grep = { type: "toolCall", id: "call_d1", name: "grep", arguments: { pattern: "func (r \\*Runner) deliver", path: "internal/runtime" } };
  const read = { type: "toolCall", id: "call_d2", name: "read", arguments: { path: "internal/runtime/runner.go", offset: 1, limit: 80 } };
  m.push({ role: "assistant", content: [
    { type: "thinking", thinking: "A flaky delegation test often means a race between two messages.\nI look at how the runner delivers messages first." },
    text("I start with the code that delivers messages to the agent."),
    grep, read,
  ], api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 4200, 310, 21000), stopReason: "toolUse", thinkingLevel: "high", timestamp: step(0.4) });
  m.push(toolResult(grep, "internal/runtime/runner.go:12:func (r *Runner) deliver(ctx context.Context, msg Message) error {", step(0.5)));
  m.push(toolResult(read, "package runtime\n\n// Runner delivers messages to one agent.\ntype Runner struct {\n\tmu      sync.Mutex\n\twaiting bool\n\tasking  bool\n}\n", step(0.6)));
  const edit = { type: "toolCall", id: "call_d3", name: "edit", arguments: { path: "internal/runtime/runner.go", edits: [{ oldText: "if r.waiting {", newText: "if r.waiting || r.asking {" }] } };
  const test = { type: "toolCall", id: "call_d4", name: "bash", arguments: { command: "go test ./e2e -run Codex -count=20" } };
  m.push({ role: "assistant", content: [
    { type: "thinking", thinking: "deliver ignores r.asking. A delivery that comes during a question goes straight to the agent." },
    edit, test,
  ], api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 2600, 520, 26000), stopReason: "toolUse", thinkingLevel: "high", timestamp: step(1.5) });
  m.push(toolResult(edit, "Successfully replaced 1 block(s) in internal/runtime/runner.go.", step(1.6), { details: { diff: README_DIFF } }));
  m.push(toolResult(test, "ok  \tgithub.com/matipan/galpon/e2e\t48.214s\n--- 20 runs, 20 passed", step(2.6), { details: { exit_code: 0 } }));
  m.push({ role: "assistant", content: [text(FIRST_ANSWER)], api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 1900, 820, 29000), stopReason: "stop", thinkingLevel: "high", timestamp: step(3) });

  m.push(custom("galpon-operation", `Work request from Galpón agent Jefesito [delivery 7f3e9c21]:\n\nPlease check that the **README** explains the new demo tab, then tell me.\n\n---\n\n${FOOTER}`, step(20)));
  const create = { type: "toolCall", id: "call_d5", name: "galpon_create_agent", arguments: { title: "README review", prompt: "Read README.md and check the section about the demo tab." } };
  m.push({ role: "assistant", content: [text("I ask a reviewer to read it while I check the code samples."), create], api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 3100, 240, 30000), stopReason: "toolUse", thinkingLevel: "high", timestamp: step(20.3) });
  m.push(toolResult(create, "{\"agentId\":\"5d1c0b7e\",\"messageId\":\"m-41\",\"status\":\"queued\"}", step(20.4)));
  m.push({ role: "assistant", content: [text("**README review** has the request. I tell you when its result arrives.")], api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 900, 60, 31000), stopReason: "stop", thinkingLevel: "high", timestamp: step(20.6) });
  m.push(custom("galpon-operation", `Durable result for assignment m-41:\n\nThe README section is correct. I fixed two typos and added the key for the demo tab.\n\n---\n\n${RESULT_FOOTER}`, step(31)));
  m.push({ role: "assistant", content: [text("The review is done: the README is correct, with two small fixes. Nothing else blocks the release.")], api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 1200, 90, 32000), stopReason: "stop", thinkingLevel: "high", timestamp: step(31.4) });
  return m;
}

function initialTodos() {
  return [
    { id: 1, subject: "Find the race in the delivery path", status: "completed" },
    { id: 2, subject: "Queue deliveries during questions", status: "completed" },
    { id: 3, subject: "Run the Codex e2e test 20 times", status: "completed" },
    { id: 4, subject: "Ship the release notes", status: "pending" },
  ];
}

function initialBridge(title) {
  const now = Date.now();
  return {
    galpon: { agentId: "demo-agent", title, workspace: "Galpon", workspaceId: "demo", cwd: "/home/demo/code/galpon" },
    work: { work: [
      { id: "w-readme", title: "README review", createdAt: now - 32 * 60000, updatedAt: now - 21 * 60000, completedAt: now - 21 * 60000,
        observation: { state: "completed", source: "observed", lease: "none" } },
      { id: "w-notes", title: "Draft the release notes", createdAt: now - 9 * 60000, updatedAt: now - 40000,
        observation: { state: "started", source: "observed", lease: "fresh", freshnessAt: now + 60000 },
        activity: { category: "tool: read", status: "started", source: "observed", observedAt: now - 8000 },
        checkpoint: { phase: "working", summary: "Read 14 merged pull requests", source: "reported", reportedAt: now - 60000 } },
      { id: "w-icons", title: "Pick the icon set", createdAt: now - 26 * 60000, updatedAt: now - 7 * 60000,
        observation: { state: "waiting", source: "observed", lease: "fresh" },
        checkpoint: { phase: "blocked", summary: "Two icon sets fit", blocker: "Waits for you to choose an icon set", source: "reported", reportedAt: now - 7 * 60000 } },
    ], truncated: false },
    tasks: { tasks: [
      { id: "bg1", kind: "monitor", description: "errors in deploy.log", command: "tail -f deploy.log | grep --line-buffered ERROR", status: "running", startedAt: now - 14 * 60000, endedAt: 0, exitCode: null, tail: ["ERROR cache miss for key build/linux-amd64"] },
      { id: "bg2", kind: "command", description: "go test ./...", command: "go test ./...", status: "completed", startedAt: now - 44 * 60000, endedAt: now - 42 * 60000, exitCode: 0, tail: ["ok  github.com/matipan/galpon/internal/app 7.7s"] },
    ] },
  };
}

// Script steps are [delay in ms, event]. A step with a function runs it: it
// can wait for the person or change the host.
export class DemoHost {
  constructor(options = {}) {
    this.mode = "demo";
    this.seq = 0;
    this.log = [];
    this.waiters = [];
    this.jobs = [];
    this.jobWaiter = null;
    this.run = null;
    this.answers = new Map();
    this.model = MODELS[0];
    this.thinking = "high";
    this.title = options.title || "Demo agent";
    this.messages = initialMessages(this.model);
    this.todos = initialTodos();
    this.bridge = initialBridge(this.title);
    this.sessionTokens = { input: 13900, output: 2040, cacheRead: 169000, cacheWrite: 0 };
    this.cost = 0.41;
    this.context = 61000;
  }

  // The view calls set with the host info of a real host.
  set() {}

  push(event) {
    this.seq++;
    this.log.push({ seq: this.seq, event });
    if (this.log.length > 4000) this.log.splice(0, this.log.length - 4000);
    const waiters = this.waiters.splice(0);
    for (const resolve of waiters) resolve();
  }

  stats() {
    const window = this.model.contextWindow;
    return {
      tokens: { ...this.sessionTokens, total: this.sessionTokens.input + this.sessionTokens.output + this.sessionTokens.cacheRead },
      cost: this.cost,
      contextUsage: { tokens: this.context, contextWindow: window, percent: Math.min(100, (100 * this.context) / window) },
    };
  }

  snapshot() {
    return {
      seq: this.seq,
      messages: this.messages,
      total: this.messages.length,
      streaming: Boolean(this.run),
      state: { model: this.model, thinkingLevel: this.thinking, sessionId: "demo", sessionName: "" },
      thinkingLevels: this.model.reasoning ? LEVELS : ["off"],
      stats: this.stats(),
      commands: COMMANDS,
      statuses: {},
      widgets: {},
      bridge: this.bridge,
      galpon: { enabled: true, agentId: "demo-agent" },
      dialogs: [],
      title: "",
      todos: this.todos,
    };
  }

  async request(method, path, body) {
    const ok = (data) => ({ status: 200, body: data });
    if (path === "/state") return ok({ alive: true, key: "demo", galpon: { enabled: true, agentId: "demo-agent" } });
    if (path === "/snapshot") return ok(this.snapshot());
    if (path.startsWith("/events")) {
      const since = Number(/since=(\d+)/.exec(path)?.[1] ?? 0);
      if (since > this.seq) return ok({ reset: true, events: [], seq: this.seq, alive: true });
      if (this.seq === since) await new Promise((resolve) => this.waiters.push(resolve));
      const events = this.log.filter((entry) => entry.seq > since).map((entry) => entry.event);
      return ok({ events, seq: this.seq, alive: true });
    }
    if (path === "/rpc") return ok(this.rpc(body ?? {}));
    if (path === "/ui") {
      const resolve = this.answers.get(body?.id);
      if (resolve) {
        this.answers.delete(body.id);
        resolve(body);
      }
      this.push({ type: "dialog_closed", id: body?.id });
      return ok({ ok: true });
    }
    if (path === "/galpon/rename") {
      this.title = String(body?.title ?? this.title);
      this.bridge = { ...this.bridge, galpon: { ...this.bridge.galpon, title: this.title } };
      this.push({ type: "host_bridge", key: "galpon", value: this.bridge.galpon });
      return ok({ title: this.title });
    }
    if (path === "/attach" || path === "/attach-data") return { status: 400, body: { error: "The demo tab does not take images." } };
    if (path === "/shutdown") return ok({ ok: true });
    return { status: 404, body: { error: `the demo has no ${path}` } };
  }

  rpc(command) {
    const done = (data) => ({ success: true, data });
    switch (command.type) {
      case "get_state": return done({ model: this.model, thinkingLevel: this.thinking, isStreaming: Boolean(this.run), sessionId: "demo" });
      case "get_available_models": return done({ models: MODELS });
      case "set_model": {
        const model = MODELS.find((item) => item.provider === command.provider && item.id === command.modelId);
        if (!model) return { success: false, error: "unknown model" };
        this.model = model;
        if (!model.reasoning) this.thinking = "off";
        else if (this.thinking === "off") this.thinking = "medium";
        return done(model);
      }
      case "get_available_thinking_levels": return done({ levels: this.model.reasoning ? LEVELS : ["off"] });
      case "set_thinking_level":
        this.thinking = command.level;
        return done({});
      case "cycle_thinking_level": {
        if (!this.model.reasoning) return done(null);
        this.thinking = LEVELS[(LEVELS.indexOf(this.thinking) + 1) % LEVELS.length];
        return done({ level: this.thinking });
      }
      case "get_session_stats": return done(this.stats());
      case "clear_queue": {
        const queue = this.run?.queue ?? { steering: [], followUp: [] };
        const data = { steering: [...queue.steering], followUp: [...queue.followUp] };
        if (this.run) this.run.queue = { steering: [], followUp: [] };
        this.push({ type: "queue_update", steering: [], followUp: [] });
        return done(data);
      }
      case "abort":
        if (this.run) this.run.aborted = true;
        return done({});
      case "new_session":
        this.messages = [];
        this.todos = [];
        this.context = 0;
        this.push({ type: "rpc_response", command: "new_session", success: true });
        return done({});
      case "compact":
        this.queueJob(() => this.compactScript());
        return done({});
      case "set_session_name": return done({});
      case "prompt": return this.prompt(command);
      default: return done({});
    }
  }

  prompt(command) {
    const message = String(command.message ?? "");
    const stop = /^\/galpon-app-stop\s+(\S+)/.exec(message);
    if (stop) {
      this.updateTask(stop[1], { status: "stopped", endedAt: Date.now(), exitCode: null });
      return { success: true, data: { disposition: "handled" } };
    }
    if (message === "/pick") {
      this.queueJob(() => this.pickScript());
      return { success: true, data: { disposition: "handled" } };
    }
    if (this.run && command.streamingBehavior) {
      const queue = this.run.queue;
      (command.streamingBehavior === "followUp" ? queue.followUp : queue.steering).push(message);
      this.push({ type: "queue_update", steering: [...queue.steering], followUp: [...queue.followUp] });
      return { success: true, data: { disposition: "queued" } };
    }
    this.queueJob(() => this.script(message));
    return { success: true, data: { disposition: "started" } };
  }

  queueJob(make) {
    this.jobs.push(make);
    const waiter = this.jobWaiter;
    this.jobWaiter = null;
    waiter?.();
  }

  async nextJob() {
    while (!this.jobs.length) await new Promise((resolve) => { this.jobWaiter = resolve; });
    return this.jobs.shift();
  }

  // play runs the scripts, one at a time, until the view closes.
  async play(sleep, closed) {
    for (;;) {
      const make = await this.nextJob();
      if (closed()) return;
      this.run = { aborted: false, queue: { steering: [], followUp: [] } };
      const steps = make();
      try {
        for (const step of steps) {
          if (closed()) return;
          if (this.run.aborted) break;
          const [delay, action] = step;
          if (delay) await sleep(delay);
          if (this.run.aborted) break;
          if (typeof action === "function") await action();
          else if (action) this.record(action);
        }
      } finally {
        const run = this.run;
        if (run.aborted) this.abortScript();
        this.run = null;
        if (run.queue.followUp.length) {
          for (const next of run.queue.followUp) this.queueJob(() => this.script(next));
          this.push({ type: "queue_update", steering: [], followUp: [] });
        }
      }
    }
  }

  // record pushes an event and keeps the messages for the next snapshot.
  record(event) {
    if (event.type === "message_end" && event.message) this.messages.push(event.message);
    this.push(event);
  }

  // abortScript ends the run as Pi does after an abort: the open answer ends
  // with what it has, and the stop shows as a notice.
  abortScript() {
    const model = this.model;
    const open = this.open;
    this.open = null;
    if (!open) this.push({ type: "message_start", message: { role: "assistant", content: [], timestamp: Date.now() } });
    const content = open ? [...open.blocks, ...(open.current ? [open.current] : [])] : [];
    this.record({ type: "message_end", message: { role: "assistant", content, api: "anthropic-messages", provider: model.provider, model: model.id, usage: usage(model, 0, 0, 0), stopReason: "aborted", timestamp: Date.now() } });
    this.push({ type: "agent_end", willRetry: false });
    this.push({ type: "agent_settled", aborted: true });
  }

  updateTask(id, fields) {
    const tasks = (this.bridge.tasks?.tasks ?? []).map((task) => (task.id === id ? { ...task, ...fields } : task));
    this.bridge = { ...this.bridge, tasks: { tasks } };
    this.push({ type: "host_bridge", key: "tasks", value: this.bridge.tasks });
  }

  addTask(task) {
    const tasks = [task, ...(this.bridge.tasks?.tasks ?? [])];
    this.bridge = { ...this.bridge, tasks: { tasks } };
    this.push({ type: "host_bridge", key: "tasks", value: this.bridge.tasks });
  }

  setWork(id, change) {
    const work = (this.bridge.work?.work ?? []).map((item) => (item.id === id ? change(item) : item));
    this.bridge = { ...this.bridge, work: { work, truncated: false } };
    this.push({ type: "host_bridge", key: "work", value: this.bridge.work });
  }

  addWork(item) {
    const work = [item, ...(this.bridge.work?.work ?? [])];
    this.bridge = { ...this.bridge, work: { work, truncated: false } };
    this.push({ type: "host_bridge", key: "work", value: this.bridge.work });
  }

  // --- scripts

  // spend adds one model call to the session stats.
  spend(input, output, cacheRead) {
    const model = this.model;
    const cost = usage(model, input, output, cacheRead).cost.total;
    this.sessionTokens.input += input;
    this.sessionTokens.output += output;
    this.sessionTokens.cacheRead += cacheRead;
    this.cost += cost;
    this.context = Math.min(this.model.contextWindow * 0.97, this.context + input + output);
    return usage(model, input, output, cacheRead);
  }

  // answer streams one assistant message: its thinking, its text and its
  // tool calls, then its end.
  *answer({ thinking = "", say = "", calls = [], stop = "stop", input = 1800, output = 240 } = {}) {
    // open is the answer that streams now, for an abort in the middle.
    const open = { blocks: [], current: null };
    this.open = open;
    yield [180, { type: "message_start", message: { role: "assistant", content: [], timestamp: Date.now() } }];
    let index = 0;
    if (thinking && this.thinking !== "off") {
      open.current = { type: "thinking", thinking: "" };
      yield [60, { type: "message_update", assistantMessageEvent: { type: "thinking_start", contentIndex: index } }];
      for (const part of chunks(thinking, 9)) {
        open.current.thinking += part;
        yield [45, { type: "message_update", assistantMessageEvent: { type: "thinking_delta", contentIndex: index, delta: part } }];
      }
      yield [40, { type: "message_update", assistantMessageEvent: { type: "thinking_end", contentIndex: index, content: thinking } }];
      open.blocks.push({ type: "thinking", thinking });
      open.current = null;
      index++;
    }
    if (say) {
      open.current = text("");
      yield [80, { type: "message_update", assistantMessageEvent: { type: "text_start", contentIndex: index } }];
      for (const part of chunks(say, 7)) {
        open.current.text += part;
        yield [28, { type: "message_update", assistantMessageEvent: { type: "text_delta", contentIndex: index, delta: part } }];
      }
      yield [30, { type: "message_update", assistantMessageEvent: { type: "text_end", contentIndex: index, content: say } }];
      open.blocks.push(text(say));
      open.current = null;
      index++;
    }
    for (const call of calls) {
      const json = JSON.stringify(call.arguments);
      yield [90, { type: "message_update", assistantMessageEvent: { type: "toolcall_start", contentIndex: index, id: call.id, toolName: call.name } }];
      for (const part of chunks(json, 12)) yield [35, { type: "message_update", assistantMessageEvent: { type: "toolcall_delta", contentIndex: index, delta: part } }];
      yield [30, { type: "message_update", assistantMessageEvent: { type: "toolcall_end", contentIndex: index, toolCall: call } }];
      open.blocks.push(call);
      index++;
    }
    const model = this.model;
    const spent = this.spend(input, output, Math.round(this.context * 0.9));
    yield [60, { type: "message_end", message: { role: "assistant", content: open.blocks, api: "anthropic-messages", provider: model.provider, model: model.id, usage: spent, stopReason: stop, timestamp: Date.now(), thinkingLevel: this.thinking } }];
    this.open = null;
  }

  // tool runs one tool call: its start, its output while it runs, its end and
  // its result message.
  *tool(call, output, { lines = [], every = 260, details = null, isError = false, durationMs = 0 } = {}) {
    const started = Date.now();
    yield [120, { type: "tool_execution_start", toolCallId: call.id, toolName: call.name, args: call.arguments }];
    let partial = "";
    for (const line of lines) {
      partial += `${line}\n`;
      yield [every, { type: "tool_execution_update", toolCallId: call.id, toolName: call.name, args: call.arguments, partialResult: { content: [text(partial)] } }];
    }
    if (durationMs) yield [durationMs, null];
    const result = { content: [text(output)], details };
    yield [80, { type: "tool_execution_end", toolCallId: call.id, toolName: call.name, result, isError, durationMs: Date.now() - started }];
    const message = toolResult(call, output, Date.now(), { details, isError });
    yield [10, { type: "message_start", message }];
    yield [10, { type: "message_end", message }];
  }

  *begin(prompt) {
    yield [0, { type: "agent_start" }];
    yield [10, { type: "turn_start" }];
    if (prompt !== null) {
      const message = userMessage(prompt, Date.now());
      yield [10, { type: "message_start", message }];
      yield [10, { type: "message_end", message }];
    }
  }

  *end() {
    yield [40, { type: "turn_end" }];
    yield [20, { type: "agent_end", willRetry: false }];
    yield [20, { type: "agent_settled", aborted: false }];
  }

  // steer adds the messages that the person sent during the run.
  *steer() {
    const queue = this.run?.queue;
    if (!queue?.steering.length) return;
    const all = queue.steering.splice(0);
    yield [0, { type: "queue_update", steering: [], followUp: [...queue.followUp] }];
    for (const value of all) {
      const message = userMessage(value, Date.now());
      yield [20, { type: "message_start", message }];
      yield [20, { type: "message_end", message }];
    }
    yield* this.answer({ say: `Noted: ${all.join(" ")}. I take it into account in the next steps.`, input: 600, output: 40 });
  }

  todo(action, fields) {
    const call = { type: "toolCall", id: nextId("call_todo"), name: "todo", arguments: { action, ...fields } };
    return call;
  }

  *todoStep(call, change) {
    yield* this.answer({ calls: [call], stop: "toolUse", input: 500, output: 60 });
    change();
    const tasks = this.todos.map((task) => ({ ...task }));
    yield* this.tool(call, `Open tasks (${tasks.filter((task) => task.status !== "completed").length})`, { details: { tasks } });
  }

  script(prompt) {
    const words = prompt.toLowerCase();
    if (/delegat|review|helper|agent/.test(words)) return this.delegateScript(prompt);
    if (/\bask\b|choose|which|pick/.test(words)) return this.askScript(prompt);
    if (/background|watch|monitor|server/.test(words)) return this.backgroundScript(prompt);
    if (/fail|error|break|crash/.test(words)) return this.failScript(prompt);
    return this.codingScript(prompt);
  }

  *codingScript(prompt) {
    yield* this.begin(prompt);
    const base = this.todos.length ? Math.max(...this.todos.map((task) => task.id)) : 0;
    const one = base + 1;
    const two = base + 2;
    yield* this.todoStep(this.todo("create", { subject: "Read the delivery code" }), () => {
      this.todos.push({ id: one, subject: "Read the delivery code", activeForm: "Reading the delivery code", status: "in_progress" });
      this.todos.push({ id: two, subject: "Change it and run the tests", activeForm: "Changing it and running the tests", status: "pending" });
    });
    const grep = { type: "toolCall", id: nextId("call_grep"), name: "grep", arguments: { pattern: "queue\\(msg\\)", path: "internal" } };
    const read = { type: "toolCall", id: nextId("call_read"), name: "read", arguments: { path: "internal/runtime/queue.go" } };
    yield* this.answer({
      thinking: `The request: "${prompt.slice(0, 80)}". I check where the runner queues messages, then I read the queue.`,
      say: "I look at the queue first.",
      calls: [grep, read], stop: "toolUse", input: 2400, output: 180,
    });
    yield* this.tool(grep, "internal/runtime/runner.go:16:\t\treturn r.queue(msg)\ninternal/runtime/queue.go:8:func (r *Runner) queue(msg Message) error {");
    yield* this.tool(read, "package runtime\n\nfunc (r *Runner) queue(msg Message) error {\n\tr.pending = append(r.pending, msg)\n\treturn nil\n}\n");
    yield* this.steer();
    yield* this.todoStep(this.todo("update", { id: one, status: "completed" }), () => {
      for (const task of this.todos) {
        if (task.id === one) task.status = "completed";
        if (task.id === two) task.status = "in_progress";
      }
    });
    const edit = { type: "toolCall", id: nextId("call_edit"), name: "edit", arguments: { path: "internal/runtime/queue.go", edits: [{ oldText: "r.pending = append(r.pending, msg)", newText: "r.pending = append(r.pending, msg)\n\tr.notify()" }] } };
    const diff = "@@ -3,5 +3,6 @@ package runtime\n func (r *Runner) queue(msg Message) error {\n \tr.pending = append(r.pending, msg)\n+\tr.notify()\n \treturn nil\n }";
    yield* this.answer({
      thinking: "The queue never wakes the drain loop. One notify call fixes it.",
      calls: [edit], stop: "toolUse", input: 1600, output: 120,
    });
    yield* this.tool(edit, "Successfully replaced 1 block(s) in internal/runtime/queue.go.", { details: { diff } });
    const test = { type: "toolCall", id: nextId("call_bash"), name: "bash", arguments: { command: "go test ./internal/runtime/..." } };
    yield* this.answer({ say: "Now the tests.", calls: [test], stop: "toolUse", input: 900, output: 60 });
    yield* this.tool(test, "ok  \tgithub.com/matipan/galpon/internal/runtime\t2.418s\nok  \tgithub.com/matipan/galpon/internal/runtime/queue\t0.311s", {
      lines: ["=== RUN   TestDeliverQueues", "--- PASS: TestDeliverQueues (0.02s)", "=== RUN   TestQueueWakesDrain", "--- PASS: TestQueueWakesDrain (0.11s)", "ok  \tgithub.com/matipan/galpon/internal/runtime\t2.418s"],
      every: 420, details: { exit_code: 0 },
    });
    yield* this.steer();
    yield* this.todoStep(this.todo("update", { id: two, status: "completed" }), () => {
      for (const task of this.todos) if (task.id === two) task.status = "completed";
    });
    yield* this.answer({
      say: [
        "Done. The queue now **wakes the drain loop** when a message arrives.",
        "",
        "- `queue.go`: one `r.notify()` call after the append.",
        "- The runtime tests pass: `TestQueueWakesDrain` covers it.",
        "",
        "```go",
        "func (r *Runner) queue(msg Message) error {",
        "\tr.pending = append(r.pending, msg)",
        "\tr.notify()",
        "\treturn nil",
        "}",
        "```",
        "",
        "_This is a demo run: no model was called._",
      ].join("\n"),
      input: 2100, output: 340,
    });
    yield* this.end();
  }

  *delegateScript(prompt) {
    yield* this.begin(prompt);
    const list = { type: "toolCall", id: nextId("call_list"), name: "galpon_list_agents", arguments: { workspace: "Galpon" } };
    yield* this.answer({ thinking: "This fits a second agent. I check who is free first.", calls: [list], stop: "toolUse", input: 2000, output: 90 });
    yield* this.tool(list, "[{\"title\":\"Jefesito\",\"state\":\"working\"},{\"title\":\"Release notes\",\"state\":\"idle\"}]");
    const create = { type: "toolCall", id: nextId("call_create"), name: "galpon_create_agent", arguments: { title: "Second opinion", prompt: prompt.slice(0, 120) } };
    yield* this.answer({ say: "Nobody fits. I start a new agent for it.", calls: [create], stop: "toolUse", input: 1200, output: 110 });
    yield* this.tool(create, "{\"agentId\":\"9a7e44f0\",\"messageId\":\"m-77\",\"status\":\"queued\"}");
    const now = Date.now();
    yield [0, () => this.addWork({ id: "w-second", title: "Second opinion", createdAt: now, updatedAt: now, observation: { state: "queued", source: "observed", lease: "none" } })];
    yield* this.answer({ say: "**Second opinion** has the request. I tell you its result when it arrives.", input: 700, output: 50 });
    yield* this.end();
    yield [1500, () => this.setWork("w-second", (item) => ({ ...item, updatedAt: Date.now(), observation: { state: "started", source: "observed", lease: "fresh" }, activity: { category: "tool: read", status: "started", source: "observed", observedAt: Date.now() } }))];
    yield [2500, () => this.setWork("w-second", (item) => ({ ...item, updatedAt: Date.now(), checkpoint: { phase: "working", summary: "Read the change and the tests", source: "reported", reportedAt: Date.now() } }))];
    yield [3000, () => this.setWork("w-second", (item) => ({ ...item, updatedAt: Date.now(), completedAt: Date.now(), observation: { state: "completed", source: "observed", lease: "none" } }))];
    const result = custom("galpon-operation", `Durable result for assignment m-77:\n\nThe change is correct. One test name is unclear: rename \`TestQueue2\` to \`TestQueueWakesDrain\`.\n\n---\n\n${RESULT_FOOTER}`, Date.now());
    yield [300, { type: "agent_start" }];
    yield [10, { type: "message_start", message: result }];
    yield [10, { type: "message_end", message: result }];
    yield* this.answer({ say: "The second opinion agrees with the change, with one rename. I renamed the test.", input: 1500, output: 70 });
    yield* this.end();
  }

  *askScript(prompt) {
    yield* this.begin(prompt);
    yield* this.answer({ thinking: "Two good options. The person should choose.", say: "I need one decision from you.", input: 1400, output: 60 });
    const id = nextId("ask");
    let answer = null;
    yield [200, () => {
      this.push({ type: "extension_ui_request", id, method: "select", title: "Which icon set do we use?", options: ["Lucide", "Phosphor", "Draw our own"], timeout: 0 });
      return new Promise((resolve) => this.answers.set(id, (body) => { answer = body; resolve(); }));
    }];
    yield [100, () => {
      const choice = answer?.cancelled ? null : answer?.value;
      this.pending = choice ? `You chose **${choice}**. I switch the marks to it and keep the sizes.` : "You did not choose. I keep the current icons.";
    }];
    yield* this.lazyAnswer(() => this.pending);
    yield* this.end();
  }

  // lazyAnswer streams a text that is known only when the step runs.
  *lazyAnswer(make) {
    const self = this;
    const steps = [];
    yield [0, () => { steps.push(...self.answer({ say: make(), input: 900, output: 70 })); }];
    // The steps exist after the function above ran; the player reads this
    // generator lazily, so the list is full when the loop gets here.
    for (const step of steps) yield step;
  }

  *backgroundScript(prompt) {
    yield* this.begin(prompt);
    const call = { type: "toolCall", id: nextId("call_bg"), name: "bash", arguments: { command: "go test ./e2e -count=1", run_in_background: true } };
    yield* this.answer({ thinking: "The e2e suite takes a while. It runs in the background.", say: "I start the e2e suite in the background.", calls: [call], stop: "toolUse", input: 1300, output: 90 });
    const id = `bg${Date.now().toString(36).slice(-4)}`;
    yield [0, () => this.addTask({ id, kind: "command", description: "go test ./e2e", command: "go test ./e2e -count=1", status: "running", startedAt: Date.now(), endedAt: 0, exitCode: null, tail: [] })];
    yield* this.tool(call, `Started background task ${id}`, { details: { task_id: id, status: "running", output_file: `/tmp/galpon-pi-tasks/${id}.output` } });
    yield* this.answer({ say: "The suite runs. I tell you when it ends; you can keep writing.", input: 600, output: 40 });
    yield* this.end();
    yield [2500, () => this.updateTask(id, { tail: ["=== RUN   TestCodexDelegation", "--- PASS: TestCodexDelegation (12.40s)"] })];
    yield [3500, () => this.updateTask(id, { status: "completed", endedAt: Date.now(), exitCode: 0, tail: ["--- PASS: TestCodexDelegation (12.40s)", "ok  github.com/matipan/galpon/e2e  61.2s"] })];
    const notice = custom("galpon-background", "Background task finished", Date.now(), { notices: [{ id, kind: "command", description: "go test ./e2e", status: "completed", summary: "exit 0 after 6s", lines: ["ok  github.com/matipan/galpon/e2e  61.2s"], outputPath: `/tmp/galpon-pi-tasks/${id}.output` }] });
    yield [100, { type: "agent_start" }];
    yield [10, { type: "message_start", message: notice }];
    yield [10, { type: "message_end", message: notice }];
    yield* this.answer({ say: "The e2e suite passed in the background: all tests are green.", input: 800, output: 40 });
    yield* this.end();
  }

  *failScript(prompt) {
    yield* this.begin(prompt);
    const read = { type: "toolCall", id: nextId("call_read"), name: "read", arguments: { path: "internal/runtime/missing.go" } };
    yield* this.answer({ say: "I read the file that the error names.", calls: [read], stop: "toolUse", input: 1000, output: 60 });
    yield* this.tool(read, "ENOENT: no such file or directory, open 'internal/runtime/missing.go'", { isError: true });
    yield [200, { type: "auto_retry_start", attempt: 1, maxAttempts: 3, delayMs: 2500, errorMessage: "529 overloaded" }];
    yield [2600, { type: "auto_retry_end", success: true, attempt: 1 }];
    yield* this.answer({ say: "That file does not exist: the error comes from an old build. A clean build fixes it: `go clean -cache && go build ./...`.", input: 1100, output: 80 });
    yield* this.end();
  }

  *pickScript() {
    const id = nextId("pick");
    yield [100, () => {
      this.push({ type: "extension_ui_request", id, method: "select", title: "Pick a color", options: ["Red", "Blue"], timeout: 0 });
      return new Promise((resolve) => this.answers.set(id, () => resolve()));
    }];
  }

  *compactScript() {
    yield [0, { type: "compaction_start", reason: "manual" }];
    const before = this.context;
    yield [2200, () => { this.context = Math.round(before * 0.22); }];
    yield [0, { type: "compaction_end", result: { summary: "## Goal\nFix the flaky Codex e2e test.\n\n## Done\n- Deliveries queue during questions.\n- The queue wakes the drain loop.", tokensBefore: before, estimatedTokensAfter: Math.round(before * 0.22) } }];
  }
}

function chunks(value, size) {
  const out = [];
  for (let at = 0; at < value.length; at += size) out.push(value.slice(at, at + size));
  return out;
}
