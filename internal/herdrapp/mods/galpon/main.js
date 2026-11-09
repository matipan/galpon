// Galpon command center: the popup of Ctrl+K. `galpon herdr-app install`
// writes this mod. Run that command again after the galpon executable moves.
//
// On a screen, another computer runs Galpon: the host. The mod then runs
// galpon on the host through herdr-core, and names and focuses here what
// Galpon opens there.
//
// The mod reads Galpon through the galpon command: `herdr-app view` gives the
// rows, `herdr-app brief` the last reply and the open questions of an agent,
// `herdr-app form` the first draft of a form, and `herdr-app start` runs an
// action. The list screen is in list.js and its rows in model.js: one search
// field, one list and a footer of key chips. The forms, the choices and the
// places of a terminal are in screens.js, with the same look.

import { View } from "gpui-kit";
import { v_flex, InputState, VirtualListScrollHandle } from "gpui-base";
import { closeOverlay, exec, notify, panes, pickFolder, props, readTextFile, run, setShared, shared, spaces, window as appWindow } from "herdr";
import { selectable } from "./model.js";
import { createForm, filterOptions } from "./forms.js";
import { ListMethods } from "./list.js";
import { ScreenMethods } from "./screens.js";
import { S, SANS, keepSelection, palette } from "./look.js";

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
const JOB_POLL = 400;
const JOB_LIMIT = 15 * 60 * 1000;
const CONTEXT = "GalponCommandCenter";
const DRAFTS = "galpon.messageDrafts";

// The search field and the form fields keep the keyboard, so the command
// center binds its keys in its own context. They win over the app's global
// keys. Each binding is [keystroke, action, key]: the screens get the key.
const KEYS = [
  ["up", "up", "up"], ["ctrl-p", "up", "up"], ["down", "down", "down"],
  ["pageup", "page-up", "pageup"], ["pagedown", "page-down", "pagedown"],
  ["tab", "expand", "tab"], ["shift-tab", "back", "shift-tab"], ["enter", "enter", "enter"],
  ["left", "left", "left"], ["right", "right", "right"],
  ["alt-p", "pull-requests", "alt-p"], ["ctrl-r", "reorder", "ctrl-r"],
  ["ctrl-o", "github", "ctrl-o"], ["ctrl-n", "new-agent", "ctrl-n"], ["ctrl-f", "fork-agent", "ctrl-f"],
  ["ctrl-s", "save", "ctrl-s"], ["ctrl-h", "hidden", "ctrl-h"], ["ctrl-space", "actions", "ctrl-k"],
  ["ctrl-k", "actions", "ctrl-k"], ["ctrl-enter", "reply", "ctrl-enter"], ["ctrl-t", "terminal", "ctrl-t"], ["ctrl-e", "editor", "ctrl-e"],
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
  if (place.app) {
    // An agent in the app view shows in its tab, which holds the agent mod.
    const tab = `${HOST}/${place.tab}`;
    while (Date.now() - begin < SCREEN_WAIT) {
      if (spaces().some((space) => space.tabs.some((item) => item.id === tab))) {
        run("tab.select", { id: tab });
        return true;
      }
      await cx.sleep(200);
    }
    return false;
  }
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
    this.query = "";
    this.expanded = new Set();
    this.order = new Map();
    this.rows = [];
    this.cursor = 0;
    this.selected = "";
    this.hidden = false;
    this.pendingMove = "";
    this.wanted = "";
    this.width = 0;
    this.form = null;
    this.formInputs = new Map();
    this.formOrigin = "list";
    this.formError = null;
    this.focusKey = "";
    this.choice = null;
    this.targets = null;
    this.moreOptions = false;
    // "close" when the command center opened on one action: leaving that
    // screen closes the command center.
    this.startOrigin = "list";
    this.drafts = { ...(shared(DRAFTS) ?? {}) };
    this.handle = cx.focus_handle();
    this.scroller = VirtualListScrollHandle.new();
    this.choiceScroller = VirtualListScrollHandle.new();
    this.targetScroller = VirtualListScrollHandle.new();
    this.enterAt = 0;
    this.hostMachine = HOST || null;
    this.hostId = (id) => hostId(id);
    this.input = InputState.new({ placeholder: "Search agents, pull requests and workspaces" });
    this.input.on("change", (_e, cx) => this.search(cx));
    this.input.on("submit", (_e, cx) => this.enter(cx));
    // The filter of the choices and of the places of a terminal.
    this.choiceInput = InputState.new({ placeholder: "Filter…" });
    this.choiceInput.on("change", (_e, cx) => {
      if (this.screen === "choice") this.filterChoice();
      if (this.screen === "targets") this.filterTargets();
      cx.notify();
    });
    this.choiceInput.on("submit", (_e, cx) => this.enter(cx));
    if (!keysBound) {
      try {
        cx.bind_keys(KEYS.map(([keystroke, action]) => ({ keystroke, action: `galpon::${action}`, context: CONTEXT })));
        keysBound = true;
      } catch (error) {
        this.status = { text: `Galpon keys: ${message(error)}`, tone: "error" };
      }
    }
    this.listInit(cx);
    // The overlay takes the keyboard after its first frames.
    this.refocus();
    cx.timer.after(120, () => this.refocus());
    cx.timer.after(600, () => this.refocus());
    this.load(cx);
    cx.timer.every(POLL, (cx) => this.load(cx));
    if (props()?.start === "new-agent") this.startNewAgent(cx);
    if (props()?.start === "action") this.startAction(cx);
  }

  // --- Galpon

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
          this.refreshRows();
          this.afterLoad(cx);
        }
      } catch (error) {
        this.error = message(error);
      }
      this.loading = false;
      if (this.screen === "list") this.briefSelected(cx);
      cx.notify();
    });
  }

  refreshRows() {
    const rows = this.listRows();
    // A virtual list needs one key per row.
    const seen = new Set();
    for (const row of rows) {
      while (seen.has(row.key)) row.key += "+";
      seen.add(row.key);
    }
    this.rows = rows;
    let index = rows.findIndex((row) => row.key === this.selected && this.isSelectable(row));
    if (index < 0) index = this.nearestSelectable(Math.min(this.cursor, rows.length - 1));
    this.cursor = Math.max(0, index);
    this.selected = rows[this.cursor]?.key ?? "";
  }

  nearestSelectable(from) {
    for (let index = Math.max(0, from); index < this.rows.length; index++) if (this.isSelectable(this.rows[index])) return index;
    for (let index = Math.min(from, this.rows.length - 1); index >= 0; index--) if (this.isSelectable(this.rows[index])) return index;
    return -1;
  }

  isSelectable(row) {
    return selectable(row);
  }

  // The brief of the selected agent comes after the selection stops for a
  // moment.
  briefSelected(cx) {
    const row = this.rows[this.cursor];
    if (row?.type !== "item" || row.item.kind !== "agent") return;
    const id = row.item.id;
    this.wanted = `agent:${id}`;
    cx.timer.after(this.briefs.has(id) ? 0 : 80, (cx) => {
      if (this.wanted === `agent:${id}`) this.loadBrief(id, cx);
    });
  }

  // --- keys

  // refocus gives the keyboard to the field of the current screen.
  refocus() {
    switch (this.screen) {
      case "list":
        if (this.actions) return this.actionInput.focus();
        if (this.reply) return this.replyInput.focus();
        if (this.answering) return this.answerInput.focus();
        return this.input.focus();
      case "form": return this.focusField(this.focusKey);
      case "choice":
      case "targets": return this.choiceInput.focus();
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
      case "list": return this.listKey(name, cx);
      case "form": return this.formKey(name, cx);
      case "choice": return this.choiceKey(name, cx);
      case "targets": return this.targetsKey(name, cx);
    }
  }

  // keyDown takes Backspace in an empty search of a scope, and Space, + and
  // d on form fields that hold no text. A field that holds text gets every
  // letter.
  keyDown(event, cx) {
    if (this.listKeyDown(event, cx)) return;
    if (this.busy || event.modifiers?.control || event.modifiers?.alt || event.modifiers?.platform) return;
    const letter = event.key === "space" ? " " : event.key_char ?? event.key;
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
    // A peek closes when the selection leaves its agent.
    if (this.peek && !this.inPeek(this.rows[index])) {
      const key = this.rows[index].key;
      this.peek = null;
      this.reply = null;
      this.answering = null;
      this.refreshRows();
      index = Math.max(0, this.rows.findIndex((row) => row.key === key));
    }
    const key = this.rows[index].key;
    if (key !== this.selected) {
      this.pendingMove = "";
      this.status = null;
    }
    this.cursor = index;
    this.selected = key;
    this.scroller.scroll_to_item(index);
    this.briefSelected(cx);
  }

  move(delta, cx) {
    const step = Math.sign(delta);
    let index = this.cursor;
    for (let count = 0; count < Math.abs(delta); count++) {
      let next = index + step;
      while (next >= 0 && next < this.rows.length && !this.isSelectable(this.rows[next])) next += step;
      if (next < 0 || next >= this.rows.length) break;
      index = next;
    }
    this.select(index, cx);
  }

  search(cx) {
    const query = this.input.value();
    if (query === this.query) return;
    this.query = query;
    this.order = new Map();
    this.groups = new Map();
    for (const key of [...this.expanded]) if (key.startsWith("search-") || key.startsWith("workspace:")) this.expanded.delete(key);
    this.cursor = 0;
    this.selected = "";
    this.status = null;
    this.pendingMove = "";
    this.refreshRows();
    this.scroller.scroll_to_item(this.cursor);
    this.briefSelected(cx);
    cx.notify();
  }

  selectedItem() {
    const row = this.rows[this.cursor];
    return row?.type === "item" ? row.item : null;
  }

  click(key, cx) {
    this.actions = null;
    if (this.reply) this.replyKey("escape", cx);
    this.answering = null;
    this.input.focus();
    const index = this.rows.findIndex((row) => row.key === key);
    if (index < 0 || !this.isSelectable(this.rows[index])) return;
    if (index === this.cursor) this.activate(cx);
    else this.select(index, cx);
    cx.notify();
  }

  // A right click selects a row and opens its actions.
  secondaryClick(key, cx) {
    const index = this.rows.findIndex((row) => row.key === key);
    if (index < 0 || !this.isSelectable(this.rows[index])) return;
    this.select(index, cx);
    this.openActions(this.rows[index], cx);
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

  // switchView moves a Pi agent between a tab of the app and a terminal,
  // and opens it there. Its process restarts with the same conversation.
  switchView(item, cx) {
    const view = item.view === "app" ? "terminal" : "app";
    this.busy = view === "app" ? `Opening ${item.title} in a tab` : `Opening ${item.title} in a terminal`;
    this.status = null;
    cx.notify();
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["agent-view", item.id, view]);
      } catch (error) {
        out = { error: message(error) };
      }
      if (!out.error) return this.finish(out.place, cx);
      this.busy = "";
      this.say(sentence(out.error), "error");
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

  perform(action, item, cx) {
    const origin = this.startOrigin;
    switch (action) {
      case "message": return this.messageAgent(item, cx);
      case "terminal":
      case "editor": {
        const editor = action === "editor";
        if (item.kind === "repository") return this.openForm(cx, "worktree", ["worktree", item.id, ...(editor ? ["--editor"] : [])]);
        return this.openTargets(item, editor, cx);
      }
      case "new-agent-here": return this.openForm(cx, "agent", ["agent", "here", item.kind, item.id], { origin });
      case "fork-agent": return this.openForm(cx, "agent", ["agent", "fork", "agent", item.id], { fork: true, origin });
      case "rename": return this.showForm(createForm("rename", item), { origin });
      case "switch-view": return this.switchView(item, cx);
      case "hide": return this.showForm(createForm("hide", item), { origin });
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

  // showForm shows a form. options.name fills in the name of a new agent.
  showForm(form, options = {}) {
    this.releaseFormInputs();
    if (options.name) form.setText("name", options.name);
    this.form = form;
    this.formOrigin = options.origin ?? "list";
    this.formError = null;
    this.moreOptions = false;
    this.screen = "form";
    this.actions = null;
    this.reply = null;
    this.answering = null;
    this.status = form.status ? { text: form.status, tone: "warn" } : null;
    this.syncInputs();
    this.focusField(this.formRows()[0].key);
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

  // formRows gives the rows of the form in order: the strong field, which
  // shows at the top, then the others. The fields marked more show after the
  // "More options" row, only when it is open. The submit row comes last.
  formRows() {
    const fields = this.form.fields();
    const head = fields.filter((field) => field.strong);
    const rest = fields.filter((field) => !field.strong && field.type !== "submit");
    const more = rest.filter((field) => field.more);
    const rows = [...head, ...rest.filter((field) => !field.more)];
    if (more.length) {
      const labels = [...new Set(more.map((field) => field.label).filter(Boolean))];
      rows.push({ key: "more", type: "more", label: this.moreOptions ? "Fewer options" : "More options", value: labels.join(", ") });
      if (this.moreOptions) rows.push(...more);
    }
    return rows.concat(fields.filter((field) => field.type === "submit"));
  }

  currentField() {
    if (!this.form) return null;
    return this.formRows().find((field) => field.key === this.focusKey) ?? null;
  }

  focusField(key) {
    this.focusKey = key;
    const state = this.formInputs.get(key);
    if (state) state.focus();
    else this.handle.focus();
  }

  moveField(delta) {
    const rows = this.formRows();
    const index = Math.max(0, rows.findIndex((field) => field.key === this.focusKey));
    this.focusField(rows[wrap(index, delta, rows.length)].key);
  }

  // changed follows a change of the draft: the fields and their inputs can
  // change, and the focus stays on the same field when it is still there.
  changed() {
    this.formError = null;
    this.syncInputs();
    if (this.currentField()) return this.focusField(this.focusKey);
    const rows = this.formRows();
    this.focusField((rows.find((field) => field.key === "placement") ?? rows[rows.length - 1]).key);
  }

  toggleMore() {
    this.moreOptions = !this.moreOptions;
    this.focusField("more");
  }

  addWorktree() {
    const error = this.form.add?.();
    if (error) {
      this.formError = { text: sentence(error), field: "add" };
      return;
    }
    const count = this.form.draft.worktrees.length;
    this.moreOptions = true;
    this.syncInputs();
    this.focusField(`repository:${count - 1}`);
  }

  clickField(field, cx) {
    if (this.busy) return;
    this.focusField(field.key);
    switch (field.type) {
      case "choice": return this.openChoice(field);
      case "toggle":
        this.form.toggle(field.key);
        return this.changed();
      case "action": return this.addWorktree();
      case "more": return this.toggleMore();
      case "submit": return this.submitForm(cx);
    }
  }

  // formKey: Enter sends the form from a text field and from the submit
  // row, opens a choice, switches a toggle, and adds a repository.
  formKey(name, cx) {
    const field = this.currentField();
    switch (name) {
      case "escape": return this.leaveForm();
      case "tab":
      case "down":
        return this.moveField(1);
      case "up":
      case "shift-tab":
        return this.moveField(-1);
      case "ctrl-s": return this.submitForm(cx);
      case "ctrl-o":
        // The folder picker shows the folders of this computer, not of the host.
        if (this.form.kind === "repository" && !HOST) this.chooseFolder(cx);
        return;
      case "enter":
        switch (field?.type) {
          case "choice": return this.openChoice(field);
          case "toggle":
            this.form.toggle(field.key);
            return this.changed();
          case "action": return this.addWorktree();
          case "more": return this.toggleMore();
          default: return this.submitForm(cx);
        }
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
        const marked = form.fields().find((field) => field.key === result.field);
        if (marked?.more) this.moreOptions = true;
        this.formError = { text: sentence(result.error), field: marked ? result.field : "" };
        if (marked) this.focusField(result.field);
      } else {
        if (form.kind === "rename") renameTab(form.agentId, form.payload().title);
        // An agent that exists but did not open stays: the notice says so.
        const tone = form.kind === "agent" ? "warn" : "info";
        // From the sidebar menu, the command center closes with a toast.
        if (this.formOrigin === "close") {
          notify({ title: result.message ?? "Done", level: tone === "warn" ? "warning" : "success" });
          return this.close();
        }
        this.leaveForm({ text: result.message ?? "Done", tone });
        this.load(cx);
      }
      cx.notify();
    });
  }

  // --- choices

  openChoice(field) {
    const choice = this.form.choice(field.key);
    if (!choice) return this.moveField(1);
    this.choice = {
      key: field.key, label: field.label, options: choice.options, filtered: choice.options, cursor: choice.cursor,
      current: choice.options[choice.cursor]?.value,
    };
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
    if (this.screen === "form") this.changed();
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
    this.targets = { all: targets, rows: [], editor, cursor: 0 };
    this.choiceInput.set_value("");
    this.screen = "targets";
    this.filterTargets();
    this.choiceInput.focus();
  }

  // filterTargets shows the places that match the filter, under the title
  // of their agent.
  filterTargets() {
    const targets = this.targets;
    if (!targets) return;
    const options = targets.all.map((target) => ({ label: target.label, search: `${target.detail ?? ""} ${target.agentTitle ?? ""}`, target }));
    const rows = [];
    let group = null;
    for (const { target } of filterOptions(options, this.choiceInput.value())) {
      if (target.agentTitle !== group) {
        group = target.agentTitle;
        rows.push({ type: "header", key: `group-${rows.length}`, title: group || "Worktrees" });
      }
      rows.push({ type: "target", key: `target-${rows.length}`, target });
    }
    targets.rows = rows;
    targets.cursor = Math.max(0, rows.findIndex((row) => row.type === "target"));
    this.targetScroller.scroll_to_item(targets.cursor);
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
    if (name === "enter") {
      const row = targets.rows[targets.cursor];
      if (row?.type === "target") this.openTarget(row.target, targets.editor, cx);
      return;
    }
    if (name === "escape") return this.backToList();
  }

  // backToList shows the list again. A command center that opened on one
  // action closes instead, unless it has an error to show.
  backToList(status = null) {
    this.targets = null;
    this.screen = "list";
    this.status = status;
    if (this.startOrigin === "close" && status?.tone !== "error") return this.close();
    this.input.focus();
  }

  // --- message

  // messageAgent opens the reply field of an agent. An agent that the list
  // does not show shows in the list of its workspace.
  messageAgent(item, cx) {
    const shown = this.rows.some((row) => row.type === "item" && row.item.kind === "agent" && row.item.id === item.id);
    if (!shown && item.workspaceId) this.enterScope({ kind: "workspace", id: item.workspaceId, title: item.workspaceTitle || "Workspace" }, cx);
    this.startReply(item, cx);
  }

  // Drafts stay in the app after the command center closes.
  saveDraft(agentId, text) {
    if (text.trim()) this.drafts[agentId] = text;
    else delete this.drafts[agentId];
    setShared(DRAFTS, { ...this.drafts });
  }

  // --- the root and the facts that the screens show

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

  // rootFrame is the root element: the keyboard, the actions of the keys and
  // the size of the popup.
  rootFrame(id) {
    let root = v_flex().id(id).track_focus(this.handle).key_context(CONTEXT).size_full()
      .on_key_down((event, cx) => this.keyDown(event, cx))
      .on_size_change((size, cx) => {
        if (Math.abs(size.width - this.width) < 1 && Math.abs(size.height - (this.height ?? 0)) < 1) return;
        this.width = size.width;
        this.height = size.height;
        cx.notify();
      });
    for (const [action, key] of ACTION_KEYS) {
      root = root.on_action(`galpon::${action}`, (_e, cx) => {
        if (key === "enter") return this.enter(cx);
        this.key(key, cx);
        cx.notify();
      });
    }
    return root;
  }

  // runKey runs a key of a hint or a button, as the key does.
  runKey(name, cx) {
    if (name === "enter") return this.enter(cx);
    this.key(name, cx);
    cx.notify();
  }

  // --- drawing: the list screen of list.js, or the screen of a form, a
  // choice or the places of a terminal of screens.js.

  render(cx) {
    const P = palette();
    this.P = P;
    keepSelection(P, cx);
    const root = this.rootFrame("gl-root").bg(P.bg).text_color(P.text).font_family(SANS).text_size(S.ui)
      .rounded(14).border_1().border_color(P.line2).shadow_lg().overflow_hidden();
    switch (this.screen) {
      case "form": return root.children(this.formScreen());
      case "choice": return root.children(this.choiceScreen());
      case "targets": return root.children(this.targetsScreen());
      default: return this.listScreen(root);
    }
  }
}

Object.assign(Galpon.prototype, ListMethods, ScreenMethods);
