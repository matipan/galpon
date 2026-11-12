// The list screen of the Galpon popup. main.js gives these methods to the
// Galpon view.
//
// One search field and one list, without tabs and without a side pane. With
// an empty search, the list shows what needs the person, then the recent
// agents. A row that needs the person has a second line that tells why: the
// question of the agent, or the first line of its reply. Tab opens a peek
// below the selected agent: rows with its question and each answer, its last
// reply and the last message to it. The arrows move onto an answer, and
// Enter or a click gives it. Ctrl+Enter replies to the agent there. A new
// agent needs only its name: it opens, and the person writes the task in it.
// Ctrl+K shows all the actions of the selected row.

import { div } from "gpui-kit";
import { h_flex, v_flex, Input, InputState, v_virtual_list, Scrollbar } from "gpui-base";
import { agents, focused, notify, panes, props, shared, spaces, terminals } from "herdr";
import { buildWorkspaces } from "./sidebar.js";
import { agentState, buildRows, duration, excerpt, fuzzyScore, normalize, pullRequestAgent, pullRequestLookupKey, stateWords } from "./model.js";
import { S, MONO, icon, kbd, mark, toneColor } from "./look.js";

// The sidebar shares the turns of the agents and its Galpon facts.
const TURNS = "galpon.sidebar.turns";
const FACTS = "galpon.sidebar.facts";

const ROW = 34;
const TWO_LINES = 50;
const HEADING = 30;
// The line heights of the text rows of a peek.
const PEEK_LINE = { ui: 21, meta: 19 };
// A brief older than this is read again when its agent shows.
const BRIEF_AGE = 4000;

const message = (error) => String(error?.message ?? error);
const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");
const oneLine = (text) => String(text ?? "").replace(/\s+/g, " ").trim();

// The tone of a live state, for its mark.
function toneOf(state) {
  switch (state.state) {
    case "needs": return "need";
    case "failed": return "fail";
    case "ready": return "done";
    case "running":
    case "delegating":
    case "background": return "run";
    case "pending": return "pending";
    default: return "idle";
  }
}

export const ListMethods = {
  listInit(cx) {
    this.scope = null;
    this.groups = new Map();
    this.homeOrder = new Map();
    this.briefs = new Map();
    this.reply = null;
    this.answering = null;
    this.actions = null;
    this.peek = null;
    this.firstBriefs = false;
    this.prLookup = null;
    this.replyInput = InputState.new({ placeholder: "Reply…" });
    this.replyInput.on("submit", (_e, cx) => this.enter(cx));
    this.answerInput = InputState.new({ placeholder: "Your answer…" });
    this.answerInput.on("submit", (_e, cx) => this.enter(cx));
    this.actionInput = InputState.new({ placeholder: "Search actions…" });
    this.actionInput.on("change", (_e, cx) => {
      if (!this.actions) return;
      this.actions.cursor = 0;
      cx.notify();
    });
    this.actionInput.on("submit", (_e, cx) => this.enter(cx));
  },

  // --- data

  // live gives the live state of each agent that has a tab in the app, as
  // the sidebar shows it.
  live() {
    let rows = [];
    try {
      rows = buildWorkspaces({
        spaces: spaces(), panes: panes(), terminals: terminals(), agents: agents(), focused: focused(),
        facts: shared(FACTS) ?? null, host: this.hostMachine,
      }).flatMap((workspace) => workspace.rows);
    } catch {
      rows = [];
    }
    const turns = new Map((shared(TURNS)?.turns ?? []).map((turn) => [turn.key, turn]));
    const out = new Map();
    for (const row of rows) {
      if (row.kind !== "agent" || !row.agentId || out.has(row.agentId)) continue;
      out.set(row.agentId, {
        state: row.state, fresh: Boolean(turns.get(row.key)?.fresh), changedAt: row.changedAt ?? null,
        workingSince: row.workingSince ?? null, focused: Boolean(row.focused),
      });
    }
    return out;
  },

  listRows() {
    // The list without a search keeps its own order, so that it is the same
    // after a search or a scope.
    const home = !this.scope && !normalize(this.query);
    const live = this.live();
    const rows = buildRows(this.data, {
      query: this.query, scope: this.scope, expanded: this.expanded, order: home ? this.homeOrder : this.order, groups: this.groups, hidden: this.hidden,
      prLookup: this.prLookup,
    }, live);
    if (!this.peek) return rows;
    const at = rows.findIndex((row) => row.type === "item" && row.item.kind === "agent" && row.item.id === this.peek);
    if (at < 0) {
      this.peek = null;
      this.reply = null;
      this.answering = null;
      return rows;
    }
    rows.splice(at + 1, 0, ...this.peekRows(rows[at].item, agentState(rows[at].item, live)));
    return rows;
  },

  // Debounce typed numbers, and discard replies for a superseded lookup.
  lookupPullRequestCreators(cx) {
    const key = this.scope ? "" : pullRequestLookupKey(this.query);
    if (!key) {
      this.prLookup = null;
      return;
    }
    const known = this.prLookup;
    if (known?.key === key && (known.loading || Date.now() - known.at < 3000)) return;
    const lookup = { key, loading: true, at: 0, items: known?.key === key ? known.items : null, error: "" };
    this.prLookup = lookup;
    cx.timer.after(120, (cx) => {
      if (this.prLookup !== lookup || this.scope || pullRequestLookupKey(this.query) !== key) return;
      cx.spawn(async (cx) => {
        try {
          const out = await this.galpon(["pr-creators", key]);
          lookup.error = out.error ?? "";
          lookup.items = out.error ? null : out.items;
        } catch (error) {
          lookup.error = message(error);
          lookup.items = null;
        }
        lookup.loading = false;
        lookup.at = Date.now();
        if (this.prLookup !== lookup || this.scope || pullRequestLookupKey(this.query) !== key) return;
        this.refreshRows();
        this.briefSelected(cx);
        cx.notify();
      });
    });
  },

  // peekRows gives the rows of the peek of an agent: its question and its
  // answers, the reply field, its last reply and the last message to it.
  peekRows(item, current) {
    const known = this.briefs.get(item.id);
    const brief = known?.data ?? null;
    const id = item.id;
    const rows = [];
    const add = (type, extra) => rows.push({ type, key: `peek:${id}:${type}:${rows.length}`, item, ...extra });
    const perLine = Math.max(30, Math.floor((this.listWidth() - 60) / 7));
    const lines = (text, max) => Math.min(max, String(text ?? "").split("\n").reduce((count, line) => count + Math.max(1, Math.ceil(line.length / perLine)), 0));
    const dialog = brief?.dialogs?.[0];
    if (dialog) {
      const target = { agentId: id, title: item.title, dialogId: dialog.id };
      add("peek-label", { text: "Asks you", tone: "need" });
      add("peek-title", { text: oneLine(dialog.title ?? "A question") });
      if (dialog.message) {
        const text = excerpt(dialog.message, 5);
        add("peek-text", { text, size: "meta", height: lines(text, 6) * PEEK_LINE.meta + 4 });
      }
      if (dialog.method === "select") {
        (dialog.options ?? []).forEach((value, index) => add("peek-option", { label: String(value), index, target, answer: { value } }));
      } else if (dialog.method === "confirm") {
        add("peek-option", { label: "Yes", index: 0, target, answer: { confirmed: true } });
        add("peek-option", { label: "No", index: 1, target, answer: { confirmed: false } });
      } else {
        add("peek-input", { target });
      }
    } else if (current.state === "needs" && known && !known.loading) {
      add("peek-label", { text: "Needs you", tone: "need" });
      add("peek-text", { text: item.view === "app" ? "It waits for you in its tab. Enter opens it." : "It asks you something in its terminal. Enter opens it.", size: "ui", height: PEEK_LINE.ui + 4 });
    }
    if (this.reply?.agentId === id) add("peek-reply", {});
    if (brief?.reply) {
      add("peek-label", { text: brief.replyAt ? `Last reply · ${duration(Date.now() - brief.replyAt)} ago` : "Last reply" });
      const text = excerpt(brief.reply, 8);
      add("peek-text", { text, size: "ui", height: lines(text, 10) * PEEK_LINE.ui + 4 });
    }
    if (brief?.prompt) {
      const from = brief.from ? `Message from ${brief.from}` : "Your last message";
      add("peek-label", { text: brief.promptAt ? `${from} · ${duration(Date.now() - brief.promptAt)} ago` : from });
      const text = excerpt(brief.prompt, 2);
      add("peek-text", { text, size: "meta", height: lines(text, 3) * PEEK_LINE.meta + 4 });
    }
    if (item.delegatedCount > 0) add("peek-note", { text: `${item.delegatedCount} delegated ${item.delegatedCount === 1 ? "agent" : "agents"} · Ctrl K shows them` });
    if (!brief && known?.loading !== false) add("peek-note", { text: "Reading the conversation…" });
    if (known?.error) add("peek-note", { text: sentence(known.error), tone: "fail" });
    if (brief && !brief.reply && !brief.prompt && !dialog) add("peek-note", { text: "No messages yet." });
    add("peek-end", {});
    return rows;
  },

  briefOf(agentId) {
    return this.briefs.get(agentId)?.data ?? null;
  },

  // loadBrief reads the last reply, the last message and the open questions
  // of an agent.
  loadBrief(agentId, cx, force = false) {
    if (!agentId) return;
    const known = this.briefs.get(agentId);
    if (known?.loading || (!force && known && Date.now() - known.at < BRIEF_AGE)) return;
    this.briefs.set(agentId, { ...(known ?? {}), loading: true, at: known?.at ?? 0 });
    cx.spawn(async (cx) => {
      let data = known?.data ?? null;
      let error = "";
      try {
        const out = await this.galpon(["brief", agentId]);
        if (out.error) error = out.error;
        else data = out;
      } catch (err) {
        error = message(err);
      }
      this.briefs.set(agentId, { data, error, at: Date.now(), loading: false });
      if (this.answering?.agentId === agentId && !(data?.dialogs ?? []).some((dialog) => dialog.id === this.answering.dialogId)) this.answering = null;
      if (this.peek === agentId && this.screen === "list") this.refreshRows();
      cx.notify();
    });
  },

  // Refresh the active PR lookup and the briefs needed by the list.
  afterLoad(cx) {
    this.lookupPullRequestCreators(cx);
    for (const row of this.rows) {
      if (row.type === "item" && row.item.kind === "agent" && (row.group === "needs" || row.item.id === this.peek)) this.loadBrief(row.item.id, cx);
    }
    const item = this.selectedItem();
    if (item?.kind === "agent") this.loadBrief(item.id, cx);
  },

  // --- keys

  listKeyDown(event, cx) {
    if (this.busy || event.modifiers?.control || event.modifiers?.alt || event.modifiers?.platform) return false;
    if (this.screen === "list" && event.key === "backspace" && this.scope && !this.input.value() && !this.reply && !this.actions && !this.answering) {
      cx.stop_propagation();
      this.leaveScope(cx);
      cx.notify();
      return true;
    }
    return false;
  },

  listKey(name, cx) {
    if (this.actions) return this.actionsKey(name, cx);
    if (this.reply) return this.replyKey(name, cx);
    if (this.answering) return this.answerKey(name, cx);
    const row = this.rows[this.cursor];
    const item = this.selectedItem();
    switch (name) {
      case "up": return this.move(-1, cx);
      case "down": return this.move(1, cx);
      case "pageup": return this.move(-10, cx);
      case "pagedown": return this.move(10, cx);
      case "tab": return this.expand(cx);
      case "shift-tab": return this.scope ? this.leaveScope(cx) : undefined;
      case "enter": return this.activate(cx);
      case "ctrl-enter":
        if (item?.kind === "agent") return this.replyOrAnswer(item, cx);
        if (row?.type === "launch") return this.activate(cx);
        if (row?.type?.startsWith("peek")) return this.replyOrAnswer(row.item, cx);
        return undefined;
      case "ctrl-k": return this.openActions(row, cx);
      case "ctrl-n": return this.newAgent(normalize(this.query) && !this.scope ? this.input.value() : "", this.newAgentWorkspace(), cx);
      case "ctrl-t":
        if (item && item.kind !== "pull-request") return this.perform("terminal", item, cx);
        return undefined;
      case "ctrl-e":
        if (item && item.kind !== "pull-request") return this.perform("editor", item, cx);
        return undefined;
      case "ctrl-o":
        if (item?.kind === "pull-request") this.browse(item.url, cx);
        return undefined;
      case "alt-p":
        if (this.scope?.kind === "pull-requests") return this.leaveScope(cx);
        return this.enterScope({ kind: "pull-requests", title: "Pull requests" }, cx);
      case "ctrl-r": return this.scope?.kind === "pull-requests" ? this.runCommand("refresh", cx) : this.resetOrder(cx);
      case "ctrl-h": return this.toggleHidden(cx);
      case "ctrl-f":
        if (item?.kind === "agent") return this.perform("fork-agent", item, cx);
        return undefined;
      case "escape":
        if (this.peek) return this.closePeek(cx);
        if (this.scope) return this.leaveScope(cx);
        return this.close();
    }
    return undefined;
  },

  // resetOrder sorts the list again: the rows of the agents that changed
  // their state move to their new groups.
  resetOrder(cx) {
    this.order = new Map();
    this.homeOrder = new Map();
    this.groups = new Map();
    this.refreshRows();
    this.load(cx);
  },

  toggleHidden(cx) {
    this.hidden = !this.hidden;
    this.say(this.hidden ? "Hidden items show. Unhide one from its actions." : "Hidden items no longer show.", "info");
    this.load(cx);
  },

  // --- the list

  activate(cx) {
    const row = this.rows[this.cursor];
    if (!row || this.busy) return;
    switch (row.type) {
      case "more": return this.expand(cx);
      case "scope": return this.enterScope(row.scope, cx);
      case "launch": return this.newAgent(row.text, row.workspaceId ?? this.newAgentWorkspace(), cx);
      case "peek-option": return this.sendAnswer(row.target, row.answer, cx);
      case "peek-input": return this.startAnswer(row.item, cx);
      case "item": break;
      default: return;
    }
    const item = row.item;
    if (item.hidden) return this.say(`${item.title} is hidden. Unhide it from its actions (Ctrl+K).`, "warn");
    switch (item.kind) {
      case "agent": return this.openAgent(item, cx);
      case "pull-request": {
        const agent = pullRequestAgent(this.data, item);
        return agent ? this.openAgent(agent, cx) : this.browse(item.url, cx);
      }
      case "workspace": return this.enterScope({ kind: "workspace", id: item.id, title: item.title }, cx);
      case "worktree": return this.openTargets(item, false, cx);
      case "repository": return this.perform("terminal", item, cx);
      case "command": return this.runCommand(item.id, cx);
    }
  },

  // expand is Tab: it opens or closes the peek of an agent, shows the
  // agents of a workspace, or shows more rows.
  expand(cx) {
    const row = this.rows[this.cursor];
    if (!row) return;
    if (row.type === "scope") return this.enterScope(row.scope, cx);
    if (row.type === "item" && row.item.kind === "workspace") return this.enterScope({ kind: "workspace", id: row.item.id, title: row.item.title }, cx);
    if (row.type.startsWith("peek")) return this.closePeek(cx);
    if (row.type === "item" && row.item.kind === "agent") {
      if (this.peek === row.item.id) return this.closePeek(cx);
      this.openPeek(row.item, cx);
      return;
    }
    if (row.type === "more") this.toggleExpanded(row.key, cx);
  },

  // inPeek tells if a row is the open peek or its agent.
  inPeek(row) {
    return Boolean(row) && (row.type.startsWith("peek") || (row.type === "item" && row.item.kind === "agent" && row.item.id === this.peek));
  },

  openPeek(item, cx) {
    this.peek = item.id;
    this.loadBrief(item.id, cx);
    this.refreshRows();
    this.scroller.scroll_to_item(this.cursor);
  },

  // closePeek closes the peek, and its agent stays selected.
  closePeek(cx) {
    const id = this.peek;
    if (this.reply) this.saveDraft(this.reply.agentId, this.replyInput.value());
    this.peek = null;
    this.reply = null;
    this.answering = null;
    this.selected = `agent:${id}`;
    this.refreshRows();
    this.input.focus();
  },

  toggleExpanded(key, cx) {
    if (this.expanded.has(key)) this.expanded.delete(key);
    else this.expanded.add(key);
    this.refreshRows();
    this.briefSelected(cx);
  },

  enterScope(scope, cx) {
    this.scope = scope;
    this.prLookup = null;
    this.input.set_value("");
    this.query = "";
    this.order = new Map();
    this.cursor = 0;
    this.selected = "";
    this.status = null;
    this.refreshRows();
    this.scroller.scroll_to_item(0);
    this.briefSelected(cx);
    this.input.focus();
  },

  leaveScope(cx) {
    const left = this.scope;
    this.scope = null;
    this.prLookup = null;
    this.input.set_value("");
    this.query = "";
    this.order = new Map();
    this.refreshRows();
    // The workspace that the person left stays selected.
    const key = left?.kind === "workspace" ? `workspace:${left.id}` : left ? `scope-${left.kind}` : "";
    const index = this.rows.findIndex((row) => row.key === key);
    this.select(Math.max(0, index >= 0 ? index : this.nearestSelectable(0)), cx);
    this.input.focus();
  },

  runCommand(id, cx) {
    switch (id) {
      case "new-agent": return this.newAgent("", this.newAgentWorkspace(), cx);
      case "pull-requests": return this.enterScope({ kind: "pull-requests", title: "Pull requests" }, cx);
      case "workspaces": return this.enterScope({ kind: "workspaces", title: "Workspaces" }, cx);
      case "hidden": return this.toggleHidden(cx);
      case "refresh":
        this.busy = "Reading the pull requests from GitHub…";
        cx.spawn(async (cx) => {
          try {
            const out = await this.galpon(["refresh"]);
            this.busy = "";
            if (out.error) this.say(sentence(out.error), "error");
            else this.say("The pull requests are up to date.", "info");
          } catch (error) {
            this.busy = "";
            this.say(message(error), "error");
          }
          this.load(cx);
          cx.notify();
        });
        return undefined;
      default: return this.perform(id, null, cx);
    }
  },

  // newAgentWorkspace is the workspace of a new agent: the open scope, else
  // the workspace that the window shows.
  newAgentWorkspace() {
    if (this.scope?.kind === "workspace") return this.scope.id;
    const space = props()?.space || focused()?.space || "";
    const id = this.hostId(space);
    return id?.startsWith("galpon-") ? id.slice("galpon-".length) : "";
  },

  // --- the actions panel

  openActions(row, cx) {
    if (!row || row.type === "header" || row.type === "empty") row = null;
    this.actions = { row, cursor: 0 };
    this.actionInput.set_value("");
    this.actionInput.focus();
  },

  closeActions() {
    this.actions = null;
    this.input.focus();
  },

  // actionList gives the actions of a row, then the Galpon commands.
  actionList(row) {
    const item = row?.type === "item" ? row.item : null;
    const out = [];
    const add = (id, label, keys, run, detail = "") => out.push({ id, label, keys, run, detail });
    if (item?.hidden) {
      add("unhide", "Unhide", "", (cx) => this.perform("hide", item, cx), "Show this item again");
    } else if (item?.kind === "agent") {
      const brief = this.briefOf(item.id);
      const asks = (brief?.dialogs ?? []).length > 0;
      add("open", "Open", "↵", (cx) => this.activate(cx));
      if (asks) add("answer", "Answer", "Ctrl ↵", (cx) => this.startAnswer(item, cx));
      add("reply", "Reply", asks ? "" : "Ctrl ↵", (cx) => this.startReply(item, cx));
      add("terminal", "Open terminal", "Ctrl T", (cx) => this.perform("terminal", item, cx), "A terminal at its worktree");
      add("editor", "Open editor", "Ctrl E", (cx) => this.perform("editor", item, cx), "Your $EDITOR at its worktree");
      add("new-agent-here", "New agent in this workspace", "", (cx) => this.newAgent("", item.workspaceId, cx));
      add("fork-agent", "Fork agent", "Ctrl F", (cx) => this.perform("fork-agent", item, cx), "A new agent with this conversation");
      add("rename", "Rename", "", (cx) => this.perform("rename", item, cx));
      add("switch-view", item.view === "app" ? "Open in a terminal" : "Open in a tab", "", (cx) => this.perform("switch-view", item, cx));
      if (item.delegatedCount > 0) add("delegated", this.expanded.has(`agent:${item.id}`) ? "Hide delegated agents" : "Show delegated agents", "", (cx) => this.toggleExpanded(`agent:${item.id}`, cx));
      add("hide", "Hide", "", (cx) => this.perform("hide", item, cx));
    } else if (item?.kind === "pull-request") {
      if (pullRequestAgent(this.data, item)) add("open", "Open its agent", "↵", (cx) => this.activate(cx));
      add("github", "Open on GitHub", "Ctrl O", (cx) => this.browse(item.url, cx));
    } else if (item?.kind === "workspace") {
      add("agents", "Show its agents", "↵", (cx) => this.activate(cx));
      add("new-agent-here", "New agent here", "", (cx) => this.newAgent("", item.id, cx));
      add("terminal", "Open terminal", "Ctrl T", (cx) => this.perform("terminal", item, cx));
      add("editor", "Open editor", "Ctrl E", (cx) => this.perform("editor", item, cx));
      add("hide", "Hide", "", (cx) => this.perform("hide", item, cx));
    } else if (item?.kind === "worktree") {
      add("terminal", "Open terminal", "↵", (cx) => this.perform("terminal", item, cx));
      add("editor", "Open editor", "Ctrl E", (cx) => this.perform("editor", item, cx));
      add("new-agent-here", "New agent here", "", (cx) => this.perform("new-agent-here", item, cx));
      add("hide", "Hide", "", (cx) => this.perform("hide", item, cx));
    } else if (item?.kind === "repository") {
      add("terminal", "New worktree", "↵", (cx) => this.perform("terminal", item, cx), "A checkout and a terminal in it");
      add("editor", "New worktree in the editor", "Ctrl E", (cx) => this.perform("editor", item, cx));
      add("new-remote", "Add remote", "", (cx) => this.perform("new-remote", item, cx));
      add("hide", "Hide", "", (cx) => this.perform("hide", item, cx));
    }
    const own = out.length;
    add("new-agent", "New agent", "Ctrl N", (cx) => this.newAgent("", this.newAgentWorkspace(), cx));
    add("pull-requests", "Pull requests", "Alt P", (cx) => this.enterScope({ kind: "pull-requests", title: "Pull requests" }, cx));
    add("new-workspace", "New workspace", "", (cx) => this.perform("new-workspace", null, cx));
    add("new-repository", "Add repository", "", (cx) => this.perform("new-repository", null, cx));
    add("hidden", this.hidden ? "Hide hidden items" : "Show hidden items", "Ctrl H", (cx) => this.toggleHidden(cx));
    add("refresh", "Refresh pull requests", "", (cx) => this.runCommand("refresh", cx));
    for (const action of out.slice(own)) action.galpon = true;
    return out;
  },

  filteredActions() {
    const list = this.actionList(this.actions?.row);
    const query = this.actionInput.value();
    if (!normalize(query)) return list;
    return list.map((action) => ({ action, score: fuzzyScore(action.label, query) }))
      .filter((match) => match.score >= 0).sort((a, b) => b.score - a.score).map((match) => match.action);
  },

  actionsKey(name, cx) {
    const list = this.filteredActions();
    switch (name) {
      case "up":
        this.actions.cursor = Math.max(0, this.actions.cursor - 1);
        return undefined;
      case "down":
        this.actions.cursor = Math.min(list.length - 1, this.actions.cursor + 1);
        return undefined;
      case "enter": {
        const action = list[this.actions.cursor];
        if (!action) return undefined;
        this.closeActions();
        return action.run(cx);
      }
      case "escape":
      case "ctrl-k":
        return this.closeActions();
    }
    return undefined;
  },

  // --- reply and answer

  replyOrAnswer(item, cx) {
    const dialog = this.briefOf(item.id)?.dialogs?.[0];
    if (dialog) return this.startAnswer(item, cx);
    return this.startReply(item, cx);
  },

  // focusAgent selects the row of an agent.
  focusAgent(agentId, cx) {
    const index = this.rows.findIndex((row) => row.type === "item" && row.item.kind === "agent" && row.item.id === agentId);
    if (index >= 0) {
      this.cursor = index;
      this.selected = this.rows[index].key;
      this.scroller.scroll_to_item(index);
    }
  },

  startReply(item, cx) {
    this.answering = null;
    this.reply = { agentId: item.id, title: item.title };
    this.peek = item.id;
    this.loadBrief(item.id, cx);
    this.refreshRows();
    this.focusAgent(item.id, cx);
    this.replyInput.set_value(this.drafts[item.id] ?? "");
    this.replyInput.focus();
  },

  replyKey(name, cx) {
    if (name === "enter") return this.sendReply(cx);
    if (name !== "escape") return undefined;
    this.saveDraft(this.reply.agentId, this.replyInput.value());
    // The sidebar menu opened the popup for this reply.
    if (this.startOrigin === "close") return this.close();
    this.reply = null;
    this.refreshRows();
    this.input.focus();
    return undefined;
  },

  sendReply(cx) {
    const target = this.reply;
    const text = this.replyInput.value();
    if (!target || !text.trim() || this.busy) return;
    this.saveDraft(target.agentId, text);
    this.busy = `Sending to ${target.title}`;
    cx.notify();
    cx.spawn(async (cx) => {
      let result;
      try {
        result = await this.start(cx, "reply", { agentId: target.agentId, text });
      } catch (error) {
        result = { error: message(error) };
      }
      this.busy = "";
      if (result.error) {
        this.say(sentence(result.error), "error");
      } else {
        this.saveDraft(target.agentId, "");
        this.replyInput.set_value("");
        if (this.startOrigin === "close") {
          notify({ title: `Sent to ${target.title}`, level: "success" });
          return this.close();
        }
        if (this.reply === target) this.reply = null;
        this.refreshRows();
        this.say(result.message === "Sent" ? `Sent to ${target.title}.` : sentence(result.message ?? "Sent."), "info");
        this.input.focus();
        cx.timer.after(1500, (cx) => this.loadBrief(target.agentId, cx, true));
      }
      cx.notify();
    });
  },

  // startAnswer puts the cursor on the first answer of the question of an
  // agent; a question that wants text gets the answer field.
  startAnswer(item, cx) {
    const dialog = this.briefOf(item.id)?.dialogs?.[0];
    if (!dialog) return this.startReply(item, cx);
    this.reply = null;
    this.peek = item.id;
    this.refreshRows();
    if (dialog.method === "input" || dialog.method === "editor") {
      this.answering = { agentId: item.id, title: item.title, dialogId: dialog.id };
      this.focusAgent(item.id, cx);
      this.answerInput.set_value("");
      this.answerInput.focus();
      return undefined;
    }
    const index = this.rows.findIndex((row) => row.type === "peek-option" && row.item.id === item.id);
    if (index >= 0) this.select(index, cx);
    return undefined;
  },

  answerKey(name, cx) {
    const target = this.answering;
    if (name === "enter") return this.sendAnswer(target, { value: this.answerInput.value() }, cx);
    if (name === "escape") {
      this.answering = null;
      this.input.focus();
    }
    return undefined;
  },

  sendAnswer(target, answer, cx) {
    if (!target || this.busy) return;
    this.busy = `Answering ${target.title}`;
    cx.notify();
    cx.spawn(async (cx) => {
      let result;
      try {
        result = await this.start(cx, "answer", { agentId: target.agentId, id: target.dialogId, ...answer });
      } catch (error) {
        result = { error: message(error) };
      }
      this.busy = "";
      if (this.answering?.dialogId === target.dialogId) this.answering = null;
      if (result.error) this.say(sentence(result.error), "error");
      else this.say(`Answered ${target.title}.`, "info");
      this.input.focus();
      this.focusAgent(target.agentId, cx);
      this.loadBrief(target.agentId, cx, true);
      cx.notify();
    });
  },

  // --- a new agent

  // newAgent opens the New Agent form with a name. The agent opens when the
  // form is sent, and the person writes its task there.
  newAgent(name, workspaceId, cx) {
    if (this.busy) return;
    const args = workspaceId ? ["agent", "new", "workspace", workspaceId] : ["agent", "new", "", ""];
    this.openForm(cx, "agent", args, { name: name?.trim() ?? "" });
  },

  // --- drawing

  listScreen(root) {
    const live = this.live();
    return root
      .child(this.jumpSearchBar())
      .child(this.jumpList(live))
      .child(this.jumpFooter(this.rows[this.cursor]))
      .when(Boolean(this.actions), (el) => el.child(this.actionsPanel()));
  },

  jumpSearchBar() {
    const P = this.P;
    const scope = this.scope;
    return h_flex().id("gj-search").h(50).flex_none().px(16).gap(10).items_center().border_b(1).border_color(P.line)
      .child(this.loading && !this.data ? icon("spinner", 15).text_color(P.text2).spin(900) : icon("search", 15).text_color(P.text3))
      .when(Boolean(scope), (el) => el.child(h_flex().id("gj-scope").h(24).px(8).gap(6).flex_none().items_center().rounded(6).bg(P.raised).cursor_pointer()
        .hover((s) => s.bg(P.raised2))
        .on_click((_e, cx) => {
          this.leaveScope(cx);
          cx.notify();
        })
        .child(icon(scope.kind === "pull-requests" ? "git-compare" : "layers", 12).text_color(P.text2))
        .child(div().text_size(S.ui).text_color(P.text).child(scope.title))
        .child(icon("x", 11).text_color(P.text3))))
      .child(Input.new(this.input).flex_1().h_full().text_size(15).text_color(P.text));
  },

  jumpList(live) {
    const P = this.P;
    const rows = this.rows;
    let body;
    if (!rows.length) {
      const text = !this.data ? (this.error ? "Galpon is not available." : "Loading…") : "Nothing here yet.";
      body = v_flex().flex_1().items_center().justify_center().child(div().text_size(S.ui).text_color(P.text3).child(text));
    } else {
      body = v_virtual_list(
        "gj-rows",
        rows.length,
        rows.map((row) => this.rowHeight(row)),
        (index) => rows[index].key,
        (range) => rows.slice(range.start, range.end).map((row, offset) => this.jumpRow(row, range.start + offset, live)),
      ).track_scroll(this.scroller)
        .on_item_click((key, cx) => this.click(key, cx))
        .on_item_secondary_click((key, _event, cx) => this.secondaryClick(key, cx))
        .size_full();
    }
    return v_flex().id("gj-list").relative().flex_1().min_h(0).w_full().px(6).py(4)
      .when(Boolean(this.actions), (el) => el.opacity(0.6))
      .child(body).child(Scrollbar.vertical("gj-rows").absolute().inset_0());
  },

  // Attention rows show why the agent needs the person. Creator rows show
  // the PR references that led to the agent.
  twoLines(row) {
    return row.type === "item" && row.item.kind === "agent" && (row.group === "needs" || row.group === "creators");
  },

  rowHeight(row) {
    switch (row.type) {
      case "header": return HEADING;
      case "peek-label": return 24;
      case "peek-title": return 26;
      case "peek-text": return row.height;
      case "peek-option": return 30;
      case "peek-input": return 38;
      case "peek-reply": return 62;
      case "peek-note": return 24;
      case "peek-end": return 10;
      default: return this.twoLines(row) ? TWO_LINES : ROW;
    }
  },

  // reasonOf gives the words and the color of the state of a one-line agent
  // row.
  reasonOf(item, current) {
    const P = this.P;
    if (current.state === "needs") return { text: "Needs you", color: P.yellow };
    if (current.state === "failed") return { text: "Failed", color: P.red };
    if (current.state === "ready" && current.fresh) return { text: "Replied", color: P.green };
    return { text: stateWords(current), color: P.text3 };
  },

  // The second line identifies the matching PRs, or explains why an agent
  // needs the person.
  snippetOf(item, current) {
    const P = this.P;
    if (item.ref) return { label: "Created", text: item.ref + (item.hidden ? " · Hidden" : ""), color: P.text3 };
    const brief = this.briefOf(item.id);
    const first = oneLine(excerpt(brief?.reply ?? "", 2));
    const dialog = brief?.dialogs?.[0];
    switch (current.state) {
      case "needs":
        if (dialog) return { label: "Asks", text: oneLine(dialog.title ?? "a question"), color: P.yellow };
        return { label: "Needs you", text: item.view === "app" ? "It waits for you in its tab." : "It waits for you in its terminal.", color: P.yellow };
      case "failed": return { label: "Failed", text: first, color: P.red };
      case "ready": return { label: current.fresh ? "Replied" : "Ready", text: first, color: P.green };
      default: return { label: stateWords(current) || "Idle", text: first, color: P.text3 };
    }
  },

  jumpRow(row, index, live) {
    const P = this.P;
    const selected = index === this.cursor;
    if (row.type === "header") {
      return h_flex().h(HEADING).px(10).pb(4).items_end().text_size(S.small).font_weight(600).text_color(P.text3).child(row.title);
    }
    if (row.type === "empty") {
      return h_flex().h(ROW).px(10).items_center().text_size(S.ui).text_color(P.text3).child(row.title);
    }
    if (row.type.startsWith("peek")) return this.peekRow(row, selected);
    const agentRow = row.type === "item" && row.item.kind === "agent";
    let el = h_flex().h(ROW).px(10).gap(10).items_center().rounded(7).when(selected && !agentRow, (it) => it.bg(P.raised2));
    const enter = selected ? kbd(P, "↵") : null;
    const quiet = (text, max = 180) => div().flex_none().max_w(max).truncate().text_size(S.meta).text_color(P.text3).child(text);
    if (row.type === "more") {
      return el.child(icon(row.open ? "chevron-down" : "chevron-right", 13).text_color(P.text3))
        .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(P.text2).child(row.title))
        .child(quiet(row.detail));
    }
    if (row.type === "scope") {
      return el.child(h_flex().w(14).flex_none().justify_center().child(icon(row.icon, 13).text_color(P.text3)))
        .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(P.text).child(row.title))
        .child(quiet(row.detail))
        .child(icon("chevron-right", 13).text_color(P.text3));
    }
    if (row.type === "launch") {
      const label = row.text ? `New agent “${oneLine(row.text)}”` : row.workspaceTitle ? `New agent in ${row.workspaceTitle}` : "New agent";
      return el.child(h_flex().w(14).flex_none().justify_center().child(icon("plus", 13).text_color(P.accent)))
        .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(P.text).child(label))
        .child(kbd(P, "Ctrl N"));
    }
    const item = row.item;
    if (row.guide?.length) el = el.child(h_flex().h_full().flex_none().children(row.guide.map((guide) => this.guide(guide))));
    // The title keeps its width up to most of the row; the workspace and the
    // state words give their width first.
    const titleWidth = Math.max(140, Math.round(this.listWidth() * 0.55));
    const title = div().flex_none().max_w(titleWidth).truncate().text_size(S.ui).text_color(P.text).when(selected, (it) => it.font_weight(500)).child(item.title);
    const shrink = (text, color, max) => div().flex_shrink_1().min_w(0).max_w(max).truncate().text_size(S.meta).text_color(color).child(text);
    const age = div().flex_none().w(30).text_right().text_size(S.small).text_color(P.text3).child(item.age ?? "");
    switch (item.kind) {
      case "agent": {
        const current = agentState(item, live);
        const two = this.twoLines(row);
        const reason = two ? { text: "" } : this.reasonOf(item, current);
        const repos = item.repositoryTitles ?? [];
        const repoLabel = repos.slice(0, 2).join(" · ") + (repos.length > 2 ? ` +${repos.length - 2}` : "");
        const head = el.h(two ? 26 : ROW).w_full().px(0)
          .child(h_flex().w(14).flex_none().justify_center().child(mark(P, toneOf(current), 13)))
          .child(title)
          .when(!this.scope && Boolean(item.workspaceTitle), (it) => it.child(shrink(item.workspaceTitle, P.text3, 160)))
          .child(div().flex_1().min_w(4))
          .when(repos.length > 0, (it) => it.child(div().flex_shrink_1().min_w(0).max_w(260).mr(12).truncate().font_family(MONO).text_size(S.small).text_color(P.text3).child(repoLabel).tooltip(repos.join(" · "))))
          .when(Boolean(reason.text), (it) => it.child(shrink(reason.text, reason.color, 220)))
          .when(Boolean(item.badge), (it) => it.child(div().flex_none().font_family(MONO).text_size(S.small).text_color(P.text3).child(item.badge)))
          .child(age)
          .when(Boolean(enter), (it) => it.child(enter));
        let block = v_flex().w_full().h(this.rowHeight(row)).px(10).rounded(7).overflow_hidden()
          .when(selected, (it) => it.bg(P.raised2))
          .when(two, (it) => it.pt(4))
          .child(head);
        if (two) {
          const snippet = this.snippetOf(item, current);
          block = block.child(h_flex().h(18).w_full().pl(24 + (row.guide?.length ?? 0) * 14).gap(6).items_center()
            .child(div().flex_none().text_size(S.meta).font_weight(500).text_color(snippet.color).child(snippet.label))
            .when(Boolean(snippet.text), (it) => it.child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(P.text2).child(snippet.text))));
        }
        return block;
      }
      case "pull-request": {
        const tone = (item.group ?? 0) === 0 ? "need" : (item.group ?? 0) === 1 ? "fail" : "idle";
        return el.child(h_flex().w(14).flex_none().justify_center().child(icon("git-compare", 13).text_color(tone === "idle" ? P.text3 : toneColor(P, tone))))
          .child(div().flex_none().font_family(MONO).text_size(S.small).text_color(P.text3).child(item.ref ?? ""))
          .child(title)
          .child(div().flex_1())
          .child(quiet(item.agentTitle || "No agent", 150))
          .child(age)
          .when(Boolean(enter), (it) => it.child(enter));
      }
      case "workspace": {
        const count = this.data.items.filter((other) => other.kind === "agent" && other.workspaceId === item.id).length;
        return el.child(h_flex().w(14).flex_none().justify_center().child(icon("layers", 13).text_color(P.text3)))
          .child(title)
          .child(div().flex_1())
          .child(quiet(`${count} ${count === 1 ? "agent" : "agents"}`))
          .child(icon("chevron-right", 13).text_color(P.text3));
      }
      case "command":
        return el.child(h_flex().w(14).flex_none().justify_center().child(icon("zap", 13).text_color(P.text3)))
          .child(title)
          .child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(P.text3).child(item.detail ?? ""))
          .when(Boolean(item.keys), (it) => it.child(kbd(P, this.keyText(item.keys))));
      default:
        return el.child(h_flex().w(14).flex_none().justify_center().child(icon(item.kind === "repository" ? "folder" : "git-branch", 13).text_color(P.text3)))
          .child(title)
          .child(div().flex_1())
          .when(Boolean(item.detail), (it) => it.child(quiet(item.detail, 240)))
          .when(Boolean(enter), (it) => it.child(enter));
    }
  },

  // guide draws one level of the tree of delegated agents.
  guide(mark) {
    const P = this.P;
    const line = (el) => el.absolute().w(1).bg(P.line2);
    let cell = div().w(14).h_full().flex_none().relative();
    if (mark === "│" || mark === "├") cell = cell.child(line(div()).left(6).top(0).bottom(0));
    if (mark === "└") cell = cell.child(line(div()).left(6).top(0).h(ROW / 2));
    if (mark === "├" || mark === "└") cell = cell.child(div().absolute().left(6).top(ROW / 2).w(7).h(1).bg(P.line2));
    return cell;
  },

  // listWidth is the width of the list column, without its padding.
  listWidth() {
    return Math.max(200, (this.width || 640) - 32);
  },

  keyText(keys) {
    return String(keys).split("-").map((part) => (part === "ctrl" ? "Ctrl" : part === "alt" ? "Alt" : part === "shift" ? "Shift" : part.toUpperCase())).join(" ");
  },

  // peekRow draws a row of a peek, indented under its agent.
  peekRow(row, selected) {
    const P = this.P;
    const pad = (el) => el.w_full().pl(34).pr(10);
    switch (row.type) {
      case "peek-label":
        return pad(h_flex().h(24).items_end().pb(2)).text_size(S.small).font_weight(600)
          .text_color(row.tone === "need" ? P.yellow : P.text3).child(row.text);
      case "peek-title":
        return pad(h_flex().h(26).items_center()).child(div().flex_1().min_w(0).truncate().text_size(S.title).font_weight(600).text_color(P.text).child(row.text));
      case "peek-text":
        return pad(div().h(row.height).overflow_hidden()).text_size(row.size === "meta" ? S.meta : S.ui)
          .text_color(row.size === "meta" ? P.text2 : P.text).child(row.text);
      case "peek-option":
        return h_flex().w_full().h(30).pl(28).pr(10).gap(8).items_center().rounded(6).when(selected, (el) => el.bg(P.raised2))
          .child(div().w(16).flex_none().font_family(MONO).text_size(S.small).text_color(P.text3).child(row.index < 9 ? String(row.index + 1) : ""))
          .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(selected ? P.text : P.text2).when(selected, (el) => el.font_weight(500)).child(oneLine(row.label)))
          .when(selected, (el) => el.child(kbd(P, "↵")));
      case "peek-input":
        return pad(h_flex().h(38).items_center()).child(h_flex().flex_1().h(30).items_center().border_b(1).border_color(this.answering ? P.accent : P.line2)
          .child(this.answering ? Input.new(this.answerInput).flex_1().h_full().text_size(S.ui).text_color(P.text) : div().text_size(S.ui).text_color(P.text3).child("Ctrl ↵ to write the answer")));
      case "peek-reply":
        return pad(v_flex().h(62).pt(4).gap(2))
          .child(div().text_size(S.small).font_weight(600).text_color(P.text3).child(`Reply to ${row.item.title}`))
          .child(h_flex().h(30).items_center().border_b(1).border_color(P.accent)
            .child(Input.new(this.replyInput).flex_1().h_full().text_size(S.ui).text_color(P.text)))
          .child(div().text_size(S.small).text_color(P.text3).child("Enter sends · Esc keeps a draft"));
      case "peek-note":
        return pad(h_flex().h(24).items_center()).text_size(S.meta).text_color(row.tone === "fail" ? P.red : P.text3).child(row.text);
      default:
        return div().h(10);
    }
  },

  jumpFooter(row) {
    const P = this.P;
    let notice = null;
    if (this.error) notice = { text: `Galpon: ${this.error}`, tone: "error" };
    else if (this.busy) notice = { text: this.busy, tone: "busy" };
    else if (this.status) notice = this.status;
    let hints;
    if (this.actions) hints = [["↵", "Run"], ["↑↓", "Select"], ["Esc", "Close"]];
    else if (this.reply) hints = [["↵", "Send"], ["Esc", "Cancel"]];
    else if (this.answering) hints = [["↵", "Answer"], ["Esc", "Cancel"]];
    else {
      const item = row?.type === "item" ? row.item : null;
      hints = [["↵", this.primaryVerb(row)]];
      if (row?.type?.startsWith("peek")) hints = [["↵", row.type === "peek-option" ? "Answer" : "Open"], ["Tab", "Close peek"]];
      else if (item?.kind === "agent") hints.push(["Tab", this.peek === item.id ? "Close peek" : "Peek"]);
      else if (row?.type === "scope" || item?.kind === "workspace") hints.push(["Tab", "Show"]);
      const agent = item?.kind === "agent" ? item : row?.type?.startsWith("peek") ? row.item : null;
      if (agent) hints.push(["Ctrl ↵", (this.briefOf(agent.id)?.dialogs ?? []).length ? "Answer" : "Reply"]);
      hints.push(["Ctrl K", "Actions"]);
    }
    const color = notice?.tone === "error" ? P.red : notice?.tone === "warn" ? P.yellow : P.text2;
    return h_flex().id("gj-footer").h(34).flex_none().px(14).gap(14).items_center().bg(P.side).border_t(1).border_color(P.line)
      .when(Boolean(notice), (el) => el.child(h_flex().flex_1().min_w(0).gap(8).items_center()
        .child(notice.tone === "busy" ? icon("spinner", 12).text_color(P.text2).spin(900) : icon(notice.tone === "error" || notice.tone === "warn" ? "alert" : "check", 12).text_color(color))
        .child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(color).child(notice.text))))
      .when(!notice, (el) => el.child(h_flex().flex_1().min_w(0).gap(14).items_center().overflow_hidden()
        .children(hints.map(([keys, label]) => h_flex().flex_none().gap(6).items_center()
          .child(div().text_size(S.meta).text_color(P.text2).child(label)).child(kbd(P, keys))))))
      .when(!this.actions && !this.reply && !this.answering, (el) => el.child(h_flex().flex_none().gap(6).items_center()
        .child(div().text_size(S.meta).text_color(P.text3).child("New agent")).child(kbd(P, "Ctrl N"))));
  },

  primaryVerb(row) {
    if (!row) return "Open";
    if (row.type === "scope" || (row.type === "item" && row.item.kind === "workspace")) return "Show";
    if (row.type === "more") return row.open ? "Fold" : "Show";
    if (row.type === "launch") return "New agent";
    if (row.type === "item" && row.item.kind === "command") return "Run";
    if (row.type === "item" && row.item.kind === "repository") return "New worktree";
    if (row.type === "item" && row.item.kind === "worktree") return "Open terminal";
    return "Open";
  },

  actionsPanel() {
    const P = this.P;
    const list = this.filteredActions();
    const cursor = Math.min(this.actions.cursor, Math.max(0, list.length - 1));
    const title = this.actions.row?.type === "item" ? this.actions.row.item.title : "";
    const heading = (text) => div().flex_none().px(8).pt(8).pb(2).text_size(S.small).font_weight(600).text_color(P.text3).truncate().child(text);
    const rows = [];
    list.forEach((action, index) => {
      if (index === 0 && title && !action.galpon) rows.push(heading(title));
      if (action.galpon && (index === 0 || !list[index - 1].galpon)) rows.push(heading("Galpon"));
      rows.push(this.actionRow(action, index === cursor));
    });
    // The panel sits above the footer and never covers the search field.
    const panel = v_flex().id("gj-actions").w_full().max_h_full().rounded(10).bg(P.bg).border_1().border_color(P.line2).shadow_lg().overflow_hidden()
      .child(h_flex().h(40).flex_none().px(12).gap(8).items_center().border_b(1).border_color(P.line)
        .child(Input.new(this.actionInput).flex_1().h_full().text_size(S.ui).text_color(P.text)))
      .child(v_flex().id("gj-action-list").flex_1().min_h(0).px(4).pb(6).overflow_y_scroll().children(rows));
    return v_flex().absolute().right(12).top(58).bottom(42).w(340).justify_end().child(panel);
  },

  actionRow(action, selected) {
    const P = this.P;
    return h_flex().id(`gj-action-${action.id}`).flex_none().h(30).px(8).gap(8).items_center().rounded(6).cursor_pointer()
      .when(selected, (el) => el.bg(P.raised2)).when(!selected, (el) => el.hover((s) => s.bg(P.raised)))
      .on_click((_e, cx) => {
        this.closeActions();
        action.run(cx);
        cx.notify();
      })
      .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(selected ? P.text : P.text2).child(action.label))
      .when(Boolean(action.keys), (el) => el.child(kbd(P, action.keys)));
  },
};
