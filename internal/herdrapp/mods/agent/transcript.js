// The transcript of a Pi session as a list of view items. It is built from Pi
// RPC messages (a snapshot) and from session events (live). This file has no
// UI code, so Node can test it.
//
// Item kinds: user, delivery, text, thinking, tool, custom, bash, compaction,
// branch, notice and turn. A tool item holds its call and its result. A turn
// item closes one run: its time, tokens, cost and model.

export function createTranscript() {
  return {
    items: [],
    byId: new Map(),
    counter: 0,
    // The assistant message that streams now: { id, calls: {contentIndex: callId} }.
    current: null,
    // The non-assistant message that started last, so that its end adds nothing.
    started: null,
    hidden: 0,
    working: false,
    compacting: null,
    retry: null,
    queue: { steering: [], followUp: [] },
    // The run that adds up now, how the last answer ended, and if a
    // snapshot loads.
    turn: null,
    lastStop: "",
    loading: false,
    tasks: null,
    usage: null,
    name: "",
    thinkingLevel: "",
    exited: null,
    version: 0,
  };
}

export function textOf(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.filter((block) => block?.type === "text").map((block) => block.text ?? "").join("");
}

export function imagesOf(content) {
  if (!Array.isArray(content)) return [];
  return content.filter((block) => block?.type === "image" && block.path)
    .map((block) => ({ path: block.path, mimeType: block.mimeType, width: block.width, height: block.height, bytes: block.bytes }));
}

function add(t, item) {
  t.items.push(item);
  t.byId.set(item.id, item);
  t.version++;
  return item;
}

function upsert(t, id, fields) {
  const item = t.byId.get(id);
  if (item) {
    Object.assign(item, fields);
    t.version++;
    return item;
  }
  return add(t, { id, ...fields });
}

function tool(t, callId, fields) {
  const id = `tool:${callId}`;
  const item = t.byId.get(id);
  if (item) {
    for (const [key, value] of Object.entries(fields)) if (value !== undefined) item[key] = value;
    t.version++;
    return item;
  }
  return add(t, { id, kind: "tool", callId, name: "", args: null, argsText: "", status: "pending", partial: "", result: null, ...fields });
}

function notice(t, id, level, text) {
  return upsert(t, id, { kind: "notice", level, text });
}

// A turn is the run that answers one message. Live, it goes from agent_start
// to agent_settled. In a snapshot, a message of the person starts the next.
function beginTurn(t, at) {
  t.turn = { start: at || Date.now(), end: 0, calls: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0, model: "", provider: "", thinking: "" };
}

function countTurn(t, message) {
  if (!t.turn) beginTurn(t, message.timestamp);
  const turn = t.turn;
  const usage = message.usage ?? {};
  turn.calls++;
  turn.input += Number(usage.input) || 0;
  turn.output += Number(usage.output) || 0;
  turn.cacheRead += Number(usage.cacheRead) || 0;
  turn.cacheWrite += Number(usage.cacheWrite) || 0;
  turn.cost += Number(usage.cost?.total) || 0;
  turn.model = message.model || turn.model;
  turn.provider = message.provider || turn.provider;
  turn.thinking = message.thinkingLevel || turn.thinking;
  turn.end = Math.max(turn.end, Number(message.timestamp) || 0);
}

// boundary starts the next turn of a snapshot at a message that comes after
// the run ended: a message of the person, a delivery or a notice. A message
// that comes while tools run steers the same run.
function boundary(t, message) {
  if (!t.loading) return;
  if (t.turn?.calls && t.lastStop === "toolUse") return;
  endTurn(t);
  beginTurn(t, message.timestamp);
}

function endTurn(t, at) {
  const turn = t.turn;
  t.turn = null;
  if (!turn?.calls) return;
  const end = at || turn.end || turn.start;
  add(t, { id: `turn${++t.counter}`, kind: "turn", ...turn, end, duration: Math.max(0, end - turn.start) });
}

function finishTool(t, callId, name, result, isError, durationMs) {
  const item = tool(t, callId, { name: name || undefined });
  item.status = isError ? "error" : "done";
  // Pi sends tool details as details, or as structuredContent for built-in tools.
  item.result = { text: textOf(result?.content), images: imagesOf(result?.content), details: result?.details ?? result?.structuredContent ?? null };
  item.partial = "";
  if (durationMs !== undefined) item.durationMs = durationMs;
  if (/^todo$/i.test(item.name) && Array.isArray(result?.details?.tasks)) t.tasks = result.details.tasks;
  t.version++;
  return item;
}

// One assistant message, complete. During streaming the same ids already hold
// partial items; this replaces their content with the final content.
function assistant(t, message, id) {
  const content = Array.isArray(message.content) ? message.content : [];
  content.forEach((block, index) => {
    const key = `${id}:${index}`;
    if (block?.type === "text") upsert(t, key, { kind: "text", text: block.text ?? "", streaming: false });
    else if (block?.type === "thinking") upsert(t, key, { kind: "thinking", text: block.thinking ?? "", redacted: Boolean(block.redacted), streaming: false });
    else if (block?.type === "toolCall") {
      const item = tool(t, block.id, { name: block.name, args: block.arguments ?? {} });
      if (item.status === "streaming") item.status = "pending";
    }
  });
  if (message.usage) t.usage = message.usage;
  if (message.stopReason && message.stopReason !== "pending") {
    countTurn(t, message);
    t.lastStop = message.stopReason;
  }
  if (message.stopReason === "error") notice(t, `${id}:error`, "error", message.errorMessage || "The model returned an error.");
  if (message.stopReason === "aborted") notice(t, `${id}:aborted`, "warning", "Stopped.");
  if (message.stopReason === "length") notice(t, `${id}:length`, "warning", "The answer reached the output limit.");
}

function addMessage(t, message) {
  switch (message?.role) {
    case "user": {
      const text = textOf(message.content);
      boundary(t, message);
      // A Galpon delivery with images comes as a user message.
      const deliveries = parseDelivery(text);
      if (deliveries) return add(t, { id: `d${++t.counter}`, kind: "delivery", deliveries, images: imagesOf(message.content), at: message.timestamp });
      return add(t, { id: `u${++t.counter}`, kind: "user", text, images: imagesOf(message.content), at: message.timestamp });
    }
    case "assistant":
      return assistant(t, message, `a${++t.counter}`);
    case "toolResult":
      return finishTool(t, message.toolCallId, message.toolName, message, message.isError);
    case "custom": {
      if (message.display === false) return null;
      const text = textOf(message.content);
      const deliveries = message.customType === "galpon-operation" ? parseDelivery(text) : null;
      boundary(t, message);
      if (deliveries) return add(t, { id: `d${++t.counter}`, kind: "delivery", deliveries, images: imagesOf(message.content), at: message.timestamp });
      return add(t, { id: `c${++t.counter}`, kind: "custom", customType: message.customType, text, images: imagesOf(message.content), details: message.details ?? null, at: message.timestamp });
    }
    case "bashExecution":
      return add(t, { id: `b${++t.counter}`, kind: "bash", command: message.command, output: message.output ?? "", exitCode: message.exitCode, cancelled: Boolean(message.cancelled), truncated: Boolean(message.truncated) });
    case "compactionSummary":
      return add(t, { id: `k${++t.counter}`, kind: "compaction", summary: message.summary ?? "", tokensBefore: message.tokensBefore });
    case "branchSummary":
      return add(t, { id: `s${++t.counter}`, kind: "branch", summary: message.summary ?? "" });
    default:
      return null;
  }
}

export function loadSnapshot(t, snapshot) {
  const fresh = createTranscript();
  Object.assign(t, fresh, { counter: t.counter, version: t.version + 1 });
  const messages = snapshot?.messages ?? [];
  t.hidden = Math.max(0, (snapshot?.total ?? messages.length) - messages.length);
  t.loading = true;
  for (const message of messages) addMessage(t, message);
  t.loading = false;
  // A run that still works keeps its turn open: agent_settled closes it.
  if (!snapshot?.streaming) endTurn(t);
  // The todo list can be older than the messages of the snapshot.
  if (!t.tasks && Array.isArray(snapshot?.todos)) t.tasks = snapshot.todos;
  t.working = Boolean(snapshot?.streaming);
  if (t.working) t.workingSince = Date.now();
  t.name = snapshot?.state?.sessionName ?? "";
  t.thinkingLevel = snapshot?.state?.thinkingLevel ?? "";
  return t;
}

const sameMessage = (a, b) => a && b && a.role === b.role && a.timestamp === b.timestamp;

export function applyEvent(t, event) {
  switch (event?.type) {
    case "agent_start":
      t.working = true;
      t.workingSince = Date.now();
      t.exited = null;
      if (!t.turn) beginTurn(t, t.workingSince);
      break;
    case "turn_cost":
      // Claude Code gives the cost of a run when the run ends, not with
      // each message.
      if (t.turn && Number(event.cost) > 0) t.turn.cost += Number(event.cost);
      break;
    case "agent_settled":
      t.working = false;
      t.retry = null;
      endTurn(t, Date.now());
      // A tool that never reported an end stays drawn as unfinished.
      for (const item of t.items) if (item.kind === "tool" && (item.status === "running" || item.status === "streaming")) item.status = "stopped";
      break;
    case "message_start": {
      const message = event.message;
      if (message?.role === "assistant") {
        t.current = { id: `a${++t.counter}`, calls: {} };
        break;
      }
      if (message?.role === "toolResult") break;
      t.started = message;
      addMessage(t, message);
      break;
    }
    case "message_update": {
      const update = event.assistantMessageEvent ?? {};
      if (!t.current) t.current = { id: `a${++t.counter}`, calls: {} };
      const key = `${t.current.id}:${update.contentIndex}`;
      switch (update.type) {
        case "text_start": upsert(t, key, { kind: "text", text: "", streaming: true }); break;
        case "text_delta": {
          const item = t.byId.get(key) ?? upsert(t, key, { kind: "text", text: "", streaming: true });
          item.text += update.delta ?? "";
          t.version++;
          break;
        }
        case "text_end": upsert(t, key, { kind: "text", text: update.content ?? "", streaming: false }); break;
        case "thinking_start": upsert(t, key, { kind: "thinking", text: "", streaming: true }); break;
        case "thinking_delta": {
          const item = t.byId.get(key) ?? upsert(t, key, { kind: "thinking", text: "", streaming: true });
          item.text += update.delta ?? "";
          t.version++;
          break;
        }
        case "thinking_end": upsert(t, key, { kind: "thinking", text: update.content ?? "", streaming: false }); break;
        case "toolcall_start":
          t.current.calls[update.contentIndex] = update.id;
          tool(t, update.id, { name: update.toolName, status: "streaming" });
          break;
        case "toolcall_delta": {
          const callId = t.current.calls[update.contentIndex];
          const item = callId ? t.byId.get(`tool:${callId}`) : null;
          if (item) {
            item.argsText += update.delta ?? "";
            t.version++;
          }
          break;
        }
        case "toolcall_end":
          if (update.toolCall?.id) {
            t.current.calls[update.contentIndex] = update.toolCall.id;
            tool(t, update.toolCall.id, { name: update.toolCall.name, args: update.toolCall.arguments ?? {}, status: "pending" });
          }
          break;
      }
      if (event.usage) t.usage = event.usage;
      break;
    }
    case "message_end": {
      const message = event.message;
      if (message?.role === "assistant") {
        assistant(t, message, t.current?.id ?? `a${++t.counter}`);
        t.current = null;
        break;
      }
      if (message?.role === "toolResult") {
        finishTool(t, message.toolCallId, message.toolName, message, message.isError);
        break;
      }
      // The start event added it already, unless the start came before a snapshot.
      if (!sameMessage(t.started, message)) addMessage(t, message);
      t.started = null;
      break;
    }
    case "tool_execution_start":
      tool(t, event.toolCallId, { name: event.toolName, args: event.args ?? undefined, status: "running", startedAt: Date.now() });
      break;
    case "tool_execution_update": {
      const item = tool(t, event.toolCallId, { name: event.toolName, status: "running" });
      item.partial = textOf(event.partialResult?.content);
      t.version++;
      break;
    }
    case "tool_execution_end":
      finishTool(t, event.toolCallId, event.toolName, event.result, event.isError, event.durationMs);
      break;
    case "queue_update":
      t.queue = { steering: event.steering ?? [], followUp: event.followUp ?? [] };
      t.version++;
      break;
    case "compaction_start":
      t.compacting = event.reason ?? "manual";
      t.version++;
      break;
    case "compaction_end":
      t.compacting = null;
      if (event.result) add(t, { id: `k${++t.counter}`, kind: "compaction", summary: event.result.summary ?? "", tokensBefore: event.result.tokensBefore, tokensAfter: event.result.estimatedTokensAfter });
      else if (event.aborted) notice(t, `k${++t.counter}`, "warning", "Compaction stopped.");
      else if (event.errorMessage) notice(t, `k${++t.counter}`, "error", `Compaction failed: ${event.errorMessage}`);
      break;
    case "auto_retry_start":
      t.retry = { attempt: event.attempt, maxAttempts: event.maxAttempts, delayMs: event.delayMs, error: event.errorMessage, at: Date.now() };
      t.version++;
      break;
    case "auto_retry_end":
      t.retry = null;
      if (event.success === false) notice(t, `r${++t.counter}`, "error", `The model failed after ${event.attempt} attempts: ${event.finalError ?? "unknown error"}`);
      t.version++;
      break;
    case "session_info_changed":
      t.name = event.name ?? "";
      t.version++;
      break;
    case "thinking_level_changed":
      t.thinkingLevel = event.level ?? "";
      t.version++;
      break;
    case "extension_error":
      notice(t, `e${++t.counter}`, "error", `${baseName(event.extensionPath)} (${event.event}): ${event.error}`);
      break;
    case "rpc_error":
      notice(t, `e${++t.counter}`, "error", `${event.command}: ${event.error}`);
      break;
    case "rpc_response":
      if (event.success === false) notice(t, `e${++t.counter}`, "error", `${event.command}: ${event.error}`);
      break;
    case "host_pi_exit":
      t.working = false;
      t.exited = { code: event.code, signal: event.signal, error: event.error, stderr: event.stderr ?? [] };
      notice(t, `x${++t.counter}`, "error", `Pi stopped${event.code !== null && event.code !== undefined ? ` with code ${event.code}` : ""}${event.signal ? ` (${event.signal})` : ""}.${event.error ? ` ${event.error}` : ""}`);
      break;
  }
  return t;
}

// Galpon delivers messages of other agents, and their results, with protocol
// lines for the model. parseDelivery gives the parts that a person reads:
// [{ kind, sender, body }], kind request, question, inform, result or blocker.
// It gives null for a text that is not a Galpon delivery.
const DELIVERY_FOOTERS = new Set([
  "Delivery instructions: This is one-way information. Use it if it is relevant. Address it in this turn, but do not send a reply to the sender. Your final assistant text is stored only as the durable local completion record.",
  "Delivery instructions: Address every delivery in this batch. Your final assistant text is the durable result for this batch. State what you completed, the main result, and any error or remaining work. Do not use galpon_send_agent to return a result for a current delivery. Galpón sends your final text to the requester when this turn settles.",
  "This is a result from an earlier assignment, not a new assignment. Do not treat it as a reply to an unrelated user request. Do not repeat a completion report that was already given.",
  "Continue the original task from the saved conversation. Use these results, then give the final result for that task.",
]);
const DELIVERY_HEADER = /^(Work request|Question|One-way information|Blocked work notification|Message(?: \d+ of \d+)?)(?: from Galpón agent ([^\n]+?))? \[delivery [A-Za-z0-9:_-]{1,128}\]:(?:\n\n|$)/;
const RECEIPT_HEADER = /^Durable (result|blocker) for assignment [A-Za-z0-9:_-]{1,128}:(?:\n\n|$)/;
const INTENTS = { "Work request": "request", Question: "question", "One-way information": "inform", "Blocked work notification": "blocker" };

export function parseDelivery(text) {
  const separator = "\n\n---\n\n";
  let body = String(text ?? "").replace(/\r\n?/g, "\n").trim();
  const footer = body.lastIndexOf(separator);
  if (footer >= 0 && DELIVERY_FOOTERS.has(body.slice(footer + separator.length).trim())) body = body.slice(0, footer);
  const out = [];
  for (const part of body.split(separator)) {
    const receipt = RECEIPT_HEADER.exec(part);
    if (receipt) {
      out.push({ kind: receipt[1] === "blocker" ? "blocker" : "result", sender: "", body: part.slice(receipt[0].length).trim() });
      continue;
    }
    const head = DELIVERY_HEADER.exec(part);
    if (!head) return null;
    let rest = part.slice(head[0].length);
    let kind = INTENTS[head[1]] ?? "request";
    const result = /^Completed correlated result(?: for message [A-Za-z0-9:_-]{1,128})?\. This is a result notification, not a new work request\.(?:\n\nLinked todo #\d+ was reconciled automatically\. Review the result before you close any dependent todo\.)?(?:\n\n|$)/.exec(rest);
    if (result) {
      kind = "result";
      rest = rest.slice(result[0].length);
    }
    const blocker = /^This is an agent-reported blocker\. It is not a completed result\. Review it and request user input when necessary\.(?:\n\n|$)/.exec(rest);
    if (blocker) {
      kind = "blocker";
      rest = rest.slice(blocker[0].length);
    }
    out.push({ kind, sender: head[2] ?? "", body: rest.trim() });
  }
  return out.length ? out : null;
}

export function baseName(path) {
  const text = String(path ?? "");
  const parts = text.split("/").filter(Boolean);
  return parts.length ? parts[parts.length - 1] : text;
}

const oneLine = (value) => String(value ?? "").split("\n").map((line) => line.trim()).find(Boolean)?.slice(0, 240) ?? "";
const list = (value) => (Array.isArray(value) ? value.join(" · ") : value);

// toolSummary gives the one line that a closed tool card shows after its name.
export function toolSummary(name, args) {
  const a = args && typeof args === "object" ? args : {};
  switch (String(name).toLowerCase()) {
    case "bash": return oneLine(a.command);
    case "read": return [a.path, a.offset ? `from line ${a.offset}` : "", a.limit ? `${a.limit} lines` : ""].filter(Boolean).join(" · ");
    case "edit":
    case "write": return a.path ?? "";
    case "grep": return `${a.pattern ?? ""}${a.path ? ` in ${a.path}` : ""}`;
    case "find":
    case "ls": return a.pattern ?? a.path ?? "";
    case "web_search": return oneLine(list(a.queries ?? a.query));
    case "fetch_content": return oneLine(list(a.urls ?? a.url));
    case "monitor": return oneLine(a.description ?? a.command);
    case "taskoutput":
    case "taskstop": return a.task_id ?? a.bash_id ?? a.shell_id ?? "";
    case "todo": return [a.action, a.subject ?? (a.id !== undefined ? `#${a.id}` : "")].filter(Boolean).join(" ");
    default: {
      const value = Object.values(a).find((item) => typeof item === "string" && item.trim());
      return value ? oneLine(value) : "";
    }
  }
}

// partialArgs reads the arguments of a tool call that still streams, as far
// as they go, so a card can show its command before the call ends.
export function partialArgs(text) {
  try {
    return JSON.parse(text);
  } catch {
    const match = /"(command|path|description|query|url|pattern)"\s*:\s*"((?:[^"\\]|\\.)*)/.exec(text ?? "");
    if (!match) return {};
    try {
      return { [match[1]]: JSON.parse(`"${match[2]}"`) };
    } catch {
      return { [match[1]]: match[2] };
    }
  }
}

export function formatTokens(value) {
  const n = Number(value) || 0;
  if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}k`;
  return String(Math.round(n));
}

export function formatCost(value) {
  const n = Number(value) || 0;
  if (n === 0) return "$0";
  if (n < 0.01) return "<$0.01";
  return `$${n < 10 ? n.toFixed(2) : n.toFixed(n < 100 ? 1 : 0)}`;
}

export function formatDuration(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n < 0) return "";
  if (n < 1000) return `${Math.round(n)}ms`;
  if (n < 60000) return `${(n / 1000).toFixed(n < 10000 ? 1 : 0)}s`;
  const minutes = Math.floor(n / 60000);
  return `${minutes}m${String(Math.round((n % 60000) / 1000)).padStart(2, "0")}s`;
}

// ago gives the age of a fact: now, 8s, 4m, 2h, 3d. A timer of a run that
// works uses elapsed, which counts each second.
export function ago(ms) {
  return Number(ms) < 5000 ? "now" : elapsed(ms);
}

// clock gives the time of day of a timestamp: 14:32.
export function clock(at) {
  const date = new Date(Number(at) || 0);
  if (!Number(at) || Number.isNaN(date.getTime())) return "";
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

// plainMarkdown makes a text that markdown shows as it is: every mark is
// escaped, and each line break stays a line break.
export function plainMarkdown(text) {
  return String(text ?? "").replace(/[\\`*_{}[\]()#+\-.!|<>~&]/g, (mark) => `\\${mark}`).replace(/\n/g, "\\\n");
}

// userMarkdown makes the markdown of a message that a person typed, as a
// chat shows it. Each line break stays: markdown joins the lines of a
// paragraph, so a line that another line follows ends with two spaces. Text
// in angle brackets, such as <remote>, stays too: markdown reads it as HTML,
// which the view drops. Code spans and fenced code stay as typed.
export function userMarkdown(text) {
  const lines = String(text ?? "").split("\n");
  const fenceOf = (line) => /^\s{0,3}(`{3,}|~{3,})/.exec(line);
  let fence = null;
  return lines.map((line, index) => {
    const mark = fenceOf(line);
    if (fence) {
      if (mark && mark[1][0] === fence[0] && mark[1].length >= fence.length && !line.slice(mark[0].length).trim()) fence = null;
      return line;
    }
    if (mark) {
      fence = mark[1];
      return line;
    }
    const next = lines[index + 1];
    const out = escapeTags(line);
    return line.trim() && next?.trim() && !fenceOf(next) ? `${out.replace(/\s+$/, "")}  ` : out;
  }).join("\n");
}

// escapeTags escapes each < of a line outside its code spans.
function escapeTags(line) {
  let out = "";
  let at = 0;
  while (at < line.length) {
    if (line[at] !== "`") {
      out += line[at] === "<" ? "\\<" : line[at];
      at++;
      continue;
    }
    let run = 1;
    while (line[at + run] === "`") run++;
    // A code span ends at the next run of the same count of backticks.
    let end = -1;
    for (let from = at + run; end < 0;) {
      const found = line.indexOf("`".repeat(run), from);
      if (found < 0) break;
      let length = run;
      while (line[found + length] === "`") length++;
      if (length === run) end = found;
      from = found + length;
    }
    const stop = end < 0 ? at + run : end + run;
    out += line.slice(at, stop);
    at = stop;
  }
  return out;
}

// splitMarkdown cuts markdown into prose and fenced code blocks, so that the
// view draws code in its own monospace block.
export function splitMarkdown(text) {
  const parts = [];
  const lines = String(text ?? "").split("\n");
  let prose = [];
  let code = null;
  const flush = () => {
    if (prose.join("\n").trim()) parts.push({ kind: "prose", text: prose.join("\n") });
    prose = [];
  };
  for (const line of lines) {
    const fence = /^(\s{0,3})(`{3,}|~{3,})\s*([\w+#.-]*)/.exec(line);
    if (code) {
      if (fence && !fence[3] && fence[2][0] === code.mark[0] && fence[2].length >= code.mark.length && !line.slice(fence[0].length).trim()) {
        parts.push({ kind: "code", language: code.language, text: code.lines.join("\n") });
        code = null;
      } else {
        code.lines.push(line);
      }
    } else if (fence) {
      flush();
      code = { mark: fence[2], language: fence[3] ?? "", lines: [] };
    } else {
      prose.push(line);
    }
  }
  // A block that still streams has no end yet.
  if (code) parts.push({ kind: "code", language: code.language, text: code.lines.join("\n"), open: true });
  flush();
  return parts;
}

// elapsed gives a short age: 8s, 4m, 2h, 3d.
export function elapsed(ms) {
  const n = Math.max(0, Number(ms) || 0);
  if (n < 60000) return `${Math.floor(n / 1000)}s`;
  if (n < 3600000) return `${Math.floor(n / 60000)}m`;
  if (n < 86400000) return `${Math.floor(n / 3600000)}h`;
  return `${Math.floor(n / 86400000)}d`;
}

// todoRows orders the todo list as the Work Dock does: the task in progress,
// the ready tasks, the blocked tasks, then the completed tasks, newest first.
export function todoRows(tasks) {
  const all = (tasks ?? []).filter((task) => task && task.status !== "deleted");
  const byId = new Map(all.map((task) => [task.id, task]));
  const blockers = (task) => (task.blockedBy ?? []).filter((id) => byId.get(id)?.status !== "completed");
  const rank = (task) => (task.status === "in_progress" ? 0 : task.status === "completed" ? 3 : blockers(task).length ? 2 : 1);
  const open = all.filter((task) => task.status !== "completed").sort((a, b) => rank(a) - rank(b) || a.id - b.id);
  const done = all.filter((task) => task.status === "completed").sort((a, b) => b.id - a.id);
  return {
    open: open.map((task) => ({ ...task, blockers: blockers(task) })),
    done,
    total: all.length,
  };
}

export const LIVE_WORK = new Set(["queued", "started", "waiting"]);

// workRows flattens Galpon's delegated work into rows with their depth. A
// branch with live work comes first, as in the Work Dock of the terminal.
export function workRows(work) {
  const active = new Map();
  const mark = (item) => {
    let live = LIVE_WORK.has(item?.observation?.state);
    for (const child of item?.children ?? []) if (mark(child)) live = true;
    active.set(item, live);
    return live;
  };
  for (const item of work ?? []) mark(item);
  const rows = [];
  const visit = (items, depth) => {
    const ordered = (items ?? []).map((item, index) => ({ item, index }))
      .sort((a, b) => Number(active.get(b.item)) - Number(active.get(a.item)) || a.index - b.index);
    for (const { item } of ordered) {
      rows.push({ item, depth, live: active.get(item) });
      visit(item.children, depth + 1);
    }
  };
  visit(work, 0);
  return rows;
}

// The thinking levels of Pi, from none to the most.
export const THINKING_LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];

// thinkingStep gives the place of a level among the levels of the model, and
// how many places there are: the bars of the thinking control.
export function thinkingStep(level, levels) {
  const list = (levels?.length ? levels : THINKING_LEVELS.slice(0, 5)).filter((name) => THINKING_LEVELS.includes(name));
  return { index: Math.max(0, list.indexOf(level || "off")), count: list.length, list };
}

// shortPath shows a path under the home folder as ~/...
export function shortPath(path, home) {
  const text = String(path ?? "");
  if (home && (text === home || text.startsWith(`${home}/`))) return `~${text.slice(home.length)}`;
  return text;
}

// commandMatches filters the slash commands for the text in the composer.
export function commandMatches(commands, text, limit = 8) {
  const query = String(text ?? "");
  if (!query.startsWith("/") || /\s/.test(query)) return [];
  const needle = query.slice(1).toLowerCase();
  const scored = [];
  for (const command of commands ?? []) {
    const name = String(command.name ?? "").toLowerCase();
    const at = name.indexOf(needle);
    if (needle && at < 0) continue;
    scored.push({ command, score: at === 0 ? 0 : at > 0 ? 1 : 2 });
  }
  scored.sort((a, b) => a.score - b.score || String(a.command.name).localeCompare(String(b.command.name)));
  return scored.slice(0, limit).map((entry) => entry.command);
}

// isSendInsert tells if one text change is a single typed Enter: the new text
// is the old text with one newline (and maybe indentation) inserted.
export function isSendInsert(before, after) {
  // Most changes add no line. This check uses native search, so a long text
  // does not cost a loop in JavaScript for each typed character.
  if (after.length <= before.length || newlines(after) === newlines(before)) return false;
  let start = 0;
  while (start < before.length && before[start] === after[start]) start++;
  let endBefore = before.length;
  let endAfter = after.length;
  while (endBefore > start && endAfter > start && before[endBefore - 1] === after[endAfter - 1]) {
    endBefore--;
    endAfter--;
  }
  if (endBefore !== start) return false;
  return /^\n[ \t]*$/.test(after.slice(start, endAfter));
}

function newlines(text) {
  let count = 0;
  for (let at = text.indexOf("\n"); at >= 0; at = text.indexOf("\n", at + 1)) count++;
  return count;
}
