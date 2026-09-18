const palette = {
  blue: "#82aaff",
  green: "#c3e88d",
  yellow: "#ffc777",
};

const workDockFrames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const activeWorkStates = new Set(["queued", "started", "waiting"]);

function workItem(title, status, options = {}) {
  const now = Date.now();
  return {
    id: options.id || `work-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`,
    title,
    status,
    lease: status === "started" ? "fresh" : "none",
    observedAt: now - (options.age || 0),
    checkpoint: options.checkpoint,
    activity: options.activity,
    coordination: options.coordination || [],
    children: options.children || [],
  };
}

function createWorkDockStage(stage = 3) {
  const buildStatus = stage >= 4 ? "completed" : "in_progress";
  const verifyStatus = stage >= 5 ? "completed" : stage >= 4 ? "in_progress" : "pending";
  const publishStatus = stage >= 6 ? "completed" : stage >= 5 ? "in_progress" : "pending";
  const releaseStatus = stage >= 4 ? "completed" : "pending";
  const reviewStatus = stage === 0 ? "queued" : stage < 3 ? "started" : "completed";
  const websiteStatus = stage === 0 ? "queued" : stage === 2 ? "waiting" : stage >= 4 ? "completed" : "started";
  const documentationStatus = stage < 2 ? "queued" : stage === 3 ? "waiting" : stage >= 5 ? "completed" : "started";
  const deploymentStatus = stage < 5 ? "queued" : stage >= 6 ? "completed" : "started";

  return {
    collapsed: false,
    stage,
    todos: [
      { id: 1, subject: "Define the demo story", status: "completed" },
      { id: 2, subject: "Build the interactive site", status: buildStatus, activeForm: buildStatus === "in_progress" ? "building the browser demo" : "" },
      { id: 3, subject: "Verify keyboard flows", status: verifyStatus, activeForm: verifyStatus === "in_progress" ? "testing the Work Dock" : "", blockedBy: [2] },
      { id: 4, subject: "Publish galpon.dev", status: publishStatus, activeForm: publishStatus === "in_progress" ? "preparing the static release" : "", blockedBy: [3] },
      { id: 5, subject: "Prepare release notes", status: releaseStatus },
    ],
    delegations: [
      workItem("Website", websiteStatus, {
        id: "work-website",
        checkpoint: websiteStatus === "started"
          ? { phase: stage === 1 ? "planning" : "working", summary: stage === 1 ? "Mapping the Pi layout" : "Building the Work Dock" }
          : websiteStatus === "waiting" ? { phase: "waiting", summary: "Waiting for the interface review" } : undefined,
        activity: websiteStatus === "started" ? { category: "tool: edit", status: "started" } : undefined,
        coordination: websiteStatus === "waiting" ? ["operation waiting"] : [],
        children: [workItem("Interface review", reviewStatus, {
          id: "work-review",
          checkpoint: reviewStatus === "started" ? { phase: "verifying", summary: "Checking keyboard and layout behavior" } : undefined,
          activity: reviewStatus === "started" ? { category: "tool: read", status: "started" } : undefined,
        })],
      }),
      workItem("Documentation", documentationStatus, {
        id: "work-documentation",
        checkpoint: documentationStatus === "started"
          ? { phase: "working", summary: "Writing the browser demo guide" }
          : documentationStatus === "waiting" ? { phase: "waiting", summary: "Waiting for the final interface labels" } : undefined,
        coordination: documentationStatus === "waiting" ? ["operation waiting"] : [],
      }),
      workItem("Deployment", deploymentStatus, {
        id: "work-deployment",
        checkpoint: deploymentStatus === "started" ? { phase: "finishing", summary: "Preparing the static assets" } : undefined,
        activity: deploymentStatus === "started" ? { category: "tool: bash", status: "started" } : undefined,
      }),
    ],
  };
}

const state = {
  activeWorkspace: "galpon",
  activeAgent: "agents-control",
  selectedResult: 0,
  commandMode: "search",
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
      title: "Agents under control",
      shortTitle: "Agents under control",
      workspaceId: "galpon",
      role: "guide",
      status: "active",
      seen: true,
      repositoryId: "repo-galpon",
      workDock: createWorkDockStage(3),
      messages: [
        {
          role: "user",
          text: "I want several coding agents to work at the same time. How does Galpon keep all of that under control?",
        },
        {
          role: "tool",
          command: "$ galpon snapshot",
          timeout: "(timeout 20s)",
          collapsed: "... (2 earlier lines, ctrl+o to expand)",
          result: "✓  2 workspaces\n✓  3 agents\n✓  3 managed worktrees",
          time: "Took 0.0s",
        },
        { role: "thinking", text: "Thinking..." },
        {
          role: "assistant",
          html: `
            <p>Galpon gives every piece of work a durable home. You use four simple concepts:</p>
            <ul>
              <li><strong>Workspaces</strong> group the people, agents, and files for one task or project.</li>
              <li><strong>Agents</strong> are durable Pi conversations. Close a tab today and open it tomorrow. The same agent and session continue.</li>
              <li><strong>Repositories</strong> are the Git projects that Galpon can use. Add each project once.</li>
              <li><strong>Worktrees</strong> give each agent its own safe copy of the repository.</li>
            </ul>
            <p>You do not create branches, choose worktree folders, or clean up agent processes. Galpon manages the Git worktrees and the complete agent lifecycle for you.</p>
            <blockquote>You decide what must be done. Galpon keeps every agent, conversation, and checkout in the correct place.</blockquote>
            <p>The Work Dock below separates local TODOs from observed and reported delegation facts. Send any prompt to replay its browser-only lifecycle. Press <code>Ctrl-Space</code>, then <code>d</code>, to collapse or expand it.</p>
            <p>Press <code>Ctrl-K</code> to see all of Galpon from one command center.</p>`,
        },
      ],
    },
    {
      id: "command-guide",
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
          collapsed: "... (4 earlier lines, ctrl+o to expand)",
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
            <p>Start typing to filter titles. Use the arrow keys to move, then press <code>Enter</code>. The command center keeps agents, workspaces, worktrees, and repositories in stable groups.</p>
            <p>This page is interactive. Press <code>Ctrl-K</code>, add a repository, and create an agent. Nothing leaves your browser.</p>`,
        },
      ],
    },
    {
      id: "sandbox-agent",
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
  openTabs: ["agents-control", "command-guide"],
};

const elements = {
  spaces: document.querySelector("#space-list"),
  agents: document.querySelector("#agent-list"),
  tabs: document.querySelector("#tab-bar"),
  conversation: document.querySelector("#conversation"),
  workDock: document.querySelector("#work-dock"),
  composer: document.querySelector("#pi-composer"),
  prompt: document.querySelector("#pi-prompt"),
  statusPath: document.querySelector("#status-path"),
  statusWorkspace: document.querySelector("#status-workspace"),
  command: document.querySelector("#command-center"),
  commandSearch: document.querySelector("#command-search"),
  commandResults: document.querySelector("#command-results"),
  commandFooter: document.querySelector("#command-footer"),
  resourceCounts: document.querySelector("#resource-counts"),
  resourceForm: document.querySelector("#resource-form"),
  dynamicForm: document.querySelector("#dynamic-form"),
  formTitle: document.querySelector("#form-title"),
  formSubtitle: document.querySelector("#form-subtitle"),
  note: document.querySelector("#demo-note"),
};

function escapeHTML(value) {
  return String(value).replace(/[&<>'"]/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;",
  })[character]);
}

function slug(value) {
  return String(value).toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "") || `item-${Date.now()}`;
}

function activeAgent() {
  return state.agents.find((agent) => agent.id === state.activeAgent) || state.agents[0];
}

function activeWorkspace() {
  return state.workspaces.find((workspace) => workspace.id === state.activeWorkspace) || state.workspaces[0];
}

function repositoryFor(agent) {
  return state.repositories.find((repository) => repository.id === agent.repositoryId) || state.repositories[0];
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

function workGlyph(item) {
  if (item.status === "started" && item.lease === "fresh") return { glyph: workDockFrames[workDockFrame], style: "live", live: true };
  if (item.status === "queued") return { glyph: "○", style: "pending" };
  if (item.status === "started") return { glyph: "◐", style: "progress" };
  if (item.status === "waiting") return { glyph: "◇", style: "waiting" };
  if (item.status === "completed") return { glyph: "✓", style: "completed" };
  return { glyph: "✗", style: "failed" };
}

function formatTodoLine(todo, showIds, prefix) {
  const glyphs = { pending: "○", in_progress: "◐", completed: "✓" };
  const styles = { pending: "pending", in_progress: "progress", completed: "completed" };
  const style = styles[todo.status] || "failed";
  const id = showIds ? ` <span class="dock-meta">#${todo.id}</span>` : "";
  const activeForm = todo.status === "in_progress" && todo.activeForm
    ? ` <span class="dock-meta">(${escapeHTML(todo.activeForm)})</span>` : "";
  const blockedBy = todo.blockedBy?.length
    ? ` <span class="dock-meta">⛓ ${todo.blockedBy.map((value) => `#${value}`).join(",")}</span>` : "";
  return `<div class="dock-line dock-todo" data-todo-id="${todo.id}"><span class="dock-tree">${prefix}</span> <span class="dock-glyph ${style}" aria-hidden="true">${glyphs[todo.status] || "✗"}</span>${id} <span class="dock-title ${style}">${escapeHTML(todo.subject)}</span>${activeForm}${blockedBy}</div>`;
}

function formatDelegationLine(item, depth, last) {
  const mark = workGlyph(item);
  const titleStyle = item.status === "started" ? "progress" : item.status === "completed" ? "completed" : "";
  const prefix = `   ${"  ".repeat(depth)}${last ? "└─" : "├─"}`;
  const checkpoint = item.checkpoint
    ? ` <span class="dock-meta">(${escapeHTML(item.checkpoint.phase)} · ${escapeHTML(item.checkpoint.summary)} · reported)</span>${item.checkpoint.blocker ? ` <span class="dock-blocker">⛓ ${escapeHTML(item.checkpoint.blocker)}</span>` : ""}`
    : "";
  const observation = item.status === "started"
    ? `${item.lease === "stale" ? "stale observation" : "lease observed"} now`
    : "observed now";
  const activity = item.activity
    ? ` <span class="dock-observation">activity: ${escapeHTML(item.activity.category)} · ${escapeHTML(item.activity.status)} · observed now</span>` : "";
  const coordination = item.coordination?.length
    ? ` <span class="dock-meta">{${item.coordination.map(escapeHTML).join(" · ")}}</span>` : "";
  return `<div class="dock-line dock-delegation" data-work-id="${escapeHTML(item.id)}"><span class="dock-tree">${prefix}</span> <span class="dock-glyph ${mark.style}"${mark.live ? ' data-live="true"' : ""} aria-hidden="true">${mark.glyph}</span><span class="sr-only">${escapeHTML(item.status)}</span> <span class="dock-title ${titleStyle}">${escapeHTML(item.title)}</span> <span class="dock-meta">[${escapeHTML(item.status)} · observed]</span>${checkpoint} <span class="dock-observation">${observation}</span>${activity}${coordination}</div>`;
}

function renderWorkDock() {
  const dock = activeAgent().workDock;
  if (!dock || (!dock.todos.length && !dock.delegations.length)) {
    elements.workDock.hidden = true;
    elements.workDock.replaceChildren();
    return;
  }

  const work = flatDelegations(dock.delegations);
  const activeDelegations = work.filter(({ item }) => activeWorkStates.has(item.status)).length;
  const ready = readyTodoCount(dock);
  const active = activeDelegations > 0 || dock.todos.some((todo) => todo.status === "pending" || todo.status === "in_progress");
  const todoLabel = dock.todos.length === 1 ? "todo" : "todos";
  const delegationLabel = work.length === 1 ? "delegation" : "delegations";
  const heading = `<div class="dock-line dock-heading${active ? "" : " inactive"}">${active ? "●" : "○"} <strong>Work Dock · ${dock.todos.length} ${todoLabel} · ${ready} ready · ${work.length} ${delegationLabel}</strong></div>`;
  const lines = [heading];

  if (dock.collapsed) {
    lines.push('<div class="dock-line dock-hint"><span class="dock-tree">└─</span> ctrl+space d to expand</div>');
  } else {
    const both = dock.todos.length > 0 && work.length > 0;
    if (dock.todos.length) {
      lines.push(`<div class="dock-line dock-section"><span class="dock-tree">${both ? "├─" : "└─"}</span> <strong>Todos (${dock.todos.filter((todo) => todo.status === "completed").length}/${dock.todos.length}) · ${ready} ready</strong></div>`);
      const showIds = dock.todos.some((todo) => todo.blockedBy?.length);
      dock.todos.forEach((todo, index) => {
        const connector = both || index < dock.todos.length - 1 ? "│  ├─" : "   └─";
        lines.push(formatTodoLine(todo, showIds, connector));
      });
    }
    if (work.length) {
      lines.push(`<div class="dock-line dock-section"><span class="dock-tree">└─</span> <strong>Delegations (${activeDelegations}/${work.length} active)</strong></div>`);
      work.forEach(({ item, depth }, index) => lines.push(formatDelegationLine(item, depth, index === work.length - 1)));
    }
  }

  elements.workDock.innerHTML = lines.join("");
  elements.workDock.hidden = false;
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
}

function renderTabs() {
  const openAgents = state.openTabs
    .map((id) => state.agents.find((agent) => agent.id === id))
    .filter((agent) => agent && agent.workspaceId === state.activeWorkspace);
  const nodes = openAgents.map((agent) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `tab-button${agent.id === state.activeAgent ? " active" : ""}`;
    button.textContent = agent.shortTitle;
    button.title = agent.title;
    button.addEventListener("click", () => selectAgent(agent.id));
    return button;
  });
  const add = document.createElement("button");
  add.type = "button";
  add.className = "tab-button new-tab";
  add.textContent = "+";
  add.setAttribute("aria-label", "Create an agent");
  add.addEventListener("click", () => openAgentForm(state.activeWorkspace));
  nodes.push(add);
  elements.tabs.replaceChildren(...nodes);
}

function renderConversation() {
  const agent = activeAgent();
  const workspace = activeWorkspace();
  const repository = repositoryFor(agent);
  const fragment = document.createDocumentFragment();

  for (const message of agent.messages) {
    if (message.role === "tool") {
      const tool = document.createElement("section");
      tool.className = "message tool-block";
      tool.innerHTML = `<div class="tool-command">${escapeHTML(message.command)}${message.timeout ? ` <span class="tool-timeout">${escapeHTML(message.timeout)}</span>` : ""}</div>${message.collapsed ? `<div class="tool-collapsed">${escapeHTML(message.collapsed)}</div>` : ""}<div class="tool-result">${escapeHTML(message.result)}</div><div class="tool-time">${escapeHTML(message.time)}</div>`;
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
    thinking.innerHTML = `Working in ${escapeHTML(repository?.title || "managed directory")} <span class="cursor-block"></span>`;
    fragment.append(thinking);
  }
  elements.conversation.replaceChildren(fragment);
  const placement = `${workspace.id}-747ca24a/${agent.id}-2fdf63c9/galpon-1a9e2464`;
  elements.statusPath.innerHTML = `~/.local/state/galpon/worktrees/${escapeHTML(placement)} <span>(${escapeHTML(workspace.branch)})</span> · ${escapeHTML(agent.title)}`;
  const activeDelegated = agent.workDock
    ? flatDelegations(agent.workDock.delegations).filter(({ item }) => activeWorkStates.has(item.status)).length
    : 0;
  elements.statusWorkspace.textContent = `🛖 ${workspace.title} · 🤖 ${activeDelegated}`;
  renderWorkDock();
  requestAnimationFrame(() => { elements.conversation.scrollTop = elements.conversation.scrollHeight; });
}

function render() {
  renderSpaces();
  renderAgents();
  renderTabs();
  renderConversation();
}

function selectWorkspace(workspaceId) {
  state.activeWorkspace = workspaceId;
  const selectedWorkspace = state.workspaces.find((workspace) => workspace.id === workspaceId);
  if (selectedWorkspace) selectedWorkspace.seen = true;
  const agent = state.agents.find((item) => item.workspaceId === workspaceId);
  if (agent) {
    state.activeAgent = agent.id;
    agent.seen = true;
    if (!state.openTabs.includes(agent.id)) state.openTabs.push(agent.id);
  }
  render();
}

function selectAgent(agentId) {
  const agent = state.agents.find((item) => item.id === agentId);
  if (!agent) return;
  state.activeAgent = agent.id;
  state.activeWorkspace = agent.workspaceId;
  agent.status = "active";
  agent.seen = true;
  const workspace = state.workspaces.find((item) => item.id === agent.workspaceId);
  if (workspace) workspace.seen = true;
  if (!state.openTabs.includes(agent.id)) state.openTabs.push(agent.id);
  render();
}

function worktrees() {
  return state.agents.map((agent) => {
    const workspace = state.workspaces.find((item) => item.id === agent.workspaceId);
    const repository = repositoryFor(agent);
    return {
      id: `worktree-${agent.id}`,
      title: `${workspace?.title || "Workspace"} · ${repository?.title || "managed directory"}`,
      detail: repository ? `galpon/${workspace.id}/${agent.id}/${repository.id}` : "managed directory",
      workspaceId: agent.workspaceId,
      agentId: agent.id,
    };
  });
}

function commandGroups() {
  return [
    {
      name: "AGENTS",
      type: "agent",
      items: state.agents.map((agent) => ({
        id: agent.id,
        title: agent.title,
        detail: `${agent.role}  ·  ${state.workspaces.find((item) => item.id === agent.workspaceId)?.title || "workspace"}  ·  ${agent.status === "working" ? "working" : "idle"}`,
        workspaceId: agent.workspaceId,
      })),
    },
    {
      name: "WORKSPACES",
      type: "workspace",
      items: state.workspaces.map((workspace) => ({ id: workspace.id, title: workspace.title, detail: "durable workspace", workspaceId: workspace.id })),
    },
    { name: "WORKTREES", type: "worktree", items: worktrees() },
    {
      name: "REPOSITORIES",
      type: "repository",
      items: state.repositories.map((repository) => ({
        id: repository.id,
        title: repository.title,
        detail: `${repository.branch}  ·  ${repository.remotes} ${repository.remotes === 1 ? "remote" : "remotes"}`,
      })),
    },
  ];
}

function filteredGroups() {
  const query = elements.commandSearch.value.trim().toLowerCase();
  return commandGroups().map((group) => ({
    ...group,
    items: group.items.filter((item) => !query || item.title.toLowerCase().includes(query)),
  })).filter((group) => group.items.length);
}

function flatResults() {
  return filteredGroups().flatMap((group) => group.items.map((item) => ({ ...item, type: group.type })));
}

function renderCommandFooter() {
  const hints = state.commandMode === "search"
    ? [
        ["SEARCH", "type", "search"], ["tab", "expand", "expand"], ["ctrl+n", "new agent", "agent"],
        ["ctrl+f", "fork agent", "fork"], ["ctrl+s", "new repository", "repo"], ["ctrl+h", "show hidden", "hidden"],
        ["ctrl+space", "actions", "mode"], ["esc", "close", "close"],
      ]
    : [
        ["NORMAL", "actions", "mode"], ["enter", "open", "open"], ["a", "agent", "agent"], ["o", "operations", "operations"],
        ["d", "dock", "dock"], ["t/e", "term/edit", "terminal"], ["x", "hide", "hide"], ["r/R", "repository", "repo"],
        ["w", "workspace", "space"], ["q", "close", "close"], ["ctrl+space", "search", "mode"],
      ];
  elements.commandFooter.innerHTML = hints.map(([key, label, action]) => `<button type="button" class="footer-action" data-action="${action}"><kbd>${key}</kbd> ${label}</button>`).join("");
}

function renderCommandCenter() {
  const groups = filteredGroups();
  const flat = groups.flatMap((group) => group.items);
  state.selectedResult = Math.max(0, Math.min(state.selectedResult, flat.length - 1));
  let cursor = 0;
  const nodes = [];

  for (const group of groups) {
    const section = document.createElement("section");
    section.className = "result-group";
    const heading = document.createElement("h3");
    heading.className = "result-heading";
    heading.textContent = group.name;
    section.append(heading);

    for (const item of group.items) {
      const index = cursor++;
      const row = document.createElement("button");
      row.type = "button";
      row.className = `result-row${index === state.selectedResult ? " selected" : ""}`;
      row.setAttribute("role", "option");
      row.setAttribute("aria-selected", String(index === state.selectedResult));
      row.innerHTML = `<span class="row-title">${escapeHTML(item.title)}</span><span class="row-detail">${escapeHTML(item.detail)}</span>`;
      row.addEventListener("mouseenter", () => { state.selectedResult = index; renderCommandCenter(); });
      row.addEventListener("click", () => openResult({ ...item, type: group.type }));
      section.append(row);
    }
    nodes.push(section);
  }

  if (!nodes.length) {
    const empty = document.createElement("p");
    empty.className = "form-copy";
    empty.textContent = `No title matches “${elements.commandSearch.value}”`;
    nodes.push(empty);
  }

  elements.commandResults.replaceChildren(...nodes);
  renderCommandFooter();
  elements.resourceCounts.textContent = `${state.workspaces.length} workspaces  ·  ${worktrees().length} worktrees  ·  ${state.agents.length} agents`;
}

function openCommandCenter() {
  closeDialog(elements.resourceForm);
  state.selectedResult = 0;
  state.commandMode = "search";
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
  renderCommandFooter();
  if (state.commandMode === "search") elements.commandSearch.focus();
  else elements.command.focus();
}

function showSelectedAction(action) {
  const selected = selectedCommandResult();
  if (!selected) return;
  const title = selected.title;
  if (action === "terminal") showNote(`Local Galpon would open ${title} in your real terminal.`, true);
  else if (action === "editor") showNote(`Local Galpon would open ${title} in your configured editor.`, true);
  else if (action === "operations") showNote(selected.type === "agent" ? `Operations opened for ${title}. This demo has no live server facts.` : "Select an agent to open Operations.", true);
  else if (action === "hide") showNote(`${title} remains visible because this browser demo does not change durable state.`, true);
}

function formFooter(primaryLabel, title) {
  if (title === "New agent") {
    return `<footer class="form-actions">
      <span><kbd>tab</kbd> list / next</span>
      <span><kbd>← →</kbd> change</span>
      <span><kbd>+</kbd> secondary</span>
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
      ${copy ? `<p class="form-copy">${copy}</p>` : ""}
      ${sections || `<section class="form-section"><h3 class="form-section-title">${defaultSection}</h3>${fields}</section>`}
      <div class="form-error" hidden></div>
    </div>
    ${formFooter(submitLabel, title)}`;
  elements.dynamicForm.onsubmit = (event) => {
    event.preventDefault();
    const data = new FormData(elements.dynamicForm);
    const error = elements.dynamicForm.querySelector(".form-error");
    try {
      onSubmit(data);
      closeDialog(elements.resourceForm);
    } catch (cause) {
      error.hidden = false;
      error.textContent = `! ${cause.message}`;
    }
  };
  elements.dynamicForm.querySelector(".cancel-form").addEventListener("click", () => closeDialog(elements.resourceForm));
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
        <h3 class="form-section-title">WORKSPACE</h3>
        <div class="form-row"><label for="agent-workspace">Workspace</label><select id="agent-workspace" name="workspace">${optionList(state.workspaces, workspaceId)}</select></div>
      </section>
      <section class="form-section">
        <h3 class="form-section-title">CONTEXT</h3>
        <div class="form-row"><label for="agent-context">Context</label><select id="agent-context" name="context"><option value="fresh">Fresh</option><option value="fork">Fork from ${escapeHTML(activeAgent().title)} [${escapeHTML(activeWorkspace().title)}]</option></select></div>
      </section>
      <section class="form-section">
        <h3 class="form-section-title">PLACEMENT</h3>
        <div class="form-row"><label for="agent-placement">Type</label><select id="agent-placement" name="placement"><option value="worktree">New private worktrees</option><option value="copy">Copy an agent placement</option><option value="directory">New managed directory</option><option value="external">Use external directory</option></select></div>
      </section>
      <section class="form-section">
        <h3 class="form-section-title">WORKTREES</h3>
        <div class="form-row"><label for="agent-repository">Primary repository</label><select id="agent-repository" name="repository">${optionList(state.repositories, repositoryId || state.repositories[0]?.id)}</select></div>
        <div class="form-row"><label for="agent-remote">Source remote</label><select id="agent-remote" name="remote"><option value="origin">origin</option></select></div>
        <div class="form-row"><label for="agent-ref">Source ref</label><input id="agent-ref" name="ref" value="main" placeholder="default branch"></div>
        <div class="form-row"><label for="agent-fetch">Fetch first</label><select id="agent-fetch" name="fetch"><option value="yes">Yes</option><option value="no">No</option></select></div>
        <div class="form-row static-row" tabindex="0"><span class="field-label">+</span><span class="field-value">Add secondary repository</span></div>
      </section>
      <section class="form-section">
        <h3 class="form-section-title">ACTION</h3>
        <div class="form-row static-row" tabindex="0"><span class="field-label">Start</span><span class="field-value">Create agent and open Pi</span></div>
      </section>`,
    onSubmit(data) {
      const title = String(data.get("name") || "").trim();
      if (!title) throw new Error("An agent name is required.");
      const workspaceIdValue = String(data.get("workspace") || "");
      const workspace = state.workspaces.find((item) => item.id === workspaceIdValue);
      if (!workspace) throw new Error("Choose an available workspace.");
      const repositoryIdValue = String(data.get("repository") || "");
      const repository = state.repositories.find((item) => item.id === repositoryIdValue);
      if (!repository && data.get("placement") === "worktree") throw new Error("Add a repository first.");
      let id = slug(title);
      if (state.agents.some((agent) => agent.id === id)) id += `-${state.agents.length + 1}`;
      const agent = {
        id,
        title,
        shortTitle: title,
        workspaceId: workspace.id,
        role: String(data.get("role") || "").trim() || "agent",
        status: "working",
        seen: true,
        repositoryId: repository?.id || "",
        messages: [],
      };
      agent.messages.push({
        role: "assistant",
        html: `<p>Ready. Galpon created my durable Pi session${repository ? ` and a private <strong>${escapeHTML(repository.title)}</strong> worktree` : " in a managed directory"}.</p><p>I am now active in the <strong>${escapeHTML(workspace.title)}</strong> workspace. In local Galpon, I can inspect files, use tools, and keep working after this view closes.</p>`,
      });
      state.agents.push(agent);
      state.openTabs.push(agent.id);
      state.activeWorkspace = workspace.id;
      state.activeAgent = agent.id;
      workspace.seen = true;
      render();
      showNote(`${title} started in a private placement.`);
      setTimeout(() => {
        agent.status = "active";
        renderAgents();
        renderConversation();
      }, 2200);
    },
  });
}

let noteTimer;
let workDockFrame = 0;
const dockDemoTimers = new Map();

function clearWorkDockDemo(agentId) {
  for (const timer of dockDemoTimers.get(agentId) || []) clearTimeout(timer);
  dockDemoTimers.delete(agentId);
}

function startWorkDockDemo(agent) {
  clearWorkDockDemo(agent.id);
  const collapsed = agent.workDock?.collapsed === true;
  const showStage = (stage) => {
    const next = createWorkDockStage(stage);
    next.collapsed = collapsed;
    agent.workDock = next;
    if (agent.id === state.activeAgent) {
      renderWorkDock();
      const active = flatDelegations(next.delegations).filter(({ item }) => activeWorkStates.has(item.status)).length;
      elements.statusWorkspace.textContent = `🛖 ${activeWorkspace().title} · 🤖 ${active}`;
    }
  };
  showStage(0);
  const timers = [
    [1, 550],
    [2, 1300],
    [3, 2200],
    [4, 3200],
    [5, 4300],
    [6, 5400],
  ].map(([stage, delay]) => setTimeout(() => showStage(stage), delay));
  dockDemoTimers.set(agent.id, timers);
}

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
    showNote("All demo results are already expanded.");
  } else if (event.key === "Escape") {
    event.preventDefault();
    closeDialog(elements.command);
  }
}

document.querySelector("#new-agent-shortcut").addEventListener("click", openCommandCenter);
elements.commandSearch.addEventListener("input", () => {
  state.selectedResult = 0;
  renderCommandCenter();
});
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
  if (action === "repo") openRepositoryForm();
  else if (action === "space") openWorkspaceForm();
  else if (action === "agent") openAgentForm();
  else if (action === "fork") openAgentForm(state.activeWorkspace, activeAgent().repositoryId);
  else if (action === "mode") toggleCommandMode();
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
  if (modified && event.code === "Space") {
    event.preventDefault();
    if (!elements.command.open) {
      openCommandCenter();
      state.commandMode = "actions";
      renderCommandFooter();
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
    if (key === "f") {
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
  agent.messages.push({ role: "user", text });
  agent.status = "working";
  if (agent.workDock) startWorkDockDemo(agent);
  elements.prompt.value = "";
  renderAgents();
  renderConversation();
  setTimeout(() => {
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
  const liveGlyphs = elements.workDock.querySelectorAll('[data-live="true"]');
  if (!liveGlyphs.length) return;
  workDockFrame = (workDockFrame + 1) % workDockFrames.length;
  liveGlyphs.forEach((glyph) => { glyph.textContent = workDockFrames[workDockFrame]; });
}, 80);

render();
elements.prompt.focus();
