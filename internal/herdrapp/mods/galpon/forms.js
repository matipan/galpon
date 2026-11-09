// The forms of the Galpon command center. These functions do not use the app
// or GPUI, so they also run outside the app. They follow the terminal forms:
// the same fields, choices and defaults. Galpon makes the first draft and
// checks the submit, so a draft names resources by ID.
//
// A form gives its fields in order. The field marked strong goes in the
// field at the top of the popup. A field marked more shows only after "More
// options". Each field has a key, a section, a type and the text to show:
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
  // "pi-app", "claude-app" and "codex-app" are agents in a tab of the Herdr
  // app: their harness is Pi, Claude Code or Codex.
  const inTab = () => APP_CHOICES.has(harness());
  const kind = () => (inTab() ? harness().replace(/-app$/, "") : harness());
  const contextAgents = () => (catalog.agents ?? []).filter((agent) => agent.context);
  const contextAgent = () => (draft.contextAgentId ? find.agents.get(draft.contextAgentId) : null);
  const crossHarness = () => Boolean(contextAgent()) && contextAgent().harness !== kind();
  const placementCount = () => (draft.suggestedWorktreeId ? 5 : 4);
  const repositories = catalog.repositories ?? [];
  const worktree = (index) => draft.worktrees[index];

  function fields() {
    const list = [
      field("name", "text", "IDENTITY", "Name", draft.name, {
        placeholder: inTab() ? "Agent name · optional, its first message names it" : "Agent name", strong: true,
      }),
      field("workspace", "choice", "WORKSPACE / CONTEXT", "Workspace", find.workspaces.get(draft.workspaceId)?.title ?? "Not available"),
      field("harness", "choice", "IDENTITY", "Agent", find.harness(harness())),
      field("placement", "choice", "PLACEMENT", "Files", PLACEMENTS[draft.placement] ?? "Choose a placement type"),
    ];
    const extra = [];
    switch (draft.placement) {
      case 0:
        draft.worktrees.forEach((item, index) => {
          const repository = find.repositories.get(item.repositoryId);
          const secondary = index > 0 ? { hint: "d removes this secondary repository", worktree: index, more: true } : { worktree: index };
          let repositoryValue = repository?.title ?? "No longer available";
          if (!repositories.length) repositoryValue = "No repositories";
          let remoteValue = item.remote || "No remotes";
          if (!repository) remoteValue = "No remotes";
          else if (!repository.remotes?.length) remoteValue = "No remotes";
          else if (!repository.remotes.some((remote) => remote.name === item.remote)) remoteValue = "No longer available";
          const into = index === 0 ? list : extra;
          into.push(field(`repository:${index}`, "choice", "PLACEMENT", index === 0 ? "Repository" : "Second repository", repositoryValue, secondary));
          extra.push(
            field(`remote:${index}`, "choice", "PLACEMENT", "Source remote", remoteValue, { ...secondary, more: true }),
            field(`ref:${index}`, "text", "PLACEMENT", "Source ref", item.ref, { placeholder: "default branch", ...secondary, more: true }),
            field(`fetch:${index}`, "toggle", "PLACEMENT", "Fetch first", item.fetchFirst ? "Yes" : "No", { on: item.fetchFirst, ...secondary, more: true }),
          );
        });
        extra.push(field("add", "action", "PLACEMENT", "", "Add another repository", { more: true }));
        break;
      case 1: {
        const source = find.agents.get(draft.placementAgentId);
        let value = source?.title ?? "No longer available";
        if (!(catalog.agents ?? []).length) value = "No agents";
        list.push(field("placementSource", "choice", "PLACEMENT", "From agent", value), shareField());
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
    // A fork shows its context: it is the reason of the form.
    list.push(field("context", "choice", "WORKSPACE / CONTEXT", "Context", contextLabel(), {
      hint: crossHarness() ? "Copies saved history, images, and active context. Past tools stay as text." : "",
      more: !data.fork,
    }));
    list.push(field("role", "text", "IDENTITY", "Role", draft.role, { placeholder: "optional", more: true }));
    list.push(...extra);
    list.push(field("create", "submit", "", "Create", `Create and open ${find.harness(harness())}`));
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
        return (catalog.harnesses ?? []).map((item) => ({ label: item.label, detail: HARNESS_DETAILS[item.value] ?? "Permanent for this agent", value: item.value }));
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
    busy: "Creating the agent and its placement…",
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
    // An agent in a tab can start without a name: the tab names it after
    // its first message.
    payload: () => ({ ...draft, name: draft.name?.trim() || (inTab() ? PROGRAM_NAMES[kind()] ?? "Pi" : draft.name), harness: harness() }),
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
    title: draft.editor ? "New worktree in the editor" : "New worktree",
    subtitle: find.repositories.get(draft.repositoryId)?.title ?? "",
    busy: "Creating managed worktree…",
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
        field("create", "submit", "", "Create", draft.editor ? "Create the worktree and open the editor" : "Create the worktree and open a terminal"),
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
    busy: "Adding repository remote…",
    status: "",
    draft,
    fields: () => [
      field("name", "text", "REMOTE", "Name", draft.name, { placeholder: "Remote name", strong: true }),
      field("repository", "choice", "REMOTE", "Repository", find.repositories.get(draft.repositoryId)?.title ?? "Choose a repository"),
      field("fetchUrl", "text", "REMOTE", "Fetch URL", draft.fetchUrl, { placeholder: "Git SSH or HTTPS URL" }),
      field("pushUrl", "text", "REMOTE", "Push URL", draft.pushUrl, { placeholder: "Optional separate push URL" }),
      field("pushDefault", "toggle", "REMOTE", "Default push", draft.pushDefault ? "Use this remote" : "Keep current", { on: draft.pushDefault }),
      field("save", "submit", "", "Add", "Add remote"),
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
    payload: () => ({ ...draft }),
  };
}

const SIMPLE = {
  workspace: {
    title: "New workspace", label: "Title", placeholder: "Workspace title", verb: "Create", submit: "Create workspace",
    busy: "Creating workspace…", description: "Group related agent and human work.",
  },
  repository: {
    title: "Add repository", label: "Path or URL", placeholder: "Local path or Git URL", verb: "Add", submit: "Add repository",
    busy: "Fetching repository branches…", description: "Use a local path or a Git SSH / HTTPS URL.",
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
    busy: spec.busy,
    description: spec.description,
    status: "",
    draft,
    fields: () => [
      field("value", "text", "DETAILS", spec.label, draft.value, { placeholder: spec.placeholder, strong: true }),
      field("create", "submit", "", spec.verb, spec.submit),
    ],
    text: () => draft.value,
    setText(_key, value) {
      draft.value = value;
    },
    choice: () => null,
    choose() {},
    cycle: () => false,
    toggle: () => false,
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
    busy: "Renaming…",
    description: "Give this agent a new name. Other agents can have the same name.",
    status: "",
    agentId: data?.id ?? "",
    draft,
    fields: () => [
      field("value", "text", "AGENT", "Name", draft.value, { placeholder: "Agent name", strong: true }),
      field("rename", "submit", "", "Rename", "Rename the agent"),
    ],
    text: () => draft.value,
    setText(_key, value) {
      draft.value = value;
    },
    choice: () => null,
    choose() {},
    cycle: () => false,
    toggle: () => false,
    payload: () => ({ agentId: data?.id ?? "", title: trim(draft.value) }),
  };
}

// hideForm asks before it hides or unhides a row. data is the row.
function hideForm(data) {
  const verb = data?.hidden ? "Unhide" : "Hide";
  const kind = data?.kind === "pull-request" ? "pull request" : data?.kind ?? "item";
  return {
    kind: "hide",
    action: "hide",
    title: verb,
    subtitle: data?.title ?? "",
    busy: data?.hidden ? `Unhiding ${data?.title}…` : `Hiding ${data?.title}…`,
    description: data?.hidden
      ? `The ${kind} and its related items show again in Galpon.`
      : `Galpon stops showing this ${kind} and the items that depend on it. Their files stay, and you can unhide them later.`,
    status: "",
    draft: {},
    fields: () => [field("hide", "submit", "", verb, `${verb} ${data?.title ?? ""}`)],
    text: () => "",
    setText() {},
    choice: () => null,
    choose() {},
    cycle: () => false,
    toggle: () => false,
    payload: () => ({ kind: data?.kind, id: data?.id, title: data?.title, hidden: Boolean(data?.hidden) }),
  };
}

// createForm makes a form from the output of `galpon herdr-app form`, an
// empty workspace or repository form, the rename form of an agent row, or
// the hide form of a row.
// host is the machine that runs Galpon when this computer is a screen, else
// null.
export function createForm(kind, data, host = null) {
  switch (kind) {
    case "agent": return agentForm(data);
    case "worktree": return worktreeForm(data);
    case "remote": return remoteForm(data);
    case "rename": return renameForm(data);
    case "hide": return hideForm(data);
    case "workspace":
    case "repository": return simpleForm(kind, host);
  }
  throw new Error(`unknown form ${kind}`);
}

// APP_CHOICES are the harness choices of agents in a tab of the Herdr app.
const APP_CHOICES = new Set(["pi-app", "claude-app", "codex-app"]);
// PROGRAM_NAMES name the program of a harness for people.
const PROGRAM_NAMES = { pi: "Pi", claude: "Claude", codex: "Codex" };
// HARNESS_DETAILS say where an agent of each harness choice works.
const HARNESS_DETAILS = {
  "pi-app": "Pi in a tab of the Herdr app",
  "claude-app": "Claude Code in a tab of the Herdr app",
  "codex-app": "Codex in a tab of the Herdr app",
  pi: "Pi in a terminal",
  claude: "Claude Code in a terminal",
  codex: "Codex in a terminal",
};
