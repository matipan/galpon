import { icons, activityFrames, escapeHTML, visualState, stateMark, sectionTitle, treeGuides, resourceDetail } from "./interface.js?v=work-dock";

import { createWorkDockSample } from "./work-dock-demo.js?v=static";

const workDockFrames = activityFrames;

const state = {
  activeWorkspace: "galpon",
  activeAgent: "agents-control",
  activeTab: "agents-control",
  selectedResult: 0,
  commandMode: "search",
  commandView: "agent",
  detailOnly: false,
  operationsAgent: null,
  commandOrder: [],
  toolsExpanded: false,
  expandedCommandRows: new Set(),
  repositories: [
    { id: "repo-galpon", title: "galpon", branch: "main", remotes: 1 },
    { id: "repo-demo", title: "demo-app", branch: "main", remotes: 1 },
  ],
  workspaces: [
    { id: "galpon", title: "Galpon", branch: "main", repo: "repo-galpon", seen: true },
    { id: "sandbox", title: "Sandbox", branch: "galpon/try-it", repo: "repo-demo", seen: false },
  ],
  agents: [
    {
      id: "agents-control",
      lastUsed: 3,
      title: "Agents under control",
      shortTitle: "Agents under control",
      workspaceId: "galpon",
      role: "guide",
      status: "active",
      seen: true,
      repositoryId: "repo-galpon",
      workDock: createWorkDockSample(),
      messages: [
        {
          role: "user",
          text: "I want several coding agents to work at the same time. How does Galpon keep all of that under control?",
        },
        {
          role: "tool",
          command: "$ galpon snapshot",
          timeout: "(timeout 20s)",
          result: "2 workspaces\n3 agents\n3 managed worktrees",
          time: "Took 0.0s",
        },
        { role: "thinking", text: "Thinking..." },
        {
          role: "assistant",
          html: `
            <p>Galpon gives every piece of work a durable home. You use four simple concepts:</p>
            <ul>
              <li><strong>Workspaces</strong> group your agents and files for one task or project.</li>
              <li><strong>Agents</strong> are durable Pi conversations. Close a tab today and open it tomorrow. The same agent and session continue.</li>
              <li><strong>Repositories</strong> are the Git projects that Galpon can use. Add each project once.</li>
              <li><strong>Worktrees</strong> give each agent its own safe copy of the repository.</li>
            </ul>
            <p>You do not create branches, choose worktree folders, or clean up agent processes. Galpon manages the Git worktrees and the complete agent lifecycle for you.</p>
            <p>Galpon sits on top of Herdr, so agents do not replace your terminal workflow. Select the <code>+</code> tab to open a normal shell beside this agent. The shell opens in this agent's exact placement, so both tabs see the same files.</p>
            <blockquote>You decide what must be done. Galpon keeps every agent, conversation, and checkout in the correct place.</blockquote>
            <p>The Work Dock shows task states and dependencies. <strong>Interface</strong> is working on #2; the keyboard check waits for it.</p>
            <p>Press <code>Ctrl-K</code> to see all of Galpon from one command center.</p>`,
        },
      ],
    },
    {
      id: "command-guide",
      lastUsed: 2,
      title: "Your command center",
      shortTitle: "Your command center",
      workspaceId: "galpon",
      role: "guide",
      status: "idle",
      seen: false,
      repositoryId: "repo-galpon",
      messages: [
        {
          role: "user",
          text: "What can I do from the Galpon command center?",
        },
        {
          role: "tool",
          command: "$ galpon snapshot",
          timeout: "(timeout 20s)",
          result: "workspace   agents   worktrees\nGalpon      2        2\nSandbox     1        1",
          time: "Took 0.0s",
        },
        { role: "thinking", text: "Thinking..." },
        {
          role: "assistant",
          html: `
            <p><code>Ctrl-K</code> opens one fast view of your complete workstation.</p>
            <ul>
              <li>Find and resume any durable agent.</li>
              <li>Create workspaces for new tasks.</li>
              <li>Add a local checkout or a remote Git repository.</li>
              <li>Launch a fresh agent, fork an existing conversation, or reuse a placement.</li>
              <li>Open the correct worktree in your real terminal or editor.</li>
            </ul>
            <p>The command center starts with agents. Press <code>Shift-Tab</code> to change resource views. Search checks titles across all groups. Use the arrow keys to move, <code>Tab</code> to expand, and <code>Enter</code> to open. <code>Ctrl-G</code> shows the full detail view.</p>
            <p>This page is interactive. Press <code>Ctrl-K</code>, add a repository, and create an agent. Nothing leaves your browser.</p>`,
        },
      ],
    },
    {
      id: "sandbox-agent",
      lastUsed: 1,
      title: "Build something",
      shortTitle: "Build something",
      workspaceId: "sandbox",
      role: "implementer",
      status: "idle",
      seen: false,
      repositoryId: "repo-demo",
      messages: [
        { role: "user", text: "Show me what a new Galpon agent feels like." },
        {
          role: "assistant",
          html: `<p>I am a durable Pi agent in the <strong>Sandbox</strong> workspace.</p><p>My conversation and private worktree remain here when you change spaces or close a terminal view. Try the command center to create another agent beside me.</p>`,
        },
      ],
    },
  ],
  terminals: [],
  openTabs: ["agents-control", "command-guide"],
};

const elements = {
  spaces: document.querySelector("#space-list"),
  agents: document.querySelector("#agent-list"),
  tabs: document.querySelector("#tab-bar"),
  terminalPane: document.querySelector("#terminal-pane"),
  conversation: document.querySelector("#conversation"),
  workDock: document.querySelector("#work-dock"),
  composer: document.querySelector("#pi-composer"),
  piStatus: document.querySelector("#pi-status"),
  shell: document.querySelector("#shell-session"),
  prompt: document.querySelector("#pi-prompt"),
  statusPath: document.querySelector("#status-path"),
  statusWorkspace: document.querySelector("#status-workspace"),
  command: document.querySelector("#command-center"),
  commandSearch: document.querySelector("#command-search"),
  commandResults: document.querySelector("#command-results"),
  commandFooter: document.querySelector("#command-footer"),
  commandViews: document.querySelector("#control-views"),
  commandBody: document.querySelector("#control-body"),
  commandDetail: document.querySelector("#control-detail"),
  commandNotice: document.querySelector("#control-notice"),
  resourceCounts: document.querySelector("#resource-counts"),
  resourceForm: document.querySelector("#resource-form"),
  dynamicForm: document.querySelector("#dynamic-form"),
  formTitle: document.querySelector("#form-title"),
  formSubtitle: document.querySelector("#form-subtitle"),
  note: document.querySelector("#demo-note"),
};

function slug(value) {
  return String(value).toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "") || `item-${Date.now()}`;
}

function activeAgent() {
  return state.agents.find((agent) => agent.id === state.activeAgent) || state.agents[0];
}

function activeWorkspace() {
  return state.workspaces.find((workspace) => workspace.id === state.activeWorkspace) || state.workspaces[0];
}

function activeTerminal() {
  return state.terminals.find((terminal) => terminal.id === state.activeTab);
}

function placementPath(agent) {
  const workspace = state.workspaces.find((item) => item.id === agent.workspaceId) || activeWorkspace();
  return `~/.local/state/galpon/worktrees/${workspace.id}-747ca24a/${agent.id}-2fdf63c9/galpon-1a9e2464`;
}

function repositoryFor(agent) {
  return state.repositories.find((repository) => repository.id === agent.repositoryId);
}

function flatDelegations(items, depth = 0) {
  return items.flatMap((item) => [{ item, depth }, ...flatDelegations(item.children || [], depth + 1)]);
}

function readyTodoCount(dock) {
  const byId = new Map(dock.todos.map((todo) => [todo.id, todo]));
  return dock.todos.filter((todo) => todo.status === "pending"
    && !todo.owner
    && !todo.delegationActive
    && (todo.blockedBy || []).every((id) => byId.get(id)?.status === "completed")).length;
}

function renderWorkDock() {
  const agent = activeAgent();
  const dock = agent.workDock;
  if (!dock) {
    elements.workDock.hidden = true;
    elements.workDock.replaceChildren();
    return;
  }
  const scrollTop = elements.workDock.querySelector(".dock-body")?.scrollTop || 0;
  const openBlockers = todo => todo.blockedBy.filter(id => dock.todos.find(item => item.id === id)?.status !== "completed");
  const lines = [`<div class="dock-heading">${sectionTitle("WORK DOCK")}<span>· ${readyTodoCount(dock)} ready</span></div>`];
  if (dock.collapsed) lines.push('<div class="dock-hint">ctrl+space d to expand</div>');
  else {
    const todos = dock.todos.filter(todo => dock.history || todo.status !== "completed");
    lines.push('<div class="dock-body">');
    for (const todo of todos) {
      const blockers = openBlockers(todo);
      const status = todo.status === "pending" && blockers.length ? "blocked" : todo.status;
      const detail = blockers.length ? `blocked by ${blockers.map(id => `#${id}`).join(", ")}` : todo.status === "pending" && !todo.owner ? "ready" : "";
      lines.push(`<div class="dock-line" data-todo-id="${todo.id}" data-status="${status}">${stateMark(status)}<span><span class="sr-only">${escapeHTML(visualState(status).label)}, </span><span class="dock-meta">#${todo.id}</span> ${escapeHTML(todo.subject)}${detail ? ` <span class="dock-meta">· ${escapeHTML(detail)}</span>` : ""}</span></div>`);
    }
    const delegated = flatDelegations(dock.delegations);
    if (todos.length && delegated.length) {
      lines.push(`<h4 class="dock-line dock-delegation-heading"><span aria-hidden="true">${icons.delegation}</span><span>DELEGATED</span></h4>`);
    }
    // Match Galpon's compact formatWorkLine: observed state, then reported progress.
    for (const { item } of delegated) {
      const report = item.checkpoint ? ` ${escapeHTML(item.checkpoint.summary)} (reported)` : "";
      lines.push(`<div class="dock-line dock-delegation" data-work-id="${escapeHTML(item.id)}">${stateMark(item.status, item.lease === "fresh")}<span>${escapeHTML(item.title)} <span class="dock-meta">[${escapeHTML(item.status)} · observed]${report}</span></span></div>`);
    }
    lines.push("</div>");
  }
  elements.workDock.innerHTML = lines.join("");
  elements.workDock.hidden = Boolean(activeTerminal());
  const body = elements.workDock.querySelector(".dock-body");
  if (body) body.scrollTop = scrollTop;
}

function renderSpaces() {
  elements.spaces.replaceChildren(...state.workspaces.map((workspace) => {
    const button = document.createElement("button");
    button.type = "button";
    const unseen = !workspace.seen;
    button.className = `space-button${workspace.id === state.activeWorkspace ? " active" : ""}`;
    button.dataset.workspaceId = workspace.id;
    button.innerHTML = `
      <span class="space-led ${unseen ? "unseen" : "seen"}">${unseen ? "●" : "○"}</span>
      <span class="space-name">${escapeHTML(workspace.title)}</span>
      <span></span>
      <span class="space-branch"><span>${escapeHTML(workspace.branch)}</span>${workspace.id === "galpon" ? '<span class="ahead">↑1</span>' : ""}</span>`;
    button.addEventListener("click", () => selectWorkspace(workspace.id));
    return button;
  }));
}

function renderAgents() {
  const workspaceOrder = new Map(state.workspaces.map((workspace, index) => [workspace.id, index]));
  const groupedAgents = [...state.agents].sort((left, right) =>
    (workspaceOrder.get(left.workspaceId) ?? Number.MAX_SAFE_INTEGER) - (workspaceOrder.get(right.workspaceId) ?? Number.MAX_SAFE_INTEGER));
  elements.agents.replaceChildren(...groupedAgents.map((agent) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `agent-button${agent.id === state.activeAgent ? " active" : ""}`;
    const unseen = !agent.seen;
    button.innerHTML = `
      <span class="agent-state ${unseen ? "unseen" : "seen"}">${unseen ? "●" : "○"}</span>
      <span class="agent-title">${escapeHTML(agent.title)}</span>
      <span class="agent-meta">${escapeHTML(agent.role)} · ${escapeHTML(state.workspaces.find((workspace) => workspace.id === agent.workspaceId)?.title || "Workspace")}</span>`;
    button.addEventListener("click", () => selectAgent(agent.id));
    return button;
  }));
  if (elements.command.open) {
    renderCommandCenter(elements.commandResults.querySelector(".selected")?.dataset.key);
  }
}

function renderTabs() {
  const nodes = state.openTabs.flatMap((id) => {
    const agent = state.agents.find((item) => item.id === id);
    const terminal = state.terminals.find((item) => item.id === id);
    const tab = agent || terminal;
    if (!tab || tab.workspaceId !== state.activeWorkspace) return [];
    const button = document.createElement("button");
    button.type = "button";
    button.className = `tab-button${terminal ? " terminal-tab" : ""}${id === state.activeTab ? " active" : ""}`;
    button.textContent = terminal ? `$ ${terminal.title}` : agent.shortTitle;
    button.title = terminal ? `Terminal · ${terminal.placementLabel}` : agent.title;
    button.addEventListener("click", () => terminal ? selectTerminal(terminal.id) : selectAgent(agent.id));
    return [button];
  });
  const add = document.createElement("button");
  add.type = "button";
  add.className = "tab-button new-tab";
  add.textContent = "+";
  add.setAttribute("aria-label", "Open a terminal beside the current tab");
  add.title = "Open a normal Herdr terminal in this placement";
  add.addEventListener("click", openTerminalTab);
  nodes.push(add);
  elements.tabs.replaceChildren(...nodes);
}

function renderShell(terminal) {
  elements.shell.innerHTML = `
    <header class="shell-context">
      <strong>HERDR TERMINAL</strong>
      <span>${escapeHTML(terminal.placementLabel)}</span>
    </header>
    <div class="shell-history">
      <div><span class="shell-prompt">❯</span> pwd</div>
      <div class="shell-output">${escapeHTML(terminal.path)}</div>
      <div><span class="shell-prompt">❯</span> git status --short --branch</div>
      <div class="shell-output">## ${escapeHTML(terminal.branch)}</div>
      <div><span class="shell-prompt">❯</span> <span class="cursor-block"></span></div>
    </div>
    <p class="shell-explainer"><strong>This is a normal Herdr shell, not an agent.</strong> It is beside ${escapeHTML(terminal.agentTitle)} and uses the same placement. Changes from either tab are immediately visible in the other.</p>`;
}

let renderedAgentId;

function renderConversation(scrollToEnd = false) {
  const wasAtEnd = elements.conversation.scrollHeight - elements.conversation.scrollTop - elements.conversation.clientHeight < 40;
  const previousScroll = elements.conversation.scrollTop;
  const terminal = activeTerminal();
  const terminalActive = Boolean(terminal);
  elements.terminalPane.classList.toggle("shell-active", terminalActive);
  elements.conversation.hidden = terminalActive;
  elements.workDock.hidden = terminalActive;
  elements.composer.hidden = terminalActive;
  elements.piStatus.hidden = terminalActive;
  elements.shell.hidden = !terminalActive;
  if (terminal) {
    renderShell(terminal);
    return;
  }

  const agent = activeAgent();
  const workspace = activeWorkspace();
  if (!state.agents.some(item => item.workspaceId === workspace.id)) {
    elements.conversation.innerHTML = `<header class="session-heading"><strong>${icons.brand} GALPON</strong><span>${escapeHTML(workspace.title)}</span></header><p>No agents in this workspace. Press Ctrl-N to create a sample agent.</p>`;
    elements.workDock.hidden = true;
    elements.composer.hidden = true;
    elements.piStatus.hidden = true;
    return;
  }
  const repository = repositoryFor(agent);
  const fragment = document.createDocumentFragment();
  const changedAgent = renderedAgentId !== agent.id;
  if (changedAgent) elements.prompt.value = agent.draft || "";
  renderedAgentId = agent.id;
  const heading = document.createElement("header");
  heading.className = "session-heading";
  heading.innerHTML = `<strong>${icons.brand} GALPON</strong><span>${escapeHTML(agent.title)} / ${escapeHTML(workspace.title)}</span>`;
  fragment.append(heading);

  for (const message of agent.messages) {
    if (message.role === "tool") {
      const tool = document.createElement("section");
      tool.className = `message tool-block${message.failed ? " failed" : ""}`;
      const all = message.result.split("\n");
      const visible = state.toolsExpanded ? all : all.slice(-5);
      tool.innerHTML = `<div class="tool-command">${escapeHTML(message.command)}${message.timeout ? ` <span class="tool-timeout">${escapeHTML(message.timeout)}</span>` : ""}</div>${visible.length < all.length ? `<div class="tool-collapsed">... (${all.length - visible.length} earlier lines, ctrl+o to expand)</div>` : ""}<div class="tool-result">${visible.map(line => `<div${/^(fatal:|error:)/i.test(line) ? ' class="tool-error"' : ""}>${escapeHTML(line)}</div>`).join("")}</div>${message.time ? `<div class="tool-time">${escapeHTML(message.time)}</div>` : ""}<div class="tool-status" aria-label="${message.failed ? "Tool failed" : "Tool completed"}">${stateMark(message.failed ? "failed" : "completed")}${message.failed ? " failed" : ""}</div>`;
      fragment.append(tool);
      continue;
    }
    if (message.role === "thinking") {
      const thinking = document.createElement("div");
      thinking.className = "message thinking";
      thinking.textContent = message.text;
      fragment.append(thinking);
      continue;
    }
    const article = document.createElement("article");
    article.className = `message ${message.role}-message`;
    if (message.role === "user") article.textContent = message.text;
    else article.innerHTML = message.html;
    fragment.append(article);
  }

  if (agent.status === "working") {
    const thinking = document.createElement("div");
    thinking.className = "message thinking";
    thinking.innerHTML = `${stateMark("working")} Working in ${escapeHTML(repository?.title || "managed directory")}`;
    fragment.append(thinking);
  }
  elements.conversation.replaceChildren(fragment);
  elements.statusPath.innerHTML = `<span class="stat-label">MODEL</span> gpt-5.6-sol &nbsp;<span aria-label="Reasoning level">${icons.reasoning} high</span>${repository ? ` &nbsp;<span class="stat-label">REF</span> ${escapeHTML(agent.ref || workspace.branch)}` : ""}`;
  elements.statusWorkspace.textContent = "BROWSER DEMO · sample data · no model or file access · ctrl+k control";
  renderWorkDock();
  requestAnimationFrame(() => { elements.conversation.scrollTop = scrollToEnd || wasAtEnd || changedAgent ? elements.conversation.scrollHeight : previousScroll; });
}

function render() {
  renderSpaces();
  renderAgents();
  renderTabs();
  renderConversation();
}

function saveDraft() {
  if (!activeTerminal()) activeAgent().draft = elements.prompt.value;
}

function selectWorkspace(workspaceId) {
  saveDraft();
  state.activeWorkspace = workspaceId;
  const selectedWorkspace = state.workspaces.find((workspace) => workspace.id === workspaceId);
  if (selectedWorkspace) selectedWorkspace.seen = true;
  const agent = state.agents.find((item) => item.workspaceId === workspaceId);
  if (agent) {
    state.activeAgent = agent.id;
    state.activeTab = agent.id;
    agent.lastUsed = Date.now();
    agent.seen = true;
    if (!state.openTabs.includes(agent.id)) state.openTabs.push(agent.id);
  } else {
    state.activeTab = "";
  }
  render();
}

function selectAgent(agentId) {
  saveDraft();
  const agent = state.agents.find((item) => item.id === agentId);
  if (!agent) return;
  state.activeAgent = agent.id;
  state.activeTab = agent.id;
  state.activeWorkspace = agent.workspaceId;
  if (["idle", "active"].includes(agent.status)) agent.status = "active";
  agent.lastUsed = Date.now();
  agent.seen = true;
  const workspace = state.workspaces.find((item) => item.id === agent.workspaceId);
  if (workspace) workspace.seen = true;
  if (!state.openTabs.includes(agent.id)) state.openTabs.push(agent.id);
  render();
}

function selectTerminal(terminalId) {
  saveDraft();
  const terminal = state.terminals.find((item) => item.id === terminalId);
  if (!terminal) return;
  state.activeTab = terminal.id;
  state.activeWorkspace = terminal.workspaceId;
  if (terminal.agentId) state.activeAgent = terminal.agentId;
  render();
}

function openTerminalTab(agentId = state.activeAgent) {
  saveDraft();
  const sourceAgent = state.agents.find((agent) => agent.id === agentId) || activeAgent();
  if (sourceAgent.workspaceId !== state.activeWorkspace) return showNote("Create an agent in this workspace first.", true);
  const workspace = state.workspaces.find((item) => item.id === sourceAgent.workspaceId) || activeWorkspace();
  const number = state.terminals.length + 1;
  const terminal = {
    id: `terminal-${number}`,
    title: number === 1 ? "zsh" : `zsh ${number}`,
    workspaceId: workspace.id,
    agentId: sourceAgent.id,
    agentTitle: sourceAgent.title,
    placementLabel: `${sourceAgent.title} · exact agent placement`,
    path: placementPath(sourceAgent),
    branch: workspace.branch,
  };
  state.terminals.push(terminal);
  state.openTabs.push(terminal.id);
  state.activeWorkspace = workspace.id;
  state.activeAgent = sourceAgent.id;
  state.activeTab = terminal.id;
  render();
  showNote(`Opened a normal Herdr terminal in ${sourceAgent.title}'s exact placement.`);
}

function worktrees() {
  return state.agents.filter(agent => !["directory", "external"].includes(agent.placement) && agent.repositoryId).map((agent) => {
    const workspace = state.workspaces.find((item) => item.id === agent.workspaceId);
    const repository = repositoryFor(agent);
    return {
      id: `worktree-${agent.id}`,
      title: `${workspace?.title || "Workspace"} · ${repository?.title || "managed directory"} · ${agent.title}`,
      detail: repository ? "" : "managed directory",
      workspaceId: agent.workspaceId,
      agentId: agent.id,
    };
  });
}

const resourceViews = [["agent", "Agents"], ["workspace", "Workspaces"], ["worktree", "Worktrees"], ["repository", "Repositories"]];

function changeView() {
  const index = resourceViews.findIndex(([type]) => type === state.commandView);
  state.commandView = resourceViews[(index + 1) % resourceViews.length][0];
  elements.commandSearch.value = "";
  state.selectedResult = 0;
  state.detailOnly = false;
  renderCommandCenter();
}

function displayedAgentStatus(agent) {
  return ["working", "failed", "running", "starting"].includes(agent.status)
    ? agent.status : agent.workDock?.coordinatorStatus || agent.status;
}

function rankAgents() {
  const priority = agent => displayedAgentStatus(agent) === "failed" ? 0 : ["working", "running", "starting"].includes(displayedAgentStatus(agent)) ? 1 : 2;
  state.commandOrder = [...state.agents].sort((a, b) => priority(a) - priority(b) || (b.lastUsed || 0) - (a.lastUsed || 0)).map(agent => agent.id);
}

function recentCommandAgents() {
  return [...state.agents].sort((left, right) => (right.lastUsed || 0) - (left.lastUsed || 0)
    || left.title.localeCompare(right.title) || left.id.localeCompare(right.id));
}

function commandAgent(agent, workspaceParent = "") {
  const observed = displayedAgentStatus(agent);
  const status = ["working", "starting", "running"].includes(observed) ? "working"
    : observed === "failed" ? "failed"
    : state.openTabs.includes(agent.id) || agent.status === "active" ? "active"
    : agent.status === "changed" ? "changed" : "idle";
  const key = `agent:${agent.id}:${workspaceParent}`;
  return {
    id: agent.id, key, type: "agent", title: agent.title, role: agent.role,
    detail: status === "active" ? "idle" : status, status: status === "active" ? "idle" : status,
    agent, age: agent.lastUsed > 100 ? "now" : `${4 - agent.lastUsed}m`,
    workspaceId: agent.workspaceId,
    workspaceTitle: state.workspaces.find((item) => item.id === agent.workspaceId)?.title || "Workspace",
    workspaceParent, depth: workspaceParent ? 1 : 0,
    delegatedCount: workspaceParent ? 0 : agent.workDock?.delegations.length || 0,
    expanded: state.expandedCommandRows.has(key),
  };
}

// These are the existing Work Dock records, not new agents or live requests.
function commandDelegations(agent, items = agent.workDock?.delegations || [], depth = 1) {
  return items.flatMap((item) => {
    const key = `delegation:${agent.id}:${item.id}`;
    const status = item.status;
    const result = {
      id: item.id, key, type: "delegation", title: item.title, detail: item.status,
      status, work: item, agent,
      workspaceId: agent.workspaceId, workspaceTitle: state.workspaces.find((workspace) => workspace.id === agent.workspaceId)?.title,
      depth, delegatedCount: item.children?.length || 0, expanded: state.expandedCommandRows.has(key),
    };
    return [result, ...(result.expanded ? commandDelegations(agent, item.children || [], depth + 1) : [])];
  });
}

function commandGroups() {
  return [
    { name: "AGENTS", type: "agent", items: [...state.agents].sort((a, b) => {
      const order = id => { const index = state.commandOrder.indexOf(id); return index < 0 ? state.commandOrder.length : index; };
      return order(a.id) - order(b.id);
    }).map(agent => commandAgent(agent)) },
    {
      name: "WORKSPACES", type: "workspace",
      items: state.workspaces.map((workspace) => {
        const count = state.agents.filter((agent) => agent.workspaceId === workspace.id).length;
        const expanded = state.expandedCommandRows.has(`workspace:${workspace.id}`);
        return {
          id: workspace.id, title: workspace.title, workspaceId: workspace.id, expanded,
          count, detail: `${count} ${count === 1 ? "agent" : "agents"}`, workspace,
        };
      }),
    },
    { name: "WORKTREES", type: "worktree", items: worktrees() },
    {
      name: "REPOSITORIES", type: "repository",
      items: state.repositories.map((repository) => ({
        id: repository.id,
        repository, title: repository.title,
        detail: `${repository.branch} · ${repository.remotes} ${repository.remotes === 1 ? "remote" : "remotes"}`,
      })),
    },
  ];
}

function filteredGroups() {
  const query = elements.commandSearch.value.trim().toLowerCase();
  if (state.operationsAgent) {
    const agent = state.agents.find(item => item.id === state.operationsAgent);
    return [{ name: "AGENT WORK", type: "delegation", items: flatDelegations(agent.workDock?.delegations || []).map(({ item, depth }) => ({
      id: item.id, key: `operation:${item.id}`, type: "delegation", title: item.title, status: item.status, detail: item.status, work: item, agent, depth,
    })) }];
  }
  return commandGroups().filter(group => query || group.type === state.commandView).map((group) => ({
    ...group,
    items: group.items.filter((item) => !query || item.title.toLowerCase().includes(query)).flatMap((item) => {
      const result = { type: group.type, key: `${group.type}:${item.id}`, ...item };
      if (!result.expanded) return [result];
      if (result.type === "workspace") {
        return [result, ...recentCommandAgents().filter((agent) => agent.workspaceId === item.id).map((agent) => commandAgent(agent, item.id))];
      }
      if (result.type === "agent") {
        return [result, ...commandDelegations(state.agents.find((agent) => agent.id === result.id))];
      }
      return [result];
    }),
  })).filter((group) => group.items.length);
}

function flatResults() {
  return filteredGroups().flatMap((group) => group.items);
}

function toggleCommandExpansion() {
  const selected = selectedCommandResult();
  if (!selected || (selected.type !== "workspace" && !selected.delegatedCount)) return;
  if (state.expandedCommandRows.has(selected.key)) state.expandedCommandRows.delete(selected.key);
  else state.expandedCommandRows.add(selected.key);
  renderCommandCenter(selected.key);
}

function commandTitle(title) {
  const query = elements.commandSearch.value.trim().toLowerCase();
  const at = query ? title.toLowerCase().indexOf(query) : -1;
  if (at < 0) return escapeHTML(title);
  return `${escapeHTML(title.slice(0, at))}<mark>${escapeHTML(title.slice(at, at + query.length))}</mark>${escapeHTML(title.slice(at + query.length))}`;
}

function commandFooterHints(columns) {
  if (state.operationsAgent) return [["↑↓", "select", ""], ["ctrl+g", "detail", "detail"], ["esc", "back", "close"]];
  if (state.commandMode === "actions") return [
    ["a", "agent", "agent"], ["r", "repository", "repo"], ["w", "workspace", "space"],
    ["o", "operations", "operations"], ["t", "terminal", "terminal"], ["d", "dock", "dock"],
    ["ctrl+space", "search", "mode"], ["esc", "close", "close"],
  ];
  const hints = [["enter", "open", "open"], ["esc", "close", "close"], ["ctrl+g", "detail", "detail"]];
  if (columns >= 65) hints.push(["ctrl+n", "new agent", "agent"]);
  if (columns >= 85) hints.push(["shift+tab", "views", "views"]);
  if (columns >= 110) hints.push(["ctrl+space", "actions", "mode"]);
  return hints;
}

const commandFontMeasure = document.createElement("canvas").getContext("2d");
function renderCommandFooter() {
  commandFontMeasure.font = getComputedStyle(elements.commandFooter).font;
  const columnWidth = commandFontMeasure.measureText("0").width;
  const columns = Math.floor((elements.commandFooter.clientWidth || window.innerWidth * .88 - 20) / columnWidth);
  const hints = commandFooterHints(columns);
  elements.commandFooter.innerHTML = hints.map(([key, label, action]) => `<button type="button" class="footer-action" data-action="${action}"><kbd>${key}</kbd>${label ? ` ${label}` : ""}</button>`).join("");
}

function renderCommandCenter(selectedKey) {
  const groups = filteredGroups();
  const flat = groups.flatMap(group => group.items);
  const index = selectedKey ? flat.findIndex(item => item.key === selectedKey) : -1;
  if (index >= 0) state.selectedResult = index;
  state.selectedResult = Math.max(0, Math.min(state.selectedResult, flat.length - 1));
  const searching = Boolean(elements.commandSearch.value.trim());
  elements.commandViews.innerHTML = resourceViews.map(([type, title]) => `<span class="control-view${type === state.commandView && !searching ? " active" : ""}"><span aria-hidden="true">${icons[type]}</span> ${title}</span>`).join("");
  elements.commandViews.hidden = Boolean(state.operationsAgent);
  elements.commandViews.querySelector(".active")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  elements.commandSearch.parentElement.hidden = Boolean(state.operationsAgent);
  document.querySelector("#command-title").textContent = state.operationsAgent ? "OPERATIONS" : "CONTROL";
  elements.command.classList.toggle("operations-view", Boolean(state.operationsAgent));
  elements.commandBody.classList.toggle("detail-only", state.detailOnly);
  let cursor = 0;
  const guides = treeGuides(flat);
  const agentColumns = !searching && state.commandView === "agent" && !state.operationsAgent;
  elements.commandResults.classList.toggle("agent-columns", agentColumns);
  const nodes = [];
  if (agentColumns) {
    const columns = document.createElement("div");
    columns.className = "result-columns";
    columns.innerHTML = '<span>NAME</span><span>WORKSPACE</span><span>STATE</span><span>AGE</span>';
    nodes.push(columns);
  }
  for (const group of groups) {
    const section = document.createElement("section");
    section.className = "result-group";
    section.dataset.type = group.type;
    if (searching || state.operationsAgent) section.innerHTML = sectionTitle(group.name);
    for (const item of group.items) {
      const rowIndex = cursor++;
      const selected = rowIndex === state.selectedResult;
      const row = document.createElement("div");
      row.id = `resource-row-${rowIndex}`;
      row.className = `result-row${selected ? " selected" : ""}`;
      Object.assign(row.dataset, { index: rowIndex, key: item.key, id: item.id, type: item.type });
      if (item.workspaceParent) row.dataset.workspaceParent = item.workspaceParent;
      row.setAttribute("role", "option");
      row.setAttribute("aria-selected", String(selected));
      if (item.type === "workspace" || item.delegatedCount) row.setAttribute("aria-expanded", String(item.expanded));
      const disclosure = item.type === "workspace" ? `<span class="row-disclosure" aria-hidden="true">${item.expanded ? icons.expanded : icons.collapsed}</span>` : "";
      row.innerHTML = `<span class="row-prefix" aria-hidden="true">${selected ? icons.focus : ""}</span>
        <span class="row-leading"><span class="tree-guide" aria-hidden="true">${guides[rowIndex]}</span>${disclosure}<span class="row-title">${commandTitle(item.title)}</span>
        ${item.delegatedCount ? `<span class="row-delegated" aria-label="${item.delegatedCount} delegated agents">${icons.delegation} ${item.delegatedCount}</span>` : ""}</span>
        ${agentColumns ? `<span class="row-workspace">${item.workspaceParent ? "" : escapeHTML(item.workspaceTitle || "")}</span>` : ""}
        <span class="row-detail">${item.status ? `${stateMark(item.status, item.work ? item.work.lease === "fresh" : true)} ${escapeHTML(visualState(item.status).label)}` : escapeHTML(item.detail || "")}</span>
        ${agentColumns ? `<span class="row-age">${escapeHTML(item.age || "")}</span>` : ""}`;
      section.append(row);
    }
    nodes.push(section);
  }
  if (!flat.length) {
    const empty = document.createElement("p");
    empty.className = "form-copy";
    empty.textContent = searching ? `No title matches “${elements.commandSearch.value}”` : "No resources in this view.";
    nodes.push(empty);
  }
  const scroll = elements.commandResults.scrollTop;
  elements.commandResults.replaceChildren(...nodes);
  elements.commandResults.scrollTop = scroll;
  elements.commandSearch.setAttribute("aria-controls", "command-results");
  if (flat.length) elements.commandSearch.setAttribute("aria-activedescendant", `resource-row-${state.selectedResult}`);
  else elements.commandSearch.removeAttribute("aria-activedescendant");
  elements.commandDetail.innerHTML = resourceDetail(flat[state.selectedResult], state);
  elements.resourceCounts.textContent = `${flat.length ? state.selectedResult + 1 : 0} / ${flat.length}`;
  elements.commandNotice.textContent = state.operationsAgent ? "Observed and reported sample facts · no live server" : state.commandMode === "actions" ? "Actions · a agent · r repository · w workspace · R remote · o operations · d dock" : "Tab expand · shift+tab views · ctrl+space actions · ctrl+r reorder";
  renderCommandFooter();
}

function openCommandCenter() {
  closeDialog(elements.resourceForm);
  state.selectedResult = 0;
  state.commandMode = "search";
  state.expandedCommandRows.clear();
  state.operationsAgent = null;
  state.detailOnly = false;
  state.commandView = "agent";
  rankAgents();
  elements.commandSearch.value = "";
  renderCommandCenter();
  if (!elements.command.open) elements.command.showModal();
  requestAnimationFrame(() => elements.commandSearch.focus());
}

function closeDialog(dialog) {
  if (dialog.open) dialog.close();
}

function openResult(item) {
  switch (item.type) {
    case "delegation":
      showNote(`${item.title} is a browser-only delegation record. No live agent is opened.`);
      break;
    case "agent":
      closeDialog(elements.command);
      selectAgent(item.id);
      break;
    case "workspace":
      closeDialog(elements.command);
      selectWorkspace(item.id);
      break;
    case "worktree":
      closeDialog(elements.command);
      if (item.agentId) selectAgent(item.agentId);
      showNote("Opened the managed worktree in its existing agent tab.");
      break;
    case "repository":
      openAgentForm(state.activeWorkspace, item.id);
      break;
  }
}

function moveSelection(delta) {
  const count = flatResults().length;
  if (!count) return;
  state.selectedResult = (state.selectedResult + delta + count) % count;
  renderCommandCenter();
  elements.commandResults.querySelector(".selected")?.scrollIntoView({ block: "nearest" });
}

function selectedCommandResult() {
  return flatResults()[state.selectedResult];
}

function toggleCommandMode() {
  state.commandMode = state.commandMode === "search" ? "actions" : "search";
  renderCommandCenter();
  if (state.commandMode === "search") elements.commandSearch.focus();
  else elements.command.focus();
}

function showSelectedAction(action) {
  const selected = selectedCommandResult();
  if (!selected) return;
  const title = selected.title;
  if (action === "terminal") {
    const agentId = selected.type === "agent" ? selected.id : selected.agentId;
    if (!agentId) {
      showNote("Select an agent or managed worktree to open its terminal.", true);
      return;
    }
    closeDialog(elements.command);
    openTerminalTab(agentId);
  } else if (action === "editor") showNote(`Local Galpon would open ${title} in your configured editor.`, true);
  else if (action === "operations") {
    if (selected.type !== "agent") return showNote("Select an agent to open Operations.", true);
    state.operationsAgent = selected.id;
    state.selectedResult = 0;
    state.detailOnly = false;
    renderCommandCenter();
    elements.command.focus();
  }
  else if (action === "hide") showNote(`${title} remains visible because this browser demo does not change durable state.`, true);
}

function formFooter(primaryLabel, title) {
  if (title === "New agent") {
    return `<footer class="form-actions">
      <span><kbd>tab</kbd> list / next</span>
      <span><kbd>← →</kbd> change</span>
      <button class="terminal-action" type="submit"><kbd>ctrl+s</kbd> ${escapeHTML(primaryLabel)}</button>
      <button class="terminal-action cancel-form" type="button"><kbd>esc</kbd> close</button>
    </footer>`;
  }
  return `<footer class="form-actions">
    <button class="terminal-action" type="submit"><kbd>ctrl+s</kbd> ${escapeHTML(primaryLabel)}</button>
    <button class="terminal-action cancel-form" type="button"><kbd>esc</kbd> cancel</button>
  </footer>`;
}

function showForm({ title, subtitle, copy = "", fields = "", sections = "", submitLabel, onSubmit }) {
  closeDialog(elements.command);
  elements.formTitle.textContent = title;
  elements.formSubtitle.textContent = subtitle;
  const defaultSection = title === "New workspace" ? "WORKSPACE" : title === "Add remote" ? "REMOTE" : "REPOSITORY";
  elements.dynamicForm.innerHTML = `
    <div class="form-body">
      <div class="form-fields">
        ${sections || `<section class="form-section"><h3 class="form-section-title">${defaultSection}</h3>${fields}</section>`}
        ${title === "New agent" ? "" : `<button class="form-row form-start static-row" type="submit"><span class="field-label">${submitLabel.startsWith("create") ? "Create" : `${icons.add} Add`}</span><span class="field-value">${escapeHTML(submitLabel.replace(/^(add|create)\s+/, ""))}</span></button>`}
        <div class="form-error" role="alert" hidden></div>
      </div>
      <aside class="form-summary" aria-label="Creation effects">${sectionTitle("ON START")}<div id="form-effects">${copy ? `<p>${copy}</p>` : ""}</div><p class="detail-disclaimer">Browser demo · no files, processes, or repositories are changed. Refresh clears all demo changes.</p></aside>
    </div>
    ${formFooter(submitLabel, title)}`;
  elements.dynamicForm.noValidate = true;
  elements.dynamicForm.onsubmit = (event) => {
    event.preventDefault();
    const data = new FormData(elements.dynamicForm);
    const error = elements.dynamicForm.querySelector(".form-error");
    try {
      onSubmit(data);
      closeDialog(elements.resourceForm);
    } catch (cause) {
      error.hidden = false;
      error.textContent = cause.message;
      const field = [...elements.dynamicForm.querySelectorAll("input[required]")].find(input => !input.disabled && !input.value.trim())
        || (cause.message.includes("directory") ? document.getElementById("external-path") : null);
      if (field) {
        field.setAttribute("aria-invalid", "true");
        field.closest(".form-row").after(error);
        field.focus();
      }
    }
  };
  elements.dynamicForm.querySelector(".cancel-form").addEventListener("click", () => closeDialog(elements.resourceForm));
  elements.dynamicForm.onkeydown = event => {
    if (event.key === "Enter" && event.target.matches("input, select") && !event.ctrlKey && !event.metaKey) {
      event.preventDefault();
      const fields = [...elements.dynamicForm.querySelectorAll("input, select, button.form-start")].filter(field => !field.disabled && field.getClientRects().length);
      fields[(fields.indexOf(event.target) + 1) % fields.length]?.focus();
    }
    if (["ArrowLeft", "ArrowRight"].includes(event.key) && event.target.matches("select")) {
      event.preventDefault();
      const field = event.target;
      field.selectedIndex = (field.selectedIndex + (event.key === "ArrowRight" ? 1 : -1) + field.options.length) % field.options.length;
      field.dispatchEvent(new Event("change", { bubbles: true }));
    }
  };
  elements.dynamicForm.oninput = event => {
    if (event.target.hasAttribute("aria-invalid")) {
      event.target.removeAttribute("aria-invalid");
      elements.dynamicForm.querySelector(".form-error").hidden = true;
    }
  };
  elements.dynamicForm.onchange = title === "New agent" ? updateAgentEffects : null;
  if (title === "New agent") {
    let nextSecondary = 0;
    document.getElementById("add-secondary").addEventListener("click", () => {
      const container = document.getElementById("secondary-repositories");
      const used = new Set([elements.dynamicForm.elements.repository.value, ...new FormData(elements.dynamicForm).getAll("secondary")]);
      const choices = state.repositories.filter(repository => !used.has(repository.id));
      if (!choices.length) return showNote("No other repositories are available. Add one from Control.", true);
      const id = `secondary-${++nextSecondary}`;
      const group = document.createElement("div");
      group.innerHTML = `<div class="form-row"><label for="${id}">Secondary repo</label><select id="${id}" name="secondary">${optionList(choices, choices[0].id)}</select></div><button class="form-row form-start static-row" type="button"><span class="field-label"></span><span class="field-value">Remove secondary repository</span></button>`;
      group.querySelector("button").addEventListener("click", () => { group.remove(); document.getElementById("add-secondary").focus(); });
      container.append(group);
      group.querySelector("select").focus();
    });
    updateAgentEffects();
  }
  if (!elements.resourceForm.open) elements.resourceForm.showModal();
  requestAnimationFrame(() => elements.dynamicForm.querySelector("input, select")?.focus());
}

function openRepositoryForm() {
  showForm({
    title: "Add repository",
    subtitle: "source repository",
    copy: "Use a local path or a Git SSH/HTTPS URL. In local Galpon, branches are fetched into a private bare repository. This demo keeps the repository only in this browser session.",
    submitLabel: "add repository",
    fields: `
      <div class="form-row"><label for="repository-source">Path or Git URL</label><input id="repository-source" name="source" required placeholder="git@github.com:you/project.git"></div>
      <div class="form-row"><label for="repository-title">Title</label><input id="repository-title" name="title" placeholder="project"><small>Optional. Galpon normally detects this from the source.</small></div>`,
    onSubmit(data) {
      const source = String(data.get("source") || "").trim();
      if (!source) throw new Error("A local path or Git URL is required.");
      const inferred = source.split(/[/:]/).pop()?.replace(/\.git$/, "") || "repository";
      const title = String(data.get("title") || "").trim() || inferred;
      let id = `repo-${slug(title)}`;
      if (state.repositories.some((repository) => repository.id === id)) id += `-${state.repositories.length + 1}`;
      state.repositories.push({ id, title, branch: "main", remotes: 1 });
      showNote(`Repository ${title} is ready. Create an agent from the command center.`);
      render();
      setTimeout(openCommandCenter, 350);
    },
  });
}

function openRemoteForm() {
  const selected = selectedCommandResult();
  const repository = selected?.type === "repository"
    ? state.repositories.find((item) => item.id === selected.id)
    : state.repositories[0];
  if (!repository) return openRepositoryForm();
  showForm({
    title: "Add remote",
    subtitle: "repository settings",
    copy: `Add a named Git remote to ${escapeHTML(repository.title)}. This demo updates only the on-screen remote count.`,
    submitLabel: "add remote",
    fields: `
      <div class="form-row"><label for="remote-name">Name</label><input id="remote-name" name="name" required placeholder="upstream"></div>
      <div class="form-row"><label for="remote-url">Fetch URL</label><input id="remote-url" name="url" required placeholder="git@github.com:org/project.git"></div>`,
    onSubmit(data) {
      const name = String(data.get("name") || "").trim();
      const url = String(data.get("url") || "").trim();
      if (!name || !url) throw new Error("A remote name and URL are required.");
      repository.remotes += 1;
      showNote(`Remote ${name} added to ${repository.title}.`);
      setTimeout(openCommandCenter, 350);
    },
  });
}

function openWorkspaceForm() {
  showForm({
    title: "New workspace",
    subtitle: "durable work group",
    copy: "A workspace groups related human work, agent work, and managed worktrees.",
    submitLabel: "create workspace",
    fields: `<div class="form-row"><label for="workspace-title">Workspace title</label><input id="workspace-title" name="title" required placeholder="Launch the new site"></div>`,
    onSubmit(data) {
      const title = String(data.get("title") || "").trim();
      if (!title) throw new Error("A workspace title is required.");
      let id = slug(title);
      if (state.workspaces.some((workspace) => workspace.id === id)) id += `-${state.workspaces.length + 1}`;
      state.workspaces.push({ id, title, branch: `galpon/${id}`, repo: state.repositories[0]?.id, seen: true });
      state.activeWorkspace = id;
      showNote(`Workspace ${title} created.`);
      render();
      setTimeout(openCommandCenter, 350);
    },
  });
}

function optionList(items, selected) {
  return items.map((item) => `<option value="${escapeHTML(item.id)}"${item.id === selected ? " selected" : ""}>${escapeHTML(item.title)}</option>`).join("");
}

function openAgentForm(workspaceId = state.activeWorkspace, repositoryId = "") {
  if (!state.workspaces.length) return openWorkspaceForm();
  showForm({
    title: "New agent",
    subtitle: "workspace placement",
    submitLabel: "start",
    sections: `
      <section class="form-section">
        <h3 class="form-section-title">IDENTITY</h3>
        <div class="form-row"><label for="agent-name">Name</label><input id="agent-name" name="name" required placeholder="required"></div>
        <div class="form-row"><label for="agent-role">Role</label><input id="agent-role" name="role" placeholder="optional"></div>
      </section>
      <section class="form-section">
        <h3 class="form-section-title">WORKSPACE / CONTEXT</h3>
        <div class="form-row"><label for="agent-workspace">Workspace</label><select id="agent-workspace" name="workspace">${optionList(state.workspaces, workspaceId)}</select></div>
        <div class="form-row"><label for="agent-context">Context</label><select id="agent-context" name="context"><option value="fresh">Fresh</option><option value="fork">Fork from ${escapeHTML(activeAgent().title)} [${escapeHTML(activeWorkspace().title)}]</option></select></div>
      </section>
      <section class="form-section">
        <h3 class="form-section-title">PLACEMENT</h3>
        <div class="form-row"><label for="agent-placement">Type</label><select id="agent-placement" name="placement"><option value="worktree">New private worktrees</option><option value="copy">Copy an agent placement</option><option value="directory">New managed directory</option><option value="external">Use external directory</option></select></div>
      </section>
      <section class="form-section" id="worktree-fields">
        <div class="form-row"><label for="agent-repository">Primary repository</label><select id="agent-repository" name="repository">${optionList(state.repositories, repositoryId || state.repositories[0]?.id)}</select></div>
        <div class="form-row"><label for="agent-remote">Source remote</label><select id="agent-remote" name="remote"><option value="origin">origin</option></select></div>
        <div class="form-row"><label for="agent-ref">Source ref</label><input id="agent-ref" name="ref" value="main" placeholder="default branch"></div>
        <div class="form-row"><label for="agent-fetch">Fetch first</label><select id="agent-fetch" name="fetch"><option value="yes">[x] Yes</option><option value="no">[ ] No</option></select></div>
        <div id="secondary-repositories"></div>
        <button class="form-row form-start static-row" id="add-secondary" type="button"><span class="field-label"></span><span class="field-value">+ Add secondary repository</span></button>
      </section>
      <section class="form-section" id="copy-fields" hidden>
        <div class="form-row"><label for="source-agent">Placement from</label><select id="source-agent" name="sourceAgent">${optionList(state.agents, state.activeAgent)}</select></div>
        <div class="form-row"><label for="share-files">Share files</label><select id="share-files" name="share"><option value="no">[ ] No</option><option value="yes">[x] Yes</option></select></div>
      </section>
      <section class="form-section" id="external-fields" hidden>
        <div class="form-row"><label for="external-path">Directory</label><input id="external-path" name="directory" placeholder="/home/you/project"></div>
      </section>
      <button class="form-row form-start static-row" type="submit"><span class="field-label">Start</span><span class="field-value">Create agent and open Pi</span></button>`,
    onSubmit(data) {
      const title = String(data.get("name") || "").trim();
      if (!title) throw new Error("An agent name is required.");
      const workspaceIdValue = String(data.get("workspace") || "");
      const workspace = state.workspaces.find((item) => item.id === workspaceIdValue);
      if (!workspace) throw new Error("Choose an available workspace.");
      const placement = String(data.get("placement"));
      const sourceAgent = state.agents.find(item => item.id === data.get("sourceAgent"));
      const repositoryIdValue = placement === "copy" ? sourceAgent?.repositoryId : String(data.get("repository") || "");
      const repository = state.repositories.find((item) => item.id === repositoryIdValue);
      if (placement === "external" && !String(data.get("directory") || "").trim().startsWith("/")) throw new Error("Enter an absolute directory path.");
      const repositories = [repository?.id, ...data.getAll("secondary")].filter(Boolean);
      if (new Set(repositories).size !== repositories.length) throw new Error("Choose each repository only once.");
      const placementLabel = placement === "copy" ? (data.get("share") === "yes" ? "Shared agent files" : "Copied private worktrees") : placement === "external" ? "Existing directory" : placement === "directory" ? "Managed directory" : "Private worktree";
      if (!repository && data.get("placement") === "worktree") throw new Error("Add a repository first.");
      let id = slug(title);
      if (state.agents.some((agent) => agent.id === id)) id += `-${state.agents.length + 1}`;
      const agent = {
        id,
        title,
        shortTitle: title,
        lastUsed: Date.now(),
        workspaceId: workspace.id,
        role: String(data.get("role") || "").trim() || "agent",
        status: "working",
        seen: true,
        repositoryId: repository?.id || "",
        placement, placementLabel, ref: String(data.get("ref") || sourceAgent?.ref || "main"),
        directory: String(data.get("directory") || ""),
        secondary: data.getAll("secondary").map(String),
        messages: data.get("context") === "fork" ? activeAgent().messages.map(message => ({ ...message })) : [],
      };
      agent.messages.push({
        role: "assistant",
        html: `<p>This sample agent is ready in <strong>${escapeHTML(workspace.title)}</strong>.</p><p>Placement: ${escapeHTML(placementLabel)}${repository ? ` · ${escapeHTML(repository.title)}` : ""}. ${data.get("context") === "fork" ? "The source conversation was copied." : "The conversation starts fresh."}</p><p>Local Galpon creates a durable session and the selected placement. This browser only adds a sample record. No files or processes were created.</p>`,
      });
      state.agents.push(agent);
      state.openTabs.push(agent.id);
      state.activeWorkspace = workspace.id;
      state.activeAgent = agent.id;
      state.activeTab = agent.id;
      workspace.seen = true;
      render();
      showNote(`${title} added to this browser session.`);
      setTimeout(() => {
        agent.status = "active";
        renderAgents();
        renderConversation();
      }, 2200);
    },
  });
}

function updateAgentEffects() {
  const form = elements.dynamicForm;
  const placement = form.elements.placement.value;
  for (const [id, visible] of [["worktree-fields", placement === "worktree"], ["copy-fields", placement === "copy"], ["external-fields", placement === "external"]]) {
    const section = document.getElementById(id);
    section.hidden = !visible;
    section.querySelectorAll("input, select, button").forEach(field => { field.disabled = !visible; });
  }
  const shared = placement === "copy" && form.elements.share.value === "yes";
  const fetchFirst = placement === "worktree" && form.elements.fetch.value === "yes";
  const title = shared ? "Shared files" : placement === "external" ? "Existing files" : "Isolated files";
  const files = shared ? "The agents use the exact same files. Changes are visible to both." : placement === "external" ? "Local Galpon uses this directory as it is. It does not create or copy a checkout." : placement === "directory" ? "Local Galpon creates a private managed directory with no Git checkout." : `Local Galpon creates private worktrees on new agent branches.${fetchFirst ? " It fetches the marked sources first." : " It uses the available local refs."}`;
  const context = form.elements.context.value === "fork";
  document.getElementById("form-effects").innerHTML = `<h4>${title}</h4><p>${files}</p><h4>${context ? "Copied conversation" : "New conversation"}</h4><p>${context ? "The existing messages are copied. New work belongs to the new agent." : "No previous messages are copied."}</p><h4>Durable agent</h4><p>In local Galpon, closing Pi does not delete the agent or its placement.</p>`;
}

let noteTimer;
let workDockFrame = 0;
function toggleWorkDock() {
  const agent = activeAgent();
  if (!agent.workDock) {
    showNote(`${agent.title} has no TODOs or delegated work.`, true);
    return;
  }
  agent.workDock.collapsed = !agent.workDock.collapsed;
  renderWorkDock();
  showNote(`Work Dock ${agent.workDock.collapsed ? "collapsed" : "expanded"}.`);
}

function showNote(message, warning = false) {
  clearTimeout(noteTimer);
  elements.note.textContent = message;
  elements.note.classList.toggle("warning", warning);
  elements.note.classList.add("visible");
  noteTimer = setTimeout(() => elements.note.classList.remove("visible"), 3200);
}

function handleCommandKey(event) {
  if (!elements.command.open) return;
  if (event.key === "ArrowDown") {
    event.preventDefault();
    moveSelection(1);
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    moveSelection(-1);
  } else if (event.key === "Enter") {
    event.preventDefault();
    const result = selectedCommandResult();
    if (result) openResult(result);
  } else if (event.key === "Tab") {
    event.preventDefault();
    if (state.operationsAgent) return;
    if (event.shiftKey) changeView();
    else toggleCommandExpansion();
  } else if (event.key === "Escape") {
    event.preventDefault();
    if (state.detailOnly) {
      state.detailOnly = false;
      renderCommandCenter();
    } else if (state.operationsAgent) {
      state.operationsAgent = null;
      state.selectedResult = 0;
      renderCommandCenter();
    } else closeDialog(elements.command);
  }
}

document.querySelector("#new-agent-shortcut").addEventListener("click", openCommandCenter);
elements.commandSearch.addEventListener("input", () => {
  state.selectedResult = 0;
  state.expandedCommandRows.clear();
  renderCommandCenter();
});
new ResizeObserver(() => {
  if (elements.command.open) renderCommandFooter();
}).observe(elements.command);
elements.command.addEventListener("keydown", handleCommandKey);
elements.command.addEventListener("cancel", (event) => {
  event.preventDefault();
  closeDialog(elements.command);
});
elements.resourceForm.addEventListener("cancel", (event) => {
  event.preventDefault();
  closeDialog(elements.resourceForm);
});

elements.commandFooter.addEventListener("click", (event) => {
  const action = event.target.closest("[data-action]")?.dataset.action;
  if (action === "expand") toggleCommandExpansion();
  else if (action === "repo") openRepositoryForm();
  else if (action === "space") openWorkspaceForm();
  else if (action === "agent") openAgentForm();
  else if (action === "fork") openAgentForm(state.activeWorkspace, activeAgent().repositoryId);
  else if (action === "mode") toggleCommandMode();
  else if (action === "views") changeView();
  else if (action === "detail") { state.detailOnly = !state.detailOnly; renderCommandCenter(); }
  else if (action === "dock") {
    closeDialog(elements.command);
    toggleWorkDock();
  } else if (action === "open") {
    const selected = selectedCommandResult();
    if (selected) openResult(selected);
  } else if (["operations", "terminal", "hide"].includes(action)) showSelectedAction(action);
  else if (action === "close") closeDialog(elements.command);
});

document.addEventListener("keydown", (event) => {
  const modified = event.ctrlKey || event.metaKey;
  const key = event.key.toLowerCase();
  if (modified && key === "o" && !elements.command.open && !elements.resourceForm.open) {
    event.preventDefault();
    state.toolsExpanded = !state.toolsExpanded;
    renderConversation();
    return;
  }
  if (modified && event.code === "Space") {
    if (elements.resourceForm.open) return;
    event.preventDefault();
    if (!elements.command.open) {
      openCommandCenter();
      state.commandMode = "actions";
      renderCommandCenter();
      elements.command.focus();
    } else {
      toggleCommandMode();
    }
    return;
  }
  if (modified && key === "k") {
    event.preventDefault();
    openCommandCenter();
    return;
  }
  if (modified && key === "n") {
    event.preventDefault();
    if (!elements.resourceForm.open) {
      const selected = elements.command.open ? selectedCommandResult() : null;
      openAgentForm(selected?.workspaceId || state.activeWorkspace, selected?.type === "repository" ? selected.id : "");
    }
    return;
  }
  if (modified && key === "s") {
    event.preventDefault();
    if (elements.resourceForm.open) elements.dynamicForm.requestSubmit();
    else openRepositoryForm();
    return;
  }
  if (!elements.command.open) return;
  if (modified) {
    if (key === "g") {
      event.preventDefault();
      state.detailOnly = !state.detailOnly;
      renderCommandCenter();
    } else if (key === "r") {
      event.preventDefault();
      rankAgents();
      renderCommandCenter();
    } else if (key === "f") {
      event.preventDefault();
      openAgentForm(state.activeWorkspace, activeAgent().repositoryId);
    } else if (key === "h") {
      event.preventDefault();
      showNote("No hidden demo resources. Local Galpon can show and restore hidden state.");
    }
    return;
  }
  if (state.commandMode !== "actions") return;
  if (event.key === "a") {
    event.preventDefault();
    const selected = selectedCommandResult();
    openAgentForm(selected?.workspaceId || state.activeWorkspace, selected?.type === "repository" ? selected.id : "");
  } else if (event.key === "r") {
    event.preventDefault();
    openRepositoryForm();
  } else if (event.key === "R") {
    event.preventDefault();
    openRemoteForm();
  } else if (event.key === "w") {
    event.preventDefault();
    openWorkspaceForm();
  } else if (event.key === "o") {
    event.preventDefault();
    showSelectedAction("operations");
  } else if (event.key === "d") {
    event.preventDefault();
    closeDialog(elements.command);
    toggleWorkDock();
  } else if (event.key === "t") {
    event.preventDefault();
    showSelectedAction("terminal");
  } else if (event.key === "e") {
    event.preventDefault();
    showSelectedAction("editor");
  } else if (event.key === "x") {
    event.preventDefault();
    showSelectedAction("hide");
  } else if (event.key === "q") {
    event.preventDefault();
    closeDialog(elements.command);
  }
}, true);

elements.composer.addEventListener("submit", (event) => event.preventDefault());
elements.prompt.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || event.shiftKey) return;
  event.preventDefault();
  const text = elements.prompt.value.trim();
  if (!text) return;
  const agent = activeAgent();
  if (["/workdock history", "/todos", "/workdock"].includes(text)) {
    if (agent.workDock) {
      if (text === "/workdock") agent.workDock.collapsed = !agent.workDock.collapsed;
      else { agent.workDock.history = text === "/todos" || !agent.workDock.history; agent.workDock.collapsed = false; }
      renderWorkDock();
    } else showNote("This agent has no tasks or delegated work.");
    elements.prompt.value = "";
    agent.draft = "";
    return;
  }
  if (agent.status === "working") return showNote("Wait for the current sample reply.", true);
  agent.messages.push({ role: "user", text });
  agent.draft = "";
  agent.lastUsed = Date.now();
  agent.status = "working";
  elements.prompt.value = "";
  renderAgents();
  renderConversation();
  setTimeout(() => {
    if (/\bfail(?:ure)?\b/i.test(text)) agent.messages.push({role: "tool", command: "$ git fetch origin", failed: true, result: "fatal: sample remote is not available\nThis is a sample failure. No network request was sent.", time: ""});
    agent.messages.push({
      role: "assistant",
      html: `<p>This browser is a safe Galpon demo, so I will not run a real model or change files.</p><p>In local Galpon, this message goes to the same durable Pi session. You can close the tab, resume the agent later, or ask another agent to help.</p>`,
    });
    agent.status = "active";
    renderAgents();
    renderConversation();
  }, 900);
});

elements.prompt.addEventListener("input", () => {
  elements.prompt.style.height = "auto";
  elements.prompt.style.height = `${Math.min(elements.prompt.scrollHeight, 110)}px`;
});

document.addEventListener("mousedown", (event) => {
  if (event.target.closest(".spaces-panel, .tab-bar")) return;
  event.preventDefault();
}, true);
document.addEventListener("contextmenu", (event) => event.preventDefault());
elements.command.addEventListener("close", () => {
  if (!elements.resourceForm.open) elements.prompt.focus();
});
elements.resourceForm.addEventListener("close", () => {
  if (!elements.command.open) elements.prompt.focus();
});

const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
setInterval(() => {
  if (document.hidden || reducedMotion.matches) return;
  const surface = elements.command.open ? elements.command : elements.resourceForm.open ? elements.resourceForm : elements.terminalPane;
  const liveGlyphs = surface.querySelectorAll('[data-live="true"]');
  if (!liveGlyphs.length) return;
  workDockFrame = (workDockFrame + 1) % workDockFrames.length;
  liveGlyphs.forEach((glyph) => { glyph.textContent = workDockFrames[workDockFrame]; });
}, 80);

document.querySelectorAll(".brand").forEach(brand => { brand.textContent = `${icons.brand} GALPON`; });
render();
elements.prompt.focus();
