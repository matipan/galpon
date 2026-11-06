// Galpon command center. `galpon herdr-app install` writes this mod. Run that
// command again after the galpon executable moves.
//
// On a screen, another computer runs Galpon: the host. The mod then runs
// galpon on the host through herdr-core, and names and focuses here what
// Galpon opens there.
//
// The mod reads Galpon through the galpon command: `herdr-app view` gives the
// rows, `herdr-app inspect` the fields of the selected row, `herdr-app form`
// the first draft of a form, and `herdr-app start` runs an action. It looks
// like the terminal command center: a flat prompt band, result rows with a
// selection band, an inspector and a blue statusline, in the Galpon palette.

import { View, div } from "gpui-kit";
import { h_flex, v_flex, InputState, Input, TextareaState, Textarea, v_virtual_list, VirtualListScrollHandle, Scrollbar } from "gpui-base";
import { closeOverlay, exec, notify, panes, pickFolder, props, readTextFile, run, setShared, shared, spaces, window as appWindow } from "herdr";
import {
  GROUP_TITLES, VIEWS, VIEW_ICONS, VIEW_LABELS,
  buildRows, expansionKey, normalize, pullRequestAgent, selectable,
} from "./model.js";
import { actionsFor, createForm, filterOptions } from "./forms.js";

const GALPON = __GALPON__;
// HOST is the herdr-core machine name of the computer that runs Galpon, or
// null when Galpon runs on this computer.
const HOST = __HOST__;
// BUILD is the build of the galpon that installed this mod. On a screen,
// SCREEN_GALPON is the galpon of this computer. It updates this computer when
// Galpon on the host has another build, and the app then loads the new mods.
// It is null on the host.
const BUILD = __BUILD__;
const SCREEN_GALPON = __SCREEN_GALPON__;
// The mod asks again for an update to the same build after this time.
const UPDATE_RETRY = 10 * 60 * 1000;
// On a screen, this is how long the mod waits until this window shows a
// terminal that Galpon opened on the host. The window adds it after its next
// core listing, in about a second.
const SCREEN_WAIT = 10000;

// The terminal palette (Tokyo Night Moon) until Galpon sends its own.
const DEFAULT_PALETTE = {
  background: "#222436", surface: "#1e2030", "surface-raised": "#2f334d", prompt: "#2d3149",
  selection: "#2d3f76", border: "#589ed7", foreground: "#c8d3f5", muted: "#9aa5ce", comment: "#828bb8",
  status: "#7aa2f7", "status-ink": "#222436", blue: "#82aaff", cyan: "#65bcff", purple: "#c099ff",
  green: "#c3e88d", orange: "#ff966c", red: "#ff757f", yellow: "#ffc777", teal: "#4fd6be",
};

// lastUpdate is the update that this mod asked for last: { build, at }. The
// mod also keeps it in localStorage, for the next copy after a reload.
let lastUpdate = null;

// askUpdate starts an update of this screen when the result of the host has
// another build than this mod. The update runs in its own process. A failed
// request does not change the result.
async function askUpdate(out) {
  const build = out?.build;
  if (!HOST || !SCREEN_GALPON || typeof build !== "string" || !build || build === BUILD) return;
  if (!lastUpdate) {
    try {
      lastUpdate = JSON.parse(localStorage.getItem("galpon.update") ?? "null");
    } catch {
      lastUpdate = null;
    }
  }
  if (lastUpdate?.build === build && Date.now() - lastUpdate.at < UPDATE_RETRY) return;
  lastUpdate = { build, at: Date.now() };
  try {
    localStorage.setItem("galpon.update", JSON.stringify(lastUpdate));
  } catch {
    // The mod keeps it in memory.
  }
  try {
    await exec(SCREEN_GALPON, ["screen", "update"]);
  } catch {
    // The mod asks again after UPDATE_RETRY.
  }
}

const POLL = 2000;
const ROW = 30;
const HEADING = 34;
const FIELD = 34;
const JOB_POLL = 400;
const JOB_LIMIT = 15 * 60 * 1000;
const SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const CONTEXT = "GalponCommandCenter";
const DRAFTS = "galpon.messageDrafts";

// The search field and the form fields keep the keyboard, so the command
// center binds its keys in its own context. They win over the app's global
// keys. Each binding is [keystroke, action, key]: the screens get the key.
const KEYS = [
  ["up", "up", "up"], ["ctrl-p", "up", "up"], ["down", "down", "down"],
  ["pageup", "page-up", "pageup"], ["pagedown", "page-down", "pagedown"],
  ["tab", "expand", "tab"], ["shift-tab", "next-view", "shift-tab"], ["enter", "enter", "enter"],
  ["left", "left", "left"], ["right", "right", "right"],
  ["alt-p", "pull-requests", "alt-p"], ["ctrl-g", "detail", "ctrl-g"], ["ctrl-r", "reorder", "ctrl-r"],
  ["ctrl-o", "github", "ctrl-o"], ["ctrl-n", "new-agent", "ctrl-n"], ["ctrl-f", "fork-agent", "ctrl-f"],
  ["ctrl-s", "new-repository", "ctrl-s"], ["ctrl-h", "hidden", "ctrl-h"], ["ctrl-space", "actions", "ctrl-space"],
  ["escape", "escape", "escape"],
];
const ACTION_KEYS = new Map(KEYS.map(([, action, key]) => [action, key]));
let keysBound = false;

const message = (error) => String(error?.message ?? error);
// Galpon errors start with a lowercase letter; the notice shows sentences.
const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");

// hostId gives the ID that Galpon uses for a terminal or a space ID of this
// window. The window shows the IDs of another computer as "<machine>/<id>".
// The result is "" for an ID that is not on the computer that runs Galpon.
function hostId(id, host = HOST) {
  if (!id) return "";
  if (!host) return id.includes("/") ? "" : id;
  return id.startsWith(`${host}/`) ? id.slice(host.length + 1) : "";
}

// showOnScreen names and focuses a terminal that Galpon opened on the host,
// as Galpon does in the window of the host. place comes from Galpon: the
// terminal address and the names of its tab and space on the host. It
// returns false when this window did not show the terminal in time.
async function showOnScreen(place, cx) {
  const terminal = `${HOST}/${place.terminal}`;
  const begin = Date.now();
  while (Date.now() - begin < SCREEN_WAIT) {
    const pane = panes().find((item) => item.terminal === terminal);
    if (pane) {
      const space = spaces().find((item) => item.tabs.some((tab) => tab.id === pane.tab));
      if (place.tabName) run("tab.rename", { id: pane.tab, name: place.tabName });
      if (space && place.spaceName) run("space.rename", { id: space.id, name: place.spaceName });
      run(place.agent ? "agent.focus" : "pane.focus", { terminal });
      return true;
    }
    await cx.sleep(200);
  }
  return false;
}
const wrap = (index, delta, count) => (count <= 0 ? 0 : (((index + delta) % count) + count) % count);

// renameTab gives the tab of a renamed agent its new name in this window.
// Galpon renames it in the window of the computer that runs Galpon too. On a
// screen, this also works while that window is closed.
function renameTab(agentId, name) {
  const pane = panes().find((item) => hostId(item.tab) === `galpon-${agentId}`);
  if (pane && name) run("tab.rename", { id: pane.tab, name });
}

export default class Galpon extends View {
  init(_props, cx) {
    this.data = null;
    this.error = "";
    this.status = null;
    this.busy = "";
    this.screen = "list";
    this.view = "agent";
    this.previousView = "agent";
    this.query = "";
    this.expanded = new Set();
    this.order = new Map();
    this.rows = [];
    this.cursor = 0;
    this.selected = "";
    this.detail = false;
    this.hidden = false;
    this.pendingMove = "";
    this.generation = 0;
    this.fields = new Map();
    this.wanted = "";
    this.width = 0;
    this.menu = null;
    this.form = null;
    this.formInputs = new Map();
    this.formOrigin = "list";
    this.formError = null;
    this.focusKey = "";
    this.choice = null;
    this.targets = null;
    this.ops = null;
    this.message = null;
    this.messageError = "";
    this.confirm = null;
    // "close" when the command center opened on one action: leaving that
    // screen closes the command center.
    this.startOrigin = "list";
    this.drafts = { ...(shared(DRAFTS) ?? {}) };
    this.handle = cx.focus_handle();
    this.scroller = VirtualListScrollHandle.new();
    this.choiceScroller = VirtualListScrollHandle.new();
    this.targetScroller = VirtualListScrollHandle.new();
    this.opsScroller = VirtualListScrollHandle.new();
    this.enterAt = 0;
    this.input = InputState.new({ placeholder: "Search agents, workspaces, worktrees and repositories" });
    this.input.on("change", (_e, cx) => this.search(cx));
    this.input.on("submit", (_e, cx) => this.enter(cx));
    this.choiceInput = InputState.new({ placeholder: "Filter options…" });
    this.choiceInput.on("change", (_e, cx) => {
      this.filterChoice();
      cx.notify();
    });
    this.choiceInput.on("submit", (_e, cx) => this.enter(cx));
    this.textarea = TextareaState.new({ placeholder: "Write a message. Nothing is sent until you select Send." });
    if (!keysBound) {
      try {
        cx.bind_keys(KEYS.map(([keystroke, action]) => ({ keystroke, action: `galpon::${action}`, context: CONTEXT })));
        keysBound = true;
      } catch (error) {
        this.status = { text: `Galpon keys: ${message(error)}`, tone: "error" };
      }
    }
    // The overlay takes the keyboard after its first frames.
    this.refocus();
    cx.timer.after(120, () => this.refocus());
    cx.timer.after(600, () => this.refocus());
    this.load(cx);
    cx.timer.every(POLL, (cx) => {
      this.load(cx);
      if (this.screen === "operations") this.loadOperations(cx);
    });
    if (props()?.start === "new-agent") this.startNewAgent(cx);
    if (props()?.start === "action") this.startAction(cx);
  }

  // --- Galpon

  async galpon(args) {
    const result = HOST
      ? await exec(GALPON, ["herdr-app", "--screen", ...args], { on: HOST })
      : await exec(GALPON, ["herdr-app", ...args]);
    const text = (result.stdout ?? "").trim();
    if (!text) throw new Error((result.stderr ?? "").trim() || `galpon exited with code ${result.code}`);
    const out = JSON.parse(text);
    await askUpdate(out);
    return out;
  }

  // start runs an action through `herdr-app start`. A slow action continues in
  // its own galpon process, and its result comes later in a file.
  async start(cx, action, payload) {
    const out = await this.galpon(["start", action, JSON.stringify(payload)]);
    if (!out.job) return out;
    const begin = Date.now();
    while (Date.now() - begin < JOB_LIMIT) {
      await cx.sleep(JOB_POLL);
      const text = HOST ? await readTextFile(out.job, { on: HOST }) : await readTextFile(out.job);
      if (text) return JSON.parse(text);
    }
    return { error: `the action is still running; see ${out.log}` };
  }

  load(cx) {
    if (this.loading) return;
    this.loading = true;
    cx.spawn(async (cx) => {
      try {
        const out = await this.galpon(["view", ...(this.hidden ? ["--hidden"] : [])]);
        if (out.error) {
          this.error = out.error;
        } else {
          this.data = out;
          this.error = "";
          this.generation++;
          this.refreshRows();
        }
      } catch (error) {
        this.error = message(error);
      }
      this.loading = false;
      if (this.screen === "list") this.inspectSelected(cx);
      cx.notify();
    });
  }

  refreshRows() {
    const rows = buildRows(this.data, { view: this.view, query: this.query, expanded: this.expanded, order: this.order });
    // A virtual list needs one key per row.
    const seen = new Set();
    for (const row of rows) {
      while (seen.has(row.key)) row.key += "+";
      seen.add(row.key);
    }
    this.rows = rows;
    let index = rows.findIndex((row) => row.key === this.selected && selectable(row));
    if (index < 0) index = this.nearestSelectable(Math.min(this.cursor, rows.length - 1));
    this.cursor = Math.max(0, index);
    this.selected = rows[this.cursor]?.key ?? "";
  }

  nearestSelectable(from) {
    for (let index = Math.max(0, from); index < this.rows.length; index++) if (selectable(this.rows[index])) return index;
    for (let index = Math.min(from, this.rows.length - 1); index >= 0; index--) if (selectable(this.rows[index])) return index;
    return -1;
  }

  // The fields of the selected row come after the selection stops for a
  // moment, and again after each read.
  inspectSelected(cx) {
    const row = this.rows[this.cursor];
    if (!row || row.type !== "item") return;
    const id = `${row.item.kind}:${row.item.id}`;
    const known = this.fields.get(id);
    if (known && known.generation === this.generation) return;
    this.wanted = id;
    cx.timer.after(known ? 0 : 80, (cx) => {
      if (this.wanted !== id || this.inspecting === id) return;
      this.inspecting = id;
      const generation = this.generation;
      cx.spawn(async (cx) => {
        try {
          const out = await this.galpon(["inspect", row.item.kind, row.item.id, ...(this.hidden ? ["--hidden"] : [])]);
          this.fields.set(id, { fields: out.fields ?? [], error: out.error ?? "", generation });
        } catch (error) {
          this.fields.set(id, { fields: known?.fields ?? [], error: message(error), generation });
        }
        this.inspecting = "";
        cx.notify();
      });
    });
  }

  // --- keys

  // refocus gives the keyboard to the field of the current screen.
  refocus() {
    switch (this.screen) {
      case "list": return this.menu ? this.handle.focus() : this.input.focus();
      case "form": return this.focusField(this.focusKey);
      case "choice": return this.choiceInput.focus();
      case "message": return this.textarea.focus();
      default: return this.handle.focus();
    }
  }

  // A field passes Enter on to the key binding and also reports it as a
  // submit, so one Enter can come twice. enter runs it once.
  enter(cx) {
    const now = Date.now();
    if (now - this.enterAt < 150) return;
    this.enterAt = now;
    this.key("enter", cx);
    cx.notify();
  }

  key(name, cx) {
    if (this.busy) {
      if (name === "escape") this.close();
      return;
    }
    switch (this.screen) {
      case "list": return this.menu ? this.menuKey(name, cx) : this.listKey(name, cx);
      case "form": return this.formKey(name, cx);
      case "choice": return this.choiceKey(name, cx);
      case "targets": return this.targetsKey(name, cx);
      case "operations": return this.operationsKey(name, cx);
      case "message": return this.messageKey(name, cx);
      case "confirm": return this.confirmKey(name, cx);
    }
  }

  // keyDown takes the letter keys of the actions menu and the Operations
  // view, and Space, + and d on form fields that hold no text. A field that
  // holds text gets every letter.
  keyDown(event, cx) {
    if (this.busy || event.modifiers?.control || event.modifiers?.alt || event.modifiers?.platform) return;
    const letter = event.key === "space" ? " " : event.key_char ?? event.key;
    if (this.screen === "list" && this.menu) {
      const action = this.menu.actions.find((candidate) => candidate.key === letter);
      if (!action) return;
      cx.stop_propagation();
      this.runAction(action.action, cx);
      return cx.notify();
    }
    if (this.screen === "operations" && letter === "r") {
      cx.stop_propagation();
      this.loadOperations(cx, true);
      return cx.notify();
    }
    if (this.screen !== "form" || !this.form) return;
    const field = this.currentField();
    if (!field || field.type === "text") return;
    if (letter === " " && field.type === "toggle") {
      this.form.toggle(field.key);
      this.changed();
    } else if (letter === "+" && this.form.canAdd?.()) {
      this.addWorktree();
    } else if (letter === "d" && this.form.remove?.(field.key)) {
      this.changed();
    } else {
      return;
    }
    cx.stop_propagation();
    cx.notify();
  }

  // --- list

  select(index, cx) {
    if (index < 0 || index >= this.rows.length) return;
    const key = this.rows[index].key;
    if (key !== this.selected) {
      this.pendingMove = "";
      this.status = null;
    }
    this.cursor = index;
    this.selected = key;
    this.scroller.scroll_to_item(index);
    this.inspectSelected(cx);
  }

  move(delta, cx) {
    const step = Math.sign(delta);
    let index = this.cursor;
    for (let count = 0; count < Math.abs(delta); count++) {
      let next = index + step;
      while (next >= 0 && next < this.rows.length && !selectable(this.rows[next])) next += step;
      if (next < 0 || next >= this.rows.length) break;
      index = next;
    }
    this.select(index, cx);
  }

  setView(view, cx) {
    if (view !== "pull-request") this.previousView = view;
    this.view = view;
    this.order = new Map();
    this.cursor = 0;
    this.selected = "";
    this.detail = false;
    this.status = null;
    this.pendingMove = "";
    this.refreshRows();
    this.scroller.scroll_to_item(this.cursor);
    this.inspectSelected(cx);
  }

  search(cx) {
    const query = this.input.value();
    if (query === this.query) return;
    this.query = query;
    this.order = new Map();
    for (const key of [...this.expanded]) if (key.startsWith("search-") || key.startsWith("workspace:")) this.expanded.delete(key);
    this.cursor = 0;
    this.selected = "";
    this.status = null;
    this.pendingMove = "";
    this.refreshRows();
    this.scroller.scroll_to_item(this.cursor);
    this.inspectSelected(cx);
    cx.notify();
  }

  expand(cx) {
    const key = expansionKey(this.rows[this.cursor], this.query);
    if (!key) return;
    if (this.expanded.has(key)) this.expanded.delete(key);
    else this.expanded.add(key);
    this.refreshRows();
    this.inspectSelected(cx);
  }

  selectedItem() {
    const row = this.rows[this.cursor];
    return row?.type === "item" ? row.item : null;
  }

  listKey(name, cx) {
    const item = this.selectedItem();
    switch (name) {
      case "up": return this.move(-1, cx);
      case "down": return this.move(1, cx);
      case "pageup": return this.move(-10, cx);
      case "pagedown": return this.move(10, cx);
      case "tab": return this.expand(cx);
      case "shift-tab": return this.setView(VIEWS[(VIEWS.indexOf(this.view) + 1) % VIEWS.length], cx);
      case "alt-p": return this.setView(this.view === "pull-request" ? this.previousView : "pull-request", cx);
      case "ctrl-g":
        this.detail = !this.detail;
        return;
      case "ctrl-r": return this.reorder(cx);
      case "ctrl-o":
        if (item?.kind === "pull-request") this.browse(item.url, cx);
        return;
      case "ctrl-n": return this.openForm(cx, "agent", ["agent", "new", item?.kind ?? "", item?.id ?? ""]);
      case "ctrl-f":
        if (item?.kind !== "agent") return this.say("Select an agent to fork", "warn");
        return this.openForm(cx, "agent", ["agent", "fork", "agent", item.id], { fork: true });
      case "ctrl-s": return this.openForm(cx, "repository");
      case "ctrl-h":
        this.hidden = !this.hidden;
        this.say(this.hidden ? "Showing hidden resources · press x in actions on a hidden item to unhide it" : "Hidden resources are no longer shown", "info");
        this.fields.clear();
        return this.load(cx);
      case "ctrl-space": return this.openMenu(this.rows[this.cursor], cx);
      case "enter": return this.activate(cx);
      case "escape":
        if (this.detail) this.detail = false;
        else this.close();
        return;
    }
  }

  click(key, cx) {
    this.menu = null;
    this.input.focus();
    const index = this.rows.findIndex((row) => row.key === key);
    if (index < 0 || !selectable(this.rows[index])) return;
    if (index === this.cursor) this.activate(cx);
    else this.select(index, cx);
    cx.notify();
  }

  // A right click selects a row and opens its actions.
  secondaryClick(key, cx) {
    const index = this.rows.findIndex((row) => row.key === key);
    if (index < 0 || !selectable(this.rows[index])) return;
    this.select(index, cx);
    this.openMenu(this.rows[index], cx);
    cx.notify();
  }

  close() {
    closeOverlay(props().overlay);
  }

  // finish closes the command center after an action opened an agent or a
  // terminal. On a screen, it first names and focuses that terminal here.
  async finish(place, cx) {
    if (HOST && place?.terminal) await showOnScreen(place, cx);
    this.close();
  }

  say(text, tone) {
    this.status = text ? { text, tone } : null;
  }

  activate(cx) {
    const row = this.rows[this.cursor];
    if (!row || this.busy) return;
    if (row.type === "more") return this.expand(cx);
    const item = row.item;
    if (item.hidden) return this.say(`${item.title} is hidden · press x in actions to unhide it`, "warn");
    switch (item.kind) {
      case "agent":
        return this.openAgent(item, cx);
      case "pull-request": {
        const agent = pullRequestAgent(this.data, item);
        return agent ? this.openAgent(agent, cx) : this.browse(item.url, cx);
      }
      case "workspace":
      case "worktree":
        return this.openTargets(item, false, cx);
      case "repository":
        return this.openForm(cx, "remote", ["remote", item.kind, item.id]);
    }
  }

  // An agent opens in the app. An idle agent that runs in Herdr moves after
  // a second Enter, because its process restarts.
  openAgent(item, cx) {
    const move = this.pendingMove === item.id;
    this.busy = move ? `Moving ${item.title} to the Herdr app` : `Opening ${item.title}`;
    this.status = null;
    cx.notify();
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["open", item.id, ...(move ? ["--move"] : [])]);
      } catch (error) {
        out = { error: message(error) };
      }
      if (!out.error) return this.finish(out.place, cx);
      this.busy = "";
      if (out.code === "open-elsewhere") {
        this.pendingMove = item.id;
        this.say(`${out.error}. Press enter again to move it.`, "warn");
      } else {
        this.say(sentence(out.error), "error");
      }
      cx.notify();
    });
  }

  browse(url, cx) {
    if (!url) return;
    const opener = appWindow().platform === "macos" ? "open" : "xdg-open";
    cx.spawn(async (cx) => {
      try {
        await exec(opener, [url]);
        this.say(`Opened ${url}`, "info");
      } catch (error) {
        this.say(message(error), "error");
      }
      cx.notify();
    });
  }

  reorder(cx) {
    this.order = new Map();
    this.refreshRows();
    if (this.view === "pull-request" && !normalize(this.query)) {
      cx.spawn(async (cx) => {
        try {
          const out = await this.galpon(["refresh"]);
          if (out.error) this.say(sentence(out.error), "error");
        } catch (error) {
          this.say(message(error), "error");
        }
        this.load(cx);
      });
      return;
    }
    this.load(cx);
  }

  // --- actions menu

  openMenu(row, cx) {
    if (row?.type === "more") return this.say("Expand this group before you select an item", "warn");
    const item = row?.type === "item" ? row.item : null;
    this.menu = { item, actions: actionsFor(item), cursor: 0 };
    this.detail = false;
    this.handle.focus();
  }

  closeMenu() {
    this.menu = null;
    this.input.focus();
  }

  menuKey(name, cx) {
    const count = this.menu.actions.length;
    switch (name) {
      case "up":
      case "shift-tab":
        this.menu.cursor = wrap(this.menu.cursor, -1, count);
        return;
      case "down":
      case "tab":
        this.menu.cursor = wrap(this.menu.cursor, 1, count);
        return;
      case "enter": return this.runAction(this.menu.actions[this.menu.cursor].action, cx);
      case "escape":
      case "ctrl-space":
        return this.closeMenu();
    }
  }

  runAction(action, cx) {
    const item = this.menu?.item ?? null;
    this.closeMenu();
    return this.perform(action, item, cx);
  }

  perform(action, item, cx) {
    const origin = this.startOrigin;
    switch (action) {
      case "message": return this.openMessage(item);
      case "terminal":
      case "editor": {
        const editor = action === "editor";
        if (item.kind === "repository") return this.openForm(cx, "worktree", ["worktree", item.id, ...(editor ? ["--editor"] : [])]);
        return this.openTargets(item, editor, cx);
      }
      case "operations": return this.openOperations(item, cx);
      case "new-agent-here": return this.openForm(cx, "agent", ["agent", "here", item.kind, item.id], { origin });
      case "fork-agent": return this.openForm(cx, "agent", ["agent", "fork", "agent", item.id], { fork: true, origin });
      case "rename": return this.showForm(createForm("rename", item), { origin });
      case "hide": return this.openConfirm(item);
      case "new-workspace": return this.openForm(cx, "workspace");
      case "new-repository": return this.openForm(cx, "repository");
      case "new-remote": return this.openForm(cx, "remote", ["remote", item?.kind ?? "", item?.id ?? ""]);
    }
  }

  // --- forms

  // openForm shows a form. args reads its first draft from `galpon herdr-app
  // form`; a form without args starts empty.
  openForm(cx, kind, args, options = {}) {
    if (this.busy) return;
    if (!args) return this.showForm(createForm(kind, undefined, HOST), options);
    this.busy = "Opening the form…";
    this.status = null;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        const out = await this.galpon(["form", ...args]);
        this.busy = "";
        if (out.error) this.say(sentence(out.error), "warn");
        else this.showForm(createForm(kind, { ...out, fork: options.fork }), options);
      } catch (error) {
        this.busy = "";
        this.say(message(error), "error");
      }
      cx.notify();
    });
  }

  // The sidebar of the Galpon layout opens the command center on one action
  // of one row: props { start: "action", action, kind, id }, with an action
  // of the actions menu. The action waits for the first view, which gives
  // the row.
  startAction(cx) {
    const start = props() ?? {};
    this.startOrigin = "close";
    this.busy = "Opening…";
    cx.spawn(async (cx) => {
      for (let tries = 0; !this.data && !this.error && tries < 100; tries++) await cx.sleep(100);
      this.busy = "";
      const item = this.data?.items.find((candidate) => candidate.kind === start.kind && candidate.id === start.id);
      if (item) this.perform(start.action, item, cx);
      else this.say(this.error || `This ${start.kind ?? "row"} is no longer in Galpon`, "warn");
      cx.notify();
    });
  }

  // Ctrl-Alt-N opens the New Agent form for the terminal that had the
  // keyboard. Esc then closes the command center.
  startNewAgent(cx) {
    const start = props() ?? {};
    this.busy = "Opening the New Agent form…";
    cx.spawn(async (cx) => {
      try {
        const out = await this.galpon(["form", "agent", "focus", hostId(start.terminal), hostId(start.space)]);
        this.busy = "";
        if (out.code === "no-workspace") this.say("The focused terminal is not in a Galpon workspace. Select a row and press ctrl+n.", "warn");
        else if (out.error) this.say(sentence(out.error), "warn");
        else this.showForm(createForm("agent", out), { origin: "close" });
      } catch (error) {
        this.busy = "";
        this.say(message(error), "error");
      }
      cx.notify();
    });
  }

  showForm(form, options = {}) {
    this.releaseFormInputs();
    this.form = form;
    this.formOrigin = options.origin ?? "list";
    this.formError = null;
    this.screen = "form";
    this.menu = null;
    this.status = form.status ? { text: form.status, tone: "warn" } : null;
    this.syncInputs();
    this.focusField(form.fields()[0].key);
  }

  leaveForm(status = null) {
    if (this.formOrigin === "close" && !status) return this.close();
    this.releaseFormInputs();
    this.form = null;
    this.formError = null;
    this.choice = null;
    this.screen = "list";
    this.status = status;
    this.input.focus();
  }

  releaseFormInputs() {
    for (const state of this.formInputs.values()) state.release();
    this.formInputs.clear();
  }

  // syncInputs gives each text field an input with the draft value.
  syncInputs() {
    const keys = new Set();
    for (const field of this.form.fields()) {
      if (field.type !== "text") continue;
      keys.add(field.key);
      const known = this.formInputs.get(field.key);
      if (known) {
        if (known.value() !== this.form.text(field.key)) known.set_value(this.form.text(field.key));
        continue;
      }
      const key = field.key;
      const state = InputState.new({ placeholder: field.placeholder ?? "", value: this.form.text(key) });
      state.on("change", (_e, cx) => {
        if (!this.form || state.value() === this.form.text(key)) return;
        this.form.setText(key, state.value());
        this.formError = null;
        cx.notify();
      });
      state.on("focus", (_e, cx) => {
        if (this.screen !== "form") return;
        this.focusKey = key;
        cx.notify();
      });
      state.on("submit", (_e, cx) => this.enter(cx));
      this.formInputs.set(key, state);
    }
    for (const [key, state] of [...this.formInputs]) {
      if (keys.has(key)) continue;
      state.release();
      this.formInputs.delete(key);
    }
  }

  currentField() {
    return this.form?.fields().find((field) => field.key === this.focusKey) ?? null;
  }

  focusField(key) {
    this.focusKey = key;
    const state = this.formInputs.get(key);
    if (state) state.focus();
    else this.handle.focus();
  }

  moveField(delta) {
    const fields = this.form.fields();
    const index = Math.max(0, fields.findIndex((field) => field.key === this.focusKey));
    this.focusField(fields[wrap(index, delta, fields.length)].key);
  }

  // changed follows a change of the draft: the fields and their inputs can
  // change, and the focus stays on the same field when it is still there.
  changed() {
    this.formError = null;
    this.syncInputs();
    if (this.currentField()) return this.focusField(this.focusKey);
    const fields = this.form.fields();
    this.focusField((fields.find((field) => field.key === "placement") ?? fields[fields.length - 1]).key);
  }

  addWorktree() {
    const error = this.form.add?.();
    if (error) {
      this.formError = { text: sentence(error), field: "add" };
      return;
    }
    const count = this.form.draft.worktrees.length;
    this.syncInputs();
    this.focusField(`repository:${count - 1}`);
  }

  clickField(field, cx) {
    if (this.busy) return;
    this.focusField(field.key);
    if (field.type === "choice") this.openChoice(field);
    else if (field.type === "toggle") {
      this.form.toggle(field.key);
      this.changed();
    } else if (field.type === "action") this.addWorktree();
    else if (field.type === "submit") this.submitForm(cx);
  }

  formKey(name, cx) {
    const field = this.currentField();
    switch (name) {
      case "escape": return this.leaveForm();
      case "tab":
        if (field?.type === "choice") return this.openChoice(field);
        return this.moveField(1);
      case "down": return this.moveField(1);
      case "up":
      case "shift-tab":
        return this.moveField(-1);
      case "ctrl-s": return this.submitForm(cx);
      case "ctrl-o":
        // The folder picker shows the folders of this computer, not of the host.
        if (this.form.kind === "repository" && !HOST) this.chooseFolder(cx);
        return;
      case "enter":
        if (field?.type === "submit") return this.submitForm(cx);
        if (field?.type === "action") return this.addWorktree();
        return this.moveField(1);
      case "left":
      case "right":
        if (field && field.type !== "text" && this.form.cycle(field.key, name === "left" ? -1 : 1)) this.changed();
        return;
    }
  }

  chooseFolder(cx) {
    cx.spawn(async (cx) => {
      try {
        const folder = await pickFolder({ prompt: "Add" });
        if (folder && this.form?.kind === "repository") {
          this.form.setText("value", folder);
          this.syncInputs();
        }
      } catch (error) {
        this.say(message(error), "error");
      }
      cx.notify();
    });
  }

  submitForm(cx) {
    if (this.busy || !this.form) return;
    const form = this.form;
    this.busy = form.busy;
    this.formError = null;
    this.status = null;
    cx.notify();
    cx.spawn(async (cx) => {
      let result;
      try {
        result = await this.start(cx, form.action, form.payload());
      } catch (error) {
        result = { error: message(error) };
      }
      if (result.close) return this.finish(result.place, cx);
      this.busy = "";
      if (this.form !== form) {
        this.say(result.error ? sentence(result.error) : result.message, result.error ? "error" : "info");
      } else if (result.error) {
        const marked = form.fields().some((field) => field.key === result.field);
        this.formError = { text: sentence(result.error), field: marked ? result.field : "" };
        if (marked) this.focusField(result.field);
      } else if (form.kind === "rename") {
        renameTab(form.agentId, form.payload().title);
        // From the sidebar menu, the command center closes with a toast.
        if (this.formOrigin === "close") {
          notify({ title: result.message, level: "success" });
          return this.close();
        }
        this.leaveForm({ text: result.message, tone: "info" });
        this.load(cx);
      } else {
        // An agent that exists but did not open stays: the notice says so.
        this.leaveForm({ text: result.message ?? "Done", tone: form.kind === "agent" ? "warn" : "info" });
        this.load(cx);
      }
      cx.notify();
    });
  }

  // --- choices

  openChoice(field) {
    const choice = this.form.choice(field.key);
    if (!choice) return this.moveField(1);
    this.choice = { key: field.key, title: choice.title, options: choice.options, filtered: choice.options, cursor: choice.cursor };
    this.choiceInput.set_value("");
    this.screen = "choice";
    this.choiceInput.focus();
    this.choiceScroller.scroll_to_item(choice.cursor, "center");
  }

  // filterChoice keeps the selected option when the filter still shows it.
  filterChoice() {
    if (!this.choice) return;
    const selected = this.choice.filtered[this.choice.cursor];
    this.choice.filtered = filterOptions(this.choice.options, this.choiceInput.value());
    const index = selected ? this.choice.filtered.findIndex((option) => option.value === selected.value && option.label === selected.label) : -1;
    this.choice.cursor = Math.max(0, index);
    this.choiceScroller.scroll_to_item(this.choice.cursor);
  }

  applyChoice(option) {
    if (!option) return;
    const key = this.choice.key;
    this.form.choose(key, option.value);
    this.closeChoice();
    this.changed();
  }

  closeChoice() {
    const key = this.choice.key;
    this.choice = null;
    this.screen = "form";
    this.focusField(key);
  }

  choiceKey(name) {
    const choice = this.choice;
    const count = choice.filtered.length;
    const step = { up: -1, "shift-tab": -1, down: 1, tab: 1, pageup: -10, pagedown: 10 }[name];
    if (step) {
      choice.cursor = Math.abs(step) === 1 ? wrap(choice.cursor, step, count) : Math.max(0, Math.min(count - 1, choice.cursor + step));
      this.choiceScroller.scroll_to_item(choice.cursor);
      return;
    }
    if (name === "enter") return this.applyChoice(choice.filtered[choice.cursor]);
    if (name === "escape") return this.closeChoice();
  }

  // --- terminals

  // openTargets opens a terminal, or the editor, at the worktree of a row. A
  // row with more than one place shows them to choose from.
  openTargets(item, editor, cx) {
    if (this.busy) return;
    this.busy = editor ? "Finding where the editor can open…" : "Finding where the terminal can open…";
    this.status = null;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        const out = await this.galpon(["targets", item.kind, item.id]);
        this.busy = "";
        if (out.error) this.say(sentence(out.error), "warn");
        else if (out.targets.length === 1) this.openTarget(out.targets[0], editor, cx);
        else this.showTargets(out.targets, editor);
      } catch (error) {
        this.busy = "";
        this.say(message(error), "error");
      }
      cx.notify();
    });
  }

  showTargets(targets, editor) {
    const rows = [];
    let group = null;
    for (const target of targets) {
      if (target.agentTitle !== group) {
        group = target.agentTitle;
        rows.push({ type: "header", key: `group-${rows.length}`, title: (group || "Worktrees").toUpperCase() });
      }
      rows.push({ type: "target", key: `target-${rows.length}`, target });
    }
    this.targets = { rows, editor, cursor: rows.findIndex((row) => row.type === "target") };
    this.screen = "targets";
    this.handle.focus();
    this.targetScroller.scroll_to_item(this.targets.cursor);
  }

  openTarget(target, editor, cx) {
    this.busy = editor ? `Opening the editor at ${target.label}` : `Opening a terminal at ${target.label}`;
    cx.notify();
    cx.spawn(async (cx) => {
      let result;
      try {
        result = await this.start(cx, "terminal", { target, editor });
      } catch (error) {
        result = { error: message(error) };
      }
      if (!result.error) return this.finish(result.place, cx);
      this.busy = "";
      this.say(sentence(result.error), "error");
      cx.notify();
    });
  }

  targetsKey(name, cx) {
    const targets = this.targets;
    const step = { up: -1, "shift-tab": -1, down: 1, tab: 1, pageup: -10, pagedown: 10 }[name];
    if (step) {
      let index = targets.cursor;
      for (let count = 0; count < Math.abs(step); count++) {
        let next = index + Math.sign(step);
        while (next >= 0 && next < targets.rows.length && targets.rows[next].type !== "target") next += Math.sign(step);
        if (next < 0 || next >= targets.rows.length) break;
        index = next;
      }
      targets.cursor = index;
      this.targetScroller.scroll_to_item(index);
      return;
    }
    if (name === "enter") return this.openTarget(targets.rows[targets.cursor].target, targets.editor, cx);
    if (name === "escape") return this.backToList();
  }

  // backToList shows the list again. A command center that opened on one
  // action closes instead, unless it has an error to show.
  backToList(status = null) {
    this.targets = null;
    this.ops = null;
    this.confirm = null;
    this.screen = "list";
    this.status = status;
    if (this.startOrigin === "close" && status?.tone !== "error") return this.close();
    this.input.focus();
  }

  // --- operations

  openOperations(item, cx) {
    this.ops = { agentId: item.id, title: item.title, data: null, error: "", refreshError: "", cursor: 0, selected: "", loading: false };
    this.screen = "operations";
    this.handle.focus();
    this.loadOperations(cx);
  }

  loadOperations(cx) {
    const ops = this.ops;
    if (!ops || ops.loading) return;
    ops.loading = true;
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["operations", ops.agentId]);
      } catch (error) {
        out = { error: message(error) };
      }
      ops.loading = false;
      if (out.error) {
        if (ops.data) ops.refreshError = out.error;
        else ops.error = out.error;
      } else {
        ops.data = out;
        ops.error = "";
        ops.refreshError = "";
        const index = out.rows.findIndex((row) => row.id === ops.selected);
        ops.cursor = index >= 0 ? index : Math.max(0, Math.min(ops.cursor, out.rows.length - 1));
        ops.selected = out.rows[ops.cursor]?.id ?? "";
      }
      cx.notify();
    });
  }

  operationsKey(name) {
    const ops = this.ops;
    const count = ops.data?.rows.length ?? 0;
    const step = { up: -1, down: 1, pageup: -10, pagedown: 10 }[name];
    if (step && count) {
      ops.cursor = Math.max(0, Math.min(count - 1, ops.cursor + step));
      ops.selected = ops.data.rows[ops.cursor].id;
      this.opsScroller.scroll_to_item(ops.cursor);
      return;
    }
    if (name === "escape") return this.backToList();
  }

  // --- message

  openMessage(item) {
    this.message = { agentId: item.id, title: item.title, workspace: item.workspaceTitle ?? "" };
    this.messageError = "";
    this.textarea.set_value(this.drafts[item.id] ?? "");
    this.screen = "message";
    this.textarea.focus();
  }

  // Drafts stay in the app after the command center closes.
  saveDraft(agentId, text) {
    if (text.trim()) this.drafts[agentId] = text;
    else delete this.drafts[agentId];
    setShared(DRAFTS, { ...this.drafts });
  }

  messageKey(name, cx) {
    if (name === "ctrl-s") return this.sendMessage(cx);
    if (name !== "escape") return;
    const text = this.textarea.value();
    this.saveDraft(this.message.agentId, text);
    this.message = null;
    this.backToList(text.trim() ? { text: "Draft kept. Open Message on this agent to continue.", tone: "info" } : null);
  }

  sendMessage(cx) {
    if (this.busy || !this.message) return;
    const target = this.message;
    const text = this.textarea.value();
    if (!text.trim()) {
      this.messageError = "Write a message before sending";
      return;
    }
    this.saveDraft(target.agentId, text);
    this.busy = "Submitting message";
    this.messageError = "";
    cx.notify();
    cx.spawn(async (cx) => {
      let result;
      try {
        result = await this.start(cx, "send", { agentId: target.agentId, text });
      } catch (error) {
        result = { error: message(error) };
      }
      this.busy = "";
      if (result.error) {
        this.messageError = sentence(result.error);
      } else {
        this.saveDraft(target.agentId, "");
        this.textarea.set_value("");
        if (this.startOrigin === "close") notify({ title: `Message sent to ${target.title}`, body: result.message, level: "success" });
        if (this.message === target) {
          this.message = null;
          this.backToList();
        }
        this.say(result.message, "info");
      }
      cx.notify();
    });
  }

  // --- hide

  openConfirm(item) {
    this.confirm = { item };
    this.screen = "confirm";
    this.handle.focus();
  }

  confirmKey(name, cx) {
    if (name === "enter") return this.hide(cx);
    if (name === "escape") return this.backToList({ text: "Action cancelled", tone: "info" });
  }

  hide(cx) {
    const item = this.confirm.item;
    this.busy = item.hidden ? `Unhiding ${item.title}…` : `Hiding ${item.title}…`;
    cx.notify();
    cx.spawn(async (cx) => {
      let result;
      try {
        result = await this.start(cx, "hide", { kind: item.kind, id: item.id, title: item.title, hidden: Boolean(item.hidden) });
      } catch (error) {
        result = { error: message(error) };
      }
      this.busy = "";
      if (!result.error && this.startOrigin === "close") notify({ title: result.message, level: "success" });
      this.backToList(result.error ? { text: sentence(result.error), tone: "error" } : { text: result.message, tone: "info" });
      this.fields.clear();
      this.load(cx);
      cx.notify();
    });
  }

  // --- view

  render(_cx) {
    const P = { ...DEFAULT_PALETTE, ...(this.data?.palette ?? {}) };
    const wide = this.width === 0 || this.width >= 760;
    let root = v_flex().id("galpon").track_focus(this.handle).key_context(CONTEXT).size_full()
      .bg(P.background).text_color(P.foreground).text_size(13)
      .on_key_down((event, cx) => this.keyDown(event, cx))
      .on_size_change((size, cx) => {
        if (Math.abs(size.width - this.width) < 1) return;
        this.width = size.width;
        cx.notify();
      });
    for (const [action, key] of ACTION_KEYS) {
      root = root.on_action(`galpon::${action}`, (_e, cx) => {
        if (key === "enter") return this.enter(cx);
        this.key(key, cx);
        cx.notify();
      });
    }
    switch (this.screen) {
      case "form": return root.children(this.formScreen(P, wide));
      case "choice": return root.children(this.choiceScreen(P, wide));
      case "targets": return root.children(this.targetsScreen(P));
      case "operations": return root.children(this.operationsScreen(P, wide));
      case "message": return root.children(this.messageScreen(P));
      case "confirm": return root.children(this.confirmScreen(P));
    }
    const row = this.rows[this.cursor];
    let body = this.detail ? this.inspector(P, row) : this.body(P, row, wide);
    if (this.menu) body = this.menuBody(P, row, wide);
    return root
      .child(this.header(P))
      .child(this.prompt(P))
      .child(body)
      .child(this.notice(P))
      .child(this.statusline(P, ...this.listStatus()));
  }

  section(P, text) {
    return h_flex().flex_none().gap(6).text_size(11).font_semibold().text_color(P.muted)
      .child(div().text_color(P.status).child("▰"))
      .child(div().child(text));
  }

  // titleBar is the header of a screen inside the command center.
  titleBar(P, crumbs, right = "") {
    let bar = h_flex().flex_none().h(40).px(18).gap(10).items_center().border_b(1).border_color(P["surface-raised"])
      .child(div().text_color(P.status).child("⌂"))
      .child(div().font_semibold().child("Galpon"));
    for (const crumb of crumbs) bar = bar.child(div().text_color(P.comment).child("›")).child(div().font_semibold().truncate().child(crumb));
    return bar.child(div().flex_1()).child(div().flex_none().text_size(12).text_color(P.muted).child(right));
  }

  header(P) {
    const attention = this.data?.pullRequests?.attention ?? 0;
    const tabs = VIEWS.map((view) => {
      const active = view === this.view;
      return h_flex().id(`galpon-view-${view}`).h_full().gap(6).items_center().cursor_pointer()
        .border_b(2).border_color(active ? P.status : P.background)
        .text_color(active ? P.status : P.muted)
        .when(active, (el) => el.font_semibold())
        .on_click((_e, cx) => {
          this.menu = null;
          this.input.focus();
          this.setView(view, cx);
          cx.notify();
        })
        .child(div().child(VIEW_ICONS[view]))
        .child(div().child(VIEW_LABELS[view]))
        .when(view === "pull-request" && attention > 0, (el) => el.child(div().text_color(P.yellow).child(String(attention))));
    });
    return h_flex().flex_none().h(40).px(18).gap(24).items_center()
      .border_b(1).border_color(P["surface-raised"])
      .child(h_flex().gap(8).items_center()
        .child(div().text_color(P.status).child("⌂"))
        .child(div().font_semibold().child("Galpon")))
      .child(h_flex().h_full().gap(20).children(tabs))
      .child(div().flex_1())
      .when(this.hidden, (el) => el.child(div().text_size(12).text_color(P.yellow).child("showing hidden")));
  }

  prompt(P) {
    return h_flex().flex_none().h(42).px(18).gap(10).items_center().bg(P.prompt)
      .child(div().text_size(16).font_bold().text_color(P.status).child("›"))
      .child(Input.new(this.input).flex_1().h_full().text_size(14).text_color(P.foreground))
      .when(this.loading && !this.data, (el) => el.child(div().text_size(12).text_color(P.muted).child("Loading…")));
  }

  body(P, row, wide) {
    const list = v_flex().flex_1().min_w(0).h_full()
      .when(this.columns() && wide, (el) => el.child(this.columnHeader(P)))
      .child(this.rows.length ? this.resultList(P, wide) : this.empty(P));
    if (!wide) return h_flex().flex_1().min_h(0).child(list);
    return h_flex().flex_1().min_h(0)
      .child(list)
      .child(div().w(1).h_full().flex_none().bg(P["surface-raised"]))
      .child(v_flex().w(this.sideWidth()).flex_none().h_full().child(this.inspector(P, row)));
  }

  sideWidth() {
    return Math.round(Math.min(480, Math.max(300, this.width * 0.36)));
  }

  // The actions menu takes the place of the inspector.
  menuBody(P, row, wide) {
    if (!wide) return v_flex().flex_1().min_h(0).child(this.menuPanel(P));
    const list = v_flex().flex_1().min_w(0).h_full().opacity(0.55)
      .when(this.columns(), (el) => el.child(this.columnHeader(P)))
      .child(this.rows.length ? this.resultList(P, wide) : this.empty(P));
    return h_flex().flex_1().min_h(0)
      .child(list)
      .child(div().w(1).h_full().flex_none().bg(P["surface-raised"]))
      .child(v_flex().w(this.sideWidth()).flex_none().h_full().child(this.menuPanel(P)));
  }

  menuPanel(P) {
    const menu = this.menu;
    let pane = v_flex().id("galpon-menu").size_full().overflow_y_scroll().px(18).py(14).gap(2)
      .child(this.section(P, "ACTIONS"))
      .child(div().pt(2).pb(8).text_size(16).font_semibold().truncate().child(menu.item?.title ?? "Galpon"));
    menu.actions.forEach((action, index) => {
      const selected = index === menu.cursor;
      pane = pane.child(h_flex().id(`galpon-action-${action.action}`).flex_none().h(ROW + 2).px(10).gap(12).items_center().cursor_pointer()
        .when(selected, (el) => el.bg(P.selection))
        .on_click((_e, cx) => {
          this.runAction(action.action, cx);
          cx.notify();
        })
        .child(h_flex().w(20).h(20).flex_none().items_center().justify_center().border_1()
          .border_color(selected ? P.status : P.comment).text_size(12).font_bold()
          .text_color(selected ? P.status : P.foreground).child(action.key))
        .child(div().w(140).flex_none().truncate().text_color(selected ? P.status : P.foreground).when(selected, (el) => el.font_semibold()).child(action.label))
        .child(div().flex_1().min_w(0).truncate().text_size(12).text_color(P.muted).child(action.detail)));
    });
    return pane;
  }

  // The Agents view has columns; search and the other views do not.
  columns() {
    return this.view === "agent" && !normalize(this.query);
  }

  columnHeader(P) {
    const cell = (text) => div().text_size(11).font_semibold().text_color(P.muted).child(text);
    return h_flex().flex_none().h(28).px(18).gap(10).items_center()
      .child(div().w(10).flex_none())
      .child(cell("NAME").flex_1())
      .child(cell("WORKSPACE").w(150).flex_none())
      .child(cell("STATE").w(92).flex_none())
      .child(cell("AGE").w(34).flex_none().text_right());
  }

  resultList(P, wide) {
    const rows = this.rows;
    const refWidth = Math.min(170, 12 + 7 * Math.max(0, ...rows.filter((row) => row.item?.ref).map((row) => row.item.ref.length)));
    const list = v_virtual_list(
      "galpon-rows",
      rows.length,
      rows.map((row) => (row.type === "header" ? HEADING : ROW)),
      (index) => rows[index].key,
      (range) => rows.slice(range.start, range.end).map((row, offset) => this.row(P, row, range.start + offset, wide, refWidth)),
    ).track_scroll(this.scroller)
      .on_item_click((key, cx) => this.click(key, cx))
      .on_item_secondary_click((key, _event, cx) => this.secondaryClick(key, cx))
      .size_full();
    return v_flex().relative().flex_1().min_h(0)
      .child(list)
      .child(Scrollbar.vertical("galpon-rows").absolute().inset_0());
  }

  empty(P) {
    let text = `No ${VIEW_LABELS[this.view].toLowerCase()} in this view.`;
    if (!this.data) text = this.error ? "Galpon is not available." : "Loading…";
    else if (normalize(this.query)) text = "No matching titles. Search includes all resource types.";
    else if (this.view === "pull-request") text = this.data.pullRequests?.empty ?? text;
    return div().flex_1().px(28).py(18).text_color(P.muted).child(text);
  }

  guide(P, mark) {
    const line = (el) => el.absolute().w(1).bg(P.comment);
    let cell = div().w(16).h_full().flex_none().relative();
    if (mark === "│" || mark === "├") cell = cell.child(line(div()).left(7).top(0).bottom(0));
    if (mark === "└") cell = cell.child(line(div()).left(7).top(0).h(ROW / 2));
    if (mark === "├" || mark === "└") cell = cell.child(div().absolute().left(7).top(ROW / 2).w(8).h(1).bg(P.comment));
    return cell;
  }

  stateMark(P, item) {
    if (item.state === "working" || item.live) return div().w(12).flex_none().text_color(item.color || P.yellow).frames(SPINNER, 90);
    return div().w(12).flex_none().text_color(item.color || P.muted).child(item.mark ?? "");
  }

  row(P, row, index, wide, refWidth) {
    const selected = index === this.cursor;
    if (row.type === "header") {
      return h_flex().h(HEADING).px(18).pb(6).gap(6).items_end().text_size(11).font_semibold().text_color(P.muted)
        .child(div().text_color(P.status).child("▰"))
        .child(div().child(row.title));
    }
    const ink = selected ? P.status : P.foreground;
    let el = h_flex().h(ROW).px(18).gap(10).items_center().when(selected, (it) => it.bg(P.selection))
      .child(div().w(10).flex_none().font_bold().text_color(P.status).child(selected ? "›" : ""));
    const title = (text) => div().flex_1().min_w(0).truncate().text_color(ink).when(selected, (it) => it.font_semibold()).child(text);
    if (row.type === "more") {
      return el.child(div().flex_none().text_color(P.muted).child(row.open ? "▾" : "▸"))
        .child(div().flex_none().text_color(ink).when(selected, (it) => it.font_semibold()).child(row.title))
        .child(div().flex_1().min_w(0).truncate().text_size(12).text_color(P.muted).child(row.detail));
    }
    const item = row.item;
    if (row.guide?.length) el = el.child(h_flex().h_full().flex_none().children(row.guide.map((mark) => this.guide(P, mark))));
    const muted = (text, width) => div().w(width).flex_none().truncate().text_color(P.muted).child(text ?? "");
    const age = div().w(34).flex_none().text_right().text_size(12).text_color(P.muted).child(item.age ?? "");
    switch (item.kind) {
      case "agent":
        return el.child(title(item.title))
          .when(item.delegatedCount > 0 && !row.workspace, (it) => it.child(div().flex_none().text_size(12).text_color(P.muted).child(`⊶ ${item.delegatedCount}`)))
          .when(Boolean(item.badge), (it) => it.child(div().flex_none().text_size(12).text_color(item.badgeColor || P.muted).child(item.badge)))
          .when(wide && !row.workspace, (it) => it.child(muted(item.workspaceTitle, 150)))
          .child(h_flex().w(92).flex_none().gap(6).items_center()
            .child(this.stateMark(P, item))
            .child(div().truncate().text_color(item.color || P.muted).child(item.label ?? "")))
          .child(age);
      case "pull-request":
        return el.child(div().w(12).flex_none().text_color(item.color || P.muted).child(item.mark ?? ""))
          .child(muted(item.ref, refWidth))
          .child(title(item.title))
          .when(wide, (it) => it.child(div().w(160).flex_none().truncate().text_color(item.agentTitle ? P.foreground : P.comment).child(item.agentTitle || "—")))
          .child(age);
      case "workspace":
        return el.child(div().flex_none().text_color(P.muted).child(row.open ? "▾" : "▸"))
          .child(title(item.title))
          .child(div().flex_none().text_size(12).text_color(P.muted).child(row.detail ?? ""));
      default:
        return el.child(title(item.title))
          .when(Boolean(item.detail), (it) => it.child(div().max_w(300).flex_none().truncate().text_size(12).text_color(P.muted).child(item.detail)));
    }
  }

  inspector(P, row) {
    let pane = v_flex().id("galpon-inspector").size_full().overflow_y_scroll().px(18).py(14).gap(5);
    if (!row || row.type === "header") {
      pane = pane.child(this.section(P, "SELECTED ITEM")).child(div().text_color(P.muted).child("Select a row to inspect it."));
    } else if (row.type === "more") {
      pane = pane.child(this.section(P, "DETAIL"))
        .child(div().text_size(16).font_semibold().child(row.title))
        .child(div().text_color(P.muted).child(row.detail));
    } else {
      const item = row.item;
      const known = this.fields.get(`${item.kind}:${item.id}`);
      pane = pane.child(this.section(P, "DETAIL")).child(div().pt(2).text_size(16).font_semibold().child(item.title));
      if (item.kind === "agent" || item.kind === "pull-request") {
        pane = pane.child(h_flex().gap(6).items_center().pb(6)
          .child(this.stateMark(P, item))
          .child(div().text_color(item.color || P.muted).child(item.label ?? "")));
      }
      for (const field of known?.fields ?? []) {
        if (field.section) {
          pane = pane.child(this.section(P, field.value).mt(12));
          continue;
        }
        pane = pane.child(h_flex().gap(12).items_start().when(Boolean(field.break), (it) => it.mt(10))
          .child(div().w(96).flex_none().pt(1).text_size(11).font_semibold().text_color(P.muted).child(field.label ?? ""))
          .child(div().flex_1().min_w(0).text_color(field.color || P.foreground).child(field.value)));
      }
      if (!known) pane = pane.child(div().text_color(P.muted).child("Loading…"));
      if (known?.error) pane = pane.child(div().pt(6).text_color(P.red).child(sentence(known.error)));
    }
    return v_flex().relative().size_full()
      .child(pane)
      .child(Scrollbar.vertical("galpon-inspector").absolute().inset_0());
  }

  // notice is the line above the statusline: a hint, the status of the last
  // action, the running action, or an error. It does not repeat the keys of
  // the statusline.
  notice(P, hint = "") {
    let text = hint;
    let color = P.muted;
    if (this.screen === "list") {
      text = this.view === "agent"
        ? "Attention first · alt+p pull requests · ctrl+r reorder · right click for actions"
        : "alt+p pull requests · ctrl+r reorder · right click for actions";
      if (this.view === "pull-request" && this.data?.pullRequests) text = this.data.pullRequests.notice;
      if (normalize(this.query)) text = "Search includes agents, workspaces, worktrees and repositories.";
      if (this.detail || this.menu) text = "";
    }
    if (this.status) {
      text = this.status.text;
      color = this.status.tone === "error" ? P.red : this.status.tone === "warn" ? P.yellow : P.muted;
    }
    if (this.busy) {
      text = this.busy;
      color = P.yellow;
    }
    if (this.error && this.screen === "list") {
      text = `Galpon: ${this.error}`;
      color = P.red;
    }
    return h_flex().flex_none().h(30).px(18).gap(8).items_center().text_size(12).text_color(color)
      .border_t(1).border_color(P["surface-raised"])
      .when(Boolean(this.busy), (el) => el.child(div().w(12).flex_none().frames(SPINNER, 90)))
      .child(div().flex_1().min_w(0).truncate().child(text));
  }

  listStatus() {
    if (this.menu) return ["ACTIONS", [["a–z", "run"], ["↑↓", "select"], ["enter", "run", "enter"], ["esc", "close", "escape"]], ""];
    const searching = Boolean(normalize(this.query));
    const mode = this.detail ? "DETAIL" : searching ? "SEARCH" : GROUP_TITLES[this.view];
    const hints = this.detail
      ? [["enter", "open", "enter"], ["ctrl+space", "actions", "ctrl-space"], ["esc", "back to the list", "escape"]]
      : this.view === "pull-request" && !searching
      ? [["enter", "open agent", "enter"], ["ctrl+o", "open on GitHub", "ctrl-o"], ["ctrl+r", "refresh", "ctrl-r"], ["ctrl+space", "actions", "ctrl-space"], ["alt+p", "back", "alt-p"], ["esc", "close", "escape"]]
      : [["enter", "open", "enter"], ["ctrl+space", "actions", "ctrl-space"], ["tab", "expand", "tab"], ["shift+tab", "views", "shift-tab"], ["ctrl+n", "new agent", "ctrl-n"], ["ctrl+f", "fork", "ctrl-f"], ["ctrl+g", "detail", "ctrl-g"], ["esc", "close", "escape"]];
    const selectableRows = this.rows.filter(selectable);
    const position = selectableRows.length ? `${selectableRows.indexOf(this.rows[this.cursor]) + 1} / ${selectableRows.length}` : "0 / 0";
    return [mode, hints, position];
  }

  // statusline is the blue mode block, the key hints and a position. A hint
  // with a key name also works with a click.
  statusline(P, mode, hints, position = "") {
    return h_flex().flex_none().h(28).items_center().text_size(12)
      .child(h_flex().h_full().px(14).items_center().bg(P.status).text_color(P["status-ink"]).font_bold().child(mode))
      .child(h_flex().h_full().flex_1().min_w(0).px(14).gap(14).items_center().overflow_hidden().bg(P["surface-raised"])
        .children(hints.map(([key, label, name], index) => h_flex().id(`galpon-hint-${index}`).flex_none().gap(5)
          .when(Boolean(name), (el) => el.cursor_pointer().on_click((_e, cx) => {
            this.key(name, cx);
            cx.notify();
          }))
          .child(div().font_semibold().text_color(P.foreground).child(key))
          .child(div().text_color(P.muted).child(label)))))
      .when(Boolean(position), (el) => el.child(h_flex().h_full().px(14).items_center().bg(P.prompt).text_color(P.muted).child(position)));
  }

  // actionBar holds the button that submits a screen. While the screen
  // works, the button is quiet, and only the notice line shows the work.
  actionBar(P, label, focused, submit, key) {
    const busy = Boolean(this.busy);
    return h_flex().flex_none().h(50).px(18).gap(12).items_center().border_t(1).border_color(P["surface-raised"])
      .child(h_flex().id("galpon-submit").h(32).px(16).gap(8).items_center().when(!busy, (el) => el.cursor_pointer())
        .bg(focused && !busy ? P.status : P["surface-raised"]).text_color(busy ? P.muted : focused ? P["status-ink"] : P.foreground).font_semibold()
        .on_click((_e, cx) => {
          submit(cx);
          cx.notify();
        })
        .child(div().child(label)))
      .child(div().text_size(12).text_color(P.muted).child(key));
  }

  // --- form screens

  formScreen(P, wide) {
    const form = this.form;
    const fields = form.fields();
    const submit = fields.find((field) => field.type === "submit");
    let column = v_flex().id("galpon-form").size_full().overflow_y_scroll().py(10);
    if (form.description) column = column.child(div().flex_none().px(46).pb(8).text_color(P.muted).child(form.description));
    let section = null;
    for (const field of fields) {
      if (field.type === "submit") continue;
      if (field.section !== section) {
        section = field.section;
        if (section) column = column.child(this.section(P, section).px(18).pt(14).pb(6));
      }
      column = column.child(this.fieldRow(P, field));
      // An error of a field shows under the field. The notice line shows an
      // error that is not about one field.
      const marked = Boolean(this.formError?.field) && this.formError.field === field.key;
      if (marked) column = column.child(div().flex_none().pl(46).pr(18).py(3).text_size(12).text_color(P.red).child(`! ${this.formError.text}`));
      else if (field.key === this.focusKey && field.hint && (!wide || field.hint.startsWith("d "))) {
        column = column.child(div().flex_none().pl(46).pr(18).py(3).text_size(12).text_color(P.muted).child(field.hint));
      }
    }
    let body = h_flex().flex_1().min_h(0).child(v_flex().relative().flex_1().min_w(0).h_full()
      .child(column)
      .child(Scrollbar.vertical("galpon-form").absolute().inset_0()));
    if (wide) {
      let summary = v_flex().id("galpon-summary").size_full().overflow_y_scroll().px(18).py(14).gap(4)
        .child(this.section(P, form.summaryTitle));
      for (const effect of form.summary()) {
        summary = summary.child(v_flex().flex_none().pt(10).gap(2)
          .when(Boolean(effect.label), (el) => el.child(div().font_semibold().child(effect.label)))
          .child(div().text_color(P.muted).child(effect.text)));
      }
      body = body.child(div().w(1).h_full().flex_none().bg(P["surface-raised"]))
        .child(v_flex().w(this.sideWidth()).flex_none().h_full().child(summary));
    }
    const back = this.formOrigin === "close" ? "close" : "back";
    const hints = [["tab", "choose / next", "tab"], ["↑↓", "fields"], ["←→", "change"], ["space", "toggle"], ["ctrl+s", submit.value.split(" ")[0].toLowerCase(), "ctrl-s"], ["esc", back, "escape"]];
    if (form.kind === "repository") hints.splice(1, 3, ...(HOST ? [] : [["ctrl+o", "choose folder", "ctrl-o"]]));
    if (form.kind === "workspace" || form.kind === "rename") hints.splice(1, 3);
    const notice = this.formError?.field ? "Correct the marked field before you submit." : this.formError?.text ?? "Draft · changes apply only on submit";
    return [
      this.titleBar(P, [form.title]),
      body,
      this.actionBar(P, submit.value, this.focusKey === submit.key, (cx) => this.submitForm(cx), "ctrl+s"),
      this.formError && !this.status && !this.busy ? this.errorNotice(P, notice) : this.notice(P, notice),
      this.statusline(P, form.mode, hints),
    ];
  }

  errorNotice(P, text) {
    return h_flex().flex_none().h(30).px(18).items_center().text_size(12).text_color(P.red)
      .border_t(1).border_color(P["surface-raised"])
      .child(div().flex_1().min_w(0).truncate().child(text));
  }

  fieldRow(P, field) {
    const focused = field.key === this.focusKey;
    const marked = this.formError?.field === field.key;
    const row = h_flex().id(`galpon-field-${field.key}`).flex_none().h(FIELD).px(18).gap(10).items_center().cursor_pointer()
      .when(focused, (el) => el.bg(P.selection))
      .on_click((_e, cx) => {
        this.clickField(field, cx);
        cx.notify();
      })
      .child(div().w(10).flex_none().font_bold().text_color(marked ? P.red : P.status).child(focused ? "›" : marked ? "!" : ""))
      .child(div().w(168).flex_none().truncate().text_size(12)
        .text_color(marked ? P.red : focused ? P.status : P.muted)
        .when(focused || Boolean(field.strong), (el) => el.font_semibold())
        .child(field.label));
    switch (field.type) {
      case "text": {
        const state = this.formInputs.get(field.key);
        return row.child(h_flex().flex_1().min_w(0).h(26).items_center().border_b(1).border_color(focused ? P.status : P["surface-raised"])
          .child(state ? Input.new(state).flex_1().h_full().text_size(13).text_color(P.foreground) : div()));
      }
      case "choice":
        return row.child(div().flex_1().min_w(0).truncate().when(focused, (el) => el.font_semibold()).child(field.value))
          .child(div().flex_none().text_color(focused ? P.status : P.muted).child("▾"));
      case "toggle":
        return row.child(h_flex().flex_1().min_w(0).gap(8).items_center()
          .child(h_flex().w(14).h(14).flex_none().items_center().justify_center().border_1()
            .border_color(field.on || focused ? P.status : P.muted).text_size(10).font_bold()
            .when(Boolean(field.on), (el) => el.bg(P.status).text_color(P["status-ink"]))
            .child(field.on ? "✓" : ""))
          .child(div().truncate().child(field.value)));
      case "action":
        return row.child(div().flex_1().min_w(0).truncate().text_color(P.status).when(focused, (el) => el.font_semibold()).child(`+ ${field.value}`));
      default:
        return row.child(div().flex_1().min_w(0).truncate().text_color(P.muted).child(field.value));
    }
  }

  choiceScreen(P, wide) {
    const choice = this.choice;
    const options = choice.filtered;
    const list = options.length
      ? v_virtual_list(
        "galpon-choices",
        options.length,
        ROW,
        (index) => `option:${options[index].value}`,
        (range) => options.slice(range.start, range.end).map((option, offset) => this.optionRow(P, option, range.start + offset === choice.cursor)),
      ).track_scroll(this.choiceScroller)
        .on_item_click((key, cx) => {
          this.applyChoice(options.find((option) => `option:${option.value}` === key));
          cx.notify();
        })
        .size_full()
      : div().px(28).py(18).text_color(P.muted).child("No matching options. Change the filter.");
    let body = h_flex().flex_1().min_h(0).child(v_flex().relative().flex_1().min_w(0).h_full().pt(6)
      .child(list)
      .child(Scrollbar.vertical("galpon-choices").absolute().inset_0()));
    const option = options[choice.cursor];
    if (wide) {
      body = body.child(div().w(1).h_full().flex_none().bg(P["surface-raised"]))
        .child(v_flex().w(this.sideWidth()).flex_none().h_full().px(18).py(14).gap(5)
          .child(this.section(P, "DETAIL"))
          .when(Boolean(option), (el) => el.child(div().pt(2).text_size(16).font_semibold().child(option.label))
            .child(div().text_color(P.muted).child(option.detail ?? ""))));
    }
    return [
      this.titleBar(P, [this.form.title, choice.title]),
      h_flex().flex_none().h(42).px(18).gap(10).items_center().bg(P.prompt)
        .child(div().text_size(16).font_bold().text_color(P.status).child("›"))
        .child(Input.new(this.choiceInput).flex_1().h_full().text_size(14).text_color(P.foreground)),
      body,
      this.notice(P),
      this.statusline(P, "CHOOSE", [["type", "filter"], ["↑↓", "select"], ["enter", "use", "enter"], ["esc", "cancel", "escape"]],
        options.length ? `${choice.cursor + 1} / ${options.length}` : "0 / 0"),
    ];
  }

  optionRow(P, option, selected) {
    return h_flex().h(ROW).px(18).gap(10).items_center().when(selected, (el) => el.bg(P.selection))
      .child(div().w(10).flex_none().font_bold().text_color(P.status).child(selected ? "›" : ""))
      .child(div().flex_none().max_w(420).truncate().text_color(selected ? P.status : P.foreground).when(selected, (el) => el.font_semibold()).child(option.label))
      .child(div().flex_1().min_w(0).truncate().text_size(12).text_color(P.muted).child(option.detail ?? ""));
  }

  targetsScreen(P) {
    const targets = this.targets;
    const rows = targets.rows;
    const list = v_virtual_list(
      "galpon-targets",
      rows.length,
      rows.map((row) => (row.type === "header" ? HEADING : ROW)),
      (index) => rows[index].key,
      (range) => rows.slice(range.start, range.end).map((row, offset) => {
        if (row.type === "header") {
          return h_flex().h(HEADING).px(18).pb(6).gap(6).items_end().text_size(11).font_semibold().text_color(P.muted)
            .child(div().text_color(P.status).child("▰"))
            .child(div().child(row.title));
        }
        return this.optionRow(P, { label: row.target.label, detail: row.target.detail }, range.start + offset === targets.cursor);
      }),
    ).track_scroll(this.targetScroller)
      .on_item_click((key, cx) => {
        const row = rows.find((candidate) => candidate.key === key);
        if (row?.type === "target") this.openTarget(row.target, targets.editor, cx);
        cx.notify();
      })
      .size_full();
    const verb = targets.editor ? "Open editor" : "Open terminal";
    return [
      this.titleBar(P, [verb]),
      v_flex().relative().flex_1().min_h(0).pt(6)
        .child(list)
        .child(Scrollbar.vertical("galpon-targets").absolute().inset_0()),
      this.notice(P, targets.editor ? "Select where the editor opens" : "Select where the terminal opens"),
      this.statusline(P, targets.editor ? "EDITOR" : "TERMINAL", [["enter", "open", "enter"], ["↑↓", "select"], ["esc", "back", "escape"]]),
    ];
  }

  operationsScreen(P, wide) {
    const ops = this.ops;
    const data = ops.data;
    let summary = data?.summary ?? "Reading the facts of the selected agent…";
    if (ops.refreshError) summary += " · refresh failed; showing prior facts";
    let body;
    if (!data && ops.error) {
      body = v_flex().flex_1().px(28).py(18).gap(6)
        .child(div().font_semibold().text_color(P.red).child("Operations unavailable"))
        .child(div().text_color(P.muted).child(sentence(ops.error)));
    } else if (!data) {
      body = h_flex().flex_1().px(28).py(18).gap(8).items_start()
        .child(div().text_color(P.yellow).frames(SPINNER, 90))
        .child(div().text_color(P.muted).child("Loading operations…"));
    } else {
      const rows = data.rows;
      const list = rows.length
        ? v_virtual_list(
          "galpon-operations",
          rows.length,
          ROW,
          (index) => `work:${rows[index].id}`,
          (range) => rows.slice(range.start, range.end).map((row, offset) => {
            const selected = range.start + offset === ops.cursor;
            return h_flex().h(ROW).px(18).gap(10).items_center().when(selected, (el) => el.bg(P.selection))
              .child(div().w(10).flex_none().font_bold().text_color(P.status).child(selected ? "›" : ""))
              .child(this.stateMark(P, row))
              .child(div().flex_1().min_w(0).truncate().text_color(selected ? P.status : P.foreground).when(selected, (el) => el.font_semibold()).child(row.title))
              .child(div().flex_none().text_size(12).text_color(P.muted).child(`${row.section} · ${row.direction}`));
          }),
        ).track_scroll(this.opsScroller)
          .on_item_click((key, cx) => {
            const index = rows.findIndex((row) => `work:${row.id}` === key);
            if (index >= 0) {
              ops.cursor = index;
              ops.selected = rows[index].id;
            }
            cx.notify();
          })
          .size_full()
        : div().px(28).py(14).text_color(P.muted).child("No current work, attention, or recent results");
      const left = v_flex().relative().flex_1().min_w(0).h_full()
        .child(this.section(P, "AGENT WORK").px(18).pt(12).pb(6))
        .child(v_flex().relative().flex_1().min_h(0).child(list).child(Scrollbar.vertical("galpon-operations").absolute().inset_0()));
      const row = rows[ops.cursor];
      let side = v_flex().id("galpon-operations-detail").size_full().overflow_y_scroll().px(18).py(12).gap(4)
        .child(this.section(P, "SELECTED DETAIL"));
      if (!row) side = side.child(div().text_color(P.muted).child("No work item is selected."));
      (row?.detail ?? []).forEach((line, index) => {
        const fact = line.startsWith("Observed") || line.startsWith("Reported");
        side = side.child(div().when(index === 0, (el) => el.text_size(15).font_semibold()).text_color(fact ? P.cyan : P.foreground).child(line));
      });
      side = side.child(this.section(P, "SELECTED AGENT").mt(14))
        .child(h_flex().gap(6).items_start()
          .child(div().w(12).flex_none().text_color(data.runtime.color || P.muted).child(data.runtime.mark ?? ""))
          .child(div().flex_1().min_w(0).child(data.runtime.text)));
      for (const line of data.direct ?? []) side = side.child(div().text_color(P.muted).child(line));
      if (data.activity?.length) {
        side = side.child(div().pt(6).text_size(11).font_semibold().text_color(P.comment).child("OBSERVED ACTIVITY"));
        for (const line of data.activity) side = side.child(div().text_color(P.muted).child(line));
      }
      body = wide
        ? h_flex().flex_1().min_h(0).child(left)
          .child(div().w(1).h_full().flex_none().bg(P["surface-raised"]))
          .child(v_flex().w(Math.round(Math.min(560, Math.max(320, this.width * 0.42)))).flex_none().h_full().child(side))
        : v_flex().flex_1().min_h(0).child(left).child(v_flex().h(260).flex_none().border_t(1).border_color(P["surface-raised"]).child(side));
    }
    const count = data?.rows.length ?? 0;
    return [
      this.titleBar(P, ["Operations", ops.data?.agent || ops.title], ops.loading ? "reading…" : ""),
      h_flex().flex_none().h(42).px(18).items_center().bg(P.prompt).child(div().flex_1().min_w(0).truncate().child(summary)),
      body,
      this.notice(P, "Observed facts come from Galpon; reported facts come from the agent."),
      this.statusline(P, "OPERATIONS", [["↑↓", "select"], ["r", "refresh"], ["esc", "back", "escape"]], count ? `${ops.cursor + 1} / ${count}` : ""),
    ];
  }

  messageScreen(P) {
    const target = this.message;
    const label = (text) => div().w(56).flex_none().text_size(11).font_semibold().text_color(P.muted).child(text);
    const body = v_flex().flex_1().min_h(0).px(18).py(14).gap(8)
      .child(h_flex().gap(12).items_center().child(label("FROM")).child(div().child("You")))
      .child(h_flex().gap(12).items_center().child(label("TO"))
        .child(div().font_semibold().child(target.title))
        .when(Boolean(target.workspace), (el) => el.child(div().text_color(P.muted).child(`/ ${target.workspace}`))))
      .child(v_flex().flex_1().min_h(120).mt(6).pt(8).border_t(1).border_color(P["surface-raised"])
        .child(Textarea.new(this.textarea).size_full().text_size(13).text_color(P.foreground)))
      .when(Boolean(this.messageError), (el) => el.child(div().text_color(P.red).child(`! ${this.messageError}`)));
    return [
      this.titleBar(P, ["Message"]),
      body,
      this.actionBar(P, "Send message", false, (cx) => this.sendMessage(cx), "ctrl+s · enter adds a new line"),
      this.notice(P),
      this.statusline(P, "MESSAGE", [["ctrl+s", "send", "ctrl-s"], ["esc", "back · draft kept", "escape"]]),
    ];
  }

  confirmScreen(P) {
    const item = this.confirm.item;
    const verb = item.hidden ? "Unhide" : "Hide";
    const button = (id, text, primary, name) => h_flex().id(id).h(32).px(16).items_center().cursor_pointer().font_semibold()
      .bg(primary ? P.status : P["surface-raised"]).text_color(primary ? P["status-ink"] : P.foreground)
      .on_click((_e, cx) => {
        this.key(name, cx);
        cx.notify();
      })
      .child(text);
    return [
      this.titleBar(P, [verb]),
      v_flex().flex_1().min_h(0).px(28).py(24).gap(10)
        .child(div().text_size(18).font_semibold().child(`${verb} ${item.title}`))
        .child(div().max_w(640).text_color(P.muted).child("This action can affect dependent resources and their active views. Review the selected item before you continue."))
        .child(h_flex().pt(12).gap(10)
          .child(button("galpon-confirm", verb, true, "enter"))
          .child(button("galpon-cancel", "Cancel", false, "escape"))),
      this.notice(P, ""),
      this.statusline(P, "CONFIRM", [["enter", verb.toLowerCase(), "enter"], ["esc", "cancel", "escape"]]),
    ];
  }
}
