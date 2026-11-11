// The agent tab of Galpon: a Pi agent in the app view, drawn with native
// elements in the Herdr app. `galpon herdr-app install` writes this mod.
//
// Galpon opens an agent in the app view in the tab "galpon-<agent>" of the
// space of its workspace, with this mod as its pane and the props
// { agent }. The app host of the agent (`galpon herdr-app host`) runs Pi in
// RPC mode in a hidden herdr-core terminal on the computer that runs Galpon.
// The mod reads the session events with HTTP long polls and sends commands
// with HTTP posts (connect.js). The tab is only a view: the agent keeps
// running when the tab closes, and the tab shows the same session again.
//
// The look is Taller (look.js): the conversation folds each run into a work
// log with its steps and file changes; the composer is one box with the
// model, the thinking level and the context ring inside; a panel on the
// right has tabs: Changes, Tasks and Agents. The pane header of the Galpon
// layout is the title bar of the tab.
//
// props { demo } draws a pretend session (demo.js) with no Pi, no model and
// no Galpon, for checks and pictures.

import { View, div, image } from "gpui-kit";
import { h_flex, v_flex, TextView, TextareaState, Textarea, InputState, Input, Popup } from "gpui-base";
import { appHome, exec, focused, home, keys, nextCommand, props, run, settings, shared, window as appWindow } from "herdr";
import { HOST, Host, clipboardImage, galpon } from "./connect.js";
import { DemoHost } from "./demo.js";
import { MONO, S, SANS, badge, dot, icon, kbd, keepSelection, mix, palette, pulse, ring, spinner } from "./look.js";
import {
  ago, applyEvent, clock, commandMatches, createTranscript, elapsed, formatCost, formatDuration, formatTokens,
  isSendInsert, loadSnapshot, partialArgs, plainMarkdown, quoteBlock, shortPath, splitMarkdown, thinkingStep, todoRows, toolSummary, userMarkdown,
  withQuotes, workRows, IMAGE_MARK, LIVE_WORK, QUOTE_HEAD,
} from "./transcript.js";
import { titleFrom } from "./titles.js";
import { deleteWord } from "./words.js";
import { prettyLinks } from "./links.js";

// The check harness replaces this line with a fixture state.
const FIXTURE = null;

const CONTEXT = "GalponAgent";
const COMPOSER = "GalponAgentComposer";
const PAGE = 120;
const WAIT = 15000;
// Below this width the panel covers the conversation.
const NARROW = 780;
const KEYS = [
  // The slash list and the transcript take the arrows only when the
  // composer says so: with text in it, the arrows move the caret.
  { keystroke: "up", action: "galponagent::slash-up", context: "GalponAgentSlash > Input" },
  { keystroke: "down", action: "galponagent::slash-down", context: "GalponAgentSlash > Input" },
  { keystroke: "up", action: "galponagent::browse", context: "GalponAgentEmpty > Input" },
  { keystroke: "enter", action: "galponagent::send", context: `${COMPOSER} > Input` },
  { keystroke: "shift-enter", action: "galponagent::newline", context: `${COMPOSER} > Input` },
  { keystroke: "alt-enter", action: "galponagent::queue", context: `${COMPOSER} > Input` },
  { keystroke: "escape", action: "galponagent::escape", context: `${COMPOSER} > Input` },
  { keystroke: "tab", action: "galponagent::complete", context: `${COMPOSER} > Input` },
  { keystroke: "shift-tab", action: "galponagent::thinking-next", context: `${COMPOSER} > Input` },
  { keystroke: "ctrl-l", action: "galponagent::models", context: `${COMPOSER} > Input` },
  { keystroke: "shift-tab", action: "galponagent::thinking-next", context: CONTEXT },
  { keystroke: "ctrl-l", action: "galponagent::models", context: CONTEXT },
  { keystroke: "alt-w", action: "galponagent::work-dock", context: CONTEXT },
  { keystroke: "alt-d", action: "galponagent::delegations", context: CONTEXT },
  { keystroke: "ctrl-w", action: "galponagent::delete-word", context: CONTEXT },
];
// Commands of the tab. The agent program handles the other slash commands.
const BUILTINS = [
  { name: "new", description: "Start a new conversation for this agent", source: "view" },
  { name: "compact", description: "Compact the context: /compact [instructions]", source: "view" },
  { name: "model", description: "Choose the model", source: "view" },
  { name: "name", description: "Rename this agent: /name <name>", source: "view" },
  { name: "restart", description: "Start the agent again with the same conversation", source: "view" },
];
// The permission modes of Claude Code. Shift+Tab goes through the modes
// that are not menuOnly, in this order, as in the terminal of Claude Code.
// Another harness gives its modes in its state.
const CLAUDE_MODES = [
  { mode: "default", label: "Ask first", icon: "shield", detail: "Asks before it edits files or runs commands" },
  { mode: "acceptEdits", label: "Accept edits", icon: "square-pen", detail: "Edits files without asking" },
  { mode: "plan", label: "Plan", icon: "list-todo", detail: "Reads and plans, and changes nothing" },
  { mode: "auto", label: "Auto", icon: "zap", detail: "Asks only for risky actions" },
  { mode: "bypassPermissions", label: "Bypass", icon: "alert", detail: "Never asks. Use it with care", menuOnly: true },
];
// The commands of the tab that an agent of Claude Code or Codex does not
// have: its conversation stays the one that Galpon keeps.
const NATIVE_HIDDEN_BUILTINS = new Set(["new"]);
// The names of the programs of the agents.
const PROGRAMS = { pi: "Pi", claude: "Claude", codex: "Codex" };
// The commands of the terminal console. The tab has its own panel.
const HIDDEN_COMMANDS = new Set(["galpon-ui", "galpon-reload-extension", "workdock", "todos", "tasks", "galpon-app-stop"]);
// Statuses that the tab draws natively.
const OWN_STATUSES = new Set(["galpon", "galpon-background"]);
// Statuses that the tab does not show: the token gauges of pi-blackhole.
// The extension keeps its work; the context ring shows the context use.
const HIDDEN_STATUSES = new Set(["blackhole"]);
// A burst of composer changes: a program, such as a dictation tool, that
// types the text one key at a time, much faster than a person types.
const BURST_GAP = 15; // ms at most between two changes of a burst
const BURST_CHANGES = 4; // changes in a row that start a burst
const BURST_END = 250; // ms with no change that end a burst
let keysBound = false;
// The app commands that start a terminal, with the argument that the value
// of a key action ("pane.split:right") fills. The app starts a terminal of an
// agent tab in the folder of its space: the folder of the agent that made the
// space. So the tab runs these commands itself, with the folder of its agent.
const TERMINAL_COMMANDS = { "pane.split": "direction", "tab.new": "name", "space.new": "name" };
// The commands of the tab that start a terminal in the folder of its agent.
const TAB_TERMINALS = {
  "split-right": { command: "pane.split", args: { direction: "right" } },
  "split-down": { command: "pane.split", args: { direction: "down" } },
  "new-tab": { command: "tab.new", args: {} },
  "new-space": { command: "space.new", args: {} },
};
// The app keys that start a terminal: { keys, command, args }.
let terminalBindings = [];
let terminalKeysBound = false;

// terminalKeys gives the app keys that start a terminal, as the shown layout
// binds them. A key of the layout section wins over the same global key.
function terminalKeys(bindings, layoutName) {
  const chosen = new Map();
  for (const section of ["global", `layout:${layoutName}`]) {
    for (const binding of bindings) {
      if (binding.section === section) chosen.set(binding.keys, binding.action);
    }
  }
  const out = [];
  for (const [keystroke, action] of chosen) {
    const parsed = parseAction(action);
    if (!parsed || !(parsed.command in TERMINAL_COMMANDS)) continue;
    if (parsed.command === "pane.split" && !["right", "down"].includes(parsed.args.direction)) continue;
    out.push({ keys: keystroke, ...parsed });
  }
  return out;
}

// parseAction reads the action of a key: "name", "name:value", or the JSON
// of { command, args }.
function parseAction(action) {
  if (typeof action !== "string") return null;
  if (action.startsWith("{")) {
    try {
      const value = JSON.parse(action);
      return typeof value?.command === "string" ? { command: value.command, args: value.args ?? {} } : null;
    } catch {
      return null;
    }
  }
  const at = action.indexOf(":");
  const command = at < 0 ? action : action.slice(0, at);
  const value = at < 0 ? "" : action.slice(at + 1);
  const first = TERMINAL_COMMANDS[command];
  return { command, args: value && first ? { [first]: value } : {} };
}

const message = (error) => String(error?.message ?? error);
const zoom = () => Number(settings()?.zoom) || 1;
// composerLine gives the line height of the composer as a multiple of its
// text size. GPUI rounds a line height to whole pixels, and a screen at 125%
// uses 1.25 device pixels for each pixel. With the default line height of
// 25 pixels, two lines need 62.5 device pixels, but the layout gives the
// textarea only 62. The textarea then scrolls by a part of a pixel, and each
// typed or deleted character moves the last line by one device pixel. A
// multiple of 4 pixels is a whole number of device pixels at each scale in
// steps of 25%.
const GPUI_LINE = 1.618; // the default line height of GPUI, as a multiple of the text size
const composerLine = () => {
  const size = S.read * zoom();
  return Math.max(4, 4 * Math.round((size * GPUI_LINE) / 4)) / size;
};
const mono = () => String(settings()?.font_family ?? settings()?.font ?? "JetBrains Mono");
const sleep = (cx, ms) => new Promise((resolve) => cx.timer.after(ms, () => resolve()));
const lines = (text) => String(text ?? "").replace(/\s+$/, "").split("\n");
const clamp = (value, low, high) => Math.min(Math.max(value, low), high);
// Extensions style their status and widget text for a terminal. The tab
// draws plain text.
const plain = (text) => String(text ?? "").replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, "").replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "");
const plainWidget = (widget) => ({ ...widget, lines: (widget?.lines ?? []).map(plain) });
// oneLine puts a text with line breaks on one line, for a row that has the
// height of one line. A " · " separates the lines, except after a line
// that ends a sentence or starts a list with a colon.
const oneLine = (text) => String(text ?? "").split("\n").map((line) => line.replace(/\s+/g, " ").trim()).filter(Boolean)
  .reduce((out, line) => (out ? `${out}${/[:.!?]$/.test(out) ? " " : " · "}${line}` : line), "");
// QUOTE_MARK is the text of the chip of a quote in the composer.
const QUOTE_MARK = /\[Quote \d+\]/g;
// taskFailed tells if a background task ended with an error.
const taskFailed = (task) => task.status === "failed" || task.status === "timed_out";
// taskEnd says how a background task ended: "done", "exit 1", "timed out",
// "stopped", or "failed" when it has no exit code, for example after a signal.
const taskEnd = (task) => {
  if (task.status === "completed") return "done";
  if (task.status === "timed_out") return "timed out";
  if (task.status === "failed") return Number.isInteger(task.exitCode) ? `exit ${task.exitCode}` : "failed";
  return String(task.status || "ended");
};
// commandLine gives a shell command on one line: its first line, and an
// ellipsis when more lines follow, such as the lines of a heredoc.
const commandLine = (command) => {
  const rows = String(command ?? "").split("\n").map((line) => line.trim()).filter(Boolean);
  return rows.length > 1 ? `${rows[0]} …` : rows[0] ?? "";
};
// diffColor gives the color of a line of a diff.
const diffColor = (P, line) => (line.startsWith("+") && !line.startsWith("+++") ? P.green
  : line.startsWith("-") && !line.startsWith("---") ? P.red
  : line.startsWith("@@") ? P.blue : P.text2);
// NOTE_CHARS is about the longest note that fits in the composer row beside
// its chips. A longer note stays in the conversation.
const NOTE_CHARS = 72;
const tail = (text, count) => {
  const all = lines(text);
  return all.slice(Math.max(0, all.length - count)).join("\n");
};
const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");

const TOOL_LABELS = {
  bash: "Bash", read: "Read", edit: "Edit", write: "Write", grep: "Grep", find: "Find", ls: "List",
  monitor: "Monitor", taskoutput: "Task output", taskstop: "Stop task", todo: "Todo",
  web_search: "Web search", fetch_content: "Fetch", get_search_content: "Search content", source_check: "Source check",
  galpon_list_agents: "List agents", galpon_list_workspaces: "List workspaces", galpon_list_repositories: "List repositories",
  galpon_create_agent: "Create agent", galpon_send_agent: "Send to agent", galpon_ask_agent: "Ask agent",
  galpon_update_agent: "Update assignment", galpon_read_message: "Read message", galpon_await_agent: "Wait for agent",
  galpon_await_agents: "Wait for agents", galpon_report_progress: "Report progress", galpon_cleanup_agents: "Clean up agents",
  galpon_add_worktree: "Add worktree", galpon_remove_worktree: "Remove worktree",
  agent: "Helper", askuserquestion: "Question", exitplanmode: "Plan", skill: "Skill", toolsearch: "Tool search",
};
// CLAUDE_TOOLS gives the Pi tool that does the same work as a tool of Claude
// Code, so that the steps of both agents look the same.
const CLAUDE_TOOLS = {
  multiedit: "edit", notebookedit: "edit", glob: "find", websearch: "web_search", webfetch: "fetch_content",
  bashoutput: "taskoutput", killshell: "taskstop", killbash: "taskstop", todowrite: "todo", task: "agent",
};
const toolName = (name) => {
  const lower = String(name ?? "").toLowerCase();
  return CLAUDE_TOOLS[lower] ?? lower;
};
// toolArgs gives the arguments of a call with the names that Pi uses: Claude
// Code names the file of a call file_path or notebook_path.
const toolArgs = (args) => {
  if (!args || typeof args !== "object" || typeof args.path === "string") return args;
  const path = args.file_path ?? args.notebook_path;
  return typeof path === "string" ? { ...args, path } : args;
};
const toolLabel = (name) => {
  const lower = String(name ?? "").toLowerCase();
  return TOOL_LABELS[lower] ?? TOOL_LABELS[toolName(name)] ?? String(name || "tool");
};
const isGalponTool = (name) => String(name ?? "").startsWith("galpon_");

const THINKING_WORDS = { off: "Off", minimal: "Minimal", low: "Low", medium: "Medium", high: "High", xhigh: "Extra high", max: "Max", ultra: "Ultra" };

export default class GalponAgent extends View {
  init(_props, cx) {
    const p = props() ?? {};
    this.handle = cx.focus_handle();
    this.t = createTranscript();
    this.machine = HOST;
    this.hostHome = HOST ? "" : home();
    this.host = new Host(HOST);
    this.conn = "starting";
    this.error = "";
    this.seq = 0;
    this.model = null;
    this.thinking = "";
    this.levels = null;
    // The permission mode of a Claude Code or Codex agent, its modes, and
    // its menu.
    this.mode = "";
    this.modeList = null;
    this.modeMenu = null;
    this.stats = null;
    this.commands = [];
    this.statuses = new Map();
    this.widgets = new Map();
    this.bridge = {};
    this.galpon = { enabled: true, agentId: "" };
    this.dialogs = [];
    this.dialogCursor = 0;
    this.title = "";
    this.flash = null;
    this.open = new Set();
    this.limit = PAGE;
    this.start = null;
    this.offset = 0;
    this.viewH = 0;
    this.contentH = 0;
    this.width = 0;
    // The images of the composer, by the ID of their chip in the text.
    this.images = new Map();
    this.preview = null;
    // The quotes of the composer, by the ID of their chip in the text, and
    // the Cite button that a selection in a reply shows.
    this.quotes = new Map();
    this.citeOffer = null;
    this.press = null;
    this.pressSeq = 0;
    this.flashItem = null;
    this.sendAfterPaste = null;
    this.picker = null;
    this.thinkingMenu = null;
    this.slashCursor = 0;
    this.composerText = "";
    this.newlineAt = 0;
    this.shift = false;
    this.doneOpen = false;
    this.tasksDoneOpen = false;
    this.composerFocused = false;
    // The item under the pointer shows its time.
    this.hovered = "";
    // Items that came live fade in: they draw once at opacity 0.
    this.entering = new Set();
    // The heights of the items, for the keyboard to bring one into view.
    this.heights = new Map();
    // The markdown of each text item, cut into prose and code.
    this.parts = new Map();
    // The keyboard selection outside the composer: { where, id }.
    this.browse = null;
    this.copied = "";
    // Images of a host on another computer: path → "copying" | "ready" | "failed".
    this.copies = new Map();
    // A run ended that the person did not see yet.
    this.unseen = false;
    this.wake = null;
    this.closed = false;
    this.agentId = typeof p.agent === "string" ? p.agent : "";
    this.galpon.agentId = this.agentId;
    // The program of the agent: "pi", "claude" or "codex". Galpon gives it to the
    // tab; the facts of the sidebar know it for an older tab.
    this.harness = typeof p.harness === "string" && p.harness ? p.harness
      : String(shared("galpon.sidebar.facts")?.agents?.[this.agentId]?.harness ?? "pi");
    this.folder = "";
    this.restorePanel();

    this.textarea = TextareaState.new({ placeholder: `Message ${this.agentName}`, rows: 1 });
    this.textarea.set_auto_grow(1, 10);
    this.changedAt = 0;
    this.burstRun = 0;
    this.unwrapped = false;
    this.textarea.on("change", (_event, cx) => this.composerChanged(cx));
    this.textarea.on("submit", (_event, cx) => this.send(cx));
    this.textarea.on("focus", (_event, cx) => {
      this.composerFocused = true;
      this.browse = null;
      cx.notify();
    });
    this.textarea.on("blur", (_event, cx) => {
      this.composerFocused = false;
      // The keyboard went to another tab or pane.
      if (!this.here()) this.away = true;
      cx.notify();
    });
    this.textarea.on("paste_other", (_event, cx) => this.attachClipboard(cx));
    this.pickerInput = InputState.new({ placeholder: "Filter models" });
    this.pickerInput.on("change", (_event, cx) => {
      if (this.picker) this.picker.cursor = 0;
      cx.notify();
    });
    this.pickerInput.on("submit", (_event, cx) => this.choosePicked(cx));
    this.dialogInput = InputState.new({ placeholder: "" });
    this.dialogInput.on("submit", (_event, cx) => this.answerDialog(this.dialogInput.value(), cx));
    this.dialogEditor = TextareaState.new({ placeholder: "", rows: 6 });
    this.dialogEditor.on("submit", (_event, cx) => this.answerDialog(this.dialogEditor.value(), cx));

    if (!keysBound) {
      try {
        cx.bind_keys(KEYS);
        keysBound = true;
      } catch (error) {
        this.say(`Keys: ${message(error)}`, "error");
        // Without its keys the tab cannot send with Enter: keep this in view.
        this.flash.until = Number.MAX_SAFE_INTEGER;
      }
    }
    // In the tab, the app keys that start a terminal start it in the folder
    // of the agent. A key that the tab cannot bind keeps the app command.
    if (!terminalKeysBound) {
      terminalKeysBound = true;
      try {
        const found = terminalKeys(keys(), shared("layout"));
        if (found.length) cx.bind_keys(found.map((binding, index) => ({ keystroke: binding.keys, action: `galponagent::terminal-${index}`, context: CONTEXT })));
        terminalBindings = found;
      } catch {
        terminalBindings = [];
      }
    }

    if (FIXTURE) {
      this.fixture(FIXTURE);
      return;
    }
    // A demo tab talks to a pretend host: no Pi, no model and no Galpon.
    if (p.demo) {
      this.demo = new DemoHost({ title: typeof p.demo === "string" ? p.demo : "" });
      cx.spawn(async (cx) => this.demo.play((ms) => sleep(cx, ms), () => this.closed));
    } else if (!this.agentId) {
      this.conn = "error";
      this.error = "This tab names no Galpon agent. Open the agent from Galpon.";
      return;
    }
    this.focusComposer();
    cx.timer.after(150, () => this.focusComposer());
    cx.timer.after(700, () => this.focusComposer());
    cx.spawn(async (cx) => this.main(cx));
    cx.spawn(async (cx) => {
      for (;;) {
        const command = await nextCommand();
        if (this.closed) return;
        if (command === "focus") this.refocus();
        else this.onCommand?.(command, cx);
        cx.notify();
      }
    });
    // Ages, countdowns and notices change without events. Spinners animate
    // natively, so the tab does not draw again for them.
    cx.timer.every(1000, (cx) => {
      // A hidden tab does not draw, so the timer notes that the person left.
      if (!this.here()) this.away = true;
      if (this.flash && this.flash.until < Date.now()) this.flash = null;
      this.reportSeen();
      if (this.t.working || this.t.retry || this.dialogs.some((dialog) => dialog.timeout) || this.liveTasks() || this.flash) cx.notify();
    });
    cx.timer.every(15000, (cx) => cx.notify());
  }

  async close() {
    this.closed = true;
  }

  // restorePanel reads the panel tab that the person chose for this agent.
  restorePanel() {
    try {
      const saved = JSON.parse(localStorage.getItem(`panel:${this.agentId}`) ?? "null");
      if (saved && typeof saved === "object") {
        this.panel = saved.panel ?? null;
        this.panelChosen = Boolean(saved.chosen);
      }
    } catch {
      // A new tab starts with the panel that has content.
    }
  }

  save() {
    if (props()?.demo || !this.agentId) return;
    try {
      localStorage.setItem(`panel:${this.agentId}`, JSON.stringify({ panel: this.panel ?? null, chosen: Boolean(this.panelChosen) }));
    } catch {
      // The choice lasts until the window closes.
    }
  }

  // ownTab is the ID of the tab of this agent in this window.
  ownTab() {
    return `${HOST ? `${HOST}/` : ""}galpon-${this.agentId}`;
  }

  // shown tells if the person can see this tab now: its tab has the focus in
  // an active window.
  shown() {
    return focused()?.tab === this.ownTab() && appWindow()?.active !== false;
  }

  // here tells if the pane of this tab has the focus: its tab shows, and no
  // terminal beside it has the focus.
  here() {
    const at = focused();
    return Boolean(at && at.tab === this.ownTab() && at.pane && !at.terminal);
  }

  // reportSeen tells the host that the person saw the end of the last run,
  // so that Galpon stops showing it as ready.
  reportSeen() {
    if (!this.unseen || this.conn !== "live" || this.demo || !this.shown()) return;
    this.unseen = false;
    this.host.request("POST", "/seen", {}).catch(() => {});
  }

  fixture(f) {
    Object.assign(this, f.view ?? {});
    this.t = Object.assign(createTranscript(), f.transcript ?? {});
    this.t.byId = new Map(this.t.items.map((item) => [item.id, item]));
    this.statuses = new Map(Object.entries(f.statuses ?? {}));
    this.widgets = new Map(Object.entries(f.widgets ?? {}));
    this.open = new Set(f.open ?? []);
    this.setComposer(f.composer ?? "");
    for (const picture of f.images ?? []) this.images.set(picture.id, picture);
    for (const quote of f.quotes ?? []) this.quotes.set(quote.id, quote);
    if (f.composerContent) this.restoreComposer(f.composerContent);
    if (f.picker) this.pickerInput.set_value(f.picker.query ?? "");
    if (this.dialogs[0]?.method === "editor") this.dialogEditor.set_value(this.dialogs[0].prefill ?? "");
  }

  focusComposer() {
    if (this.dialogs.length || this.picker || this.thinkingMenu || this.modeMenu) return;
    this.browse = null;
    this.textarea.focus();
  }

  // refocus gives the keyboard to the part of the tab that takes keys now:
  // the dialog, the model picker, a menu or the selected message, else the
  // composer. Herdr does not give it to an element of the tab when the
  // person goes to the tab, so the tab does it when it draws again after
  // the person was away.
  refocus() {
    if (this.dialogs.length) this.focusDialog();
    else if (this.picker) this.pickerInput.focus();
    else if (this.thinkingMenu || this.modeMenu || this.browse) this.handle.focus();
    else this.focusComposer();
  }

  // setComposer replaces the composer text. set_value puts the cursor at the
  // start, so the text goes in with replace, which leaves the cursor at its end.
  setComposer(text) {
    this.composerText = text;
    this.textarea.set_value("");
    if (text) this.textarea.replace(text);
    this.composerText = text;
  }

  // restoreComposer puts back a text with its image chips, and the cursor at
  // its end.
  restoreComposer(content) {
    this.composerText = content.text;
    this.textarea.set_value(content);
    this.textarea.set_selected_range({ start: content.text.length, end: content.text.length });
  }

  // --- the host and the event loop

  async main(cx) {
    for (;;) {
      if (this.closed) return;
      try {
        this.conn = "starting";
        cx.notify();
        const info = await this.ensureHost(cx);
        this.host.set(info);
        await this.waitForPi(cx);
        await this.snapshot(cx);
        this.conn = "live";
        this.error = "";
        cx.notify();
        await this.poll(cx);
        if (this.restarting) {
          this.restarting = false;
          this.t.exited = null;
          continue;
        }
      } catch (error) {
        if (this.closed) return;
        if (this.restarting) {
          // /restart ended the host on purpose.
          this.restarting = false;
          continue;
        }
        this.conn = "error";
        this.error = message(error);
        cx.notify();
      }
      if (this.conn === "exited" || this.conn === "error") {
        // Wait for Start again, or try again after a pause.
        await new Promise((resolve) => {
          this.wake = resolve;
          if (this.conn === "error") cx.timer.after(5000, () => resolve());
        });
        this.wake = null;
      } else {
        await sleep(cx, 1000);
      }
    }
  }

  // ensureHost gives the port and the token of the app host of the agent.
  // Galpon starts the host when it does not run.
  async ensureHost(_cx) {
    if (this.demo) {
      this.host = this.demo;
      this.hostHome = home();
      return {};
    }
    const out = await galpon(["app", this.agentId]);
    if (out.error) throw new Error(sentence(out.error));
    if (out.home) this.hostHome = out.home;
    if (out.cwd) this.folder = out.cwd;
    if (out.harness) this.harness = out.harness;
    return out;
  }

  // waitForPi waits while the host starts Pi.
  async waitForPi(cx) {
    for (;;) {
      const response = await this.host.request("GET", "/state", undefined, 10);
      const state = response.body ?? {};
      if (response.status !== 200) throw new Error(state.error ?? `the host answered ${response.status}`);
      if (state.alive) return;
      if (state.exit) throw new Error(state.lastError || `${this.agentName} stopped while it started.`);
      await sleep(cx, 400);
    }
  }

  restart(cx) {
    this.t.exited = null;
    this.conn = "starting";
    this.wake?.();
    cx?.notify();
  }

  async snapshot(cx) {
    // A command on another computer has a time limit of 30 seconds.
    const response = await this.host.request("GET", "/snapshot", undefined, this.machine ? 25 : 120);
    if (response.status !== 200) throw new Error(response.body?.error ?? `snapshot failed (${response.status})`);
    const s = response.body;
    loadSnapshot(this.t, s);
    this.seq = s.seq;
    this.model = s.state?.model ?? null;
    this.thinking = s.state?.thinkingLevel ?? "";
    this.levels = s.thinkingLevels ?? null;
    this.mode = s.state?.permissionMode ?? "";
    if (Array.isArray(s.state?.permissionModes)) this.modeList = s.state.permissionModes;
    this.stats = s.stats;
    this.commands = (s.commands ?? []).filter((command) => !HIDDEN_COMMANDS.has(command.name));
    this.statuses = new Map(Object.entries(s.statuses ?? {}).map(([key, text]) => [key, plain(text)]));
    this.widgets = new Map(Object.entries(s.widgets ?? {}).map(([key, widget]) => [key, plainWidget(widget)]));
    this.bridge = s.bridge ?? {};
    this.dialogs = s.dialogs ?? [];
    this.dialogCursor = 0;
    this.title = plain(s.title);
    this.limit = PAGE;
    this.start = null;
    this.offset = 0;
    if (s.unseen) this.unseen = true;
    if (this.dialogs.length) this.prepareDialog();
    cx.notify();
  }

  async poll(cx) {
    let failures = 0;
    for (;;) {
      if (this.closed) return;
      let result;
      try {
        const response = await this.host.request("GET", `/events?since=${this.seq}&wait=${WAIT}`, undefined, 25);
        if (response.status !== 200) throw new Error(response.body?.error ?? `events failed (${response.status})`);
        result = response.body;
        failures = 0;
      } catch (error) {
        if (++failures >= 3) throw error;
        await sleep(cx, 400);
        continue;
      }
      if (result.reset) {
        await this.snapshot(cx);
        continue;
      }
      const before = this.t.items.length;
      for (const event of result.events ?? []) this.onEvent(event, cx);
      this.seq = result.seq;
      this.fadeIn(before, cx);
      if (result.events?.length) cx.notify();
      if (result.alive === false && !result.events?.length) {
        this.conn = "exited";
        cx.notify();
        return;
      }
      if (this.reload) {
        this.reload = false;
        await this.snapshot(cx);
      }
    }
  }

  // fadeIn draws the items that came since before once at opacity 0; the
  // next draw takes them to 1 with a transition.
  fadeIn(before, cx) {
    const fresh = this.t.items.slice(before).map((item) => item.id);
    if (!fresh.length) return;
    for (const id of fresh) this.entering.add(id);
    cx.timer.after(40, (cx) => {
      for (const id of fresh) this.entering.delete(id);
      cx.notify();
    });
  }

  onEvent(event, cx) {
    switch (event.type) {
      case "extension_ui_request": return this.onUi(event, cx);
      case "dialog_closed":
        this.dialogs = this.dialogs.filter((dialog) => dialog.id !== event.id);
        if (!this.dialogs.length) this.focusComposer();
        else this.prepareDialog();
        return;
      case "host_bridge":
        this.bridge = { ...this.bridge, [event.key]: event.value };
        return;
      case "rpc_response":
        if (event.success && ["new_session", "switch_session", "fork", "clone"].includes(event.command)) this.reload = true;
        if (event.success && event.command === "set_model") this.refreshLevels(cx);
        break;
      case "host_pi_exit":
        // /restart ends Pi on purpose; the transcript does not report it.
        if (this.restarting) return;
        break;
      case "agent_settled":
        this.refreshStats(cx);
        // The run ended: Galpon shows the agent as ready until the person
        // sees this tab.
        this.unseen = true;
        this.reportSeen();
        break;
      case "permission_mode_changed":
        this.mode = event.mode ?? "";
        break;
      case "thinking_level_changed":
        this.thinking = event.level ?? "";
        break;
    }
    applyEvent(this.t, event);
  }

  onUi(request, cx) {
    switch (request.method) {
      case "setStatus":
        if (request.statusText) this.statuses.set(request.statusKey, plain(request.statusText));
        else this.statuses.delete(request.statusKey);
        return;
      case "setWidget":
        if (Array.isArray(request.widgetLines)) this.widgets.set(request.widgetKey, plainWidget({ lines: request.widgetLines, placement: request.widgetPlacement ?? "aboveEditor" }));
        else this.widgets.delete(request.widgetKey);
        return;
      case "setTitle":
        this.title = plain(request.title);
        return;
      case "set_editor_text":
        this.setComposer(request.text ?? "");
        return;
      case "notify": {
        const text = plain(request.message).trim();
        const level = request.notifyType === "error" || request.notifyType === "warning" ? request.notifyType : "info";
        if (!text) return;
        // Notifications have no extension ID. Match the memory worker prefix.
        if (text.startsWith("Observational memory:")) {
          if (level !== "info") this.say(text, level);
          return;
        }
        if (level === "info" && !text.includes("\n") && text.length <= NOTE_CHARS) {
          this.say(text, level);
          return;
        }
        // A warning, an error, and a note with more lines, such as the
        // answer of /mcp or /todos, stay in the conversation. The composer
        // row shows them too only while the person reads above them.
        this.t.items.push({ id: `n${Date.now()}${Math.random()}`, kind: "notice", level, text });
        this.t.version++;
        if (this.scrolledUp()) this.say(text, level);
        return;
      }
      default:
        if (["select", "confirm", "input", "editor"].includes(request.method)) {
          request.at = Date.now();
          this.dialogs.push(request);
          if (this.dialogs.length === 1) this.prepareDialog();
          cx.notify();
        }
    }
  }

  prepareDialog() {
    const dialog = this.dialogs[0];
    if (!dialog) return;
    this.dialogCursor = 0;
    if (dialog.method === "input") this.dialogInput.set_value("");
    else if (dialog.method === "editor") this.dialogEditor.set_value(dialog.prefill ?? "");
    this.focusDialog();
  }

  // focusDialog gives the keyboard to the first dialog: to its field, or to
  // the tab for a choice.
  focusDialog() {
    const method = this.dialogs[0]?.method;
    if (method === "input") this.dialogInput.focus();
    else if (method === "editor") this.dialogEditor.focus();
    else this.handle.focus();
  }

  agentTitle() {
    return this.bridge.galpon?.title || this.t.name || this.title || this.agentName;
  }

  // agentName names the program of the agent for the person.
  get agentName() {
    return PROGRAMS[this.harness] ?? "Pi";
  }

  // native tells that the agent runs Claude Code or Codex in the native
  // runtime of Galpon, not Pi.
  get native() {
    return this.harness === "claude" || this.harness === "codex";
  }

  // modes are the permission modes of the agent: none for Pi.
  get modes() {
    if (this.modeList?.length) return this.modeList;
    return this.harness === "claude" ? CLAUDE_MODES : [];
  }

  modeOf(mode) {
    return this.modes.find((item) => item.mode === mode) ?? this.modes[0];
  }


  async refreshStats(cx) {
    try {
      const response = await this.host.request("POST", "/rpc", { type: "get_session_stats" });
      if (response.body?.success) this.stats = response.body.data;
      cx.notify();
    } catch {
      // The next turn tries again.
    }
  }

  async refreshLevels(cx) {
    try {
      const response = await this.host.request("POST", "/rpc", { type: "get_available_thinking_levels" });
      if (response.body?.success) this.levels = response.body.data?.levels ?? null;
      cx.notify();
    } catch {
      // The control keeps the levels that it had.
    }
  }

  async rpc(command, cx) {
    try {
      const response = await this.host.request("POST", "/rpc", command, 60);
      if (response.status >= 400 || response.body?.success === false) {
        this.say(response.body?.error ?? `${command.type} failed`, "error");
        cx?.notify();
        return null;
      }
      return response.body;
    } catch (error) {
      this.say(message(error), "error");
      cx?.notify();
      return null;
    }
  }

  say(text, level = "info") {
    this.flash = { text: sentence(text), level, until: Date.now() + 8000 };
  }

  liveTasks() {
    return (this.bridge.tasks?.tasks ?? []).some((task) => task.status === "running");
  }

  // --- the composer

  composerChanged(cx) {
    const value = this.textarea.value();
    this.watchBurst(cx);
    // A typed Enter that no key binding caught still sends.
    if (!this.shift && Date.now() - this.newlineAt > 150 && isSendInsert(this.composerText, value)) {
      this.setComposer(this.composerText);
      this.send(cx);
      return;
    }
    const before = this.composerText;
    if (value.startsWith("/") !== before.startsWith("/")) this.slashCursor = 0;
    this.composerText = value;
    // The textarea draws its own text. The tab draws again only when a part
    // that comes from the text changes: the send button, the hint, the keys
    // of an empty composer and the list of commands.
    if (!before !== !value || !before.trim() !== !value.trim() || before.startsWith("/") || value.startsWith("/")) cx.notify();
  }

  // watchBurst stops the soft wrap of the composer during a burst of changes.
  // With soft wrap, the textarea wraps its whole paragraph again for each
  // typed character: about 1 ms for 3,000 characters. A program that types
  // a long dictation 1 ms per key then keeps the window busy, and the text
  // comes in jumps. The composer wraps the text again when the burst stops.
  watchBurst(cx) {
    const now = Date.now();
    this.burstRun = now - this.changedAt <= BURST_GAP ? this.burstRun + 1 : 0;
    this.changedAt = now;
    if (this.unwrapped || this.burstRun < BURST_CHANGES) return;
    this.unwrapped = true;
    this.textarea.set_soft_wrap(false);
    const task = cx.timer.every(100, () => {
      if (Date.now() - this.changedAt < BURST_END) return;
      task.cancel();
      this.unwrapped = false;
      this.textarea.set_soft_wrap(true);
      // The textarea sets its height again only when its rows change, and it
      // scrolls to the cursor only after an edit: an empty edit at the cursor
      // shows the end of the dictation again.
      this.textarea.set_auto_grow(1, 10);
      this.textarea.replace("");
    });
  }

  newline(cx) {
    this.newlineAt = Date.now();
    this.textarea.replace("\n");
    cx.notify();
  }

  complete(cx) {
    const matches = this.slashMatches();
    const choice = matches[Math.min(this.slashCursor, matches.length - 1)];
    if (!choice) return;
    this.setComposer(`/${choice.name} `);
    this.textarea.focus();
    cx.notify();
  }

  slashMatches() {
    const builtins = this.native ? BUILTINS.filter((command) => !NATIVE_HIDDEN_BUILTINS.has(command.name)) : BUILTINS;
    return commandMatches([...builtins, ...this.commands], this.composerText);
  }

  // send sends the composer. While Pi works, Enter steers the run after its
  // current tool calls, and Alt+Enter queues the text for after the run.
  send(cx, followUp = false) {
    if (this.dialogs.length) return;
    const content = this.textarea.content();
    const text = content.text.replace(/^\s+|\s+$/g, "");
    if (!text) return;
    const pictures = this.composerImages();
    // Enter during a paste sends when the image is there.
    if (pictures.some((picture) => picture.status !== "ready")) {
      this.sendAfterPaste = { followUp };
      this.say("The message goes when the image is pasted.", "info");
      cx.notify();
      return;
    }
    this.sendAfterPaste = null;
    if (this.conn !== "live") {
      this.say(`${this.agentName} is not ready yet.`, "warning");
      cx.notify();
      return;
    }
    const slash = /^\/(\S+)(?:\s+([\s\S]*))?$/.exec(text);
    const builtin = slash && BUILTINS.find((command) => command.name === slash[1]);
    this.setComposer("");
    this.offset = 0;
    if (builtin) {
      this.builtin(builtin.name, slash[2] ?? "", cx);
      cx.notify();
      return;
    }
    // The text keeps an [Image n] mark where each chip was, and the images go
    // in the order of their chips.
    const images = pictures.map((picture) => ({ type: "image", path: picture.block.path, mimeType: picture.block.mimeType }));
    // Each quote chip becomes the quote, with the time of its reply.
    const quotes = content.tokens.filter((span) => this.quotes.has(span.token.id))
      .map((span) => ({ range: span.range, block: quoteBlock(this.quotes.get(span.token.id).text, this.quotes.get(span.token.id).time) }));
    const kept = this.images;
    const keptQuotes = this.quotes;
    this.images = new Map();
    this.quotes = new Map();
    this.preview = null;
    const command = { type: "prompt", message: quotes.length ? withQuotes(content.text, quotes).replace(/^\s+|\s+$/g, "") : text };
    if (images.length) command.images = images;
    if (this.t.working) command.streamingBehavior = followUp ? "followUp" : "steer";
    // The first message names a new agent, before its first turn reads the name.
    const title = this.needsTitle() ? titleFrom(text.replace(IMAGE_MARK, " ").replace(QUOTE_MARK, " ").trim() || (quotes.length ? "Quote" : "Image")) : "";
    this.named = true;
    (title ? this.rename(title, cx, true) : Promise.resolve())
      .then(() => this.rpc(command, cx))
      .then((result) => {
        if (!result) {
          this.images = kept;
          this.quotes = keptQuotes;
          this.restoreComposer(content);
        }
        cx.notify();
      });
    cx.notify();
  }

  // needsTitle tells if this is the first message of a new Galpon agent that
  // still has the first name.
  needsTitle() {
    return this.galpon.enabled && !this.named && this.agentTitle() === this.agentName
      && !this.t.items.some((item) => item.kind === "user" || item.kind === "delivery");
  }

  builtin(name, rest, cx) {
    switch (name) {
      case "new": return void this.rpc({ type: "new_session" }, cx);
      case "compact": return void this.rpc(rest ? { type: "compact", customInstructions: rest } : { type: "compact" }, cx);
      case "model": return void this.openModels(cx);
      case "name": return void this.rename(rest.trim(), cx);
      case "restart":
        this.restarting = true;
        this.host.request("POST", "/shutdown").catch(() => {});
        this.say(`${this.agentName} starts again with the same conversation.`);
        return;
    }
  }

  async rename(name, cx, quiet = false) {
    if (!name) {
      this.say("Write the new name after /name.", "warning");
      return;
    }
    if (this.galpon.enabled) {
      try {
        const response = await this.host.request("POST", "/galpon/rename", { title: name });
        if (response.status >= 400) throw new Error(response.body?.error ?? "rename failed");
        this.bridge = { ...this.bridge, galpon: { ...(this.bridge.galpon ?? {}), title: response.body?.title ?? name } };
        if (!quiet) this.say(`Renamed to ${response.body?.title ?? name}.`, "info");
      } catch (error) {
        if (!quiet) this.say(message(error), "error");
      }
    } else if (await this.rpc({ type: "set_session_name", name }, cx)) {
      this.t.name = name;
    }
    cx.notify();
  }

  // deleteWord takes Ctrl+W: it deletes the word before the cursor of the
  // text box of an open dialog or of the model list, else of the composer.
  // As typing does, it goes to the composer after a click in the
  // conversation.
  deleteWord(cx) {
    const method = this.dialogs[0]?.method;
    if (this.dialogs.length && method !== "input" && method !== "editor") return;
    if (this.browse || this.thinkingMenu || this.modeMenu) return;
    const state = method === "input" ? this.dialogInput
      : method === "editor" ? this.dialogEditor
      : this.picker ? this.pickerInput : this.textarea;
    if (state === this.textarea && !this.composerFocused) this.focusComposer();
    if (deleteWord(state)) cx.notify();
  }

  escape(cx) {
    if (this.picker || this.thinkingMenu || this.modeMenu) {
      this.picker = null;
      this.thinkingMenu = null;
      this.modeMenu = null;
      this.focusComposer();
    } else if (this.dialogs.length) {
      this.answerDialog(null, cx);
      return;
    } else if (this.peek) {
      this.peek = null;
    } else if (this.slashMatches().length) {
      this.setComposer("");
    } else if (this.t.working) {
      this.stop(cx);
    }
    cx.notify();
  }

  // stop takes the queued messages back into the composer, then aborts, as the
  // Pi terminal does.
  async stop(cx) {
    const cleared = await this.rpc({ type: "clear_queue" }, cx);
    const back = [...(cleared?.data?.steering ?? []), ...(cleared?.data?.followUp ?? [])];
    if (back.length) this.setComposer([this.textarea.value(), ...back].filter(Boolean).join("\n"));
    await this.rpc({ type: "abort" }, cx);
    cx.notify();
  }

  // attachClipboard puts a chip for the image of the clipboard at the cursor
  // at once, then asks the host for the image. The host reads the clipboard
  // and saves the image; a host on another computer gets the image of this
  // one. A paste that gets no image takes its chip out again.
  async attachClipboard(cx) {
    const n = Math.max(0, ...this.composerImages().map((picture) => picture.n)) + 1;
    const picture = { id: `image-${Date.now().toString(36)}-${n}`, n, status: "loading", block: null };
    this.images.set(picture.id, picture);
    this.textarea.replace_with_token({ id: picture.id, text: `[Image ${n}]`, label: `Image ${n}` });
    cx.notify();
    let error = "";
    try {
      let response;
      if (this.machine) {
        const data = await clipboardImage();
        response = data ? await this.host.request("POST", "/attach-data", data, 60) : { status: 404, body: {} };
      } else {
        response = await this.host.request("POST", "/attach", {});
      }
      if (response.status === 200) {
        picture.status = "ready";
        picture.block = response.body;
      } else {
        error = response.body?.error ?? "The clipboard has no image.";
      }
    } catch (failure) {
      error = message(failure);
    }
    if (error) {
      this.dropImage(picture.id);
      this.sendAfterPaste = null;
      this.say(/no image/i.test(error) ? "The clipboard has no image." : error, "warning");
    } else if (this.sendAfterPaste && this.composerImages().every((item) => item.status === "ready")) {
      this.send(cx, this.sendAfterPaste.followUp);
    }
    cx.notify();
  }

  // composerImages gives the images that have a chip in the composer, in the
  // order of the chips. Backspace on a chip takes its image out.
  composerImages() {
    return this.textarea.tokens().map((span) => this.images.get(span.token.id)).filter(Boolean);
  }

  // dropImage takes the chip of an image out of the composer.
  dropImage(id) {
    this.images.delete(id);
    const content = this.textarea.content();
    const gone = content.tokens.find((span) => span.token.id === id);
    if (!gone) return;
    const cut = gone.range.end - gone.range.start;
    const shift = (range) => (range.start >= gone.range.end ? { start: range.start - cut, end: range.end - cut } : range);
    this.restoreComposer({
      text: content.text.slice(0, gone.range.start) + content.text.slice(gone.range.end),
      tokens: content.tokens.filter((span) => span !== gone).map((span) => ({ ...span, range: shift(span.range) })),
    });
  }

  // tokenChip draws the chip of a token of the composer: a quote or an image.
  tokenChip(token) {
    return this.quotes.has(token.token.id) ? this.quoteChip(token) : this.imageChip(token);
  }

  // quoteChip draws the chip of a quote in the composer text: the start of
  // the quote. While the pointer rests on it, the whole quote opens above
  // the chip.
  quoteChip(token) {
    const P = this.P;
    const quote = this.quotes.get(token.token.id);
    const words = quote.text.replace(/\s+/g, " ").trim();
    const chip = h_flex().id(`pl-chip-${token.token.id}`).h(Math.max(14, token.line_height / zoom() - 4)).max_w(260).px(6).gap(5).items_center().rounded(6)
      .bg(token.selected ? P.accentSoft : P.raised2).border_1().border_color(token.selected ? P.accentLine : P.line2)
      .text_size(S.small).text_color(P.text2)
      .child(icon("quote", 11).text_color(P.accent))
      .child(div().whitespace_nowrap().child(words.length > 36 ? `${words.slice(0, 35).trimEnd()}…` : words));
    const open = this.preview === token.token.id;
    return Popup.new(`pl-chip-pop-${token.token.id}`, chip).anchor("bottom_left")
      .when(open, (el) => el.content(div().pb(6).child(v_flex().max_w(420).p(10).gap(4).rounded(10).bg(P.surface).border_1().border_color(P.line2).shadow_lg()
        .child(div().text_size(S.small).text_color(P.text3).child(quote.time ? `Quote from the reply at ${quote.time}` : "Quote from a reply"))
        .child(div().text_size(S.meta).text_color(P.text2).child(quote.text.length > 600 ? `${quote.text.slice(0, 600).trimEnd()}…` : quote.text)))));
  }

  // imageChip draws the chip of an image in the composer text. While the
  // pointer rests on a pasted image, a preview opens above the chip.
  imageChip(token) {
    const P = this.P;
    const picture = this.images.get(token.token.id);
    const n = picture?.n ?? Number(/\d+/.exec(token.token.text)?.[0] ?? 0);
    const loading = picture?.status === "loading";
    const block = picture?.status === "ready" ? picture.block : null;
    const size = block?.width && block?.height ? ` · ${block.width}×${block.height}` : "";
    const chip = h_flex().id(`pl-chip-${token.token.id}`).h(Math.max(14, token.line_height / zoom() - 4)).px(6).gap(5).items_center().rounded(6)
      .bg(token.selected ? P.accentSoft : P.raised2).border_1().border_color(token.selected ? P.accentLine : P.line2)
      .text_size(S.small).text_color(P.text2)
      .child(loading ? spinner(P.text3, 11) : icon("image", 12))
      .child(div().whitespace_nowrap().child(loading ? `Pasting image ${n}…` : `Image ${n}${size}`));
    const open = Boolean(block) && this.preview === token.token.id && this.imageState(block) === "ready";
    return Popup.new(`pl-chip-pop-${token.token.id}`, chip).anchor("bottom_left")
      .when(open, (el) => el.content(this.imagePreview(block)));
  }

  imagePreview(block) {
    const P = this.P;
    const scale = Math.min(1, 320 / (block.width || 320), 240 / (block.height || 240));
    return div().pb(6).child(v_flex().p(6).rounded(10).bg(P.surface).border_1().border_color(P.line2).shadow_lg()
      .child(image(block.path).w(Math.round((block.width || 320) * scale)).h(Math.round((block.height || 240) * scale)).rounded(6)));
  }

  // --- dialogs from extensions

  answerDialog(value, cx) {
    const dialog = this.dialogs[0];
    if (!dialog) return;
    let body;
    if (value === null) body = { id: dialog.id, cancelled: true };
    else if (dialog.method === "confirm") body = { id: dialog.id, confirmed: Boolean(value) };
    else body = { id: dialog.id, value: String(value) };
    this.dialogs.shift();
    this.host.request("POST", "/ui", body).catch((error) => this.say(message(error), "error"));
    if (this.dialogs.length) this.prepareDialog();
    else this.focusComposer();
    cx.notify();
  }

  // --- the model picker and the thinking control

  async openModels(cx) {
    this.thinkingMenu = null;
    this.modeMenu = null;
    this.picker = { items: [], cursor: 0, loading: true };
    this.pickerInput.set_value("");
    this.pickerInput.focus();
    cx.notify();
    const result = await this.rpc({ type: "get_available_models" }, cx);
    if (!this.picker) return;
    this.picker.items = result?.data?.models ?? [];
    this.picker.loading = false;
    cx.notify();
  }

  // pickedModels gives the models that match the filter, grouped by
  // provider: the provider of the current model first.
  pickedModels() {
    const words = this.pickerInput.value().toLowerCase().split(/\s+/).filter(Boolean);
    const current = this.model?.provider ?? "";
    return (this.picker?.items ?? []).filter((model) => {
      const text = `${model.provider}/${model.id} ${model.name ?? ""}`.toLowerCase();
      return words.every((word) => text.includes(word));
    }).sort((a, b) => Number(b.provider === current) - Number(a.provider === current)
      || String(a.provider).localeCompare(String(b.provider)) || String(a.name ?? a.id).localeCompare(String(b.name ?? b.id)));
  }

  async choosePicked(cx, chosen) {
    const models = this.pickedModels();
    const model = chosen ?? models[Math.min(this.picker?.cursor ?? 0, models.length - 1)];
    if (!model) return;
    this.picker = null;
    this.focusComposer();
    const result = await this.rpc({ type: "set_model", provider: model.provider, modelId: model.id }, cx);
    if (result?.data) this.model = result.data;
    this.refreshLevels(cx);
    this.refreshStats(cx);
    cx.notify();
  }

  openThinking(cx) {
    this.picker = null;
    this.modeMenu = null;
    const { index } = thinkingStep(this.thinking, this.levels);
    this.thinkingMenu = { cursor: index };
    this.handle.focus();
    cx.notify();
  }

  async setThinking(level, cx) {
    this.thinkingMenu = null;
    this.focusComposer();
    if (await this.rpc({ type: "set_thinking_level", level }, cx)) this.thinking = level;
    cx.notify();
  }

  async nextThinking(cx) {
    // Shift+Tab changes the permission mode of Claude Code or Codex, as in
    // their terminals.
    if (this.modes.length) return void this.nextMode(cx);
    const result = await this.rpc({ type: "cycle_thinking_level" }, cx);
    if (result?.data?.level) this.thinking = result.data.level;
    else if (result) this.say("This model has no thinking levels.");
    cx.notify();
  }

  openModes(cx) {
    this.picker = null;
    this.thinkingMenu = null;
    this.modeMenu = { cursor: Math.max(0, this.modes.indexOf(this.modeOf(this.mode))) };
    this.handle.focus();
    cx.notify();
  }

  async setMode(mode, cx) {
    this.modeMenu = null;
    this.focusComposer();
    const result = await this.rpc({ type: "set_permission_mode", mode }, cx);
    if (result) this.mode = result.data?.mode ?? mode;
    cx.notify();
  }

  nextMode(cx) {
    const cycle = this.modes.filter((item) => !item.menuOnly);
    const index = cycle.findIndex((item) => item.mode === this.modeOf(this.mode)?.mode);
    this.setMode(cycle[(index + 1) % cycle.length].mode, cx);
  }

  stopTask(id, cx) {
    this.rpc({ type: "prompt", message: `/galpon-app-stop ${id}` }, cx);
  }

  // --- keys

  keyDown(event, cx) {
    const key = event.key;
    this.lastKey = { key, at: Date.now() };
    const dialog = this.dialogs[0];
    if (dialog && (dialog.method === "select" || dialog.method === "confirm")) {
      const count = dialog.method === "select" ? (dialog.options ?? []).length : 2;
      if (key === "down" || key === "tab") this.dialogCursor = (this.dialogCursor + 1) % count;
      else if (key === "up") this.dialogCursor = (this.dialogCursor + count - 1) % count;
      else if (key === "enter") {
        if (dialog.method === "select") this.answerDialog(dialog.options[this.dialogCursor], cx);
        else this.answerDialog(this.dialogCursor === 0, cx);
      } else if (dialog.method === "confirm" && (key === "y" || key === "n")) this.answerDialog(key === "y", cx);
      else if (key === "escape") this.answerDialog(null, cx);
      else return;
      cx.stop_propagation();
      cx.notify();
      return;
    }
    if (this.picker) {
      const count = this.pickedModels().length;
      if (key === "down" && count) this.picker.cursor = (this.picker.cursor + 1) % count;
      else if (key === "up" && count) this.picker.cursor = (this.picker.cursor + count - 1) % count;
      else if (key === "escape") this.escape(cx);
      else return;
      cx.stop_propagation();
      cx.notify();
      return;
    }
    if (this.modeMenu) {
      const count = this.modes.length;
      if (key === "down" || key === "tab") this.modeMenu.cursor = (this.modeMenu.cursor + 1) % count;
      else if (key === "up") this.modeMenu.cursor = (this.modeMenu.cursor + count - 1) % count;
      else if (key === "enter") this.setMode(this.modes[this.modeMenu.cursor].mode, cx);
      else if (key === "escape") this.escape(cx);
      else return;
      cx.stop_propagation();
      cx.notify();
      return;
    }
    if (this.thinkingMenu) {
      const { list } = thinkingStep(this.thinking, this.levels);
      if (key === "down") this.thinkingMenu.cursor = Math.min(list.length - 1, this.thinkingMenu.cursor + 1);
      else if (key === "up") this.thinkingMenu.cursor = Math.max(0, this.thinkingMenu.cursor - 1);
      else if (key === "enter") this.setThinking(list[this.thinkingMenu.cursor], cx);
      else if (key === "escape") this.escape(cx);
      else return;
      cx.stop_propagation();
      cx.notify();
      return;
    }
    if (this.browse) return this.browseKey(event, cx);
    if (this.citeOffer && key === "escape") {
      this.citeOffer = null;
      cx.stop_propagation();
      cx.notify();
      return;
    }
    if (!this.composerFocused && this.typeIntoComposer(event, cx)) return;
    if (this.slashMatches().length && (key === "down" || key === "up")) {
      this.moveSlash(key === "down" ? 1 : -1, cx);
      cx.stop_propagation();
      return;
    }
    if (key === "escape") {
      this.escape(cx);
      cx.stop_propagation();
    } else if (key === "pageup" || key === "pagedown") {
      const step = Math.max(80, this.viewH * 0.8);
      this.scrollTo(this.offset + (key === "pageup" ? step : -step));
      cx.stop_propagation();
      cx.notify();
    }
  }

  // typeIntoComposer gives typing that starts outside the composer to the
  // composer, for example after a click in the conversation: a typed
  // character goes in at the cursor of the composer, and Ctrl+V pastes there.
  // Other chords, such as Ctrl+C for the selected text, keep their work.
  typeIntoComposer(event, cx) {
    if (this.dialogs.length || this.picker || this.thinkingMenu || this.modeMenu) return false;
    const m = event.modifiers ?? {};
    const chord = m.control || m.alt || m.platform || m.function;
    const typed = !chord && typeof event.key_char === "string" && event.key_char !== "" && !/[\u0000-\u001f\u007f]/.test(event.key_char);
    const paste = event.key === "v" && (m.control || m.platform) && !m.alt && !m.shift;
    if (!typed && !paste) return false;
    this.citeOffer = null;
    this.focusComposer();
    if (typed) {
      this.textarea.replace(event.key_char);
    } else {
      const text = cx.read_from_clipboard();
      if (text) this.textarea.replace(text);
      else this.attachClipboard(cx);
    }
    cx.stop_propagation();
    cx.notify();
    return true;
  }

  moveSlash(step, cx) {
    const count = this.slashMatches().length;
    if (!count) return;
    this.slashCursor = (Math.min(this.slashCursor, count - 1) + step + count) % count;
    cx.notify();
  }

  // --- the keyboard outside the composer. Up in an empty composer selects
  // the last item of the conversation. Up and down move, Enter opens, C
  // copies, Right goes to the dock and Left comes back, Esc returns.

  entries(where) {
    return (where === "dock" ? this.dockEntries : this.transcriptEntries) ?? [];
  }

  startBrowse(cx) {
    const entries = this.entries("transcript");
    if (!entries.length) return;
    this.browse = { where: "transcript", id: entries[entries.length - 1].id };
    this.handle.focus();
    this.reveal();
    cx.notify();
  }

  moveBrowse(step) {
    const entries = this.entries(this.browse.where);
    if (!entries.length) return;
    const at = entries.findIndex((entry) => entry.id === this.browse.id);
    this.browse.id = entries[clamp((at < 0 ? entries.length : at) + step, 0, entries.length - 1)].id;
    if (this.browse.where === "transcript") this.reveal();
  }

  browseKey(event, cx) {
    const key = event.key;
    const entry = this.entries(this.browse.where).find((item) => item.id === this.browse.id);
    if (key === "up" || key === "k") this.moveBrowse(-1);
    else if (key === "down" || key === "j") this.moveBrowse(1);
    else if (key === "enter" || key === "space") entry?.toggle?.(cx);
    else if (key === "c") entry?.copy?.(cx);
    else if (key === "x") entry?.stop?.(cx);
    else if ((key === "right" || key === "l") && this.browse.where === "transcript") {
      const dock = this.entries("dock");
      if (dock.length) this.browse = { where: "dock", id: dock[0].id, back: this.browse.id };
    } else if ((key === "left" || key === "h") && this.browse.where === "dock") {
      const back = this.browse.back;
      this.browse = { where: "transcript", id: back ?? this.entries("transcript").at(-1)?.id };
      this.reveal();
    } else if (key === "pageup" || key === "pagedown") {
      this.scrollTo(this.offset + (key === "pageup" ? 1 : -1) * Math.max(80, this.viewH * 0.8));
    } else if (key === "escape" || key === "i" || (event.key_char && !event.modifiers?.control && !event.modifiers?.alt)) {
      // Esc, or a letter to write, goes back to the composer.
      this.focusComposer();
    } else {
      return;
    }
    cx.stop_propagation();
    cx.notify();
  }

  // reveal scrolls the selected item of the conversation into view.
  reveal() {
    const items = this.shownItems ?? [];
    let below = 18;
    for (let i = items.length - 1; i >= 0; i--) {
      const height = this.heights.get(items[i].id) ?? 0;
      if (items[i].id === this.browse?.id) {
        if (below < this.offset) this.scrollTo(below);
        else if (below + height > this.offset + this.viewH) this.scrollTo(below + height - this.viewH + 12);
        return;
      }
      below += height;
    }
  }

  copyText(text, id, cx) {
    cx.write_to_clipboard(String(text ?? ""));
    this.copied = id;
    cx.timer.after(1500, (cx) => {
      if (this.copied === id) this.copied = "";
      cx.notify();
    });
    cx.notify();
  }

  // --- scrolling: offset is the distance from the bottom in logical
  // pixels. At 0 the transcript follows its end. Above it, the person reads:
  // the transcript keeps its distance from the top, so a run that grows or
  // shrinks below does not move the text they read.

  maxOffset() {
    return Math.max(0, this.contentH - this.viewH);
  }

  scrollTo(value) {
    this.offset = Math.min(Math.max(0, value), this.maxOffset());
  }

  // revealItem scrolls the conversation so that an item shows near the top,
  // and marks it for a moment.
  revealItem(id, cx) {
    const ids = this.shownSegments ?? [];
    let below = 20;
    for (let i = ids.length - 1; i >= 0; i--) {
      const height = this.heights.get(ids[i]) ?? 0;
      if (ids[i] === id) {
        this.scrollTo(below + height - this.viewH + 40);
        this.flashItem = id;
        cx.timer.after(1600, (later) => {
          if (this.flashItem !== id) return;
          this.flashItem = null;
          later.notify();
        });
        cx.notify();
        return true;
      }
      below += height;
    }
    return false;
  }

  // --- citing: a selection in a reply shows a hint where the mouse went
  // up. Ctrl+C makes it a Cite button, and Cite puts a chip of the quote in
  // the composer.

  // notePress notes a press in the text of a reply. The press of the root
  // that follows makes it the current press.
  notePress(item, event) {
    this.press = { seq: this.pressSeq + 1, id: item.id, x: event.position.x, y: event.position.y, at: Date.now() };
  }

  // afterPress shows the cite hint when the mouse goes up after a drag or a
  // double click that started in the text of a reply.
  afterPress(event, cx) {
    const press = this.press;
    this.press = null;
    if (!press || press.seq !== this.pressSeq || Date.now() - press.at > 60000) return;
    const moved = Math.hypot(event.position.x - press.x, event.position.y - press.y) > 4;
    if (!moved && (event.click_count ?? 1) < 2) return;
    const local = event.local_position ?? event.position;
    this.citeOffer = { id: press.id, text: "", x: local.x / zoom(), y: local.y / zoom(), offset: this.offset, contentH: this.contentH };
    cx.notify();
  }

  // keyUp makes the cite hint a Cite button when Ctrl+C copied the
  // selection. Herdr gives a mod no way to read a selection or to run the
  // copy action, so the tab reads the clipboard after the person copies.
  // The copy action takes the key down of Ctrl+C when there is a selection,
  // so a C that comes up without a key down that the tab saw was a copy.
  keyUp(event, cx) {
    const offer = this.citeOffer;
    if (!offer || offer.text || event.key !== "c") return;
    if (this.lastKey?.key === "c" && Date.now() - this.lastKey.at < 2000) return;
    const text = cx.read_from_clipboard()?.trim();
    if (!text) return;
    offer.text = text;
    cx.notify();
  }

  // citeButton draws the cite hint, Ctrl+C to cite, under the end of a
  // selection, and the Cite button after the copy.
  citeButton() {
    const P = this.P;
    const offer = this.citeOffer;
    const left = Math.min(Math.max(8, offer.x - 28), Math.max(8, (this.width || 400) - 140));
    const top = Math.max(8, offer.y + 52 > (this.viewH || 600) ? offer.y - 40 : offer.y + 12);
    const pill = h_flex().id("pl-cite").absolute().left(left).top(top).h(28).px(10).gap(6).items_center().rounded(8)
      .bg(P.raised2).border_1().border_color(P.line2).shadow_md().text_size(S.meta);
    if (!offer.text) {
      return pill.text_color(P.text3).child(icon("quote", 13)).child(kbd(P, "Ctrl C")).child("to cite");
    }
    return pill.cursor_pointer().text_color(P.text).hover((s) => s.bg(P.line2))
      .tooltip("Put the copied text in the message as a quote")
      .on_mouse_down("left", (_event, cx) => {
        cx.stop_propagation();
        this.cite(cx);
      })
      .child(icon("quote", 13).text_color(P.accent))
      .child("Cite");
  }

  // cite puts a chip of the offered quote in the composer, at its cursor.
  cite(cx) {
    const offer = this.citeOffer;
    this.citeOffer = null;
    if (!offer?.text) return;
    const n = Math.max(0, ...this.textarea.tokens().map((span) => this.quotes.get(span.token.id)?.n ?? 0)) + 1;
    const quote = { id: `quote-${Date.now().toString(36)}-${n}`, n, text: offer.text, time: clock(this.replyEnd(offer.id) || Date.now()) };
    this.quotes.set(quote.id, quote);
    this.focusComposer();
    this.textarea.replace_with_token({ id: quote.id, text: `[Quote ${n}]`, label: `Quote ${n}` });
    this.textarea.replace(" ");
    cx.notify();
  }

  // replyEnd gives the time when the turn of a reply ended, as its footer
  // shows it, or 0 while it runs.
  replyEnd(id) {
    const items = this.t.items;
    for (let i = items.findIndex((item) => item.id === id) + 1; i > 0 && i < items.length; i++) {
      if (items[i].kind === "turn") return items[i].end;
      if (items[i].kind === "user" || items[i].kind === "delivery") break;
    }
    return 0;
  }

  // revealQuote scrolls to the reply of quote n of a message of the person:
  // the last reply before the message whose turn ended at the time of the
  // quote.
  revealQuote(item, n, cx) {
    const heads = String(item.text ?? "").split("\n").filter((line) => QUOTE_HEAD.test(line));
    const time = QUOTE_HEAD.exec(heads[n - 1] ?? "")?.[1] ?? "";
    const items = this.t.items;
    for (let i = items.indexOf(item) - 1; i >= 0; i--) {
      const reply = items[i];
      if (reply.kind !== "text" || !this.heights.has(reply.id)) continue;
      if (time && clock(this.replyEnd(reply.id)) !== time) continue;
      if (this.revealItem(reply.id, cx)) return;
    }
    this.say("That reply is not in the part of the conversation that shows.", "info");
    cx.notify();
  }

  // --- the root, the conversation and the facts that the views show

  // rootFrame is the root element: the keyboard, the actions and the size of
  // the pane. A proposal adds its colors and children.
  rootFrame(id) {
    this.dockEntries = [];
    return v_flex().id(id).size_full().relative().track_focus(this.handle).key_context(CONTEXT)
      .on_key_down((event, cx) => this.keyDown(event, cx))
      .on_mouse_down("left", (_event, cx) => {
        this.pressSeq++;
        if (!this.citeOffer) return;
        this.citeOffer = null;
        cx.notify();
      })
      .on_mouse_up("left", (event, cx) => this.afterPress(event, cx))
      .on_key_up((event, cx) => this.keyUp(event, cx))
      .on_action("galponagent::send", (_event, cx) => this.send(cx))
      .on_action("galponagent::queue", (_event, cx) => this.send(cx, true))
      .on_action("galponagent::newline", (_event, cx) => this.newline(cx))
      .on_action("galponagent::escape", (_event, cx) => this.escape(cx))
      .on_action("galponagent::complete", (_event, cx) => this.complete(cx))
      .on_action("galponagent::slash-up", (_event, cx) => this.moveSlash(-1, cx))
      .on_action("galponagent::slash-down", (_event, cx) => this.moveSlash(1, cx))
      .on_action("galponagent::browse", (_event, cx) => this.startBrowse(cx))
      .on_action("galponagent::thinking-next", (_event, cx) => this.nextThinking(cx))
      .on_action("galponagent::models", (_event, cx) => this.openModels(cx))
      .on_action("galponagent::work-dock", (_event, cx) => this.toggleDock("workDock", cx))
      .on_action("galponagent::delegations", (_event, cx) => this.toggleDock("delegations", cx))
      .on_action("galponagent::delete-word", (_event, cx) => this.deleteWord(cx))
      .when(terminalBindings.length > 0, (el) => terminalBindings.reduce((frame, binding, index) =>
        frame.on_action(`galponagent::terminal-${index}`, () => this.openTerminal(binding)), el))
      .on_size_change((size, cx) => {
        const width = size.width / zoom();
        const height = size.height / zoom();
        if (Math.abs(width - this.width) < 1 && Math.abs(height - (this.height ?? 0)) < 1) return;
        this.width = width;
        this.height = height;
        cx.notify();
      });
  }

  // onCommand runs a command of the app: `herdr-app do galpon-agent.<command>`.
  // demo-<script> sends a prompt that plays that script in a demo tab; the
  // other commands open a part of the view.
  onCommand(command, cx) {
    const prompts = {
      "demo-code": "Make the queue wake its drain loop",
      "demo-delegate": "Delegate a second opinion on the change",
      "demo-ask": "Ask me which icon set to use",
      "demo-background": "Run the e2e suite in the background",
      "demo-fail": "Make it fail on purpose",
    };
    if (prompts[command]) {
      this.setComposer(prompts[command]);
      this.send(cx);
    } else if (command === "models") this.openModels(cx);
    else if (command === "thinking") this.openThinking(cx);
    else if (command === "browse") this.startBrowse(cx);
    else if (command === "work") this.toggleDock("workDock", cx);
    else if (command === "agents") this.toggleDock("delegations", cx);
    else if (command === "escape") this.escape(cx);
    else if (command === "slash") this.setComposer("/");
    else if (command.startsWith("panel-")) this.openPanel?.(command.slice(6), cx);
    // A key of the person can start a terminal in the folder of the agent:
    // "ctrl-shift-o": "galpon-agent.split-right" in the mod:galpon-agent
    // section. The command reaches every shown tab, so only the focused one
    // acts.
    else if (TAB_TERMINALS[command] && this.here()) this.openTerminal(TAB_TERMINALS[command]);
  }

  // openTerminal runs an app command that starts a terminal (a split beside
  // the tab, a new tab or a new space) with the folder of the agent. Without
  // that folder, or for a command that names its own folder, pane or mod, the
  // app command runs as it is.
  openTerminal({ command, args }) {
    const folder = this.agentFolder();
    if (!folder || args.folder || args.mod || args.pane) {
      run(command, args);
      return;
    }
    if (command === "pane.split") {
      run("terminal.open", { place: args.direction, folder, ...(args.argv ? { argv: args.argv } : {}) });
      return;
    }
    // A space that a mod starts is on this computer. On a screen, the agent
    // and its folder are on the host.
    run(command, { ...args, folder, ...(command === "space.new" && HOST && args.machine === undefined ? { machine: HOST } : {}) });
  }

  // backdrop closes an open picker with a click beside it.
  backdrop() {
    return div().id("pn-backdrop").absolute().inset_0()
      .on_mouse_down("left", (_event, cx) => {
        this.picker = null;
        this.thinkingMenu = null;
        this.modeMenu = null;
        this.focusComposer();
        cx.stop_propagation();
        cx.notify();
      });
  }

  // --- the conversation

  // conversation gives the items that show now: the last pages. While the
  // person reads above the end, the first item stays, so that nothing above
  // the text they read goes away when new items come.
  conversation() {
    const items = this.t.items;
    const last = Math.max(0, items.length - this.limit);
    const start = this.offset > 0 && this.start !== null && this.start <= items.length ? Math.min(this.start, last) : last;
    this.start = start;
    const shown = items.slice(start);
    this.shownItems = shown;
    this.transcriptEntries = [];
    return { start, shown, more: start > 0, hidden: this.t.hidden };
  }

  // showEarlier shows one more page above the first item that shows.
  showEarlier(cx) {
    this.limit = Math.max(this.limit, this.t.items.length - (this.start ?? 0)) + PAGE;
    cx.notify();
  }

  jumpToLatest(cx) {
    this.offset = 0;
    cx.notify();
  }

  // scrollFrame holds the conversation. content is the column of items;
  // empty shows in place of it; extra draws over it, such as a Latest
  // button. At offset 0 the content hangs from the bottom of the frame and
  // follows the end. Above it, the content hangs from its top. The size of
  // the content is known only after a draw, so a draw with a new size
  // keeps the text where it was, and the new offset that follows keeps
  // it there too.
  scrollFrame(id, content, { empty = null, extra = [] } = {}) {
    let holder = v_flex().id(`${id}-content`).absolute().left(0).right(0);
    holder = this.offset > 0 ? holder.top(this.viewH + this.offset - this.contentH) : holder.bottom(0);
    holder = holder
      .on_size_change((size, cx) => {
        const height = size.height / zoom();
        if (Math.abs(height - this.contentH) < 1) return;
        const grew = height - this.contentH;
        this.contentH = height;
        // While the person reads above, the text they read stays in place.
        if (this.offset > 0) this.offset += grew;
        this.scrollTo(this.offset);
        cx.notify();
      })
      .child(content);
    return div().id(id).flex_1().min_h(0).w_full().relative().overflow_hidden()
      .on_size_change((size, cx) => {
        const height = size.height / zoom();
        if (Math.abs(height - this.viewH) < 1) return;
        // While the person reads above, the top of what they read stays.
        if (this.offset > 0) this.offset += this.viewH - height;
        this.viewH = height;
        this.scrollTo(this.offset);
        cx.notify();
      })
      .on_scroll_wheel((event, cx) => {
        const dy = (event.delta?.y ?? 0) / zoom();
        if (!dy) return;
        this.scrollTo(this.offset + dy);
        cx.notify();
      })
      .child(empty ?? holder)
      .children(extra.filter(Boolean));
  }

  scrolledUp() {
    return this.offset > 24;
  }

  // measured wraps one item: its space above, the fade-in of a new item and
  // its height, for the keyboard.
  measured(id, el, top = 0) {
    return div().id(`${id}-at`).w_full().relative().pt(top)
      .opacity(this.entering.has(id) ? 0 : 1).transition("opacity", { duration: 220, easing: "ease-out" })
      .on_size_change((size) => this.heights.set(id, size.height / zoom()))
      .child(el);
  }

  // entry adds an item to the keyboard order and tells what Enter and C do.
  entry(item) {
    const entry = { id: item.id };
    switch (item.kind) {
      case "user": entry.copy = (cx) => this.copyText(item.text, item.id, cx); break;
      case "delivery": entry.copy = (cx) => this.copyText(item.deliveries.map((delivery) => delivery.body).join("\n\n"), item.id, cx); break;
      case "text": entry.copy = (cx) => this.copyText(item.text, item.id, cx); break;
      case "thinking":
        entry.toggle = (cx) => this.toggle(item.id, cx);
        entry.copy = (cx) => this.copyText(item.text, item.id, cx);
        break;
      case "tool":
        entry.toggle = (cx) => this.toggle(item.id, cx);
        entry.copy = (cx) => this.copyText(item.result?.text ?? "", item.id, cx);
        break;
      case "custom": entry.toggle = (cx) => this.toggle(`${item.id}-0`, cx); break;
      case "bash": entry.copy = (cx) => this.copyText(`$ ${item.command}\n${item.output ?? ""}`, item.id, cx); break;
      case "compaction":
      case "branch": entry.toggle = (cx) => this.toggle(item.id, cx); break;
      case "notice": entry.copy = (cx) => this.copyText(item.text, item.id, cx); break;
    }
    if (item.kind !== "turn") this.transcriptEntries.push(entry);
    return entry;
  }

  // entryGroup adds one keyboard entry for a group of items, such as a folded
  // work log.
  entryGroup(id, toggle) {
    const entry = { id, toggle };
    this.transcriptEntries.push(entry);
    return entry;
  }

  isSelected(id, where = "transcript") {
    return this.browse?.where === where && this.browse.id === id;
  }

  // dockEntry adds a row of the dock to the keyboard order.
  dockEntry(id, toggle = null, stop = null) {
    this.dockEntries.push({ id, toggle, stop });
    return this.isSelected(id, "dock");
  }

  toggle(id, cx) {
    if (this.open.has(id)) this.open.delete(id);
    else this.open.add(id);
    cx?.notify();
  }

  hover(id, on, cx) {
    if (on) this.hovered = id;
    else if (this.hovered === id) this.hovered = "";
    cx.notify();
  }

  // Markdown formatting only changes the view. Copying a whole message
  // still uses its original text, and each link retains its full target.
  markdownParts(id, text) {
    let cached = this.parts.get(id);
    if (!cached || cached.text !== text) {
      const parts = splitMarkdown(text).map((part) => part.kind === "code" ? part
        : { ...part, text: prettyLinks(part.text) });
      cached = { text, parts };
      this.parts.set(id, cached);
    }
    return cached.parts;
  }

  // imageState tells if an image of the session shows here. On a screen the
  // images are on the host: the view copies each one here first.
  imageState(picture) {
    if (!this.machine) return "ready";
    const state = this.copies.get(picture.path);
    if (state !== "ready") this.copyImage(picture.path);
    return this.copies.get(picture.path) ?? "copying";
  }

  copyImage(path) {
    if (this.copies.has(path)) return;
    this.copies.set(path, "copying");
    this.host.copyImage(path)
      .then(() => this.copies.set(path, "ready"))
      .catch(() => this.copies.set(path, "failed"));
  }

  // --- facts for the views

  agentFolder() {
    return this.bridge.galpon?.cwd || this.folder;
  }

  folderLabel(limit = 56) {
    // A managed directory of Galpon has a long path with the agent ID.
    if (/\/agents\/[0-9a-f-]{36}\/workspace$/.test(this.agentFolder())) return "Agent folder";
    const short = shortPath(this.agentFolder(), this.hostHome || home());
    if (short.length <= limit) return short;
    return `…/${short.split("/").filter(Boolean).slice(-2).join("/")}`;
  }

  modelName(provider, id) {
    if (!id) return "";
    if (this.model?.id === id) return this.model.name ?? id;
    return (this.picker?.items ?? []).find((model) => model.id === id && (!provider || model.provider === provider))?.name ?? id;
  }

  workspaceName() {
    return this.bridge.galpon?.workspace || "";
  }

  // runState names what the session does now.
  runState() {
    if (this.conn !== "live") return this.conn === "starting" ? "starting" : "offline";
    if (this.dialogs.length) return "asks";
    if (this.t.compacting) return "compacting";
    if (this.t.retry) return "retry";
    if (this.t.working) return "working";
    return "idle";
  }

  // workingText is the line under a run that works.
  workingText() {
    if (this.t.compacting) return "Compacting the context";
    if (this.t.retry) {
      const left = Math.max(0, Math.ceil((this.t.retry.at + (this.t.retry.delayMs ?? 0) - Date.now()) / 1000));
      return `Retry ${this.t.retry.attempt}/${this.t.retry.maxAttempts}${left ? ` in ${left}s` : ""} · ${this.t.retry.error ?? ""}`;
    }
    if (this.t.workingSince) return `Working · ${elapsed(Date.now() - this.t.workingSince)}`;
    return "Working";
  }

  workingSeconds() {
    return this.t.workingSince ? Math.max(0, Math.floor((Date.now() - this.t.workingSince) / 1000)) : 0;
  }

  // bannerState tells what the banner over the conversation says, or null.
  bannerState() {
    if (this.t.exited) {
      const last = this.t.exited.stderr?.length ? this.t.exited.stderr[this.t.exited.stderr.length - 1] : this.t.exited.error;
      return { text: `${this.agentName} stopped.${last ? ` ${last}` : ""}`, tone: "error", action: ["Start again", (cx) => this.restart(cx)] };
    }
    if (this.conn === "starting") {
      let text = `Starting ${this.agentName}…`;
      if (this.machine) text = `${text.slice(0, -1)} on ${this.machine}…`;
      return { text, tone: "busy", action: null };
    }
    if (this.conn === "error") return { text: this.error || `No connection to ${this.agentName}.`, tone: "error", action: ["Try again", (cx) => this.restart(cx)] };
    if (this.conn === "exited") return { text: `${this.agentName} stopped.`, tone: "error", action: ["Start again", (cx) => this.restart(cx)] };
    return null;
  }

  // toolFacts gives what a view shows of a tool call.
  toolFacts(item) {
    const args = toolArgs(item.args ?? partialArgs(item.argsText));
    const name = toolName(item.name);
    const running = item.status === "running" || item.status === "streaming" || item.status === "pending";
    const failed = item.status === "error";
    const stopped = item.status === "stopped";
    const command = name === "bash" ? String(args?.command ?? "") : "";
    const diff = typeof item.result?.details?.diff === "string" ? item.result.details.diff : "";
    let added = 0;
    let removed = 0;
    for (const line of diff ? lines(diff) : []) {
      if (line.startsWith("+") && !line.startsWith("+++")) added++;
      else if (line.startsWith("-") && !line.startsWith("---")) removed++;
    }
    return {
      args, name, running, failed, stopped, command, diff, added, removed,
      label: toolLabel(item.name),
      summary: toolSummary(name, args),
      path: typeof args?.path === "string" ? args.path : "",
      background: Boolean(args?.run_in_background) || Boolean(item.result?.details?.task_id),
      galpon: isGalponTool(item.name),
      // A tool that takes less than a second is not worth a number.
      duration: item.durationMs >= 1000 ? formatDuration(item.durationMs) : "",
      text: item.result?.text ?? "",
      images: item.result?.images ?? [],
      open: this.open.has(item.id) !== failed,
      partial: running ? item.partial ?? "" : "",
    };
  }

  // turnFacts gives the parts of the stats line of a run.
  turnFacts(item) {
    const tokens = `↑${formatTokens(item.input + item.cacheRead + item.cacheWrite)} ↓${formatTokens(item.output)}`;
    return {
      clock: clock(item.end),
      duration: item.duration >= 1000 ? formatDuration(item.duration) : "",
      tokens,
      cost: item.cost > 0 ? formatCost(item.cost) : "",
      model: this.modelName(item.provider, item.model),
      thinking: item.thinking && item.thinking !== "off" ? (THINKING_WORDS[item.thinking] ?? item.thinking).toLowerCase() : "",
      detail: [
        `Input ${formatTokens(item.input)} · cache read ${formatTokens(item.cacheRead)} · cache write ${formatTokens(item.cacheWrite)} · output ${formatTokens(item.output)}`,
        `${item.calls} model ${item.calls === 1 ? "call" : "calls"} · ${formatCost(item.cost)} at API prices`,
      ].join("\n"),
    };
  }

  // contextFacts gives the use of the context window.
  contextFacts() {
    const usage = this.stats?.contextUsage;
    const percent = Number.isFinite(usage?.percent) ? usage.percent : null;
    return { percent, used: usage?.tokens ?? 0, window: usage?.contextWindow ?? this.model?.contextWindow ?? 0 };
  }

  thinkingWord(level = this.thinking) {
    return THINKING_WORDS[level] ?? (level || "Off");
  }

  // queued gives the messages that wait for the run.
  queued() {
    return [...this.t.queue.steering.map((text) => ["steer", text]), ...this.t.queue.followUp.map((text) => ["after the run", text])];
  }

  otherStatuses() {
    return [...this.statuses.entries()].filter(([key]) => !OWN_STATUSES.has(key) && !HIDDEN_STATUSES.has(key)).map(([, text]) => text).filter(Boolean);
  }

  composerHint() {
    const empty = !this.composerText;
    if (this.t.working) return "enter steer · alt+enter after the run · esc stop";
    return `enter send · shift+enter new line · / commands${this.modes.length ? " · shift+tab mode" : ""}${empty && this.t.items.length ? " · ↑ browse" : ""}`;
  }

  // composerKeys gives the element that holds the composer the key context
  // of the composer: its bindings win over the defaults of the text area.
  composerKeys(el) {
    const empty = !this.composerText;
    const context = [COMPOSER, empty ? "GalponAgentEmpty" : "", this.slashMatches().length ? "GalponAgentSlash" : ""].filter(Boolean).join(" ");
    return el.key_context(context).on_modifiers_changed((event) => { this.shift = Boolean(event.modifiers?.shift); });
  }

  dialogLeft(dialog) {
    return dialog?.timeout ? Math.max(0, Math.ceil((dialog.at + dialog.timeout - Date.now()) / 1000)) : 0;
  }

  // --- the dock

  todoFacts() {
    return todoRows(this.t.tasks);
  }

  backgroundTasks() {
    const order = { running: 0, failed: 1, timed_out: 1, completed: 2, stopped: 3 };
    return [...(this.bridge.tasks?.tasks ?? [])].sort((a, b) => (order[a.status] ?? 4) - (order[b.status] ?? 4) || b.startedAt - a.startedAt);
  }

  // delegationFacts splits the delegated work: the work in view, and the
  // work that is done or canceled, which folds under one line. Work that
  // failed stays in view: it is the turn of the agent or of the person.
  delegationFacts() {
    const work = this.bridge.work?.work ?? [];
    const finished = (item) => ["completed", "canceled"].includes(item.observation?.state) && (item.children ?? []).every(finished);
    return {
      live: workRows(work).filter((row) => LIVE_WORK.has(row.item.observation?.state)).length,
      shown: workRows(work.filter((item) => !finished(item))),
      folded: workRows(work.filter(finished)),
      roots: work.filter(finished).length,
      truncated: Boolean(this.bridge.work?.truncated),
    };
  }

  // workNote is the line under a delegation: what its mark does not say.
  workNote(item) {
    const state = item.observation?.state ?? "queued";
    const stale = state === "started" && item.observation?.lease === "stale";
    if (item.checkpoint?.blocker) return { text: item.checkpoint.blocker, tone: "need" };
    if (state === "waiting") return { text: "Waits for you", tone: "need" };
    if (stale) return { text: "No recent sign of work", tone: "need" };
    if (item.checkpoint?.summary && LIVE_WORK.has(state)) return { text: item.checkpoint.summary, tone: "quiet" };
    if (state === "started" && item.activity) return { text: `Runs ${item.activity.category.replace(/^tool: /, "")}`, tone: "quiet" };
    if (state === "queued") return { text: "Queued", tone: "quiet" };
    return null;
  }

  dockCounts() {
    const todos = todoRows(this.t.tasks).open.length;
    const tasks = (this.bridge.tasks?.tasks ?? []).filter((task) => task.status === "running").length;
    const rows = workRows(this.bridge.work?.work);
    return {
      work: todos + tasks,
      todos,
      tasks,
      delegations: rows.filter((row) => LIVE_WORK.has(row.item.observation?.state)).length,
      // Galpon's yellow means that the person must act.
      waiting: rows.some((row) => row.item.observation?.state === "waiting"),
    };
  }

  thinkingLeft() {
    return clamp(this.thinkingAnchor ?? 200, 0, Math.max(0, (this.width || 900) - 250));
  }

  // measureInto keeps the width (or the height) of an element, for menus
  // that open above it.
  measureInto(key, side = "width") {
    return (size, cx) => {
      const value = size[side] / zoom();
      if (Math.abs(value - (this[key] ?? 0)) < 1) return;
      this[key] = value;
      cx.notify();
    };
  }
  // --- Taller: the agent tab as a workbench. A slim top bar with the place
  // of the agent; the conversation folds each run into a work log with its
  // steps and file changes; the composer is one box with the model, the
  // thinking level and the context ring inside. The panel on the right has
  // tabs: Changes, Tasks and Agents.

  render(cx) {
    // The person came back to this tab: give the keyboard to it again.
    if (this.away && this.here()) {
      this.away = false;
      cx.timer.after(0, () => {
        if (this.here()) this.refocus();
      });
    }
    const P = palette();
    this.P = P;
    keepSelection(P, cx);
    const dock = this.dockLayout();
    const full = this.width || 1200;
    const mainW = full - (dock.shown && !dock.over ? dock.width + 1 : 0);
    const column = clamp(mainW - 56, 260, 760);
    this.columnLeft = Math.max(0, (mainW - column) / 2);
    this.columnW = column;
    let root = this.rootFrame("pl-root").bg(P.bg).text_color(P.text).font_family(SANS).text_size(S.ui);
    const main = v_flex().id("pl-main").flex_1().min_w(0).h_full()
      .child(this.contextBar(dock))
      .when(Boolean(this.bannerState()), (el) => el.child(this.banner(this.bannerState(), column)))
      .child(this.conversationView(column))
      .child(this.composer(column));
    let row = h_flex().id("pl-row").flex_1().min_h(0).w_full().items_stretch().child(main);
    if (dock.shown && !dock.over) row = row.child(div().w(1).h_full().bg(P.line)).child(this.panelView(dock));
    root = root.child(row);
    if (dock.over && dock.shown) root = root.child(div().id("pl-dock-over").absolute().right(0).top(0).bottom(0).w(dock.width).border_l(1).border_color(P.line2).shadow_lg().child(this.panelView(dock)));
    if (this.picker) root = root.child(this.backdrop()).child(this.modelPicker());
    if (this.thinkingMenu) root = root.child(this.backdrop()).child(this.thinkingPicker());
    if (this.modeMenu) root = root.child(this.backdrop()).child(this.modePicker());
    // The Cite button stays only while the text under it does not move.
    const offer = this.citeOffer;
    if (offer && (offer.offset !== this.offset || (this.offset === 0 && offer.contentH !== this.contentH))) this.citeOffer = null;
    if (this.citeOffer) root = root.child(this.citeButton());
    return root;
  }

  // --- the panel: tabs instead of two toggles

  panelTabs() {
    const todos = this.todoFacts().total;
    const tasks = (this.bridge.tasks?.tasks ?? []).length;
    const work = (this.bridge.work?.work ?? []).length;
    return { changes: this.fileChanges().length > 0, tasks: todos + tasks > 0, agents: this.galpon.enabled && work > 0 };
  }

  panelTab() {
    const tabs = this.panelTabs();
    if (!this.panelChosen && this.panel === undefined) {
      const first = tabs.tasks ? "tasks" : tabs.agents ? "agents" : null;
      if (first) this.panel = first;
    }
    return this.panel ?? null;
  }

  dockLayout() {
    const full = this.width || 1200;
    const over = full < NARROW;
    const tab = this.panelTab();
    const width = clamp(Math.round(full * 0.28), 300, 420);
    const tabs = this.panelTabs();
    return {
      sections: { work: tab === "tasks", delegations: tab === "agents", hasWork: tabs.tasks, hasDelegations: tabs.agents },
      shown: Boolean(tab) && (!over || Boolean(this.peek)),
      width: over ? Math.min(width, full - 40) : width,
      over,
      tab,
    };
  }

  toggleDock(which, cx) {
    this.openPanel(which === "workDock" ? "tasks" : which === "delegations" ? "agents" : which, cx);
  }

  openPanel(tab, cx) {
    const full = this.width || 1200;
    this.panelChosen = true;
    this.panel = this.panel === tab && (full >= NARROW || this.peek) ? null : tab;
    if (full < NARROW) this.peek = this.panel;
    this.save();
    cx.notify();
  }

  // fileChanges gives the files that the session edited: their edits and
  // their line counts.
  fileChanges() {
    if (this.changesCache?.version === this.t.version) return this.changesCache.files;
    const files = new Map();
    for (const item of this.t.items) {
      if (item.kind !== "tool" || item.status !== "done") continue;
      const name = toolName(item.name);
      if (name !== "edit" && name !== "write") continue;
      const f = this.toolFacts(item);
      if (!f.path) continue;
      const file = files.get(f.path) ?? { path: f.path, added: 0, removed: 0, edits: [], wrote: false };
      file.added += f.added;
      file.removed += f.removed;
      file.edits.push(item.id);
      if (name === "write") file.wrote = true;
      files.set(f.path, file);
    }
    const list = [...files.values()];
    this.changesCache = { version: this.t.version, files: list };
    return list;
  }

  // --- the context bar: where the agent works, what it cost, and the
  // panel. The pane header of the Galpon layout shows its name and state.

  contextBar(dock) {
    const P = this.P;
    const full = this.width || 1200;
    const counts = this.dockCounts();
    const changes = this.fileChanges();
    const button = (id, name, text, count, active, onClick, attention = false) => h_flex().id(id).h(26).px(8).gap(6).flex_none().items_center().rounded(6).cursor_pointer()
      .bg(active ? P.raised2 : P.bg).text_color(active ? P.text : P.text2).hover((s) => s.bg(P.raised).text_color(P.text))
      .on_click((_e, cx) => onClick(cx))
      .child(icon(name, 14))
      .when(full > 820 && Boolean(text), (el) => el.child(div().text_size(S.meta).child(text)))
      .when(count > 0, (el) => el.child(div().text_size(S.small).text_color(P.text3).child(String(count))))
      .when(attention, (el) => el.child(div().w(6).h(6).rounded_full().bg(P.yellow)));
    const cost = this.stats?.cost;
    return h_flex().id("pl-context-bar").h(40).flex_none().w_full().px(14).gap(8).items_center()
      .child(h_flex().h(24).min_w(0).px(8).gap(6).flex_shrink_1().items_center().rounded(6).bg(P.surface).border_1().border_color(P.line)
        .tooltip(this.agentFolder() || "The folder of the agent")
        .child(icon("folder", 12).text_color(P.text3))
        .child(div().min_w(0).truncate().font_family(MONO).text_size(S.small).text_color(P.text2).child(this.folderLabel(full > 900 ? 64 : 36) || "…")))
      .child(div().flex_1())
      .when(cost !== undefined && cost !== null && full > 560, (el) => el.child(div().pr(4).text_size(S.meta).text_color(P.text3)
        .tooltip("What this session cost at API prices").child(formatCost(cost))))
      .when(dock.shown && !dock.over, (el) => el.child(button("pl-panel-toggle", "panel-right", "", 0, true, (cx) => this.openPanel(dock.tab, cx), counts.waiting && dock.tab !== "agents")))
      .when(!dock.shown || dock.over, (el) => el
        .child(button("pl-tab-changes", "git-compare", "Changes", changes.length, false, (cx) => this.openPanel("changes", cx)))
        .child(button("pl-tab-tasks", "list-todo", "Tasks", counts.work, false, (cx) => this.openPanel("tasks", cx)))
        .child(button("pl-tab-agents", "bot", "Agents", counts.delegations, false, (cx) => this.openPanel("agents", cx), counts.waiting)));
  }

  banner(state, column) {
    const P = this.P;
    const error = state.tone === "error";
    return h_flex().id("pl-banner").flex_none().w_full().justify_center().pt(10)
      .child(h_flex().w(column).min_h(38).px(12).gap(10).items_center().rounded(10)
        .bg(error ? mix(P.bg, P.red, 0.1) : P.surface).border_1().border_color(error ? mix(P.bg, P.red, 0.4) : P.line)
        .child(error ? icon("alert", 15).text_color(P.red) : icon("spinner", 14).text_color(P.text2).spin(900))
        .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(error ? P.red : P.text2).child(state.text))
        .when(Boolean(state.action), (el) => el.child(this.smallButton("pl-banner-action", state.action[0], state.action[1], true))));
  }

  smallButton(id, label, onClick, primary = false) {
    const P = this.P;
    return h_flex().id(id).h(28).px(10).flex_none().items_center().rounded(7).cursor_pointer().text_size(S.meta).font_weight(500)
      .bg(primary ? P.accent : P.raised).text_color(primary ? P.accentInk : P.text).border_1().border_color(primary ? P.accent : P.line2)
      .hover((s) => s.opacity(0.88)).on_click((_e, cx) => onClick(cx)).child(label);
  }

  // --- the conversation: messages, and each run as a work log

  conversationView(column) {
    const P = this.P;
    const { start, shown, more, hidden } = this.conversation();
    const rows = [];
    if (more || hidden > 0) {
      rows.push(h_flex().id("pl-earlier").w_full().justify_center().pb(16)
        .when(more, (el) => el.child(this.smallButton("pl-earlier-button", `Show ${Math.min(PAGE, start)} earlier`, (cx) => this.showEarlier(cx))))
        .when(!more && hidden > 0, (el) => el.child(div().text_size(S.meta).text_color(P.text3).child(`${hidden} older messages are not loaded`))));
    }
    const segments = this.segmentsOf(shown);
    this.shownSegments = segments.map((segment) => segment.id);
    // The logs after the last message belong to the run that works now.
    let boundary = -1;
    segments.forEach((segment, index) => {
      if (segment.kind === "item" && ["user", "delivery", "turn"].includes(segment.item.kind)) boundary = index;
    });
    let previous = null;
    segments.forEach((segment, index) => {
      rows.push(this.segmentView(segment, previous, index > boundary));
      previous = segment;
    });
    if (this.t.working && (!segments.length || segments[segments.length - 1].kind !== "work")) rows.push(this.workingRow());
    // Notices, such as a warning about the MCP servers when Pi starts, do
    // not start the conversation: the welcome stays in the space above them.
    const started = this.t.items.some((item) => item.kind !== "notice");
    const content = v_flex().w_full().items_center().pt(20).pb(20)
      .when(!started, (el) => el.min_h(this.viewH).child(this.welcome(column).w_full().flex_1().pb(20)))
      .child(v_flex().w(column).children(rows));
    const latest = this.scrolledUp()
      ? h_flex().id("pl-latest").absolute().bottom(14).left(this.columnLeft + column / 2 - 60).h(28).px(12).gap(6).items_center().rounded(14).cursor_pointer()
        .bg(P.raised2).border_1().border_color(P.line2).text_size(S.meta).text_color(P.text).shadow_md()
        .on_click((_e, cx) => this.jumpToLatest(cx))
        .when(this.t.working, (el) => el.child(pulse(P.purple, 12)))
        .child("Jump to latest")
      : null;
    return this.scrollFrame("pl-body", content, { empty: this.t.items.length ? null : this.welcome(column).absolute().inset_0(), extra: [latest] });
  }

  // segmentsOf folds each run into one work log: its steps (thinking, tools
  // and background notices) and the short texts between them. The texts
  // after the last step stay outside: they are the answer.
  segmentsOf(items) {
    const out = [];
    let run = [];
    const isStep = (item) => item.kind === "thinking" || item.kind === "tool" || (item.kind === "custom" && item.customType === "galpon-background");
    const flush = () => {
      let last = -1;
      run.forEach((item, index) => {
        if (isStep(item)) last = index;
      });
      if (last >= 0) {
        out.push({ kind: "work", id: `wl-${run[0].id}`, items: run.slice(0, last + 1) });
        for (const item of run.slice(last + 1)) out.push({ kind: "item", id: item.id, item });
      } else {
        for (const item of run) out.push({ kind: "item", id: item.id, item });
      }
      run = [];
    };
    for (const item of items) {
      if (isStep(item) || item.kind === "text") {
        run.push(item);
        continue;
      }
      flush();
      out.push({ kind: "item", id: item.id, item });
    }
    flush();
    return out;
  }

  segmentView(segment, previous, current) {
    const top = !previous ? 0 : segment.kind === "work" ? 12 : ["user", "delivery"].includes(segment.item?.kind) ? 28 : segment.item?.kind === "turn" ? 6 : 12;
    if (segment.kind === "work") return this.measured(segment.id, this.workLog(segment, current), top);
    const item = segment.item;
    this.entry(item);
    const selected = this.isSelected(item.id);
    let el;
    switch (item.kind) {
      case "user": el = this.userItem(item); break;
      case "delivery": el = this.deliveryItem(item); break;
      case "text": el = div().id(`${item.id}-box`).w_full().text_size(S.read).on_mouse_down("left", (event) => this.notePress(item, event)).child(this.markdown(item.id, item.text || " ")); break;
      case "bash": el = this.bashItem(item); break;
      case "notice": el = this.noticeItem(item); break;
      case "compaction": el = this.dividerItem(item, `Context compacted${item.tokensBefore ? ` from ${formatTokens(item.tokensBefore)}` : ""}${item.tokensAfter ? ` to ${formatTokens(item.tokensAfter)}` : ""}`); break;
      case "branch": el = this.dividerItem(item, "Branch summary"); break;
      case "turn": el = this.turnItem(item); break;
      case "custom": el = this.customItem(item); break;
      default: el = div().id(item.id);
    }
    const box = div().id(`${item.id}-sel`).w_full().relative().child(el)
      .when(selected || this.flashItem === item.id, (it) => it.child(div().absolute().left(-14).top(2).bottom(2).w(2).rounded(1).bg(this.P.accent)));
    return this.measured(item.id, box, top);
  }

  welcome(column) {
    const P = this.P;
    const where = this.conn === "live" || this.demo ? `Galpon agent in ${this.workspaceName() || "its workspace"}` : this.conn === "error" ? this.error || `No connection to ${this.agentName}` : "Starting…";
    const tip = (keys, label) => h_flex().gap(8).items_center().child(kbd(P, keys)).child(div().text_size(S.meta).text_color(P.text3).child(label));
    return v_flex().id("pl-welcome").items_center().justify_center().gap(6)
      .child(h_flex().w(44).h(44).items_center().justify_center().rounded(12).bg(P.raised).border_1().border_color(P.line2)
        .child(icon("sparkle", 22).text_color(P.text2)))
      .child(div().pt(10).text_size(S.head).font_weight(600).child(this.agentTitle()))
      .child(div().text_size(S.ui).text_color(P.text2).child(where))
      .child(div().font_family(MONO).text_size(S.small).text_color(P.text3).child(this.folderLabel()))
      .child(v_flex().pt(18).gap(8).items_start().child(tip("Ctrl L", "Choose the model")).child(tip("Shift Tab", this.modes.length ? "Change the permission mode" : "Change the thinking level")).child(tip("/", "Commands")).child(tip("Alt W", "Tasks panel")));
  }

  userItem(item) {
    const P = this.P;
    const hovered = this.hovered === item.id;
    return v_flex().id(item.id).w_full().items_end().gap(4)
      .on_hover((on, cx) => this.hover(item.id, on, cx))
      .child(div().max_w(Math.round((this.columnW ?? 700) * 0.82)).px(14).py(10).rounded(14).bg(P.raised).border_1().border_color(P.line).text_size(S.read)
        .child(this.markdown(item.id, userMarkdown(item.text || "(image)", item.images?.length ?? 0), (url, cx) => this.openMessageLink(item, url, cx))))
      .when(item.images?.length > 0, (el) => el.child(this.imagesView(item.id, item.images)))
      .child(div().h(14).text_size(S.small).text_color(P.text3).child(hovered && item.at ? clock(item.at) : ""));
  }

  deliveryItem(item) {
    const P = this.P;
    return v_flex().id(item.id).w_full().gap(8).children(item.deliveries.map((delivery, index) => {
      const color = { request: P.blue, question: P.purple, inform: P.text2, result: P.green, blocker: P.yellow }[delivery.kind] ?? P.blue;
      return v_flex().id(`${item.id}-${index}`).w_full().rounded(10).bg(P.surface).border_1().border_color(P.line).overflow_hidden()
        .child(h_flex().w_full().h(36).px(12).gap(8).items_center().border_b(1).border_color(P.line)
          .child(icon(delivery.kind === "result" ? "circle-check" : delivery.kind === "blocker" ? "alert" : "message", 14).text_color(color))
          .child(div().flex_none().text_size(S.ui).font_weight(600).child(DELIVERY_LABELS[delivery.kind] ?? "Message"))
          .when(Boolean(delivery.sender), (el) => el.child(div().min_w(0).truncate().text_size(S.ui).text_color(P.text2).child(`from ${delivery.sender}`)))
          .child(div().flex_1())
          .when(Boolean(item.at), (el) => el.child(div().flex_none().text_size(S.small).text_color(P.text3).child(clock(item.at)))))
        .child(div().w_full().px(14).py(10).text_size(S.read).child(this.markdown(`${item.id}-${index}`, delivery.body || " ")));
    })).when(item.images?.length > 0, (el) => el.child(this.imagesView(item.id, item.images)));
  }

  // markdown draws markdown text. onLink, when given, opens the links.
  markdown(id, text, onLink = null) {
    const parts = this.markdownParts(id, text);
    const prose = (key, value) => TextView.markdown(key, value).selectable(true).when(Boolean(onLink), (el) => el.on_link_click(onLink));
    if (!parts.length) return TextView.markdown(`${id}-md`, " ");
    if (parts.length === 1 && parts[0].kind === "prose") return prose(`${id}-md`, parts[0].text);
    return v_flex().id(`${id}-parts`).w_full().gap(10).children(parts.map((part, index) => (part.kind === "code"
      ? this.codeBlock(`${id}-code${index}`, part)
      : prose(`${id}-md${index}`, part.text))));
  }

  // openMessageLink opens a link of a message of the person: a galpon-image
  // link opens that image of the message in the image viewer of the system,
  // a galpon-quote link scrolls to the reply of that quote, and other links
  // open as usual.
  openMessageLink(item, url, cx) {
    const quote = /^galpon-quote:(\d+)$/.exec(url)?.[1];
    if (quote) return this.revealQuote(item, Number(quote), cx);
    const n = /^galpon-image:(\d+)$/.exec(url)?.[1];
    if (!n) return cx.open_url(url);
    const picture = item.images?.[Number(n) - 1];
    if (!picture?.path || this.imageState(picture) !== "ready") return;
    // The viewer runs on its own, so that the call returns at once.
    const opener = appWindow()?.platform === "macos" ? "open" : "setsid xdg-open";
    exec("sh", ["-c", `${opener} "$1" > /dev/null 2>&1 &`, "sh", `${appHome()}/${picture.path}`]).catch((error) => this.say(message(error), "error"));
  }

  codeBlock(id, part) {
    const P = this.P;
    const all = String(part.text).replace(/\t/g, "    ").split("\n");
    const shown = all.slice(0, 400);
    const copied = this.copied === id;
    return v_flex().id(id).w_full().rounded(10).bg(P.surface).border_1().border_color(P.line).overflow_hidden()
      .child(h_flex().w_full().h(30).px(12).gap(8).items_center().border_b(1).border_color(P.line)
        .child(div().font_family(MONO).text_size(S.small).text_color(P.text3).child(part.language || "text"))
        .child(div().flex_1())
        .child(h_flex().id(`${id}-copy`).h(22).px(6).gap(5).items_center().rounded(5).cursor_pointer().text_size(S.small).text_color(copied ? P.green : P.text3)
          .hover((s) => s.bg(P.raised).text_color(P.text)).on_click((_e, cx) => this.copyText(part.text, id, cx))
          .child(icon(copied ? "check" : "files", 12)).child(copied ? "Copied" : "Copy")))
      .child(div().id(`${id}-scroll`).w_full().overflow_x_scroll().px(14).py(10).font_family(mono()).text_size(S.ui).text_color(P.text)
        .children(shown.map((line, index) => div().id(`${id}-${index}`).whitespace_nowrap()
          .when(part.language === "diff", (el) => el.text_color(diffColor(P, line)))
          .child(line || " ")))
        .when(all.length > shown.length, (el) => el.child(div().pt(4).text_color(P.text3).child(`${all.length - shown.length} more lines`))));
  }

  // workLog is one work log: a header that folds, and the steps of the run.
  // A finished log folds; the live one stays open.
  workLog(segment, current) {
    const P = this.P;
    const live = current && this.t.working;
    const open = live ? !this.open.has(segment.id) : this.open.has(segment.id);
    this.entryGroup(segment.id, (cx) => this.toggle(segment.id, cx));
    const selected = this.isSelected(segment.id);
    const tools = segment.items.filter((item) => item.kind === "tool");
    const failed = tools.filter((item) => item.status === "error").length;
    let added = 0;
    let removed = 0;
    const files = new Set();
    for (const item of tools) {
      const f = this.toolFacts(item);
      if ((f.name === "edit" || f.name === "write") && f.path && item.status === "done") {
        files.add(f.path);
        added += f.added;
        removed += f.removed;
      }
    }
    const running = [...segment.items].reverse().find((item) => item.kind === "tool" && (item.status === "running" || item.status === "streaming"));
    const steps = segment.items.filter((item) => item.kind !== "text").length;
    const label = live
      ? `Working ${elapsed(Date.now() - (this.t.workingSince ?? Date.now()))}`
      : `Worked through ${steps} ${steps === 1 ? "step" : "steps"}`;
    const header = h_flex().id(`${segment.id}-head`).w_full().h(30).px(8).gap(8).items_center().rounded(8).cursor_pointer()
      .when(selected, (el) => el.bg(P.raised)).hover((s) => s.bg(P.raised))
      .on_click((_e, cx) => this.toggle(segment.id, cx))
      .child(live ? pulse(P.purple, 14) : icon(open ? "chevron-down" : "chevron-right", 14).text_color(P.text3))
      .child(div().flex_none().text_size(S.ui).text_color(live ? P.text : P.text2).font_weight(500).child(label))
      .when(live && Boolean(running), (el) => el.child(div().min_w(0).flex_shrink_1().truncate().text_size(S.ui).text_color(P.text3).child(this.stepText(running, this.toolFacts(running)))))
      .child(div().flex_1())
      .when(failed > 0, (el) => el.child(badge(`${failed} failed`, P.red, mix(P.bg, P.red, 0.12))))
      .when(files.size > 0, (el) => el.child(h_flex().flex_none().gap(6).items_center().text_size(S.small)
        .child(div().text_color(P.text3).child(`${files.size} ${files.size === 1 ? "file" : "files"}`))
        .child(div().text_color(P.green).child(`+${added}`))
        .child(div().text_color(P.red).child(`−${removed}`))));
    const box = v_flex().id(segment.id).w_full().rounded(10).border_1().border_color(open ? P.line : P.bg).when(open, (el) => el.bg(P.side)).child(header);
    if (!open) return box;
    return box.child(v_flex().w_full().px(8).pb(8).gap(1).children(segment.items.map((item) => {
      this.entry(item);
      return this.stepView(item);
    })));
  }

  stepIcon(item) {
    if (item.kind === "thinking") return "brain";
    if (item.kind === "custom") return "zap";
    const name = toolName(item.name);
    if (name === "read") return "file-text";
    if (name === "grep" || name === "find" || name === "ls") return "search";
    if (name === "edit" || name === "write") return "square-pen";
    if (name === "bash") return "terminal";
    if (name === "web_search" || name === "fetch_content" || name === "get_search_content") return "globe";
    if (name.startsWith("galpon_") || name === "agent") return "bot";
    if (name === "askuserquestion") return "message";
    if (name === "exitplanmode") return "list-todo";
    if (name === "todo") return "list-todo";
    if (name === "monitor" || name === "taskoutput" || name === "taskstop") return "zap";
    return "sparkle";
  }

  // stepText says a step as a verb and its object. A shell command shows
  // alone: its icon and its font already say that it is a command.
  stepText(item, f) {
    const name = f.name;
    const base = f.path ? f.path.split("/").filter(Boolean).pop() : "";
    if (name === "read") return `Read ${base || f.summary}`;
    if (name === "grep") return `Searched ${f.args?.pattern ? `“${oneLine(f.args.pattern)}”` : ""}${f.args?.path ? ` in ${f.args.path}` : ""}`;
    if (name === "find" || name === "ls") return `Listed ${f.summary || "files"}`;
    if (name === "edit") return `Edited ${base}`;
    if (name === "write") return `Wrote ${base}`;
    if (name === "bash") return commandLine(f.command);
    if (name === "todo") return `Updated the todos${f.summary ? ` · ${f.summary}` : ""}`;
    if (name === "askuserquestion") return "Asked you a question";
    if (name === "exitplanmode") return "Finished the plan";
    return `${f.label}${f.summary ? ` · ${f.summary}` : ""}`;
  }

  stepView(item) {
    const P = this.P;
    if (item.kind === "thinking") return this.thoughtStep(item);
    if (item.kind === "custom") return this.customItem(item);
    if (item.kind === "text") return div().id(`${item.id}-step`).w_full().px(6).py(4).text_size(S.ui).text_color(P.text2).child(this.markdown(item.id, item.text || " "));
    const f = this.toolFacts(item);
    const selected = this.isSelected(item.id);
    const status = f.running ? pulse(P.purple, 12)
      : f.failed ? icon("x", 13).text_color(P.red)
      : f.stopped ? h_flex().flex_none().gap(4).items_center().text_size(S.small).text_color(P.text3).child(icon("stop", 12).text_color(P.text3)).child("stopped")
      : null;
    const text = this.stepText(item, f);
    // The row has the height of one line, and nothing draws outside it.
    const row = h_flex().id(`${item.id}-row`).w_full().h(28).px(6).gap(8).items_center().rounded(6).overflow_hidden().cursor_pointer()
      .when(selected, (el) => el.bg(P.raised)).hover((s) => s.bg(P.raised))
      .on_click((_e, cx) => this.toggle(item.id, cx))
      .child(icon(this.stepIcon(item), 13).text_color(f.failed ? P.red : f.galpon ? P.blue : P.text3))
      .child(div().min_w(0).flex_shrink_1().truncate().text_size(S.ui).text_color(f.failed ? P.red : P.text2)
        .when(f.name === "bash", (el) => el.font_family(MONO).text_size(S.meta)).child(text))
      .when(f.added > 0 || f.removed > 0, (el) => el.child(h_flex().flex_none().gap(5).text_size(S.small)
        .child(div().text_color(P.green).child(`+${f.added}`)).child(div().text_color(P.red).child(`−${f.removed}`))))
      .child(div().flex_1())
      .when(f.background, (el) => el.child(badge("Background", P.text2, P.raised2)))
      .when(Boolean(f.duration), (el) => el.child(div().flex_none().text_size(S.small).text_color(P.text3).child(f.duration)))
      .when(Boolean(status), (el) => el.child(status));
    const box = v_flex().id(item.id).w_full().child(row);
    if (f.running && f.partial) return box.child(this.stepDetail(item.id, [this.monoText(`${item.id}-live`, tail(f.partial, 8), P.text2)]));
    if (!f.open) return box;
    const details = [];
    if (f.command) details.push(this.monoText(`${item.id}-cmd`, `$ ${f.command}`, P.text));
    else if (!f.diff && f.args && Object.keys(f.args).length) details.push(this.monoText(`${item.id}-args`, JSON.stringify(f.args, null, 2), P.text3));
    if (f.diff) details.push(this.diffView(`${item.id}-diff`, f.diff));
    if (f.text && !f.diff) {
      const all = lines(f.text);
      const full = this.open.has(`${item.id}-all`);
      details.push(this.monoText(`${item.id}-out`, full ? f.text : all.slice(0, 40).join("\n"), f.failed ? P.red : P.text2));
      if (all.length > 40) {
        details.push(div().id(`${item.id}-more`).cursor_pointer().text_size(S.small).text_color(P.accent)
          .on_click((_e, cx) => this.toggle(`${item.id}-all`, cx)).child(full ? "Show less" : `Show all ${all.length} lines`));
      }
    }
    if (f.images.length) details.push(this.imagesView(`${item.id}-img`, f.images));
    const keys = [];
    if (f.text) keys.push(this.copyButton(`${item.id}-copy-out`, "Copy output", f.text));
    if (f.command) keys.push(this.copyButton(`${item.id}-copy-cmd`, "Copy command", f.command));
    if (keys.length) details.push(h_flex().gap(4).children(keys));
    return box.child(this.stepDetail(item.id, details));
  }

  stepDetail(id, children) {
    const P = this.P;
    return v_flex().id(`${id}-detail`).ml(27).mr(6).mt(2).mb(6).px(12).py(10).gap(8).rounded(8).bg(P.bg).border_1().border_color(P.line).children(children);
  }

  monoText(id, text, color) {
    return div().id(id).w_full().font_family(mono()).text_size(S.meta).text_color(color).child(text || " ");
  }

  diffView(id, text) {
    const P = this.P;
    return v_flex().id(id).w_full().font_family(mono()).text_size(S.meta).children(lines(text).slice(0, 400).map((line, index) => {
      const added = line.startsWith("+") && !line.startsWith("+++");
      const removed = line.startsWith("-") && !line.startsWith("---");
      return div().id(`${id}-${index}`).px(4).rounded(3)
        .when(added, (el) => el.bg(mix(P.bg, P.green, 0.1)).text_color(P.green))
        .when(removed, (el) => el.bg(mix(P.bg, P.red, 0.1)).text_color(P.red))
        .when(!added && !removed, (el) => el.text_color(line.startsWith("@@") ? P.blue : P.text3))
        .child(line || " ");
    }));
  }

  copyButton(id, label, text) {
    const P = this.P;
    const copied = this.copied === id;
    return h_flex().id(id).h(22).px(6).gap(5).flex_none().items_center().rounded(5).cursor_pointer().text_size(S.small).text_color(copied ? P.green : P.text3)
      .hover((s) => s.bg(P.raised).text_color(P.text)).on_click((_e, cx) => this.copyText(text, id, cx))
      .child(icon(copied ? "check" : "files", 12)).child(copied ? "Copied" : label);
  }

  imagesView(id, images) {
    const P = this.P;
    return h_flex().id(id).gap(8).flex_wrap().items_start().children(images.map((picture, index) => {
      const width = Math.min(480, picture.width || 320);
      const height = picture.width && picture.height ? Math.round((width * picture.height) / picture.width) : 240;
      const state = this.imageState(picture);
      if (state !== "ready") {
        return h_flex().id(`${id}-${index}`).h(32).px(10).gap(8).items_center().rounded(8).bg(P.surface).text_size(S.small).text_color(P.text3)
          .child(state === "failed" ? "Image not available here" : "Loading image…");
      }
      return image(picture.path).id(`${id}-${index}`).w(width).h(height).rounded(8);
    }));
  }

  thoughtStep(item) {
    const P = this.P;
    const open = this.open.has(item.id);
    const selected = this.isSelected(item.id);
    const first = lines(item.text).find((line) => line.trim()) ?? "";
    return v_flex().id(item.id).w_full()
      .child(h_flex().id(`${item.id}-row`).w_full().h(28).px(6).gap(8).items_center().rounded(6).cursor_pointer()
        .when(selected, (el) => el.bg(P.raised)).hover((s) => s.bg(P.raised))
        .on_click((_e, cx) => this.toggle(item.id, cx))
        .child(icon("brain", 13).text_color(P.purple))
        .child(div().flex_none().text_size(S.ui).text_color(P.text2).child(item.redacted ? "Thought (hidden)" : item.streaming ? "Thinking…" : "Thought"))
        .when(!open && Boolean(first), (el) => el.child(div().min_w(0).flex_shrink_1().truncate().italic().text_size(S.ui).text_color(P.text3).child(first.slice(0, 140)))))
      .when(open && Boolean(item.text), (el) => el.child(div().w_full().pl(27).pr(6).py(4).italic().text_size(S.ui).text_color(P.text3)
        .child(TextView.markdown(`${item.id}-md`, item.text).selectable(true))));
  }

  workingRow() {
    const P = this.P;
    return h_flex().id("pl-working").w_full().mt(12).h(30).px(8).gap(8).items_center()
      .child(pulse(P.purple, 14))
      .child(div().text_size(S.ui).text_color(P.text2).child(this.workingText()));
  }

  customItem(item) {
    const P = this.P;
    const notices = item.details?.notices;
    if (item.customType === "galpon-background" && Array.isArray(notices)) {
      return v_flex().id(item.id).w_full().children(notices.map((notice, index) => {
        const failed = notice.status === "failed" || notice.status === "timed_out";
        const id = `${item.id}-${index}`;
        const open = this.open.has(id);
        const body = [...(notice.lines ?? []), ...(notice.tail ? [notice.tail] : [])].join("\n");
        const live = notice.kind === "monitor" && notice.status === "running";
        return v_flex().id(id).w_full()
          .child(h_flex().id(`${id}-row`).w_full().h(28).px(6).gap(8).items_center().rounded(6).overflow_hidden().cursor_pointer().hover((s) => s.bg(P.raised))
            .on_click((_e, cx) => this.toggle(id, cx))
            .child(icon("zap", 13).text_color(failed ? P.red : notice.status === "completed" ? P.green : P.yellow))
            .child(div().flex_none().text_size(S.ui).text_color(P.text2).child(notice.kind === "monitor" ? "Monitor" : notice.kind === "agent" ? "Helper" : "Background task"))
            .child(div().min_w(0).flex_shrink_1().truncate().text_size(S.ui).text_color(P.text).child(oneLine(notice.description ?? notice.id)))
            .child(div().flex_1())
            .child(div().flex_none().max_w(260).truncate().text_size(S.small).text_color(failed ? P.red : P.text3).child(oneLine(notice.summary ?? notice.status ?? ""))))
          .when(Boolean(body) && (open || live), (el) => el.child(this.stepDetail(id, [this.monoText(`${id}-out`, open ? body : tail(body, 6), P.text2)])));
      }));
    }
    if (item.customType === "claude-command") {
      // The output of a local command of Claude Code: Markdown, such as the
      // tables of /context, or text in columns, such as /usage.
      const text = item.text || " ";
      const markdown = /(^|\n)#{1,4} |\|.*\|/.test(text);
      return v_flex().id(item.id).w_full().px(12).py(10).gap(6).rounded(10).bg(P.surface).border_1().border_color(P.line)
        .child(h_flex().gap(8).items_center()
          .child(icon("terminal", 13).text_color(P.text3))
          .child(div().font_family(MONO).text_size(S.small).text_color(P.text3).child(item.details?.command || "Claude Code")))
        .child(markdown
          ? div().w_full().text_size(S.ui).text_color(P.text2).child(this.markdown(item.id, text))
          : this.monoText(`${item.id}-out`, text, P.text2));
    }
    return v_flex().id(item.id).w_full().px(12).py(10).gap(4).rounded(10).bg(P.surface).border_1().border_color(P.line)
      .child(div().text_size(S.small).font_weight(600).text_color(P.text3).child(item.customType ?? "Message"))
      .child(div().w_full().text_size(S.ui).text_color(P.text2).child(TextView.markdown(`${item.id}-md`, plainMarkdown(item.text || " ")).selectable(true)));
  }

  bashItem(item) {
    const P = this.P;
    return v_flex().id(item.id).w_full().rounded(10).bg(P.surface).border_1().border_color(P.line).overflow_hidden()
      .child(h_flex().w_full().h(32).px(12).gap(8).items_center().border_b(1).border_color(P.line)
        .child(icon("terminal", 13).text_color(P.text3))
        .child(div().flex_1().min_w(0).truncate().font_family(mono()).text_size(S.meta).text_color(P.text).child(commandLine(item.command))))
      .when(Boolean(item.output), (el) => el.child(div().px(12).py(8).child(this.monoText(`${item.id}-out`, tail(item.output, 30), item.exitCode ? P.red : P.text2))));
  }

  noticeItem(item) {
    const P = this.P;
    const color = item.level === "error" ? P.red : item.level === "warning" ? P.yellow : P.text2;
    // A notice can have more lines, such as a list of servers. The mark
    // stays beside its first line.
    return h_flex().id(item.id).w_full().min_h(32).px(12).py(6).gap(8).items_start().rounded(8).bg(mix(P.bg, color, 0.08))
      .child(h_flex().h(20).flex_none().items_center().child(icon(item.level === "info" ? "info" : "alert", 14).text_color(color)))
      .child(div().flex_1().min_w(0).text_size(S.ui).line_height("20px").text_color(color).child(item.text));
  }

  dividerItem(item, label) {
    const P = this.P;
    const open = this.open.has(item.id);
    return v_flex().id(item.id).w_full().gap(8).py(4)
      .child(h_flex().id(`${item.id}-row`).w_full().gap(10).items_center().cursor_pointer().on_click((_e, cx) => this.toggle(item.id, cx))
        .child(div().flex_1().h(1).bg(P.line))
        .child(h_flex().gap(4).items_center().text_size(S.small).text_color(P.text3).child(label).child(icon(open ? "chevron-down" : "chevron-right", 12)))
        .child(div().flex_1().h(1).bg(P.line)))
      .when(open, (el) => el.child(div().w_full().px(12).py(10).rounded(10).bg(P.surface).text_size(S.ui).text_color(P.text2).child(TextView.markdown(`${item.id}-md`, item.summary || " ").selectable(true))));
  }

  turnItem(item) {
    const P = this.P;
    const f = this.turnFacts(item);
    const parts = [f.model, f.thinking ? sentence(f.thinking) : "", f.duration, f.tokens, f.cost].filter(Boolean);
    return h_flex().id(item.id).w_full().h(18).gap(6).items_center().text_size(S.small).text_color(P.text3).tooltip(f.detail)
      .children(parts.flatMap((part, index) => [...(index ? [div().child("·")] : []), div().child(part)]))
      .child(div().flex_1())
      .child(div().child(f.clock));
  }

  // --- the composer: one box with its controls inside

  composer(column) {
    const P = this.P;
    const working = this.t.working;
    const focused = this.composerFocused;
    const tray = this.tray();
    const model = this.model;
    const { index, count } = thinkingStep(this.thinking, this.levels);
    const ctx = this.contextFacts();
    const chip = (id, content, onClick, tooltip) => h_flex().id(id).h(28).px(8).gap(6).flex_none().items_center().rounded(7).cursor_pointer()
      .text_size(S.meta).text_color(P.text2).hover((s) => s.bg(P.raised2).text_color(P.text))
      .when(Boolean(tooltip), (el) => el.tooltip(tooltip))
      .on_click((_e, cx) => onClick(cx)).children(content);
    const flash = this.flash;
    const statuses = this.otherStatuses();
    const queued = this.queued();
    const send = div().id("pl-send").w(30).h(30).flex_none().rounded_full().flex().items_center().justify_center().cursor_pointer()
      .bg(working ? P.raised2 : this.composerText.trim() ? P.accent : P.raised2)
      .text_color(working ? P.text : this.composerText.trim() ? P.accentInk : P.text3)
      .hover((s) => s.opacity(0.88))
      .tooltip(working ? "Stop · Esc" : "Send · Enter")
      .on_click((_e, cx) => (working ? this.stop(cx) : this.send(cx)))
      .child(icon(working ? "stop" : "arrow-up", 15));
    return v_flex().id("pl-composer").flex_none().w_full().items_center().pt(4).pb(14).gap(6)
      .when(tray.length > 0, (el) => el.child(v_flex().w(column).p(6).gap(2).rounded(12).bg(P.surface).border_1().border_color(P.line2).shadow_md().children(tray)))
      .child(v_flex().id("pl-box").w(column).rounded(14).bg(P.surface).border_1().border_color(focused ? P.accentLine : P.line2)
        .child(this.composerKeys(div().id("pl-input").w_full().px(14).pt(12).pb(4)).child(Textarea.new(this.textarea).w_full().text_size(S.read).line_height(composerLine()).text_color(P.text)
          .token((token) => this.tokenChip(token))
          .on_token_hover((event, cx) => {
            if (event.hovered) this.preview = event.token.id;
            else if (this.preview === event.token.id) this.preview = null;
            cx.notify();
          })
          .on_token_click((event, cx) => {
            this.preview = this.preview === event.token.id ? null : event.token.id;
            cx.notify();
          })))
        .child(h_flex().w_full().h(42).px(6).gap(2).items_center()
          .when(Boolean(model) || this.conn === "live", (el) => el
            .child(chip("pl-model", [icon("sparkle", 13).text_color(P.text3), div().max_w(180).truncate().child(model ? (model.name ?? model.id) : "No model"), icon("chevron-down", 12)],
              (cx) => this.openModels(cx), "Choose the model · Ctrl+L").on_size_change(this.measureInto("modelChipW")))
            .when(Boolean(model) && model.reasoning !== false, (it) => it.child(chip("pl-think",
              [this.thinkingBars(count > 1 ? index / (count - 1) : 0), div().child(this.thinkingWord()), icon("chevron-down", 12)],
              (cx) => this.openThinking(cx), this.native ? "Effort" : "Thinking level · Shift+Tab").on_size_change(this.measureInto("thinkChipW"))))
            .when(this.modes.length > 0 && Boolean(this.mode), (it) => it.child(this.modeChip(chip))))
          .child(div().flex_1().min_w(0).px(8).truncate().text_size(S.small).text_color(flash?.level === "error" ? P.red : flash?.level === "warning" ? P.yellow : P.text3)
            .child(oneLine(flash?.text ?? (queued.length ? `${queued.length} queued: ${queued.map(([, text]) => text).join(" · ")}` : statuses.join(" · ")))))
          .when(ctx.percent !== null, (el) => el.child(h_flex().id("pl-context").h(28).px(6).gap(6).flex_none().items_center().rounded(7)
            .tooltip(`Context: ${formatTokens(ctx.used)} of ${formatTokens(ctx.window)} tokens`)
            .child(ring(ctx.percent / 100, ctx.percent >= 85 ? P.red : ctx.percent >= 65 ? P.yellow : P.text2, P.raised2, { size: 16, width: 2.5 }))
            .when((this.width || 1200) > 640, (it) => it.child(div().text_size(S.small).text_color(P.text3).child(`${Math.round(ctx.percent)}%`)))))
          .child(div().w(4))
          .child(send)))
      .child(div().id("pl-hint").w(column).text_size(S.small).text_color(P.text3).on_size_change(this.measureInto("hintH", "height")).child(this.composerHint()));
  }

  thinkingBars(share, color = null) {
    const P = this.P;
    const lit = Math.round(share * 4);
    return h_flex().h(12).gap(1.5).items_end().flex_none()
      .children([5, 7, 9, 11].map((height, i) => div().w(2.5).h(height).rounded(1).bg(i < lit ? color ?? P.purple : P.line2)));
  }

  tray() {
    const P = this.P;
    const out = [];
    for (const [key, widget] of this.widgets) {
      if (widget.placement === "belowEditor" || !widget.lines.length) continue;
      out.push(v_flex().id(`pl-widget-${key}`).w_full().px(8).py(4).font_family(mono()).text_size(S.meta).text_color(P.text2)
        .children(widget.lines.map((line, index) => div().id(`pl-widget-${key}-${index}`).child(line || " "))));
    }
    if (this.dialogs.length) out.push(this.dialogPanel(this.dialogs[0]));
    else {
      const matches = this.slashMatches();
      if (matches.length) out.push(this.slashPanel(matches));
    }
    return out;
  }

  // optionRow gives an option on one line, and nothing draws outside its row.
  optionRow(id, label, selected, onClick, { detail = "", keys = "" } = {}) {
    const P = this.P;
    return h_flex().id(id).w_full().h(32).px(8).gap(10).items_center().rounded(8).overflow_hidden().cursor_pointer()
      .when(selected, (el) => el.bg(P.raised2)).when(!selected, (el) => el.hover((s) => s.bg(P.raised)))
      .on_click((_e, cx) => onClick(cx))
      // A row without a detail gives its label all the width.
      .child(div().when(Boolean(detail), (el) => el.flex_none().max_w(280)).when(!detail, (el) => el.flex_1().min_w(0)).truncate()
        .text_size(S.ui).text_color(selected ? P.text : P.text2).when(selected, (el) => el.font_weight(500)).child(oneLine(label)))
      .when(Boolean(detail), (el) => el.child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(P.text3).child(oneLine(detail))))
      .when(Boolean(keys), (el) => el.child(kbd(P, keys)))
      .when(selected, (el) => el.child(kbd(P, "↵")));
  }

  dialogPanel(dialog) {
    const P = this.P;
    const left = this.dialogLeft(dialog);
    let panel = v_flex().id("pl-dialog").w_full().p(6).gap(4)
      .child(h_flex().w_full().gap(8).items_center()
        .child(icon("message", 14).text_color(P.yellow))
        .child(div().text_size(S.small).font_weight(600).text_color(P.yellow).child(`${this.agentName} asks`))
        .child(div().flex_1())
        .when(left > 0, (el) => el.child(div().text_size(S.small).text_color(P.text3).child(`${left}s`))))
      .child(div().px(2).pb(4).text_size(S.title).font_weight(600).child(dialog.title ?? `${this.agentName} asks you`))
      // What the dialog asks about, such as the command or the change that
      // Claude Code asks to make.
      .when(Boolean(dialog.message) && dialog.method !== "confirm", (el) => el.child(this.dialogMessage(dialog)));
    const hint = (text) => div().px(2).pt(2).text_size(S.small).text_color(P.text3).child(text);
    switch (dialog.method) {
      case "select":
        return panel.children((dialog.options ?? []).map((value, index) => this.optionRow(`pl-option-${index}`, value, index === this.dialogCursor, (cx) => this.answerDialog(value, cx), { keys: index < 9 ? String(index + 1) : "" })))
          .child(hint("↑↓ choose · Enter select · Esc cancel"));
      case "confirm":
        return panel
          .when(Boolean(dialog.message), (el) => el.child(div().px(2).text_size(S.ui).text_color(P.text2).child(dialog.message)))
          .child(h_flex().gap(6).pt(4)
            .child(this.smallButton("pl-yes", "Yes", (cx) => this.answerDialog(true, cx), this.dialogCursor === 0))
            .child(this.smallButton("pl-no", "No", (cx) => this.answerDialog(false, cx), this.dialogCursor === 1)))
          .child(hint("Y yes · N no · Esc cancel"));
      case "input":
        return panel
          .child(h_flex().h(34).px(10).items_center().rounded(8).bg(P.bg).border_1().border_color(P.accentLine)
            .child(Input.new(this.dialogInput).flex_1().text_size(S.read).text_color(P.text)))
          .child(hint("Enter submit · Esc cancel"));
      case "editor":
        return panel
          .child(div().px(10).py(8).rounded(8).bg(P.bg).border_1().border_color(P.line2)
            .child(Textarea.new(this.dialogEditor).w_full().h(140).text_size(S.ui).font_family(mono()).text_color(P.text)))
          .child(h_flex().pt(4).gap(8).items_center()
            .child(this.smallButton("pl-editor-ok", "Submit", (cx) => this.answerDialog(this.dialogEditor.value(), cx), true))
            .child(this.smallButton("pl-editor-cancel", "Cancel", (cx) => this.answerDialog(null, cx)))
            .child(hint("Esc cancel")));
      default:
        return panel;
    }
  }

  // dialogMessage shows the message of a dialog. A long message scrolls in a
  // part of the height of the tab. A short one has no scroll area, because
  // the scroll area adds space under a code block.
  dialogMessage(dialog) {
    const P = this.P;
    const text = String(dialog.message);
    const rows = text.split("\n").reduce((sum, line) => sum + Math.max(1, Math.ceil(line.length / 90)), 0);
    const limit = clamp((this.height || 800) * 0.35, 120, 340);
    return v_flex().id("pl-dialog-message").w_full().px(2).pb(6).text_size(S.ui).text_color(P.text2)
      .when(rows * 20 > limit, (el) => el.max_h(limit).overflow_y_scrollbar())
      .child(this.markdown(`dialog-${dialog.id}`, text));
  }

  slashPanel(matches) {
    const cursor = Math.min(this.slashCursor, matches.length - 1);
    return v_flex().id("pl-slash").w_full().gap(1).children(matches.map((command, index) => this.optionRow(`pl-slash-${index}`, `/${command.name}`, index === cursor, (cx) => {
      this.slashCursor = index;
      this.complete(cx);
    }, { detail: command.description ?? "" })));
  }

  // --- the panel on the right

  panelView(dock) {
    const P = this.P;
    const tab = dock.tab;
    const tabs = this.panelTabs();
    const counts = this.dockCounts();
    const tabButton = (id, label, count) => h_flex().id(`pl-panel-${id}`).h(26).px(10).gap(6).flex_none().items_center().rounded(6).cursor_pointer()
      .bg(tab === id ? P.raised2 : P.side).text_color(tab === id ? P.text : P.text3).hover((s) => s.text_color(P.text))
      .on_click((_e, cx) => this.openPanel(id, cx))
      .child(div().text_size(S.meta).font_weight(500).child(label))
      .when(count > 0, (el) => el.child(div().text_size(S.small).text_color(P.text3).child(String(count))));
    let body;
    if (tab === "changes") body = this.changesTab();
    else if (tab === "agents") body = this.agentsTab();
    else body = this.tasksTab();
    return v_flex().id("pl-panel").w(dock.width).flex_none().h_full().bg(P.side)
      .child(h_flex().h(44).flex_none().px(10).gap(4).items_center().border_b(1).border_color(P.line)
        .child(tabButton("changes", "Changes", this.fileChanges().length))
        .child(tabButton("tasks", "Tasks", counts.work))
        .when(this.galpon.enabled, (el) => el.child(tabButton("agents", "Agents", counts.delegations)))
        .child(div().flex_1())
        .child(div().id("pl-panel-close").w(26).h(26).flex().items_center().justify_center().rounded(6).cursor_pointer().text_color(P.text3)
          .hover((s) => s.bg(P.raised).text_color(P.text)).tooltip("Close the panel")
          .on_click((_e, cx) => this.openPanel(tab, cx)).child(icon("x", 14))))
      .child(v_flex().id("pl-panel-list").flex_1().min_h(0).overflow_y_scrollbar().p(10).gap(14).child(body))
      .when(!tabs.changes && !tabs.tasks && !tabs.agents, (el) => el);
  }

  panelSection(title, right, rows) {
    const P = this.P;
    return v_flex().w_full().gap(2)
      .child(h_flex().h(24).px(6).items_center().child(div().text_size(S.small).font_weight(600).text_color(P.text3).child(title)).child(div().flex_1())
        .when(Boolean(right), (el) => el.child(div().text_size(S.small).text_color(P.text3).child(right))))
      .children(rows);
  }

  // panelRow gives a text and its note on one line each, and nothing draws
  // outside the row.
  panelRow(id, mark, text, right, { onClick = null, onStop = null, depth = 0, muted = false, strike = false, note = null } = {}) {
    const P = this.P;
    const selected = this.dockEntry(id, onClick, onStop);
    return v_flex().id(`${id}-box`).w_full().rounded(8).when(selected, (el) => el.bg(P.raised2))
      .child(h_flex().id(id).w_full().h(30).pl(6 + depth * 14).pr(6).gap(8).items_center().rounded(8).overflow_hidden()
        .when(Boolean(onClick), (el) => el.cursor_pointer().when(!selected, (it) => it.hover((s) => s.bg(P.raised))).on_click((_e, cx) => onClick(cx)))
        .child(h_flex().w(16).flex_none().justify_center().child(mark))
        .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(muted ? P.text3 : P.text).when(strike, (el) => el.line_through()).child(typeof text === "string" ? oneLine(text) : text))
        .when(Boolean(right), (el) => el.child(div().flex_none().text_size(S.small).text_color(P.text3).child(right))))
      .when(Boolean(note), (el) => el.child(div().w_full().pl(30 + depth * 14).pr(6).pb(6).truncate().text_size(S.meta)
        .text_color(note.tone === "need" ? P.yellow : P.text3).child(oneLine(note.text))));
  }

  changesTab() {
    const P = this.P;
    const files = this.fileChanges();
    if (!files.length) return div().px(6).py(10).text_size(S.ui).text_color(P.text3).child("No file changes in this session yet.");
    const added = files.reduce((sum, file) => sum + file.added, 0);
    const removed = files.reduce((sum, file) => sum + file.removed, 0);
    const rows = [];
    for (const file of files) {
      const id = `pl-file-${file.path}`;
      const open = this.open.has(id);
      const parts = file.path.split("/");
      const name = parts.pop();
      rows.push(this.panelRow(id, icon("file-text", 13).text_color(P.text3),
        h_flex().gap(6).items_baseline().min_w(0).child(div().flex_none().child(name)).child(div().min_w(0).truncate().text_size(S.small).text_color(P.text3).child(parts.join("/"))),
        "", { onClick: (cx) => this.toggle(id, cx) }));
      rows.push(h_flex().w_full().pl(30).pb(4).gap(6).text_size(S.small)
        .child(div().text_color(P.green).child(`+${file.added}`)).child(div().text_color(P.red).child(`−${file.removed}`))
        .child(div().text_color(P.text3).child(`${file.edits.length} ${file.edits.length === 1 ? "edit" : "edits"}`)));
      if (open) {
        for (const editId of file.edits) {
          const item = this.t.byId.get(editId);
          const diff = item ? this.toolFacts(item).diff : "";
          if (diff) rows.push(div().w_full().mb(6).px(8).py(6).rounded(8).bg(P.bg).border_1().border_color(P.line).child(this.diffView(`${id}-${editId}`, diff)));
        }
      }
    }
    return this.panelSection(`${files.length} ${files.length === 1 ? "File" : "Files"} changed`, `+${added} −${removed}`, rows);
  }

  tasksTab() {
    const P = this.P;
    const todos = this.todoFacts();
    const tasks = this.backgroundTasks();
    const out = [];
    if (todos.total) {
      const rows = todos.open.map((task) => {
        const active = task.status === "in_progress";
        const blocked = task.blockers.length > 0;
        // The task in progress pulses while the agent works on it.
        const mark = active ? (this.t.working ? pulse(P.purple, 13) : dot(P.purple, 13)) : icon("circle", 13).text_color(blocked ? P.line2 : P.text3);
        return this.panelRow(`pl-todo-${task.id}`, mark, active && task.activeForm ? task.activeForm : task.subject ?? `#${task.id}`,
          blocked ? `after #${task.blockers.join(", #")}` : "", { muted: !active && blocked });
      });
      if (todos.done.length) {
        rows.push(this.panelRow("pl-todo-done", icon("circle-check", 13).text_color(P.green), `${todos.done.length} completed`, this.doneOpen ? "Hide" : "Show",
          { muted: true, onClick: (cx) => { this.doneOpen = !this.doneOpen; cx.notify(); } }));
        if (this.doneOpen) for (const task of todos.done.slice(0, 30)) rows.push(this.panelRow(`pl-todo-${task.id}`, div(), task.subject ?? `#${task.id}`, "", { muted: true, strike: true, depth: 1 }));
      }
      out.push(this.panelSection("Todos", `${todos.done.length} of ${todos.total}`, rows));
    }
    if (tasks.length) {
      // Running tasks stay in view. Finished tasks fold under one row, as
      // done todos and finished delegations do.
      const running = tasks.filter((task) => task.status === "running");
      const finished = tasks.filter((task) => task.status !== "running")
        .sort((a, b) => (b.endedAt || b.startedAt) - (a.endedAt || a.startedAt));
      const rows = running.slice(0, 12).flatMap((task) => this.taskRows(task));
      if (finished.length) {
        const failed = finished.filter(taskFailed).length;
        rows.push(this.panelRow("pl-task-done", failed ? icon("x", 13).text_color(P.red) : icon("circle-check", 13).text_color(P.green),
          `${finished.length} finished${failed ? ` · ${failed} failed` : ""}`, this.tasksDoneOpen ? "Hide" : "Show",
          { muted: true, onClick: (cx) => { this.tasksDoneOpen = !this.tasksDoneOpen; cx.notify(); } }));
        if (this.tasksDoneOpen) rows.push(...finished.slice(0, 30).flatMap((task) => this.taskRows(task, 1)));
      }
      out.push(this.panelSection("Background", `${running.length} running`, rows));
    }
    if (!out.length) out.push(div().px(6).py(10).text_size(S.ui).text_color(P.text3).child("No tasks yet. Todos and background commands show here."));
    return v_flex().w_full().gap(14).children(out);
  }

  // taskRows gives the row of a background task, and its command and output
  // when the row is open. A finished task says how it ended.
  taskRows(task, depth = 0) {
    const P = this.P;
    const id = `pl-task-${task.id}`;
    const running = task.status === "running";
    const mark = running ? pulse(P.purple, 13)
      : taskFailed(task) ? icon("x", 13).text_color(P.red)
      : task.status === "completed" ? icon("check", 13).text_color(P.green) : icon("stop", 13).text_color(P.text3);
    const time = elapsed((task.endedAt || Date.now()) - task.startedAt);
    const right = running ? `${task.kind === "monitor" ? "monitor · " : ""}${time}` : `${taskEnd(task)} · ${time}`;
    const rows = [this.panelRow(`${id}-row`, mark, task.description || commandLine(task.command), right,
      { depth, muted: !running, onClick: (cx) => this.toggle(id, cx), onStop: running ? (cx) => this.stopTask(task.id, cx) : null })];
    if (this.open.has(id)) {
      const tailText = (task.tail ?? []).join("\n");
      rows.push(v_flex().id(`${id}-detail`).w_full().mb(6).px(10).py(8).gap(6).rounded(8).bg(P.bg).border_1().border_color(P.line)
        .child(div().truncate().font_family(mono()).text_size(S.small).text_color(P.text3).child(`$ ${task.command}`))
        .when(Boolean(tailText), (el) => el.child(div().font_family(mono()).text_size(S.small).text_color(P.text2).child(tailText)))
        .child(h_flex().gap(8).items_center()
          .child(div().text_size(S.small).text_color(P.text3).child(running ? "running" : taskEnd(task)))
          .when(running, (el) => el.child(this.smallButton(`${id}-stop`, "Stop", (cx) => this.stopTask(task.id, cx))))));
    }
    return rows;
  }

  agentsTab() {
    const P = this.P;
    const facts = this.delegationFacts();
    const mark = (item) => {
      const state = item.observation?.state ?? "queued";
      // A started agent pulses while Galpon has a recent sign that it works.
      if (state === "started") return item.observation?.lease === "fresh" ? pulse(P.purple, 13) : dot(P.purple, 13);
      if (state === "waiting") return icon("alert", 13).text_color(P.yellow);
      if (state === "completed") return icon("circle-check", 13).text_color(P.green);
      if (state === "failed" || state === "expired") return icon("x", 13).text_color(P.red);
      if (state === "canceled") return icon("stop", 13).text_color(P.text3);
      return icon("circle", 13).text_color(P.text3);
    };
    const rows = facts.shown.slice(0, 40).map(({ item, depth }) => this.panelRow(`pl-work-${item.id}`, mark(item), item.title,
      ago(Date.now() - (item.updatedAt || item.createdAt || Date.now())), { depth, muted: !LIVE_WORK.has(item.observation?.state), note: this.workNote(item) }));
    if (facts.folded.length) {
      rows.push(this.panelRow("pl-work-done", icon("circle-check", 13).text_color(P.green), `${facts.roots} finished`, this.workDoneOpen ? "Hide" : "Show",
        { muted: true, onClick: (cx) => { this.workDoneOpen = !this.workDoneOpen; cx.notify(); } }));
      if (this.workDoneOpen) for (const { item, depth } of facts.folded.slice(0, 40)) rows.push(this.panelRow(`pl-work-${item.id}`, mark(item), item.title, "", { depth: depth + 1, muted: true }));
    }
    if (facts.truncated) rows.push(div().px(6).py(4).text_size(S.small).text_color(P.text3).child("More in Galpon › Operations."));
    if (!rows.length) rows.push(div().px(6).py(10).text_size(S.ui).text_color(P.text3).child("No delegated work."));
    return this.panelSection("Delegated agents", `${facts.live} active`, rows);
  }

  // --- pickers over the composer

  // pickerBottom puts a picker 6 px above the chips of the composer, which
  // stay at the bottom of its box as the text grows: the bottom space of the
  // composer (14), the hint, the gap (6), the border of the box (1), and the
  // chip row (42) less the space under its 28 px chips (7).
  pickerBottom() {
    return 14 + (this.hintH ?? 18) + 6 + 1 + 35 + 6;
  }

  modelPicker() {
    const P = this.P;
    const models = this.pickedModels();
    const current = this.model ? `${this.model.provider}/${this.model.id}` : "";
    const cursor = clamp(this.picker.cursor, 0, Math.max(0, models.length - 1));
    const visible = Math.max(6, Math.floor(((this.height || 900) * 0.55 - 110) / 32));
    const start = clamp(cursor - Math.floor(visible / 2), 0, Math.max(0, models.length - visible));
    const rows = [];
    let provider = start > 0 ? models[start - 1]?.provider : null;
    models.slice(start, start + visible).forEach((model, offset) => {
      const index = start + offset;
      if (model.provider !== provider) {
        provider = model.provider;
        rows.push(div().h(26).px(10).pt(8).text_size(S.small).font_weight(600).text_color(P.text3).child(String(provider)));
      }
      const selected = index === cursor;
      const id = `${model.provider}/${model.id}`;
      rows.push(h_flex().id(`pl-model-${index}`).w_full().h(32).px(10).gap(10).items_center().rounded(8).cursor_pointer()
        .when(selected, (el) => el.bg(P.raised2)).when(!selected, (el) => el.hover((s) => s.bg(P.raised)))
        .on_click((_e, cx) => this.choosePicked(cx, model))
        .child(h_flex().w(14).flex_none().child(id === current ? icon("check", 13).text_color(P.accent) : div()))
        .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(selected ? P.text : P.text2).child(model.name ?? model.id))
        .when(model.reasoning, (el) => el.child(icon("brain", 12).text_color(P.text3)))
        .child(div().w(46).text_right().text_size(S.small).text_color(P.text3).child(model.contextWindow ? formatTokens(model.contextWindow) : ""))
        .child(div().w(70).text_right().text_size(S.small).text_color(P.text3).child(model.cost ? `$${model.cost.input ?? 0} / $${model.cost.output ?? 0}` : "")));
    });
    return v_flex().id("pl-models").absolute().left(this.columnLeft ?? 24).bottom(this.pickerBottom()).w(clamp((this.width || 900) - 40, 340, 520))
      .p(6).rounded(12).bg(P.surface).border_1().border_color(P.line2).shadow_lg()
      .on_mouse_down("left", (_event, cx) => cx.stop_propagation())
      .child(h_flex().h(38).px(10).gap(8).items_center().border_b(1).border_color(P.line).mb(4)
        .child(icon("search", 14).text_color(P.text3))
        .child(Input.new(this.pickerInput).flex_1().h_full().text_size(S.ui).text_color(P.text)))
      .when(this.picker.loading, (el) => el.child(div().px(10).py(8).text_size(S.ui).text_color(P.text3).child("Loading models…")))
      .when(!this.picker.loading && !models.length, (el) => el.child(div().px(10).py(8).text_size(S.ui).text_color(P.text3).child("No model matches.")))
      .children(rows)
      .child(h_flex().h(28).px(10).mt(4).gap(10).items_center().border_t(1).border_color(P.line).text_size(S.small).text_color(P.text3)
        .child(`${models.length} models`).child(div().flex_1()).child("Context · $ per M tokens in / out"));
  }

  // modeChip shows the permission mode of a Claude Code or Codex agent. A
  // mode that asks less than the default has a color. A mode of the state
  // can name its color: red, yellow, blue, or none.
  modeChip(chip) {
    const P = this.P;
    const item = this.modeOf(this.mode);
    const tone = "tone" in item ? ({ red: P.red, yellow: P.yellow, blue: P.blue })[item.tone] ?? null
      : item.mode === "bypassPermissions" ? P.red : item.mode === "default" ? null : item.mode === "plan" ? P.blue : P.yellow;
    return chip("pl-mode", [icon(item.icon, 13).text_color(tone ?? P.text3), div().when(Boolean(tone), (el) => el.text_color(tone)).child(item.label), icon("chevron-down", 12)],
      (cx) => this.openModes(cx), "Permission mode · Shift+Tab");
  }

  modePicker() {
    const P = this.P;
    const cursor = clamp(this.modeMenu.cursor, 0, this.modes.length - 1);
    const left = clamp((this.columnLeft ?? 24) + 6 + (this.modeChipX()), 8, Math.max(8, (this.width || 900) - 300));
    return v_flex().id("pl-mode-menu").absolute().bottom(this.pickerBottom()).left(left).w(290)
      .p(6).gap(1).rounded(12).bg(P.surface).border_1().border_color(P.line2).shadow_lg()
      .on_mouse_down("left", (_event, cx) => cx.stop_propagation())
      .child(div().h(26).px(10).pt(6).text_size(S.small).font_weight(600).text_color(P.text3).child("Permission mode"))
      .children(this.modes.map((item, index) => {
        const selected = index === cursor;
        return h_flex().id(`pl-mode-${item.mode}`).w_full().h(44).px(10).gap(10).items_center().rounded(8).cursor_pointer()
          .when(selected, (el) => el.bg(P.raised2)).when(!selected, (el) => el.hover((s) => s.bg(P.raised)))
          .on_click((_e, cx) => this.setMode(item.mode, cx))
          .child(icon(item.icon, 14).text_color(item.mode === "bypassPermissions" ? P.red : P.text3))
          .child(v_flex().flex_1().min_w(0)
            .child(div().text_size(S.ui).text_color(selected ? P.text : P.text2).child(item.label))
            .child(div().truncate().text_size(S.small).text_color(P.text3).child(item.detail)))
          .child(item.mode === this.modeOf(this.mode)?.mode ? icon("check", 13).text_color(P.accent) : div());
      }))
      .child(h_flex().h(28).px(10).mt(4).items_center().border_t(1).border_color(P.line).text_size(S.small).text_color(P.text3)
        .child("Shift+Tab changes the mode"));
  }

  // modeChipX is where the mode chip starts in the row of the composer.
  modeChipX() {
    const model = this.model;
    const thinking = Boolean(model) && model.reasoning !== false ? (this.thinkChipW ?? 110) + 2 : 0;
    return (this.modelChipW ?? 160) + 2 + thinking;
  }

  thinkingPicker() {
    const P = this.P;
    const { list } = thinkingStep(this.thinking, this.levels);
    const cursor = clamp(this.thinkingMenu.cursor, 0, list.length - 1);
    const left = clamp((this.columnLeft ?? 24) + 6 + (this.modelChipW ?? 160), 8, Math.max(8, (this.width || 900) - 230));
    return v_flex().id("pl-thinking-menu").absolute().bottom(this.pickerBottom()).left(left).w(220)
      .p(6).gap(1).rounded(12).bg(P.surface).border_1().border_color(P.line2).shadow_lg()
      .on_mouse_down("left", (_event, cx) => cx.stop_propagation())
      .child(div().h(26).px(10).pt(6).text_size(S.small).font_weight(600).text_color(P.text3).child(this.native ? "Effort" : "Thinking"))
      .children(list.map((level, index) => {
        const selected = index === cursor;
        return h_flex().id(`pl-level-${level}`).w_full().h(32).px(10).gap(10).items_center().rounded(8).cursor_pointer()
          .when(selected, (el) => el.bg(P.raised2)).when(!selected, (el) => el.hover((s) => s.bg(P.raised)))
          .on_click((_e, cx) => this.setThinking(level, cx))
          .child(this.thinkingBars(list.length > 1 ? index / (list.length - 1) : 0))
          .child(div().flex_1().text_size(S.ui).text_color(selected ? P.text : P.text2).child(THINKING_WORDS[level] ?? level))
          .child(level === (this.thinking || "off") ? icon("check", 13).text_color(P.accent) : div());
      }));
  }
}


const DELIVERY_LABELS = { request: "Work request", question: "Question", inform: "Information", result: "Result", blocker: "Blocked" };
