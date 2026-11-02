// Galpon command center. `galpon herdr-app install` writes this mod. Run that
// command again after the galpon executable moves.
//
// The mod reads Galpon through the galpon command: `herdr-app view` gives the
// rows, `herdr-app inspect` the fields of the selected row, and the other
// commands run the actions. It looks like the terminal command center: a flat
// prompt band, result rows with a selection band, an inspector and a blue
// statusline, in the Galpon palette.

import { View, div } from "gpui-kit";
import { h_flex, v_flex, InputState, Input, v_virtual_list, VirtualListScrollHandle, Scrollbar } from "gpui-base";
import { closeOverlay, exec, openOverlay, props, run, spaces, window as appWindow } from "herdr";
import {
  GROUP_TITLES, VIEWS, VIEW_ICONS, VIEW_LABELS,
  buildRows, expansionKey, normalize, pullRequestAgent, rowWorkspace, selectable,
} from "./model.js";

const GALPON = __GALPON__;

// The terminal palette (Tokyo Night Moon) until Galpon sends its own.
const DEFAULT_PALETTE = {
  background: "#222436", surface: "#1e2030", "surface-raised": "#2f334d", prompt: "#2d3149",
  selection: "#2d3f76", border: "#589ed7", foreground: "#c8d3f5", muted: "#9aa5ce", comment: "#828bb8",
  status: "#7aa2f7", "status-ink": "#222436", blue: "#82aaff", cyan: "#65bcff", purple: "#c099ff",
  green: "#c3e88d", orange: "#ff966c", red: "#ff757f", yellow: "#ffc777", teal: "#4fd6be",
};

const POLL = 2000;
const ROW = 30;
const HEADING = 34;
const SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const CONTEXT = "GalponCommandCenter";
// The search field keeps the keyboard, so the command center binds its keys
// in its own context. They win over the app's global keys.
const KEYS = [
  ["up", "up"], ["ctrl-p", "up"], ["down", "down"], ["pageup", "page-up"], ["pagedown", "page-down"],
  ["tab", "expand"], ["shift-tab", "next-view"], ["alt-p", "pull-requests"], ["ctrl-g", "detail"],
  ["ctrl-r", "reorder"], ["ctrl-o", "github"], ["ctrl-n", "new-agent"], ["ctrl-f", "fork-agent"],
  ["ctrl-s", "new-repository"], ["ctrl-h", "hidden"], ["escape", "escape"],
];
let keysBound = false;

const message = (error) => String(error?.message ?? error);

export default class Galpon extends View {
  init(_props, cx) {
    this.data = null;
    this.error = "";
    this.status = null;
    this.busy = "";
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
    this.handle = cx.focus_handle();
    this.scroller = VirtualListScrollHandle.new();
    this.input = InputState.new({ placeholder: "Search agents, workspaces, worktrees and repositories" });
    this.input.on("change", (_e, cx) => this.search(cx));
    this.input.on("submit", (_e, cx) => this.activate(cx));
    if (!keysBound) {
      try {
        cx.bind_keys(KEYS.map(([keystroke, action]) => ({ keystroke, action: `galpon::${action}`, context: CONTEXT })));
        keysBound = true;
      } catch (error) {
        this.status = { text: `Galpon keys: ${message(error)}`, tone: "error" };
      }
    }
    // The overlay takes the keyboard after its first frames.
    this.input.focus();
    cx.timer.after(120, () => this.input.focus());
    cx.timer.after(600, () => this.input.focus());
    this.load(cx);
    cx.timer.every(POLL, (cx) => this.load(cx));
  }

  // --- data

  async galpon(args) {
    const result = await exec(GALPON, ["herdr-app", ...args]);
    const text = (result.stdout ?? "").trim();
    if (!text) throw new Error((result.stderr ?? "").trim() || `galpon exited with code ${result.code}`);
    return JSON.parse(text);
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
      this.inspectSelected(cx);
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

  // --- navigation

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

  command(action, cx) {
    switch (action) {
      case "up": return this.move(-1, cx);
      case "down": return this.move(1, cx);
      case "page-up": return this.move(-10, cx);
      case "page-down": return this.move(10, cx);
      case "expand": return this.expand(cx);
      case "next-view": return this.setView(VIEWS[(VIEWS.indexOf(this.view) + 1) % VIEWS.length], cx);
      case "pull-requests": return this.setView(this.view === "pull-request" ? this.previousView : "pull-request", cx);
      case "detail":
        this.detail = !this.detail;
        return;
      case "reorder": return this.reorder(cx);
      case "github": return this.openOnGitHub(cx);
      case "new-agent": return this.route("new-agent", cx);
      case "fork-agent": return this.route("fork-agent", cx);
      case "new-repository": return this.route("new-repository", cx);
      case "hidden":
        this.hidden = !this.hidden;
        this.say(this.hidden ? "Showing hidden resources" : "Hidden resources are no longer shown", "info");
        this.fields.clear();
        return this.load(cx);
      case "escape":
        if (this.detail) this.detail = false;
        else this.close();
        return;
    }
  }

  click(key, cx) {
    this.input.focus();
    const index = this.rows.findIndex((row) => row.key === key);
    if (index < 0 || !selectable(this.rows[index])) return;
    if (index === this.cursor) this.activate(cx);
    else this.select(index, cx);
    cx.notify();
  }

  // --- actions

  close() {
    closeOverlay(props().overlay);
  }

  say(text, tone) {
    this.status = { text, tone };
  }

  activate(cx) {
    const row = this.rows[this.cursor];
    if (!row || this.busy) return;
    if (row.type === "more") {
      this.expand(cx);
      return cx.notify();
    }
    const item = row.item;
    if (item.hidden) {
      this.say(`${item.title} is hidden`, "warn");
      return cx.notify();
    }
    switch (item.kind) {
      case "agent":
        return this.openAgent(item, cx);
      case "pull-request": {
        const agent = pullRequestAgent(this.data, item);
        if (agent) return this.openAgent(agent, cx);
        return this.browse(item.url, cx);
      }
      case "workspace":
        return this.openWorkspace(item, cx);
      case "worktree":
        return this.act(cx, `Opening a terminal in ${item.title}`, ["terminal", item.id]);
      case "repository":
        this.say("Run galpon in a terminal to change a repository or add a remote.", "info");
        return cx.notify();
    }
  }

  // An agent opens in the app. An idle agent that runs in Herdr moves after
  // a second Enter, because its process restarts.
  openAgent(item, cx) {
    const move = this.pendingMove === item.id;
    const label = move ? `Moving ${item.title} to the Herdr app` : `Opening ${item.title}`;
    this.act(cx, label, ["open", item.id, ...(move ? ["--move"] : [])], (out) => {
      if (out.code !== "open-elsewhere") return false;
      this.pendingMove = item.id;
      this.say(`${out.error}. Press enter again to move it.`, "warn");
      return true;
    });
  }

  openWorkspace(item, cx) {
    const space = `galpon-${item.id}`;
    if (spaces().some((candidate) => candidate.id === space)) {
      run("space.select", { id: space });
      return this.close();
    }
    this.expand(cx);
    this.say(`No agent of ${item.title} is open in the app yet.`, "info");
    cx.notify();
  }

  // act runs a galpon command. The command center closes when it succeeds.
  // handled(out) returns true for a result that it shows itself.
  act(cx, label, args, handled) {
    this.busy = label;
    this.status = null;
    cx.notify();
    cx.spawn(async (cx) => {
      let done = false;
      try {
        const out = await this.galpon(args);
        if (handled?.(out)) {
          // Shown already.
        } else if (out.error) {
          this.say(out.error, "error");
        } else {
          done = true;
        }
      } catch (error) {
        this.say(message(error), "error");
      }
      this.busy = "";
      if (done) this.close();
      else cx.notify();
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

  openOnGitHub(cx) {
    const row = this.rows[this.cursor];
    if (row?.type === "item" && row.item.kind === "pull-request") this.browse(row.item.url, cx);
  }

  reorder(cx) {
    this.order = new Map();
    this.refreshRows();
    if (this.view === "pull-request" && !normalize(this.query)) {
      cx.spawn(async (cx) => {
        try {
          const out = await this.galpon(["refresh"]);
          if (out.error) this.say(out.error, "error");
        } catch (error) {
          this.say(message(error), "error");
        }
        this.load(cx);
      });
      return;
    }
    this.load(cx);
  }

  // The forms are terminal screens for now: the galpon-terminal mod shows
  // them in a dialog.
  route(name, cx) {
    const row = this.rows[this.cursor];
    const args = ["herdr-app", "route", name];
    if (name === "new-agent") {
      const workspace = rowWorkspace(row);
      if (!workspace) {
        this.say("Select an item that has a workspace", "warn");
        return cx.notify();
      }
      args.push("--workspace", workspace);
      if (row.item.kind === "agent") args.push("--agent", row.item.id);
    }
    if (name === "fork-agent") {
      if (row?.type !== "item" || row.item.kind !== "agent") {
        this.say("Select an agent to fork", "warn");
        return cx.notify();
      }
      args.push("--workspace", row.item.workspaceId, "--agent", row.item.id);
    }
    openOverlay({ mod: "galpon-terminal", props: { args }, kind: "popup", width: 2, height: 2, frame: "none" });
    this.close();
  }

  // --- view

  render(_cx) {
    const P = { ...DEFAULT_PALETTE, ...(this.data?.palette ?? {}) };
    const row = this.rows[this.cursor];
    const wide = this.width === 0 || this.width >= 760;
    let root = v_flex().id("galpon").track_focus(this.handle).key_context(CONTEXT).size_full()
      .bg(P.background).text_color(P.foreground).text_size(13)
      .on_size_change((size, cx) => {
        if (Math.abs(size.width - this.width) < 1) return;
        this.width = size.width;
        cx.notify();
      });
    for (const [, action] of KEYS) {
      root = root.on_action(`galpon::${action}`, (_e, cx) => {
        this.command(action, cx);
        cx.notify();
      });
    }
    return root
      .child(this.header(P))
      .child(this.prompt(P))
      .child(this.detail ? this.inspector(P, row) : this.body(P, row, wide))
      .child(this.notice(P))
      .child(this.statusline(P));
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
    const inspectorWidth = Math.round(Math.min(480, Math.max(300, this.width * 0.36)));
    return h_flex().flex_1().min_h(0)
      .child(list)
      .child(div().w(1).h_full().flex_none().bg(P["surface-raised"]))
      .child(v_flex().w(inspectorWidth).flex_none().h_full().child(this.inspector(P, row)));
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
    if (item.state === "working") return div().w(12).flex_none().text_color(item.color || P.yellow).frames(SPINNER, 90);
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
    const section = (text) => h_flex().gap(6).text_size(11).font_semibold().text_color(P.muted)
      .child(div().text_color(P.status).child("▰"))
      .child(div().child(text));
    let pane = v_flex().id("galpon-inspector").size_full().overflow_y_scroll().px(18).py(14).gap(5);
    if (!row || row.type === "header") {
      pane = pane.child(section("SELECTED ITEM")).child(div().text_color(P.muted).child("Select a row to inspect it."));
    } else if (row.type === "more") {
      pane = pane.child(section("DETAIL"))
        .child(div().text_size(16).font_semibold().child(row.title))
        .child(div().text_color(P.muted).child(row.detail));
    } else {
      const item = row.item;
      const known = this.fields.get(`${item.kind}:${item.id}`);
      pane = pane.child(section("DETAIL")).child(div().pt(2).text_size(16).font_semibold().child(item.title));
      if (item.kind === "agent" || item.kind === "pull-request") {
        pane = pane.child(h_flex().gap(6).items_center().pb(6)
          .child(this.stateMark(P, item))
          .child(div().text_color(item.color || P.muted).child(item.label ?? "")));
      }
      for (const field of known?.fields ?? []) {
        if (field.section) {
          pane = pane.child(section(field.value).mt(12));
          continue;
        }
        pane = pane.child(h_flex().gap(12).items_start().when(Boolean(field.break), (it) => it.mt(10))
          .child(div().w(96).flex_none().pt(1).text_size(11).font_semibold().text_color(P.muted).child(field.label ?? ""))
          .child(div().flex_1().min_w(0).text_color(field.color || P.foreground).child(field.value)));
      }
      if (!known) pane = pane.child(div().text_color(P.muted).child("Loading…"));
      if (known?.error) pane = pane.child(div().pt(6).text_color(P.red).child(known.error));
    }
    return v_flex().relative().size_full()
      .child(pane)
      .child(Scrollbar.vertical("galpon-inspector").absolute().inset_0());
  }

  notice(P) {
    let text = this.view === "agent"
      ? "Attention first · alt+p pull requests · ctrl+r reorder · tab expand"
      : "alt+p pull requests · tab expand · ctrl+r reorder";
    let color = P.muted;
    if (this.view === "pull-request" && this.data?.pullRequests) text = this.data.pullRequests.notice;
    if (normalize(this.query)) text = "Search includes agents, workspaces, worktrees and repositories.";
    if (this.detail) text = "ctrl+g or esc returns to the list";
    if (this.status) {
      text = this.status.text;
      color = this.status.tone === "error" ? P.red : this.status.tone === "warn" ? P.yellow : P.muted;
    }
    if (this.busy) {
      text = this.busy;
      color = P.yellow;
    }
    if (this.error) {
      text = `Galpon: ${this.error}`;
      color = P.red;
    }
    return h_flex().flex_none().h(30).px(18).gap(8).items_center().text_size(12).text_color(color)
      .border_t(1).border_color(P["surface-raised"])
      .when(Boolean(this.busy), (el) => el.child(div().w(12).flex_none().frames(SPINNER, 90)))
      .child(div().flex_1().min_w(0).truncate().child(text));
  }

  statusline(P) {
    const searching = Boolean(normalize(this.query));
    const mode = this.detail ? "DETAIL" : searching ? "SEARCH" : GROUP_TITLES[this.view];
    const pulls = this.view === "pull-request" && !searching;
    const hints = pulls
      ? [["enter", "open agent"], ["ctrl+o", "open on GitHub"], ["ctrl+r", "refresh"], ["ctrl+g", "detail"], ["alt+p", "back"], ["esc", "close"]]
      : [["enter", "open"], ["tab", "expand"], ["shift+tab", "views"], ["ctrl+n", "new agent"], ["ctrl+f", "fork"], ["ctrl+g", "detail"], ["esc", "close"]];
    const selectableRows = this.rows.filter(selectable);
    const position = selectableRows.length ? `${selectableRows.indexOf(this.rows[this.cursor]) + 1} / ${selectableRows.length}` : "0 / 0";
    return h_flex().flex_none().h(28).items_center().text_size(12)
      .child(h_flex().h_full().px(14).items_center().bg(P.status).text_color(P["status-ink"]).font_bold().child(mode))
      .child(h_flex().h_full().flex_1().min_w(0).px(14).gap(14).items_center().overflow_hidden().bg(P["surface-raised"])
        .children(hints.map(([key, label]) => h_flex().flex_none().gap(5)
          .child(div().font_semibold().text_color(P.foreground).child(key))
          .child(div().text_color(P.muted).child(label)))))
      .child(h_flex().h_full().px(14).items_center().bg(P.prompt).text_color(P.muted).child(position));
  }
}
