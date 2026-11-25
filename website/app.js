// app.js: the browser demo of Galpon in the Herdr app. It draws the Galpon
// layout of the app (the sidebar, an agent tab, its panel and the Changes
// view) and its popups (the command center, New agent and menus) from the
// sample records in data.js. Nothing leaves the browser.

import {
  AGENTS, CHANGES, EFFORTS, FLAKY_RUN, MODELS, PR_GROUPS, PULL_REQUESTS, RELEASE_QUESTION, TERMINALS, WORKSPACES, demoRun,
} from "./data.js";

const $ = (selector, root = document) => root.querySelector(selector);
const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const icon = (name, size = "") => `<i class="ic ${size}" style="--i:url(icons/${name}.svg)" aria-hidden="true"></i>`;
const mark = (state) => state === "failed" ? `<span class="mark failed">${icon("x", "s13")}</span>` : `<span class="mark ${state}"><span class="dot"></span></span>`;
const clock = (at) => new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
const wide = () => window.innerWidth > 900;

function ago(at) {
  const s = Math.max(0, Math.round((Date.now() - at) / 1000));
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

// --- state

const state = {
  agents: AGENTS.map((agent) => ({ ...agent, transcript: agent.transcript.map((item) => ({ ...item })), draft: "" })),
  current: "meet",
  folded: new Set(),
  prFolded: new Set(["ready"]),
  panelShown: window.innerWidth > 1280,
  changes: false,
  viewed: new Set(),
  overlay: null,
  menu: null,
  runs: new Map(),
};
const agentById = (id) => state.agents.find((agent) => agent.id === id);

// panelOf gives the panel tab of an agent: the one that the person chose,
// else the first tab with content, as the app does. Null closes the panel.
function panelOf(agent) {
  if (agent.panel !== undefined) return agent.panel;
  if (!state.panelShown) return null;
  if (agent.todos?.length) return "tasks";
  if (agent.delegations?.length) return "agents";
  if (editsOf(agent).length) return "changes";
  return null;
}
Object.defineProperty(state, "panel", { get: () => panelOf(current()), set: (value) => { current().panel = value; } });
const current = () => agentById(state.current);

// --- markdown: paragraphs, lists, `code`, **bold**, links and fenced code

function inline(text) {
  let out = "";
  const parts = String(text).split(/(`[^`]+`)/);
  for (const part of parts) {
    if (part.startsWith("`") && part.endsWith("`") && part.length > 1) {
      out += `<code>${esc(part.slice(1, -1))}</code>`;
      continue;
    }
    out += esc(part)
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  }
  return out;
}

function highlight(code, language) {
  const keywords = language === "go" ? /\b(func|if|return|for|range|go|defer|var|const|type|struct|nil|true|false)\b/g : /^(\s*)(curl|npm|git|cd|go|galpon|pi)\b/gm;
  return code.split("\n").map((line) => {
    const comment = language === "go" ? line.indexOf("//") : line.indexOf("#");
    let body = comment >= 0 ? line.slice(0, comment) : line;
    const tail = comment >= 0 ? `<span class="c">${esc(line.slice(comment))}</span>` : "";
    body = esc(body).replace(/(&quot;[^&]*?&quot;)/g, '<span class="s">$1</span>');
    body = language === "go" ? body.replace(keywords, '<span class="k">$1</span>') : body.replace(keywords, '$1<span class="k">$2</span>');
    return body + tail;
  }).join("\n");
}

let codeIds = 0;
function markdown(md) {
  const out = [];
  const lines = String(md).split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const fence = /^```(\w*)/.exec(line);
    if (fence) {
      const code = [];
      for (i++; i < lines.length && !lines[i].startsWith("```"); i++) code.push(lines[i]);
      i++;
      const id = `code-${++codeIds}`;
      const text = code.join("\n");
      out.push(`<div class="code"><div class="code-head"><span>${esc(fence[1] || "text")}</span><span class="grow"></span>
        <button class="copy" data-action="copy" data-code="${id}">${icon("files", "s12")}<span>Copy</span></button></div>
        <pre id="${id}" data-text="${esc(text)}">${highlight(text, fence[1])}</pre></div>`);
      continue;
    }
    if (/^- /.test(line)) {
      const items = [];
      for (; i < lines.length && /^- /.test(lines[i]); i++) items.push(`<li>${inline(lines[i].slice(2))}</li>`);
      out.push(`<ul>${items.join("")}</ul>`);
      continue;
    }
    if (!line.trim()) { i++; continue; }
    const para = [];
    for (; i < lines.length && lines[i].trim() && !/^```|^- /.test(lines[i]); i++) para.push(inline(lines[i]));
    out.push(`<p>${para.join("<br>")}</p>`);
  }
  return `<div class="md">${out.join("")}</div>`;
}

// --- the sidebar

function renderSidebar() {
  const rows = [`<div class="side-label">Workspaces</div>`];
  for (const workspace of WORKSPACES) {
    const folded = state.folded.has(workspace.id);
    rows.push(`<button class="ws-row ${folded ? "folded" : ""}" data-action="fold" data-id="${workspace.id}" aria-expanded="${!folded}">
      ${icon("chevron-down", "s13 chev")}<span>${esc(workspace.title)}</span></button>`);
    if (folded) continue;
    const agents = state.agents.filter((agent) => agent.workspace === workspace.id);
    for (const agent of agents) {
      rows.push(`<button class="side-row ${agent.id === state.current ? "current" : ""} ${agent.fresh ? "fresh" : ""}" data-action="open" data-id="${agent.id}" data-menu="agent">
        ${mark(agent.state)}<span class="title">${esc(agent.title)}</span><span class="age">${agent.state === "working" ? "" : ago(agent.at)}</span></button>`);
    }
    for (const term of TERMINALS.filter((t) => t.workspace === workspace.id)) {
      rows.push(`<button class="side-row term" data-action="terminal" data-id="${term.id}">
        <span class="mark term">${icon("terminal", "s13")}</span><span class="title">${esc(term.title)}</span></button>`);
    }
  }
  $("#workspaces").innerHTML = rows.join("");

  const needs = PULL_REQUESTS.filter((pr) => pr.group === "needs").length;
  const pulls = [`<div class="pulls-head"><span>Pull requests</span><span class="badge yellow">● ${needs}</span><span class="grow"></span><span>now</span>
    <button class="icon-btn" data-action="refresh" title="Refresh pull requests" aria-label="Refresh pull requests">${icon("refresh", "s13")}</button></div>`];
  for (const group of PR_GROUPS) {
    const items = PULL_REQUESTS.filter((pr) => pr.group === group.id);
    if (!items.length) continue;
    const folded = state.prFolded.has(group.id);
    pulls.push(`<button class="pr-group ${folded ? "folded" : ""}" data-action="pr-fold" data-id="${group.id}">${icon("chevron-down", "s13 chev")}<span>${group.title}</span><span class="count">${items.length}</span></button>`);
    if (folded) continue;
    for (const pr of items) {
      const agent = agentById(pr.agent);
      const tone = pr.failed ? "failed" : pr.group === "needs" ? "needs" : pr.group === "ready" ? "ready" : "working";
      pulls.push(`<button class="pr-row ${pr.group === "needs" ? "needs" : ""}" data-action="pr-open" data-id="${pr.id}" data-menu="pr"
        title="${esc(`${pr.ref} · ${pr.title}`)}">
        <div class="line1">${mark(tone)}<span class="title">${esc(pr.title)}</span></div>
        <div class="line2"><span class="mono">${esc(pr.ref)}</span>${agent ? `<span>·</span><span>${esc(agent.title)}</span>` : ""}</div></button>`);
    }
  }
  $("#pulls").innerHTML = pulls.join("");
  $("#agent-count").textContent = `${state.agents.length} agents`;
}

// --- the agent tab

function editsOf(agent) {
  const files = new Map();
  for (const item of agent.transcript) {
    if (item.kind !== "log") continue;
    for (const step of item.steps) {
      if (step.added === undefined) continue;
      const file = files.get(step.detail) ?? { path: step.detail, added: 0, removed: 0, edits: 0 };
      file.added += step.added;
      file.removed += step.removed;
      file.edits++;
      files.set(step.detail, file);
    }
  }
  return [...files.values()];
}

function renderCard() {
  const typing = document.activeElement?.id === "prompt";
  const agent = current();
  const edits = editsOf(agent);
  const todos = agent.todos ?? [];
  const delegations = agent.delegations ?? [];
  const waiting = delegations.some((d) => d.state === "needs");
  // The Changes view takes the room of the panel unless the window is wide.
  const panelOpen = Boolean(state.panel) && (!state.changes || window.innerWidth > 1760);
  const barButtons = panelOpen
    ? `<button class="bar-btn on" data-action="panel" data-id="${state.panel}" title="Close the panel">${icon("panel-right")}</button>`
    : `<button class="bar-btn" data-action="panel" data-id="changes">${icon("square-pen")}<span class="label">Edits</span>${edits.length ? `<span class="n">${edits.length}</span>` : ""}</button>
       <button class="bar-btn" data-action="panel" data-id="tasks">${icon("list-todo")}<span class="label">Tasks</span>${todos.length ? `<span class="n">${todos.length}</span>` : ""}</button>
       <button class="bar-btn" data-action="panel" data-id="agents">${icon("bot")}<span class="label">Agents</span>${delegations.length ? `<span class="n">${delegations.length}</span>` : ""}${waiting ? '<span class="att"></span>' : ""}</button>`;
  $("#card").innerHTML = `
    <header class="pane-head">${icon("bot")}<span class="name">${esc(agent.title)}</span><span class="where">${esc(WORKSPACES.find((w) => w.id === agent.workspace)?.title)}</span>
      <span class="grow"></span><span class="where">${esc(agent.harness)}</span></header>
    <div class="pane-body">
      <div class="agent-col">
        <div class="context-bar">
          <span class="folder-chip" title="The folder of the agent">${icon("folder", "s12")}<span>${esc(agent.folder)}</span></span>
          <span class="grow"></span>
          <span class="cost" title="What this session cost at API prices">${esc(agent.cost)}</span>
          <button class="bar-btn ${state.changes ? "on" : ""}" data-action="changes" title="The Git work of the agent beside the conversation · Alt+C">${icon("git-compare")}<span class="label">Changes</span></button>
          ${barButtons}
        </div>
        <div class="scroll" id="scroll"><div class="column" id="column"></div></div>
        <div class="composer-wrap" id="composer-wrap"></div>
      </div>
      ${panelOpen ? renderPanel(agent, edits, todos, delegations) : ""}
    </div>`;
  const changes = $("#changes-card");
  changes.hidden = !state.changes;
  changes.innerHTML = state.changes ? renderChanges(agent) : "";
  renderTranscript(true);
  renderComposer(typing);
}

function renderTranscript(jump = false) {
  const agent = current();
  const column = $("#column");
  if (!column) return;
  const scroll = $("#scroll");
  const atBottom = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 60;
  if (!agent.transcript.length) {
    column.innerHTML = `<div class="welcome"><div class="mark-big">${icon("sparkle", "s22")}</div><h2>${esc(agent.title)}</h2>
      <div class="where">Galpon agent in ${esc(WORKSPACES.find((w) => w.id === agent.workspace)?.title)}</div><div class="folder">${esc(agent.folder)}</div>
      <div class="tips"><div class="tip"><kbd>Ctrl K</kbd>Find any agent, pull request or workspace</div><div class="tip"><kbd>Shift Tab</kbd>Change the thinking level</div>
      <div class="tip"><kbd>Alt C</kbd>Show the Git work of the agent</div><div class="tip"><kbd>/</kbd>Commands</div></div></div>`;
    return;
  }
  column.innerHTML = agent.transcript.map((item, index) => renderItem(agent, item, index)).join("");
  if (jump) scroll.scrollTop = agent.id === "meet" && !agent.visited ? 0 : scroll.scrollHeight;
  else if (atBottom) scroll.scrollTop = scroll.scrollHeight;
  agent.visited = true;
}

function renderItem(agent, item, index) {
  switch (item.kind) {
    case "user":
      return `<div class="bubble-row"><div class="bubble">${esc(item.text)}</div><div class="bubble-time">${clock(item.at)}</div></div>`;
    case "text":
      return markdown(item.md);
    case "meta":
      return `<div class="meta-line"><span>${esc(item.model)}</span><span class="sep">·</span><span>${esc(item.effort)}</span><span class="sep">·</span><span>${esc(item.took)}</span>
        <span class="sep">·</span><span>${esc(item.tokens)}</span><span class="sep">·</span><span>${esc(item.cost)}</span><span class="grow"></span><span>${clock(item.at)}</span></div>`;
    case "delivery": {
      const look = { request: ["message", "Work request", "var(--blue)"], result: ["circle-check", "Result", "var(--green)"] }[item.type];
      return `<div class="delivery"><div class="delivery-head"><span style="color:${look[2]}">${icon(look[0], "s13")}</span><strong>${look[1]}</strong>
        <span class="from">from ${esc(item.sender)}</span><span class="time">${clock(item.at)}</span></div><div class="delivery-body">${inline(item.body)}</div></div>`;
    }
    case "log": {
      const steps = item.steps.map((step) => `<div class="step">${icon(step.icon, "s13")}<span>${esc(step.text)}</span>
        <span class="detail ${/[/.]/.test(step.detail) ? "mono" : ""}">${esc(step.detail)}</span>
        ${step.added !== undefined ? `<span class="counts"><span class="plus">+${step.added}</span><span class="minus">−${step.removed}</span></span>` : ""}</div>`).join("");
      if (item.live) {
        return `<div class="log live open"><div class="live-head">${mark(item.stopped ? "idle" : "working")}<span class="live-label">${item.stopped ? "Stopped" : `Working ${Math.round((Date.now() - item.since) / 1000)}s`}</span></div>
          <div class="steps">${steps}</div></div>`;
      }
      const edited = item.steps.filter((step) => step.added !== undefined);
      const files = edited.length ? `<span class="files">${new Set(edited.map((s) => s.detail)).size} file${new Set(edited.map((s) => s.detail)).size === 1 ? "" : "s"}
        <span class="plus">+${edited.reduce((n, s) => n + s.added, 0)}</span><span class="minus">−${edited.reduce((n, s) => n + s.removed, 0)}</span></span>` : "";
      return `<div class="log ${item.open ? "open" : ""}"><button class="log-head" data-action="log" data-index="${index}" aria-expanded="${Boolean(item.open)}">
        ${icon("chevron-right", "s13 chev")}<span>Worked through ${item.steps.length} step${item.steps.length === 1 ? "" : "s"}</span>${files}</button>
        ${item.open ? `<div class="steps">${steps}</div>` : ""}</div>`;
    }
  }
  return "";
}

function renderComposer(keepFocus = false) {
  const agent = current();
  const wrap = $("#composer-wrap");
  if (!wrap) return;
  const focused = keepFocus || document.activeElement?.id === "prompt";
  const working = agent.state === "working";
  const question = agent.question && !working ? `<div class="question" role="group" aria-label="Question from the agent">
      <h3>${icon("alert", "s13")}${esc(RELEASE_QUESTION.title)}</h3>
      ${RELEASE_QUESTION.options.map((option, i) => `<button class="option" data-action="answer" data-index="${i}"><span class="n">${i + 1}</span><span>${esc(option.label)}</span><span class="d">${esc(option.detail)}</span></button>`).join("")}
    </div>` : "";
  const flash = agent.flash ?? "";
  wrap.innerHTML = `${question}
    <div class="composer">
      <textarea id="prompt" rows="1" placeholder="Message ${esc(agent.title)}" aria-label="Message ${esc(agent.title)}">${esc(agent.draft)}</textarea>
      <div class="composer-row">
        <button class="chip" data-action="models" title="Choose the model · Ctrl L">${icon("sparkle", "s13")}${esc(agent.model)}${icon("chevron-down", "s12")}</button>
        <button class="chip" data-action="effort" title="Change the thinking level · Shift Tab">${icon("thinking", "s13")}${esc(agent.effort)}${icon("chevron-down", "s12")}</button>
        <span class="flash">${esc(flash)}</span>
        <span class="grow"></span>
        <span class="ring" title="Context used">${ring(agent.context)}${agent.context}%</span>
        <button class="send ${working ? "stop" : agent.draft.trim() ? "ready" : ""}" data-action="${working ? "stop" : "send"}" aria-label="${working ? "Stop" : "Send"}">${icon(working ? "stop" : "arrow-up", "s13")}</button>
      </div>
    </div>
    <div class="hint">${working ? "enter steer · esc stop" : agent.question ? "1 or 2 answers · enter send · shift+enter new line" : "enter send · shift+enter new line · shift+tab thinking · alt+c changes"}</div>`;
  const prompt = $("#prompt");
  grow(prompt);
  if (focused) {
    prompt.focus();
    prompt.setSelectionRange(prompt.value.length, prompt.value.length);
  }
}

function ring(percent) {
  const r = 6;
  const c = 2 * Math.PI * r;
  return `<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="${r}" fill="none" stroke="var(--line2)" stroke-width="2"/>
    <circle cx="8" cy="8" r="${r}" fill="none" stroke="var(--text3)" stroke-width="2" stroke-dasharray="${(c * percent) / 100} ${c}" transform="rotate(-90 8 8)" stroke-linecap="round"/></svg>`;
}

function grow(textarea) {
  textarea.style.height = "auto";
  textarea.style.height = `${Math.min(220, textarea.scrollHeight)}px`;
}

function renderPanel(agent, edits, todos, delegations) {
  const tab = (id, label, count) => `<button class="panel-tab ${state.panel === id ? "on" : ""}" data-action="panel-tab" data-id="${id}">${label}${count ? `<span class="muted">${count}</span>` : ""}</button>`;
  let body = "";
  if (state.panel === "changes") {
    body = edits.length
      ? `<div class="p-section"><div class="p-title"><span>${edits.length} ${edits.length === 1 ? "file" : "files"} changed</span><span><span class="plus">+${edits.reduce((n, f) => n + f.added, 0)}</span> <span class="minus">−${edits.reduce((n, f) => n + f.removed, 0)}</span></span></div>
        <button class="p-row" data-action="changes">${icon("git-compare", "s13")}<span class="t">Open the Changes view</span><span class="r">Alt C</span></button>
        ${edits.map((file) => `<div class="p-row">${icon("file-text", "s13")}<span class="t">${esc(file.path.split("/").pop())} <span class="muted">${esc(file.path.split("/").slice(0, -1).join("/"))}</span></span></div>
          <div class="p-note"><span class="plus">+${file.added}</span> <span class="minus">−${file.removed}</span> · ${file.edits} ${file.edits === 1 ? "edit" : "edits"}</div>`).join("")}</div>`
      : `<div class="p-empty">No file changes in this session yet.</div>`;
  } else if (state.panel === "agents") {
    body = delegations.length
      ? `<div class="p-section"><div class="p-title"><span>Delegated agents</span><span>${delegations.filter((d) => d.state !== "done").length} active</span></div>
        ${delegations.map((d) => `<div class="p-row">${d.state === "done" ? `<span class="mark ready"><span class="dot"></span></span>` : mark(d.state)}<span class="t">${esc(d.title)}</span><span class="r">${esc(d.age)}</span></div>
          <div class="p-note ${d.state === "needs" ? "need" : ""}">${esc(d.note)}</div>`).join("")}</div>`
      : `<div class="p-empty">No delegated work. An agent can give work to other agents; it shows here.</div>`;
  } else {
    body = todos.length
      ? `<div class="p-section"><div class="p-title"><span>Todos</span><span>${todos.filter((t) => t.done).length} of ${todos.length}</span></div>
        ${todos.map((t) => `<div class="p-row ${t.done ? "done" : ""}">${t.done ? `<span class="mark" style="color:var(--green)">${icon("circle-check", "s13")}</span>` : t.active ? mark(agent.state === "working" ? "working" : "working") : `<span class="mark" style="color:var(--text3)">${icon("circle", "s13")}</span>`}<span class="t">${esc(t.subject)}</span></div>`).join("")}</div>`
      : `<div class="p-empty">No tasks yet. Todos and background commands show here.</div>`;
  }
  return `<aside class="panel" aria-label="Panel"><div class="panel-tabs">${tab("changes", "Edits", edits.length)}${tab("tasks", "Tasks", todos.length)}${tab("agents", "Agents", delegations.length)}
    <span class="grow"></span><button class="icon-btn" data-action="panel" data-id="${state.panel}" aria-label="Close the panel">${icon("x", "s13")}</button></div>
    <div class="panel-list">${body}</div></aside>`;
}

// --- the Changes view

function changeRow(row) {
  const [kind, old, neu, text, uncommitted] = row;
  if (kind === "f") return `<div class="drow f"><span class="bar"></span><span class="no">···</span><span class="no"></span><span class="m"></span><span class="tx">${esc(text)}</span></div>`;
  if (kind === "h") return `<div class="drow h"><span class="bar"></span><span class="no"></span><span class="no"></span><span class="m"></span><span class="tx">${esc(text)}</span></div>`;
  const emph = kind === "d" ? "emd" : "ema";
  const body = esc(text.replace(/\t/g, "    ")).replace(/\{[-+](.*?)[-+]\}/g, `<span class="${emph}">$1</span>`);
  return `<div class="drow ${kind} ${uncommitted ? "unc" : ""}"><span class="bar"></span><span class="no">${old || ""}</span><span class="no">${neu || ""}</span>
    <span class="m">${kind === "d" ? "−" : kind === "a" ? "+" : ""}</span><span class="tx">${body || " "}</span></div>`;
}

function renderChanges(agent) {
  const head = `<header class="pane-head">${icon("git-compare")}<span class="name">Changes · ${esc(agent.title)}</span><span class="where">${esc(WORKSPACES.find((w) => w.id === agent.workspace)?.title)}</span>
    <span class="grow"></span><button class="icon-btn" data-action="changes" aria-label="Close the Changes view">${icon("x", "s13")}</button></header>`;
  if (!agent.changes) {
    return `${head}<section class="changes" aria-label="Changes"><div class="ch-head"><span class="ch-chip">${icon("git-branch", "s13")}${esc(agent.branch)}</span><span class="ch-chip">${icon("layers", "s13")}Branch total</span>
      </div>
      <div class="welcome"><div class="mark-big">${icon("git-compare", "s22")}</div><h2>No changes after ${esc(CHANGES.base)}</h2>
      <div class="where">This branch has no work yet.</div>
      <div class="tips"><button class="primary" data-action="open" data-id="flaky" data-then="changes">Show the changes of Fix the flaky Codex e2e test</button></div></div></section>`;
  }
  const files = CHANGES.files;
  const added = files.reduce((n, f) => n + f.added, 0);
  const removed = files.reduce((n, f) => n + f.removed, 0);
  const dirs = new Map();
  for (const file of files) {
    const cut = file.path.lastIndexOf("/");
    const dir = cut >= 0 ? file.path.slice(0, cut) : "";
    if (!dirs.has(dir)) dirs.set(dir, []);
    dirs.get(dir).push(file);
  }
  const tree = [...dirs.entries()].map(([dir, list]) => `${dir ? `<div class="tree-row dir">${icon("folder", "s13")}<span>${esc(dir)}</span></div>` : ""}
    ${list.map((file) => `<button class="tree-row ${state.viewed.has(file.path) ? "viewed" : ""}" style="padding-left:${dir ? 26 : 8}px" data-action="jump-file" data-path="${esc(file.path)}">
      <span class="st">${file.status}</span><span class="name">${esc(file.path.split("/").pop())}</span></button>`).join("")}`).join("");
  const diff = files.map((file) => {
    const viewed = state.viewed.has(file.path);
    const cut = file.path.lastIndexOf("/");
    return `<div class="file" id="file-${esc(file.path.replace(/\W/g, "-"))}">
      <div class="file-head"><span class="st">${file.status}</span><span><span class="dir">${esc(cut >= 0 ? file.path.slice(0, cut + 1) : "")}</span>${esc(file.path.slice(cut + 1))}</span>
        <span class="counts"><span class="plus">+${file.added}</span><span class="minus">−${file.removed}</span></span>
        <button class="view-btn ${viewed ? "on" : ""}" data-action="viewed" data-path="${esc(file.path)}" title="${viewed ? "Viewed · V" : "Mark viewed · V"}" aria-label="Viewed">${icon(viewed ? "circle-check" : "circle", "s13")}</button></div>
      ${viewed ? "" : `<div class="entities">${file.entities.map((e) => `<span class="${e.startsWith("+") ? "add" : "mod"}">${esc(e)}</span>`).join("")}</div>${file.rows.map(changeRow).join("")}`}</div>`;
  }).join("");
  return `${head}<section class="changes" aria-label="Changes">
    <div class="ch-head"><span class="ch-chip">${icon("git-branch", "s13")}galpon · ${esc(CHANGES.branch)}</span><span class="ch-chip">${icon("layers", "s13")}Branch total</span>
      <span class="ch-totals"><span class="plus">+${added}</span><span class="minus">−${removed}</span><span>${state.viewed.size}/${files.length} viewed</span></span></div>
    <div class="ch-body"><nav class="ch-tree" aria-label="Changed files">${tree}</nav><div class="ch-diff" id="diff">${diff}</div></div>
    <div class="ch-foot"><span>Read-only</span><span>·</span><span>the branch after ${esc(CHANGES.base)}</span><span>·</span><span style="color:var(--yellow)">▎</span><span>uncommitted</span></div>
  </section>`;
}

// --- runs: the demo plays the work of an agent step by step

function play(agent, run, { prompt = null, after = null } = {}) {
  if (prompt) agent.transcript.push({ kind: "user", text: prompt, at: Date.now() });
  const log = { kind: "log", live: true, since: Date.now(), steps: [] };
  agent.transcript.push(log);
  agent.state = "working";
  agent.flash = "";
  agent.at = Date.now();
  if (agent.todos) agent.todos = agent.todos.map((t) => (t.active ? t : t));
  let step = 0;
  const tick = () => {
    if (log.stopped) return;
    if (step < run.steps.length) {
      log.steps.push(run.steps[step++]);
      agent.cost = `$${(parseFloat(agent.cost.slice(1)) + 0.03).toFixed(2)}`;
      if (agent.id === state.current) renderTranscript();
      state.runs.set(agent.id, setTimeout(tick, 650 + Math.random() * 700));
      return;
    }
    log.live = false;
    agent.transcript.push({ kind: "text", md: run.answer });
    const meta = run.meta ?? { model: agent.model, effort: agent.effort, took: `${Math.round((Date.now() - log.since) / 1000)}s`, tokens: "↑64k ↓900", cost: "$0.06" };
    agent.transcript.push({ kind: "meta", ...meta, at: Date.now() });
    agent.state = "ready";
    agent.at = Date.now();
    agent.context = Math.min(92, agent.context + 4);
    if (agent.todos) agent.todos = agent.todos.map((t) => ({ ...t, done: true, active: false }));
    state.runs.delete(agent.id);
    if (agent.id !== state.current) {
      agent.fresh = true;
      toast(`${agent.title} is ready`, "Its turn ended. Open it from the sidebar.", "success");
    }
    after?.();
    refresh(agent.id === state.current ? true : "side");
  };
  state.runs.set(agent.id, setTimeout(tick, 500));
  refresh(agent.id === state.current ? "tab" : "side");
}

function stop(agent) {
  clearTimeout(state.runs.get(agent.id));
  state.runs.delete(agent.id);
  const log = agent.transcript.findLast((item) => item.kind === "log" && item.live);
  if (log) log.stopped = true;
  agent.state = "idle";
  agent.flash = "Stopped. The agent keeps its conversation.";
  refresh("tab");
}

function send(agent) {
  const text = agent.draft.trim();
  if (!text) return;
  agent.draft = "";
  if (agent.state === "working") {
    agent.flash = "Steering: the agent reads this after its current step.";
    agent.transcript.splice(agent.transcript.length - 1, 0, { kind: "user", text, at: Date.now() });
    refresh("tab");
    return;
  }
  agent.question = false;
  play(agent, demoRun(text), { prompt: text });
}

// Live timers: the working rows count their seconds.
setInterval(() => {
  const agent = current();
  if (agent.state === "working") {
    const head = $(".live-label");
    const log = agent.transcript.findLast((item) => item.kind === "log" && item.live);
    if (head && log && !log.stopped) head.textContent = `Working ${Math.round((Date.now() - log.since) / 1000)}s`;
  }
}, 1000);

// --- popups

function openOverlay(overlay) {
  closeMenu();
  state.overlay = overlay;
  renderOverlay();
}

function closeOverlay() {
  state.overlay = null;
  $("#overlay").hidden = true;
  $("#overlay").innerHTML = "";
  $("#prompt")?.focus();
}

function renderOverlay() {
  const o = state.overlay;
  const root = $("#overlay");
  if (!o) return closeOverlay();
  root.hidden = false;
  if (o.kind === "new") return renderNewAgent(root, o);
  const rows = o.kind === "review" ? reviewRows(o) : o.actionsFor ? actionRows(o) : searchRows(o.query);
  o.rows = rows.filter((row) => row.type === "row");
  o.cursor = Math.max(0, Math.min(o.cursor ?? 0, o.rows.length - 1));
  let selectable = -1;
  const list = rows.map((row) => {
    if (row.type === "group") return `<div class="group-title">${esc(row.title)}</div>`;
    selectable++;
    return `<button class="result ${selectable === o.cursor ? "sel" : ""}" data-action="pick" data-index="${selectable}">${row.html}<span class="enter"><kbd>↵</kbd></span></button>`;
  }).join("");
  const head = o.actionsFor
    ? `<span class="back">${icon("chevron-left", "s13")}${esc(o.actionsFor.title)}</span>`
    : o.kind === "review" ? `<span class="back">${icon("chevron-left", "s13")}Ask for a review of ${esc(o.pr.ref)}</span>` : icon("search", "s18");
  const placeholder = o.actionsFor ? "Filter actions…" : o.kind === "review" ? "Filter…" : "Search agents, pull requests and workspaces";
  const foot = o.actionsFor || o.kind === "review"
    ? `<span class="k">Choose <kbd>↵</kbd></span><span class="k">Back <kbd>Esc</kbd></span>`
    : `<span class="k">Open <kbd>↵</kbd></span><span class="k">Actions <kbd>Ctrl K</kbd></span><span class="grow"></span><span class="k">New agent <kbd>Ctrl N</kbd></span>`;
  root.innerHTML = `<div class="popup" role="dialog" aria-label="${o.kind === "review" ? "Ask an agent for a review" : "Command center"}">
    <label class="search">${head}<input id="ck-input" placeholder="${placeholder}" autocomplete="off" value="${esc(o.query ?? "")}" aria-label="${placeholder}"></label>
    <div class="results" role="listbox">${list || `<div class="empty">Nothing matches.</div>`}</div>
    <footer class="popup-foot">${foot}</footer></div>`;
  const input = $("#ck-input");
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
  $(".result.sel")?.scrollIntoView({ block: "nearest" });
}

const words = (text) => String(text).toLowerCase();
const matches = (title, query) => words(query).split(/\s+/).filter(Boolean).every((term) => words(title).includes(term));

function agentRow(agent, detail = "") {
  const workspace = WORKSPACES.find((w) => w.id === agent.workspace)?.title;
  const prs = PULL_REQUESTS.filter((pr) => pr.agent === agent.id).map((pr) => `#${pr.ref.split("#")[1]}`).join(" ");
  return {
    type: "row", kind: "agent", agent,
    html: `${mark(agent.state)}<span class="t">${esc(agent.title)}</span><span class="w">${esc(workspace)}</span>
      <span class="r">${detail ? `<span>${esc(detail)}</span>` : ""}<span class="mono repo">${esc(prs)}</span><span>${agent.state === "working" ? "Working" : agent.state === "needs" ? "Needs you" : ""}</span><span>${ago(agent.at)}</span></span>`,
  };
}

function prRow(pr) {
  const agent = agentById(pr.agent);
  return {
    type: "row", kind: "pr", pr,
    html: `<span style="color:var(--text3)">${icon("git-pull-request", "s13")}</span><span class="ref">${esc(pr.ref)}</span><span class="t">${esc(pr.title)}</span>
      <span class="r"><span>${agent ? esc(agent.title) : "No agent"}</span><span>${esc(pr.age)}</span></span>`,
  };
}

function searchRows(query = "") {
  const rows = [];
  const q = query.trim();
  const number = /^#?(\d+)$/.exec(q)?.[1];
  if (number) {
    const pulls = PULL_REQUESTS.filter((pr) => pr.ref.endsWith(`#${number}`));
    if (pulls.length) rows.push({ type: "group", title: "Pull requests" }, ...pulls.map(prRow));
    const agents = [...new Set(pulls.map((pr) => pr.agent))].map(agentById).filter(Boolean);
    if (agents.length) rows.push({ type: "group", title: "Agents" }, ...agents.map((agent) => agentRow(agent, "Created")));
    return rows;
  }
  if (!q) {
    const needs = [...state.agents.filter((a) => a.state === "needs").map((a) => agentRow(a)), ...PULL_REQUESTS.filter((pr) => pr.group === "needs").map(prRow)];
    if (needs.length) rows.push({ type: "group", title: "Needs you" }, ...needs);
    rows.push({ type: "group", title: "Recent" }, ...[...state.agents].sort((a, b) => b.at - a.at).map((a) => agentRow(a)));
    return rows;
  }
  const agents = state.agents.filter((a) => matches(a.title, q));
  const pulls = PULL_REQUESTS.filter((pr) => matches(`${pr.title} ${pr.ref}`, q));
  const spaces = WORKSPACES.filter((w) => matches(w.title, q));
  if (agents.length) rows.push({ type: "group", title: "Agents" }, ...agents.map((a) => agentRow(a)));
  if (pulls.length) rows.push({ type: "group", title: "Pull requests" }, ...pulls.map(prRow));
  if (spaces.length) {
    rows.push({ type: "group", title: "Workspaces" }, ...spaces.map((w) => ({
      type: "row", kind: "workspace", workspace: w,
      html: `<span style="color:var(--text3)">${icon("layers", "s13")}</span><span class="t">${esc(w.title)}</span><span class="r"><span>${state.agents.filter((a) => a.workspace === w.id).length} agents</span></span>`,
    })));
  }
  return rows;
}

function actionList(agent) {
  return [
    { label: "Open", detail: "Show the tab of the agent", run: () => openAgent(agent.id) },
    { label: "Message…", detail: "Write to the agent", run: () => { openAgent(agent.id); $("#prompt")?.focus(); } },
    { label: "Show changes", detail: "The Git work of the agent, beside its tab", run: () => openAgent(agent.id, "changes") },
    { label: "Fork agent", detail: "A new agent with this conversation", run: () => openOverlay(newAgentForm({ name: `${agent.title} (fork)`, workspace: agent.workspace, fork: agent.title })) },
    { label: "New agent here", detail: "A new agent in the same workspace", run: () => openOverlay(newAgentForm({ workspace: agent.workspace })) },
    { label: "Assign pull request", detail: "Show a pull request as the work of this agent", run: () => toast("Assign pull request", "In the app, this lists the pull requests. The one that you choose shows this agent.") },
    { label: "Open in a terminal", detail: `${agent.harness} restarts in a terminal with the same conversation`, run: () => toast("Open in a terminal", `In the app, ${agent.harness} restarts in a real terminal with the same conversation.`) },
  ];
}

function actionRows(o) {
  return actionList(o.actionsFor).filter((a) => matches(a.label, o.query ?? "")).map((action) => ({
    type: "row", kind: "action", action, html: `<span class="t">${esc(action.label)}</span><span class="r"><span>${esc(action.detail)}</span></span>`,
  }));
}

function reviewRows(o) {
  return [...state.agents].sort((a, b) => b.at - a.at).filter((a) => matches(a.title, o.query ?? "")).map((a) => agentRow(a));
}

function pick(o, row) {
  if (!row) return;
  if (o.kind === "review") {
    const agent = row.agent;
    closeOverlay();
    const text = `Please review the pull request ${o.pr.ref}: ${o.pr.title}\n\nRead the change and look for bugs, design problems and missing tests. Then tell me what you found.`;
    toast(`Asked ${agent.title} for a review`, `${o.pr.ref} · ${o.pr.title}`, "success");
    if (agent.state !== "working") play(agent, demoRun("review"), { prompt: text });
    return;
  }
  if (row.kind === "action") {
    closeOverlay();
    row.action.run();
    return;
  }
  if (row.kind === "agent") { closeOverlay(); openAgent(row.agent.id); return; }
  if (row.kind === "pr") { closeOverlay(); openPullRequest(row.pr); return; }
  if (row.kind === "workspace") {
    o.query = "";
    state.folded.delete(row.workspace.id);
    const first = state.agents.find((a) => a.workspace === row.workspace.id);
    closeOverlay();
    if (first) openAgent(first.id);
  }
}

function overlayKey(event) {
  const o = state.overlay;
  if (!o || o.kind === "new") return false;
  const count = o.rows.length;
  if (event.key === "ArrowDown" || (event.key === "Tab" && !event.shiftKey)) { o.cursor = (o.cursor + 1) % Math.max(1, count); renderOverlay(); return true; }
  if (event.key === "ArrowUp" || (event.key === "Tab" && event.shiftKey)) { o.cursor = (o.cursor - 1 + count) % Math.max(1, count); renderOverlay(); return true; }
  if (event.key === "Enter") { pick(o, o.rows[o.cursor]); return true; }
  if (event.key === "Escape") {
    if (o.actionsFor) { o.actionsFor = null; o.query = ""; o.cursor = 0; renderOverlay(); } else closeOverlay();
    return true;
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k" && !o.actionsFor && o.kind !== "review") {
    const row = o.rows[o.cursor];
    if (row?.kind === "agent") { o.actionsFor = row.agent; o.query = ""; o.cursor = 0; renderOverlay(); }
    return true;
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "n") {
    const row = o.rows[o.cursor];
    openOverlay(newAgentForm({ workspace: row?.agent?.workspace ?? row?.workspace?.id }));
    return true;
  }
  return false;
}

// --- New agent

function newAgentForm(preset = {}) {
  return {
    kind: "new", name: preset.name ?? "", workspace: preset.workspace ?? current().workspace, app: "Pi app",
    files: preset.fork ? "Copy an agent placement" : "New private worktree", repository: "galpon", fork: preset.fork ?? "",
  };
}

const REPOSITORIES = { galpon: "galpon", engines: "engines", docs: "docs" };
const APPS = { "Pi app": "Pi", "Claude app": "Claude Code", "Codex app": "Codex" };

function renderNewAgent(root, o) {
  const seg = (key, options) => `<div class="seg" role="radiogroup">${options.map((option) => `<button type="button" class="${o[key] === option.value ? "on" : ""}" data-action="form-set" data-key="${key}" data-value="${esc(option.value)}" role="radio" aria-checked="${o[key] === option.value}">${esc(option.label)}</button>`).join("")}</div>`;
  const workspace = WORKSPACES.find((w) => w.id === o.workspace)?.title;
  const effects = [
    `Creates the agent <strong>${esc(o.name.trim() || "…")}</strong> in ${esc(workspace)}`,
    o.fork ? `Starts with the conversation of ${esc(o.fork)}` : `Starts a fresh conversation in ${esc(APPS[o.app])}`,
    o.files === "No repository" ? "Gives it an empty directory that Galpon manages"
      : o.files === "Copy an agent placement" ? "Gives it private copies of the worktrees of the source agent"
        : `Makes a private worktree of <strong>${esc(o.repository)}</strong> on a new branch`,
    "Opens it in a tab of the app",
  ];
  root.innerHTML = `<div class="popup narrow" role="dialog" aria-label="New agent">
    <div class="search">${icon("plus", "s18")}<span style="font-size:var(--title);color:var(--text)">${o.fork ? "Fork agent" : "New agent"}</span></div>
    <form class="form" id="new-form" autocomplete="off">
      <div class="field sel"><label for="na-name">Name</label><input id="na-name" value="${esc(o.name)}" placeholder="What the agent works on" required></div>
      <div class="field"><label>Workspace</label>${seg("workspace", WORKSPACES.map((w) => ({ value: w.id, label: w.title })))}</div>
      <div class="field"><label>App</label>${seg("app", ["Pi app", "Claude app", "Codex app"].map((v) => ({ value: v, label: v })))}</div>
      <div class="field"><label>Files</label>${seg("files", ["New private worktree", "No repository", ...(o.fork ? ["Copy an agent placement"] : [])].map((v) => ({ value: v, label: v })))}</div>
      ${o.files === "New private worktree" ? `<div class="field"><label>Repository</label>${seg("repository", Object.keys(REPOSITORIES).map((v) => ({ value: v, label: v })))}</div>` : ""}
      <div class="form-summary">${effects.map((e) => `<div>· ${e}</div>`).join("")}</div>
    </form>
    <footer class="popup-foot"><span class="k">Next field <kbd>Tab</kbd></span><span class="k">Close <kbd>Esc</kbd></span><span class="grow"></span>
      <button class="primary" data-action="create">Create agent</button></footer></div>`;
  const name = $("#na-name");
  name.focus();
  name.setSelectionRange(name.value.length, name.value.length);
}

function createAgent(o) {
  const title = o.name.trim();
  if (!title) {
    $("#na-name")?.focus();
    toast("The agent needs a name", "Write what the agent works on.");
    return;
  }
  const slug = title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 32) || "agent";
  const harness = APPS[o.app];
  const model = { Pi: "GPT-6 Sol", "Claude Code": "Claude Opus 5.5", Codex: "GPT-6 Sol" }[harness];
  const agent = {
    id: `${slug}-${Date.now().toString(36)}`, title, workspace: o.workspace, harness, model, effort: "High", state: "idle", at: Date.now(),
    folder: o.files === "No repository" ? `~/galpon/agents/${slug}` : `~/galpon/agents/${slug}/${o.repository}`, branch: `galpon/${slug}`,
    cost: "$0.00", context: 0, draft: "", transcript: o.fork ? [...(agentById(state.current)?.transcript ?? []).map((item) => ({ ...item }))] : [],
  };
  state.agents.push(agent);
  state.folded.delete(o.workspace);
  closeOverlay();
  openAgent(agent.id);
  $("#prompt")?.focus();
  toast(`Created ${title}`, "Write its task in the composer.", "success");
}

// --- menus

function openMenu(x, y, title, items) {
  state.menu = { items, cursor: items.findIndex((item) => !item.sep) };
  const menu = $("#menu");
  menu.innerHTML = `<div class="menu-title">${esc(title)}</div>${items.map((item, i) => item.sep ? `<div class="menu-sep"></div>`
    : `<button class="menu-item" data-action="menu-pick" data-index="${i}" role="menuitem"><span>${esc(item.label)}</span>${item.hint ? `<span class="h">${esc(item.hint)}</span>` : ""}</button>`).join("")}`;
  menu.hidden = false;
  const box = menu.getBoundingClientRect();
  menu.style.left = `${Math.min(x, window.innerWidth - box.width - 8)}px`;
  menu.style.top = `${Math.min(y, window.innerHeight - box.height - 8)}px`;
  highlightMenu();
}

function highlightMenu() {
  document.querySelectorAll(".menu-item").forEach((el) => el.classList.toggle("sel", Number(el.dataset.index) === state.menu?.cursor));
}

function closeMenu() {
  state.menu = null;
  $("#menu").hidden = true;
}

function agentMenu(agent) {
  return [
    { label: "Message…", run: () => { openAgent(agent.id); $("#prompt")?.focus(); } },
    { label: "Changes", run: () => openAgent(agent.id, "changes") },
    { label: "Terminal", run: () => toast("Terminal", "In the app, a real terminal opens in the worktree of the agent.") },
    { label: "Fork…", run: () => openOverlay(newAgentForm({ name: `${agent.title} (fork)`, workspace: agent.workspace, fork: agent.title })) },
    { label: "Assign pull request…", run: () => toast("Assign pull request", "In the app, this lists the pull requests. The one that you choose shows this agent.") },
    { label: "Open in a terminal", hint: `${agent.harness} restarts in a terminal with the same conversation`, run: () => toast("Open in a terminal", `${agent.harness} restarts in a real terminal with the same conversation.`) },
    { sep: true },
    { label: "Close", hint: agent.state === "working" ? "It is working. Close it when it is idle." : "", off: agent.state === "working", run: () => toast("Close", "Close ends the tab. The agent stays in Galpon, and a message opens it again.") },
  ];
}

function prMenu(pr) {
  const agent = agentById(pr.agent);
  return [
    ...(agent ? [{ label: `Open ${agent.title}`, run: () => openAgent(agent.id) }] : []),
    { label: "Open on GitHub", run: () => toast("Open on GitHub", "In the app, this opens the pull request in your browser. These pull requests are samples.") },
    { label: "Ask an agent for a review…", run: () => openOverlay({ kind: "review", pr, query: "", cursor: 0 }) },
    { sep: true },
    { label: "Refresh pull requests", run: () => toast("Pull requests", "Galpon reads GitHub at most once a minute. These are up to date.") },
  ];
}

// --- navigation

function openAgent(id, then = "") {
  const agent = agentById(id);
  if (!agent) return;
  saveDraft();
  state.current = id;
  agent.fresh = false;
  if (then === "changes") state.changes = true;
  $("#app").classList.remove("side-open");
  refresh(true);
}

function openPullRequest(pr) {
  if (pr.agent) openAgent(pr.agent);
}

function saveDraft() {
  const prompt = $("#prompt");
  if (prompt) current().draft = prompt.value;
}

// refresh draws the sidebar and, as asked, the whole card (true), only the
// conversation and the composer of the tab ("tab"), or nothing more ("side").
function refresh(what = true) {
  renderSidebar();
  if (what === true) renderCard();
  else if (what === "tab") { renderTranscript(); renderComposer(); }
}

function toast(title, body, tone = "") {
  const el = document.createElement("div");
  el.className = `toast ${tone}`;
  el.innerHTML = `<strong>${esc(title)}</strong><span>${esc(body)}</span>`;
  $("#toasts").append(el);
  setTimeout(() => el.remove(), 4200);
}

function nextTurn() {
  const order = { needs: 0, ready: 1 };
  const turns = state.agents.filter((a) => a.state in order && a.id !== state.current).sort((a, b) => order[a.state] - order[b.state] || a.at - b.at);
  if (turns.length) openAgent(turns[0].id);
  else toast("No turn waits for you", "Every agent works or is idle.");
}

// --- events

document.addEventListener("click", (event) => {
  const target = event.target.closest("[data-action]");
  if (!event.target.closest("#menu")) closeMenu();
  if (event.target.id === "overlay") { closeOverlay(); return; }
  if (!target) return;
  const { action, id } = target.dataset;
  const agent = current();
  switch (action) {
    case "open": openAgent(id, target.dataset.then); break;
    case "fold": state.folded.has(id) ? state.folded.delete(id) : state.folded.add(id); renderSidebar(); break;
    case "pr-fold": state.prFolded.has(id) ? state.prFolded.delete(id) : state.prFolded.add(id); renderSidebar(); break;
    case "pr-open": openPullRequest(PULL_REQUESTS.find((pr) => pr.id === id)); break;
    case "terminal": toast("zsh", "A plain terminal of the workspace. In the app, it is a real terminal."); break;
    case "refresh": toast("Pull requests", "Galpon reads GitHub at most once a minute. These are up to date."); break;
    case "search": openOverlay({ kind: "search", query: "", cursor: 0 }); break;
    case "new-agent": openOverlay(newAgentForm()); break;
    case "close-sidebar": $("#app").classList.remove("side-open"); if (wide()) toast("Sidebar", "In the app, Ctrl+Shift+B shows and hides the sidebar."); break;
    case "open-sidebar": $("#app").classList.add("side-open"); break;
    case "panel": saveDraft(); state.panel = state.panel === id ? null : id; renderCard(); break;
    case "panel-tab": saveDraft(); state.panel = id; renderCard(); break;
    case "changes": saveDraft(); state.changes = !state.changes; renderCard(); break;
    case "log": { const item = agent.transcript[Number(target.dataset.index)]; item.open = !item.open; renderTranscript(); break; }
    case "send": saveDraft(); send(agent); break;
    case "stop": stop(agent); break;
    case "answer": answer(agent, Number(target.dataset.index)); break;
    case "copy": copy(target); break;
    case "models": {
      const box = target.getBoundingClientRect();
      openMenu(box.left, box.top - 8 - (MODELS.length * 44 + 40), "Model", MODELS.map((m) => ({ label: m.name, hint: `${m.provider} · ${m.context} context`, run: () => { agent.model = m.name; renderComposer(); } })));
      break;
    }
    case "effort": agent.effort = EFFORTS[(EFFORTS.indexOf(agent.effort) + 1) % EFFORTS.length]; renderComposer(); break;
    case "viewed": {
      const path = target.dataset.path;
      state.viewed.has(path) ? state.viewed.delete(path) : state.viewed.add(path);
      renderCard();
      break;
    }
    case "jump-file": $(`#file-${target.dataset.path.replace(/\W/g, "-")}`)?.scrollIntoView({ block: "start" }); break;
    case "pick": pick(state.overlay, state.overlay.rows[Number(target.dataset.index)]); break;
    case "form-set": {
      const o = state.overlay;
      o.name = $("#na-name")?.value ?? o.name;
      o[target.dataset.key] = target.dataset.value;
      renderOverlay();
      break;
    }
    case "create": event.preventDefault(); state.overlay.name = $("#na-name").value; createAgent(state.overlay); break;
    case "menu-pick": {
      const item = state.menu?.items[Number(target.dataset.index)];
      closeMenu();
      if (item && !item.off) item.run();
      break;
    }
  }
});

document.addEventListener("contextmenu", (event) => {
  const row = event.target.closest("[data-menu]");
  if (!row) return;
  event.preventDefault();
  if (row.dataset.menu === "agent") {
    const agent = agentById(row.dataset.id);
    openMenu(event.clientX, event.clientY, agent.title, agentMenu(agent));
  } else {
    const pr = PULL_REQUESTS.find((p) => p.id === row.dataset.id);
    openMenu(event.clientX, event.clientY, `${pr.ref} · ${pr.title}`, prMenu(pr));
  }
});

document.addEventListener("input", (event) => {
  if (event.target.id === "prompt") {
    current().draft = event.target.value;
    grow(event.target);
    $(".send")?.classList.toggle("ready", Boolean(event.target.value.trim()) && current().state !== "working");
  } else if (event.target.id === "ck-input") {
    state.overlay.query = event.target.value;
    state.overlay.cursor = 0;
    renderOverlay();
  } else if (event.target.id === "na-name") {
    state.overlay.name = event.target.value;
    const summary = $(".form-summary strong");
    if (summary) summary.textContent = event.target.value.trim() || "…";
  }
});

document.addEventListener("submit", (event) => {
  if (event.target.id === "new-form") { event.preventDefault(); state.overlay.name = $("#na-name").value; createAgent(state.overlay); }
});

document.addEventListener("keydown", (event) => {
  const key = event.key.toLowerCase();
  const mod = event.ctrlKey || event.metaKey;
  if (state.menu) {
    const items = state.menu.items;
    const move = (step) => {
      let i = state.menu.cursor;
      do { i = (i + step + items.length) % items.length; } while (items[i].sep);
      state.menu.cursor = i;
      highlightMenu();
    };
    if (event.key === "ArrowDown") { move(1); event.preventDefault(); return; }
    if (event.key === "ArrowUp") { move(-1); event.preventDefault(); return; }
    if (event.key === "Enter") { const item = items[state.menu.cursor]; closeMenu(); if (item && !item.off) item.run(); event.preventDefault(); return; }
    if (event.key === "Escape") { closeMenu(); event.preventDefault(); return; }
  }
  if (state.overlay?.kind === "new") {
    if (event.key === "Escape") { closeOverlay(); event.preventDefault(); }
    return;
  }
  if (state.overlay && overlayKey(event)) { event.preventDefault(); return; }
  if (mod && key === "k") { event.preventDefault(); state.overlay ? closeOverlay() : openOverlay({ kind: "search", query: "", cursor: 0 }); return; }
  if (mod && event.altKey && key === "n") { event.preventDefault(); openOverlay(newAgentForm()); return; }
  if (mod && event.altKey && key === "j") { event.preventDefault(); nextTurn(); return; }
  if (event.altKey && !mod && (key === "c" || event.code === "KeyC")) { event.preventDefault(); saveDraft(); state.changes = !state.changes; renderCard(); return; }
  if (state.overlay) return;
  const agent = current();
  if (event.target.id === "prompt") {
    if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); agent.draft = event.target.value; send(agent); return; }
    if (event.key === "Tab" && event.shiftKey) { event.preventDefault(); agent.effort = EFFORTS[(EFFORTS.indexOf(agent.effort) + 1) % EFFORTS.length]; renderComposer(); return; }
    if (event.key === "Escape" && agent.state === "working") { event.preventDefault(); stop(agent); return; }
    if (agent.question && !event.target.value && (event.key === "1" || event.key === "2")) { event.preventDefault(); answer(agent, Number(event.key) - 1); return; }
  }
  if (event.key === "Escape" && state.changes) { saveDraft(); state.changes = false; renderCard(); }
});

function answer(agent, index) {
  const option = RELEASE_QUESTION.options[index];
  agent.question = false;
  play(agent, {
    steps: [
      { icon: "file-text", text: "Read", detail: "14 pull requests" },
      { icon: "square-pen", text: "Write", detail: "docs/release-0.9.md", added: 46, removed: 0 },
    ],
    answer: `The notes are in \`docs/release-0.9.md\`: ${index === 0 ? "9 user-facing changes in 4 sections" : "all 14 pull requests, grouped by area"}. Open the **Changes** view to read them.\n\nThis is a demo run: no model was called.`,
  }, { prompt: option.label });
}

function copy(button) {
  const text = $(`#${button.dataset.code}`)?.dataset.text ?? "";
  navigator.clipboard?.writeText(text).then(() => {
    button.classList.add("done");
    button.querySelector("span").textContent = "Copied";
    setTimeout(() => { button.classList.remove("done"); button.querySelector("span").textContent = "Copy"; }, 1600);
  }).catch(() => toast("Copy", "The browser did not allow the copy."));
}

window.addEventListener("resize", () => { if (wide()) $("#app").classList.remove("side-open"); });
setInterval(() => renderSidebar(), 30 * 1000);

// --- start

refresh(true);
setTimeout(() => play(agentById("flaky"), FLAKY_RUN), 1200);
