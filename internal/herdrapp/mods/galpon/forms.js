// The forms of the Galpon command center. These functions do not use the app
// or GPUI, so they also run outside the app. They follow the terminal forms:
// the same fields, choices, defaults and texts. Galpon makes the first draft
// and checks the submit, so a draft names resources by ID.
//
// A form gives its fields in order. Each field has a key, a section, a type
// and the text to show:
//   text    an input; text(key) and setText(key, value) read and write it
//   choice  a list to choose from; choice(key) gives it, choose(key, value) sets it
//   toggle  on or off; toggle(key) changes it
//   action  a row that does something in the form, such as "add"
//   static  a fact that does not change
//   submit  the button that sends the form

export const PLACEMENTS = ["New private worktrees", "Copy an agent placement", "New managed directory", "Use external directory", "Use selected worktree"];

const clone = (value) => JSON.parse(JSON.stringify(value));
const cycle = (value, delta, count) => (count <= 0 ? 0 : (((value + delta) % count) + count) % count);
const trim = (text) => String(text ?? "").trim();

// filterOptions keeps the options whose label and search text hold every word
// of the query.
export function filterOptions(options, query) {
  const terms = String(query ?? "").toLowerCase().split(/\s+/).filter(Boolean);
  return options.filter((option) => {
    const text = `${option.label} ${option.search ?? ""}`.toLowerCase();
    return terms.every((term) => text.includes(term));
  });
}

function lookups(catalog) {
  const byID = (list) => new Map((list ?? []).map((item) => [item.id, item]));
  return {
    workspaces: byID(catalog.workspaces),
    repositories: byID(catalog.repositories),
    agents: byID(catalog.agents),
    worktrees: byID(catalog.worktrees),
    harness: (value) => (catalog.harnesses ?? []).find((option) => option.value === value)?.label ?? value,
  };
}

function defaultRemote(repository) {
  const remotes = repository?.remotes ?? [];
  return remotes.find((remote) => remote.name === repository.defaultRemote)?.name ?? remotes[0]?.name ?? "";
}

function field(key, type, section, label, value, extra = {}) {
  return { key, type, section, label, value, ...extra };
}

// The choice of a field: options and the index of the current value.
function choiceOf(title, options, current) {
  const cursor = Math.max(0, options.findIndex((option) => option.value === current));
  return { title, options, cursor };
}

// cycleValue moves a choice to the previous or next option.
function cycleValue(options, current, delta) {
  if (!options.length) return current;
  const index = options.findIndex((option) => option.value === current);
  return options[cycle(Math.max(0, index), delta, options.length)].value;
}

function agentForm(data) {
  const catalog = data.catalog;
  const draft = clone(data.agent);
  const find = lookups(catalog);
  const harness = () => draft.harness || catalog.defaultHarness || "pi";
  const contextAgents = () => (catalog.agents ?? []).filter((agent) => agent.context);
  const contextAgent = () => (draft.contextAgentId ? find.agents.get(draft.contextAgentId) : null);
  const crossHarness = () => Boolean(contextAgent()) && contextAgent().harness !== harness();
  const placementCount = () => (draft.suggestedWorktreeId ? 5 : 4);
  const repositories = catalog.repositories ?? [];
  const worktree = (index) => draft.worktrees[index];

  function fields() {
    const list = [
      field("name", "text", "IDENTITY", "Name", draft.name, { placeholder: "required", strong: true }),
      field("role", "text", "IDENTITY", "Role", draft.role, { placeholder: "optional" }),
      field("harness", "choice", "IDENTITY", "Harness", find.harness(harness())),
      field("workspace", "choice", "WORKSPACE / CONTEXT", "Workspace", find.workspaces.get(draft.workspaceId)?.title ?? "Not available"),
      field("context", "choice", "WORKSPACE / CONTEXT", "Context", contextLabel(), {
        hint: crossHarness() ? "Copies saved history, images, and active context. Past tools stay as text." : "",
      }),
      field("placement", "choice", "PLACEMENT", "Type", PLACEMENTS[draft.placement] ?? "Choose a placement type"),
    ];
    switch (draft.placement) {
      case 0:
        draft.worktrees.forEach((item, index) => {
          const repository = find.repositories.get(item.repositoryId);
          const secondary = index > 0 ? { hint: "d removes this secondary repository", worktree: index } : { worktree: index };
          let repositoryValue = repository?.title ?? "No longer available";
          if (!repositories.length) repositoryValue = "No repositories";
          let remoteValue = item.remote || "No remotes";
          if (!repository) remoteValue = "No remotes";
          else if (!repository.remotes?.length) remoteValue = "No remotes";
          else if (!repository.remotes.some((remote) => remote.name === item.remote)) remoteValue = "No longer available";
          list.push(
            field(`repository:${index}`, "choice", "PLACEMENT", index === 0 ? "Primary repository" : "Secondary repository", repositoryValue, secondary),
            field(`remote:${index}`, "choice", "PLACEMENT", "Source remote", remoteValue, secondary),
            field(`ref:${index}`, "text", "PLACEMENT", "Source ref", item.ref, { placeholder: "default branch", ...secondary }),
            field(`fetch:${index}`, "toggle", "PLACEMENT", "Fetch first", item.fetchFirst ? "Yes" : "No", { on: item.fetchFirst, ...secondary }),
          );
        });
        list.push(field("add", "action", "PLACEMENT", "", "Add secondary repository"));
        break;
      case 1: {
        const source = find.agents.get(draft.placementAgentId);
        let value = source?.title ?? "No longer available";
        if (!(catalog.agents ?? []).length) value = "No agents";
        list.push(
          field("placementSource", "choice", "PLACEMENT", "Agent", value),
          shareField(),
        );
        break;
      }
      case 3:
        list.push(field("cwd", "text", "PLACEMENT", "Directory", draft.cwd, { placeholder: "absolute path" }));
        break;
      case 4:
        list.push(
          field("worktreeSource", "static", "PLACEMENT", "Worktree", find.worktrees.get(draft.suggestedWorktreeId)?.label ?? "Not available"),
          shareField(),
        );
        break;
    }
    list.push(field("create", "submit", "", "Start", `Create agent and open ${find.harness(harness())}`));
    return list;
  }

  function shareField() {
    return field("share", "toggle", "PLACEMENT", "Assignment", draft.share ? "Shared files" : "Private forks", {
      on: draft.share,
      hint: draft.share ? "Both agents will edit the same files." : "",
    });
  }

  function contextLabel() {
    const agent = contextAgent();
    if (!agent) return "Fresh";
    const workspace = find.workspaces.get(agent.workspaceId);
    return workspace ? `Fork from ${agent.title} [${workspace.title}]` : `Fork from ${agent.title}`;
  }

  const split = (key) => {
    const [kind, index] = key.split(":");
    return { kind, index: Number(index ?? 0) };
  };

  function options(key) {
    const { kind, index } = split(key);
    switch (kind) {
      case "workspace":
        return (catalog.workspaces ?? []).map((item) => ({ label: item.title, detail: item.status ?? "", value: item.id }));
      case "harness":
        return (catalog.harnesses ?? []).map((item) => ({ label: item.label, detail: "Permanent for this agent", value: item.value }));
      case "context":
        return [{ label: "Fresh", detail: "Start without prior conversation context", value: "" }].concat(contextAgents().map((agent) => {
          const workspace = find.workspaces.get(agent.workspaceId)?.title ?? "Workspace not available";
          return { label: agent.title, detail: `${workspace} · ${find.harness(agent.harness)} context`, search: workspace, value: agent.id };
        }));
      case "placement":
        return PLACEMENTS.slice(0, placementCount()).map((label, value) => ({ label, value: String(value) }));
      case "repository":
        return repositories.map((item) => ({ label: item.title, detail: item.defaultBranch, value: item.id }));
      case "remote":
        return (find.repositories.get(worktree(index)?.repositoryId)?.remotes ?? []).map((item) => ({ label: item.name, detail: item.fetchUrl, value: item.name }));
      case "placementSource":
        return (catalog.agents ?? []).map((agent) => {
          const workspace = find.workspaces.get(agent.workspaceId)?.title ?? "";
          return { label: agent.title, detail: workspace, search: workspace, value: agent.id };
        });
    }
    return null;
  }

  function current(key) {
    const { kind, index } = split(key);
    switch (kind) {
      case "workspace": return draft.workspaceId;
      case "harness": return harness();
      case "context": return draft.contextAgentId ?? "";
      case "placement": return String(draft.placement);
      case "repository": return worktree(index)?.repositoryId;
      case "remote": return worktree(index)?.remote;
      case "placementSource": return draft.placementAgentId;
    }
    return undefined;
  }

  const titles = {
    workspace: "Select workspace", harness: "Select agent harness", context: "Select conversation context",
    placement: "Select placement type", repository: "Select repository", remote: "Select source remote",
    placementSource: "Select placement source",
  };

  function choose(key, value) {
    const { kind, index } = split(key);
    switch (kind) {
      case "workspace": draft.workspaceId = value; break;
      case "harness": draft.harness = value; break;
      case "context": draft.contextAgentId = value; break;
      case "placement": draft.placement = Number(value); break;
      case "repository": {
        const repository = find.repositories.get(value);
        if (!repository || !worktree(index)) return;
        Object.assign(worktree(index), { repositoryId: repository.id, remote: defaultRemote(repository), ref: repository.defaultBranch });
        break;
      }
      case "remote": if (worktree(index)) worktree(index).remote = value; break;
      case "placementSource": draft.placementAgentId = value; break;
    }
  }

  return {
    kind: "agent",
    action: "agent",
    title: data.fork ? "Fork agent" : "New agent",
    mode: data.fork ? "FORK AGENT" : "NEW AGENT",
    busy: "Creating the agent and its placement…",
    summaryTitle: "ON START",
    status: data.status ?? "",
    draft,
    fields,
    text(key) {
      const { kind, index } = split(key);
      if (kind === "ref") return worktree(index)?.ref ?? "";
      return draft[kind] ?? "";
    },
    setText(key, value) {
      const { kind, index } = split(key);
      if (kind === "ref") {
        if (worktree(index)) worktree(index).ref = value;
      } else if (kind in draft) {
        draft[kind] = value;
      }
    },
    choice(key) {
      const list = options(key);
      if (!list?.length) return null;
      return choiceOf(titles[split(key).kind], list, current(key));
    },
    choose,
    cycle(key, delta) {
      const { kind } = split(key);
      if (kind === "fetch" || kind === "share") return this.toggle(key);
      const list = options(key);
      if (!list?.length) return false;
      if (kind === "repository" && !find.repositories.get(current(key))) {
        choose(key, list[0].value);
        return true;
      }
      choose(key, cycleValue(list, current(key), delta));
      return true;
    },
    toggle(key) {
      const { kind, index } = split(key);
      if (kind === "fetch" && worktree(index)) worktree(index).fetchFirst = !worktree(index).fetchFirst;
      else if (kind === "share") draft.share = !draft.share;
      else return false;
      return true;
    },
    // add appends a secondary repository. It returns an error text, or "".
    add() {
      if (!repositories.length) return "add a repository before you create a worktree placement";
      const repository = repositories[draft.worktrees.length % repositories.length];
      draft.worktrees.push({ repositoryId: repository.id, remote: defaultRemote(repository), ref: repository.defaultBranch, fetchFirst: true });
      return "";
    },
    // remove removes the secondary repository of a field.
    remove(key) {
      const { index } = split(key);
      if (!key.includes(":") || index <= 0 || index >= draft.worktrees.length || draft.placement !== 0) return false;
      draft.worktrees.splice(index, 1);
      return true;
    },
    canAdd: () => draft.placement === 0,
    summary() {
      const effects = [];
      switch (draft.placement) {
        case 0: {
          const count = draft.worktrees.length;
          let text = count === 1 ? "Create a separate worktree on a new agent branch." : `Create ${count} separate worktrees on new agent branches.`;
          if (draft.worktrees.some((item) => item.fetchFirst)) text += " Fetch the marked sources first.";
          effects.push({ label: "Isolated files", text });
          break;
        }
        case 1:
        case 4:
          effects.push(draft.share
            ? { label: "Shared files", text: "Both agents edit the same files. Changes are visible to both; no checkout is created." }
            : { label: "Isolated files", text: "Create private forks of the selected worktrees. Other agents' files stay separate." });
          break;
        case 2:
          effects.push({ label: "Managed directory", text: "Create a private directory without a Git checkout." });
          break;
        case 3:
          effects.push({ label: "Existing files", text: "Work directly in the selected directory. No separate checkout is created." });
          break;
      }
      if (crossHarness()) {
        effects.push({ label: "History conversion", text: `Copy saved history, attachments, and active context into ${find.harness(harness())}. Past tools stay as text. Unavailable content is marked.` });
      } else if (contextAgent()) {
        effects.push({ label: "Conversation copy", text: "Copy the selected agent's conversation. Its later messages stay separate." });
      } else {
        effects.push({ label: "New conversation", text: "No previous messages are copied." });
      }
      effects.push({ label: "Durable agent", text: "Closing the harness does not delete the agent or its placement." });
      return effects;
    },
    payload: () => ({ ...draft, harness: harness() }),
  };
}

function worktreeForm(data) {
  const catalog = data.catalog;
  const draft = clone(data.worktree);
  const find = lookups(catalog);
  const repository = () => find.repositories.get(draft.repositoryId);
  const section = "WORKSPACE / SOURCE";
  const workspaceOptions = () => [{ label: "Create a new workspace", detail: "New", value: "" }]
    .concat((catalog.workspaces ?? []).map((item) => ({ label: item.title, detail: item.status ?? "", value: item.id })));
  const remoteOptions = () => (repository()?.remotes ?? []).map((item) => ({ label: item.name, detail: item.fetchUrl, value: item.name }));
  return {
    kind: "worktree",
    action: "worktree",
    title: "New worktree",
    mode: "NEW WORKTREE",
    busy: "Creating managed worktree…",
    summaryTitle: "ON START",
    status: "",
    draft,
    fields() {
      let workspace = "Create a new workspace";
      if (draft.workspaceId) workspace = find.workspaces.get(draft.workspaceId)?.title ?? "Not available";
      const list = [field("workspace", "choice", section, "Workspace", workspace)];
      if (!draft.workspaceId) list.push(field("workspaceTitle", "text", section, "Task title", draft.workspaceTitle, { placeholder: "required" }));
      list.push(
        field("remote", "choice", section, "Source remote", repository()?.remotes?.length ? draft.remote : "No remotes"),
        field("ref", "text", section, "Source ref", draft.ref, { placeholder: "default branch" }),
        field("fetch", "toggle", section, "Fetch first", draft.fetchFirst ? "Yes" : "No", { on: draft.fetchFirst }),
        field("create", "submit", "", "Open", draft.editor ? "Create worktree and open editor" : "Create worktree and open terminal"),
      );
      return list;
    },
    text: (key) => draft[key] ?? "",
    setText(key, value) {
      if (key === "workspaceTitle" || key === "ref") draft[key] = value;
    },
    choice(key) {
      if (key === "workspace") return choiceOf("Select workspace", workspaceOptions(), draft.workspaceId);
      if (key === "remote" && remoteOptions().length) return choiceOf("Select source remote", remoteOptions(), draft.remote);
      return null;
    },
    choose(key, value) {
      if (key === "workspace") draft.workspaceId = value;
      if (key === "remote") draft.remote = value;
    },
    cycle(key, delta) {
      if (key === "fetch") return this.toggle(key);
      if (key === "workspace") draft.workspaceId = cycleValue(workspaceOptions(), draft.workspaceId, delta);
      else if (key === "remote" && remoteOptions().length) draft.remote = cycleValue(remoteOptions(), draft.remote, delta);
      else return false;
      return true;
    },
    toggle(key) {
      if (key !== "fetch") return false;
      draft.fetchFirst = !draft.fetchFirst;
      return true;
    },
    summary: () => [
      { label: "", text: "Create a checkout from the selected source." },
      { label: "", text: "Open a real terminal." },
      { label: "", text: "No agent will be created." },
    ],
    payload: () => ({ ...draft }),
  };
}

function remoteForm(data) {
  const catalog = data.catalog;
  const draft = clone(data.remote);
  const find = lookups(catalog);
  const repositoryOptions = () => (catalog.repositories ?? []).map((item) => ({ label: item.title, detail: item.defaultBranch, value: item.id }));
  const texts = { name: "name", fetchUrl: "fetchUrl", pushUrl: "pushUrl" };
  return {
    kind: "remote",
    action: "remote",
    title: "Add remote",
    mode: "ADD REMOTE",
    busy: "Adding repository remote…",
    summaryTitle: "ON SAVE",
    status: "",
    draft,
    fields: () => [
      field("repository", "choice", "REMOTE", "Repository", find.repositories.get(draft.repositoryId)?.title ?? "Choose a repository"),
      field("name", "text", "REMOTE", "Name", draft.name, { placeholder: "Remote name" }),
      field("fetchUrl", "text", "REMOTE", "Fetch URL", draft.fetchUrl, { placeholder: "Git SSH or HTTPS URL" }),
      field("pushUrl", "text", "REMOTE", "Push URL", draft.pushUrl, { placeholder: "Optional separate push URL" }),
      field("pushDefault", "toggle", "REMOTE", "Default push", draft.pushDefault ? "Use this remote" : "Keep current", { on: draft.pushDefault }),
      field("save", "submit", "", "Save", "Add remote"),
    ],
    text: (key) => (key in texts ? draft[key] ?? "" : ""),
    setText(key, value) {
      if (key in texts) draft[key] = value;
    },
    choice: (key) => (key === "repository" && repositoryOptions().length ? choiceOf("Select repository", repositoryOptions(), draft.repositoryId) : null),
    choose(key, value) {
      if (key === "repository") draft.repositoryId = value;
    },
    cycle(key, delta) {
      if (key === "pushDefault") return this.toggle(key);
      if (key !== "repository" || !repositoryOptions().length) return false;
      draft.repositoryId = cycleValue(repositoryOptions(), draft.repositoryId, delta);
      return true;
    },
    toggle(key) {
      if (key !== "pushDefault") return false;
      draft.pushDefault = !draft.pushDefault;
      return true;
    },
    summary: () => [
      { label: "", text: "Add a named source to the selected repository. No agent or worktree is created." },
      ...(draft.pushDefault ? [{ label: "", text: "New pushes will use this remote by default." }] : []),
    ],
    payload: () => ({ ...draft }),
  };
}

const SIMPLE = {
  workspace: {
    title: "New workspace", mode: "NEW WORKSPACE", label: "Title", placeholder: "Workspace title", submit: "Create workspace",
    busy: "Creating workspace…", description: "Group related agent and human work.",
    summary: "Create a durable workspace. Repositories and agents are added separately.",
  },
  repository: {
    title: "Add repository", mode: "ADD REPOSITORY", label: "Path or URL", placeholder: "Local path or Git URL", submit: "Add repository",
    busy: "Fetching repository branches…", description: "Use a local path or a Git SSH / HTTPS URL.",
    summary: "Fetch branches into a private bare repository. Existing source files stay in place. No agent is created.",
  },
};

// On a screen, the host adds a repository from a path on the host.
const ON_HOST = {
  repository: (host) => ({ placeholder: `Path on ${host} or Git URL`, description: `Use a path on ${host} or a Git SSH / HTTPS URL.` }),
};

function simpleForm(kind, host) {
  const spec = host && ON_HOST[kind] ? { ...SIMPLE[kind], ...ON_HOST[kind](host) } : SIMPLE[kind];
  const draft = { value: "" };
  return {
    kind,
    action: kind,
    title: spec.title,
    mode: spec.mode,
    busy: spec.busy,
    summaryTitle: "ON CREATE",
    description: spec.description,
    status: "",
    draft,
    fields: () => [
      field("value", "text", "DETAILS", spec.label, draft.value, { placeholder: spec.placeholder, strong: true }),
      field("create", "submit", "", "Create", spec.submit),
    ],
    text: () => draft.value,
    setText(_key, value) {
      draft.value = value;
    },
    choice: () => null,
    choose() {},
    cycle: () => false,
    toggle: () => false,
    summary: () => [{ label: "", text: spec.summary }],
    payload: () => ({ value: trim(draft.value) }),
  };
}

// renameForm gives an agent a new name. data is the agent row: its field
// starts with the current name.
function renameForm(data) {
  const draft = { value: data?.title ?? "" };
  return {
    kind: "rename",
    action: "rename",
    title: "Rename agent",
    mode: "RENAME",
    busy: "Renaming…",
    summaryTitle: "ON RENAME",
    description: "Give this agent a new name. Other agents can have the same name.",
    status: "",
    agentId: data?.id ?? "",
    draft,
    fields: () => [
      field("value", "text", "AGENT", "Name", draft.value, { placeholder: "Agent name", strong: true }),
      field("rename", "submit", "", "Rename", "Rename agent"),
    ],
    text: () => draft.value,
    setText(_key, value) {
      draft.value = value;
    },
    choice: () => null,
    choose() {},
    cycle: () => false,
    toggle: () => false,
    summary: () => [
      { label: "", text: "Galpon and the open tab show the new name. A running Pi agent uses it from its next turn. Codex and Claude Code get it with their next Galpon message." },
      { label: "", text: "Old messages keep the old name." },
    ],
    payload: () => ({ agentId: data?.id ?? "", title: trim(draft.value) }),
  };
}

// createForm makes a form from the output of `galpon herdr-app form`, an
// empty workspace or repository form, or the rename form of an agent row.
// host is the machine that runs Galpon when this computer is a screen, else
// null.
export function createForm(kind, data, host = null) {
  switch (kind) {
    case "agent": return agentForm(data);
    case "worktree": return worktreeForm(data);
    case "remote": return remoteForm(data);
    case "rename": return renameForm(data);
    case "workspace":
    case "repository": return simpleForm(kind, host);
  }
  throw new Error(`unknown form ${kind}`);
}

// The actions of the actions menu. Each has a key, a label and the row kinds
// it works on; "*" is every row and no row.
export const ACTIONS = [
  { key: "m", action: "message", label: "Message", detail: "Send a message to this agent", kinds: ["agent"] },
  { key: "t", action: "terminal", label: "Open terminal", detail: "A real terminal at a worktree", kinds: ["agent", "workspace", "worktree", "repository"] },
  { key: "e", action: "editor", label: "Open editor", detail: "Your $EDITOR at a worktree", kinds: ["agent", "workspace", "worktree", "repository"] },
  { key: "o", action: "operations", label: "Operations", detail: "Observed and reported work of this agent", kinds: ["agent"] },
  { key: "a", action: "new-agent-here", label: "New agent here", detail: "An agent in this workspace", kinds: ["agent", "workspace", "worktree"] },
  { key: "f", action: "fork-agent", label: "Fork agent", detail: "A new agent with this conversation and placement", kinds: ["agent"] },
  { key: "n", action: "rename", label: "Rename", detail: "Give this agent a new name", kinds: ["agent"] },
  { key: "x", action: "hide", label: "Hide", detail: "Hide this item and its dependent items", kinds: ["agent", "workspace", "worktree", "repository"] },
  { key: "w", action: "new-workspace", label: "New workspace", detail: "Group related agent and human work", kinds: ["*"] },
  { key: "r", action: "new-repository", label: "Add repository", detail: "A local path or a Git URL", kinds: ["*"] },
  { key: "R", action: "new-remote", label: "Add remote", detail: "A named source for a repository", kinds: ["*"] },
];

// actionsFor gives the actions of a row. A hidden row can only be unhidden.
export function actionsFor(item) {
  if (item?.hidden) {
    return [{ ...ACTIONS.find((action) => action.action === "hide"), label: "Unhide", detail: "Show this item and its related items again" }];
  }
  return ACTIONS.filter((action) => action.kinds.includes("*") || (item && action.kinds.includes(item.kind)));
}
