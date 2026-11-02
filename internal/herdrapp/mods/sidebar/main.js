// Galpon sidebar of the Galpon layout. `galpon herdr-app install` writes this
// mod and the layout. One mod draws three blocks of the layout, which
// props().role selects:
//
//   header         the Galpon name and buttons. The block moves the window.
//   agents         each workspace with its open agents, then its other tabs
//   pull-requests  the pull requests that need someone
//
// The app gives the spaces, tabs, terminals and agent status, live.
// `galpon herdr-app sidebar` gives the Galpon names, the delegated work and
// the pull requests. It reads Galpon without a GitHub call.

import { View, div, svg } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { agents, closeOverlay, exec, focused, layout, nextCommand, openOverlay, panes, props, run, setAgentOrder, setShared, shared, spaces, terminals, theme, window as appWindow } from "herdr";
import {
  RANK, buildWorkspaces, duration, keepOrder, menuItems, menuTarget, nextItem, openAgentPanes, orderRows,
  pullRequestSections, signature, summary,
} from "./sidebar.js";

const GALPON = __GALPON__;
// The sidebar reads Galpon this often, and also when an open agent changes.
const READ_EVERY = 30000;
// While Galpon gets pull requests from GitHub, the sidebar reads more often,
// for at most a minute.
const REFRESH_EVERY = 1500;
const REFRESH_LIMIT = 60000;
const SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const ROW = 28;
const WIDTH_KEY = "galpon.sidebar.width";
// The right-click menu is a popup. It closes at once, so it gives slow work
// to the block that owns the row: it writes the request here and sends the
// command "galpon-sidebar.request".
const REQUEST = "galpon.sidebar.request";
const REQUEST_LIFE = 10000;
const MENU_WIDTH = 248;

function find(node, id) {
  if (node?.id === id) return node;
  for (const child of node?.children ?? []) {
    const found = find(child, id);
    if (found) return found;
  }
  return null;
}

// Narrower than 160 px, the sidebar is a rail of state marks.
const narrow = () => (find(layout(), "sidebar")?.size?.px ?? Infinity) < 160;

// The rail, or back to the width that the sidebar had.
function toggleSidebar() {
  const width = find(layout(), "sidebar")?.size?.px;
  if (!narrow()) setShared(WIDTH_KEY, width);
  run("layout.resize", { id: "sidebar", px: narrow() ? (shared(WIDTH_KEY) ?? 300) : appWindow()?.platform === "macos" ? 76 : 44 });
}

const message = (error) => String(error?.message ?? error);
// Galpon errors start with a lowercase letter; the sidebar shows sentences.
const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");

export default class GalponSidebar extends View {
  init(_props, cx) {
    this.role = props()?.role ?? "agents";
    if (this.role === "header") return;
    if (this.role === "menu") {
      this.items = menuItems(props()?.target);
      this.cursor = nextItem(this.items, -1, 1);
      this.handle = cx.focus_handle();
      this.handle.focus();
      return;
    }
    this.facts = null;
    this.error = "";
    this.dirty = true;
    this.readAt = 0;
    this.agentIds = [];
    this.seen = null;
    this.order = new Map();
    this.hovering = false;
    this.held = null;
    this.folded = new Set(JSON.parse(localStorage.getItem("folded") ?? "[]"));
    this.busy = "";
    this.notice = null;
    this.pendingMove = "";
    this.refreshAsked = 0;
    this.fastSince = 0;
    if (this.role === "agents") this.scan();
    // A request from before this copy started is not for it.
    this.handled = shared(REQUEST)?.seq ?? null;
    cx.spawn(async (cx) => {
      for (;;) {
        this.handle(cx);
        if (this.dirty || Date.now() - this.readAt >= this.every()) await this.read(cx);
        await cx.sleep(1000);
      }
    });
    cx.spawn(async (cx) => {
      for (;;) {
        if ((await nextCommand()) === "request") this.handle(cx);
      }
    });
  }

  // --- Menu

  // openMenu shows the menu of a row at the pointer.
  openMenu(target) {
    const items = menuItems(target);
    if (items.length === 0) return;
    const height = 42 + items.reduce((sum, item) => sum + (item.separator ? 9 : item.off || item.hint ? 46 : 30), 0);
    openOverlay({ mod: "galpon-sidebar", kind: "popup", width: MENU_WIDTH, height, props: { role: "menu", target } });
  }

  // choose runs a menu item and closes the menu.
  choose(item) {
    if (!item || item.separator || item.off) return;
    const { target, overlay } = props();
    if (item.galpon) {
      openOverlay({
        mod: "galpon", kind: "dialog", modal: true, width: 0.82, height: 0.8,
        props: { start: "action", action: item.galpon, kind: target.kind, id: target.id },
      });
    } else if (item.app) {
      run(item.app, item.args ?? {});
    } else if (item.request) {
      setShared(REQUEST, { seq: `${Date.now()}-${Math.random()}`, at: Date.now(), action: item.request, target });
      run("galpon-sidebar.request");
    }
    closeOverlay(overlay);
  }

  menuKey(event, cx) {
    if (event.key === "up") this.cursor = nextItem(this.items, this.cursor, -1);
    else if (event.key === "down" || event.key === "tab") this.cursor = nextItem(this.items, this.cursor, 1);
    else if (event.key === "enter") this.choose(this.items[this.cursor]);
    else if (event.key === "escape") closeOverlay(props().overlay);
    else return;
    cx.stop_propagation();
    cx.notify();
  }

  menu(t) {
    const target = props()?.target ?? {};
    const rows = this.items.map((item, index) => {
      if (item.separator) return div().h(9).flex_none().mx(10).border_b(1).border_color(t.border).mb(4);
      const selected = index === this.cursor;
      return v_flex().id(`galpon-menu-${item.id}`).flex_none().px(12).py(item.off || item.hint ? 5 : 0).justify_center()
        .h(item.off || item.hint ? 46 : 30)
        .when(selected, (el) => el.bg(t.selected))
        .when(!item.off, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)).on_click(() => this.choose(item)))
        .child(div().truncate().text_color(item.off ? t.dim : t.foreground).child(item.label))
        .when(Boolean(item.off || item.hint), (el) => el.child(div().truncate().text_size(11.5).text_color(item.off ? t.blocked : t.dim).child(item.off || item.hint)));
    });
    return v_flex().id("galpon-menu").track_focus(this.handle).size_full().min_h(0).py(5).bg(t.popover).text_size(13)
      .on_key_down((event, cx) => this.menuKey(event, cx))
      .child(div().h(28).flex_none().px(12).flex().items_center().truncate().text_size(11.5).font_semibold().text_color(t.dim)
        .child(target.title ?? ""))
      .children(rows);
  }

  // handle runs a request of the menu once, in the block that owns its row.
  handle(cx) {
    const request = shared(REQUEST);
    if (!request || request.seq === this.handled || Date.now() - request.at > REQUEST_LIFE) return;
    const mine = this.role === "pull-requests" ? request.target?.type === "pull-request" : request.target?.type === "agent";
    if (!mine) return;
    this.handled = request.seq;
    if (this.role === "agents" && request.action === "close") this.closeAgent(request.target, cx);
    if (this.role === "pull-requests") {
      const item = this.facts?.pullRequests?.find((candidate) => candidate.id === request.target.key);
      if (request.action === "open" && item) this.openPullRequest(item, openAgentPanes(panes(), terminals()), cx);
      if (request.action === "browse") this.browse(request.target.url, cx);
      if (request.action === "refresh") this.refresh(cx);
    }
    cx.notify();
  }

  // closeAgent closes the terminal of an idle agent through Galpon. The row
  // shows the work, and an error stays under the row.
  closeAgent(target, cx) {
    if (this.busy) return;
    this.busy = target.key;
    this.notice = null;
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["close", target.id]);
      } catch (error) {
        out = { error: message(error) };
      }
      this.busy = "";
      if (out.error) this.notice = { key: target.key, text: sentence(out.error) };
      cx.notify();
    });
  }

  // --- Galpon

  async galpon(args) {
    const result = await exec(GALPON, ["herdr-app", ...args]);
    const text = (result.stdout ?? "").trim();
    if (!text) throw new Error((result.stderr ?? "").trim() || `galpon exited with code ${result.code}`);
    return JSON.parse(text);
  }

  every() {
    const refreshing = Boolean(this.facts?.refreshing) || this.refreshAsked > 0;
    if (!refreshing) {
      this.fastSince = 0;
      return READ_EVERY;
    }
    if (!this.fastSince) this.fastSince = Date.now();
    return Date.now() - this.fastSince < REFRESH_LIMIT ? REFRESH_EVERY : READ_EVERY;
  }

  async read(cx) {
    this.dirty = false;
    this.readAt = Date.now();
    const args = this.role === "agents" ? ["agents", ...this.agentIds] : ["pull-requests"];
    try {
      const out = await this.galpon(["sidebar", ...args]);
      if (out.error) {
        this.error = sentence(out.error);
      } else {
        this.facts = out;
        this.error = "";
      }
    } catch (error) {
      this.error = sentence(message(error));
    }
    // A refresh that the person asked for ends when Galpon has the new data.
    if (this.refreshAsked && Date.now() - this.refreshAsked > 3000 && !this.facts?.refreshing) this.refreshAsked = 0;
    cx.notify();
  }

  fold(key) {
    if (this.folded.has(key)) this.folded.delete(key);
    else this.folded.add(key);
    localStorage.setItem("folded", JSON.stringify([...this.folded]));
  }

  render(cx) {
    const t = theme();
    if (this.role === "header") return this.header(t);
    if (this.role === "menu") return this.menu(t);
    if (this.role === "pull-requests") return this.pullRequests(t);
    return this.workspaces(t);
  }

  // --- Header

  button(id, child, tip, action) {
    const t = theme();
    return div().id(id).size(26).flex_none().flex().items_center().justify_center().text_color(t.muted)
      .hover((s) => s.bg(t.hover).text_color(t.foreground)).cursor_pointer()
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .tooltip(tip).on_click(action).child(child);
  }

  header(t) {
    const rail = narrow();
    const w = appWindow();
    const icon = (name) => svg(`mods/workspace-chrome/${name}.svg`).size(15);
    return h_flex().size_full().bg(t.chrome).border_b(1).border_color(t.border)
      .pl(rail ? 0 : 14).pr(rail ? 0 : 6).gap(2).items_center().text_size(13.5)
      .when(rail, (el) => el.justify_center())
      .when(w?.platform === "macos" && !rail, (el) => el.pl(80))
      .when(!rail, (el) => el
        .child(h_flex().flex_1().min_w(0).gap(8).items_center()
          .child(div().flex_none().text_color(t.accent).child("⌂"))
          .child(div().min_w(0).truncate().font_semibold().text_color(t.foreground).child("Galpon")))
        .child(this.button("galpon-command-center", icon("search"), "Command center · Ctrl+K", () => run("mod.open", { name: "galpon" })))
        .child(this.button("galpon-new-agent", icon("plus"), "New agent · Ctrl+Alt+N", () => run("mod.open", { name: "galpon-new-agent" })))
        .child(this.button("galpon-settings", icon("settings"), "Settings", () => run("app.settings"))))
      .when(!(rail && w?.platform === "macos"), (el) => el
        .child(this.button("galpon-rail", icon("sidebar"), rail ? "Open the sidebar" : "Fold the sidebar", toggleSidebar)))
      .when(Boolean(w?.buttons) && !rail, (el) => el
        .child(this.button("galpon-minimize", "−", "Minimize", () => run("window.minimize")))
        .child(this.button("galpon-maximize", "□", "Maximize", () => run("window.maximize")))
        .child(this.button("galpon-close", svg("mods/pane/close.svg").size(13), "Close", () => run("window.close"))));
  }

  // --- Workspaces and agents

  // scan reads the live app data. A Galpon agent that opens, closes or
  // changes its status makes the sidebar read Galpon again: its name, and
  // whether it waits for delegated work.
  scan() {
    const all = buildWorkspaces({
      spaces: spaces(), panes: panes(), terminals: terminals(), agents: agents(), focused: focused(), facts: this.facts,
    });
    const seen = signature(all);
    if (seen !== this.seen) {
      this.seen = seen;
      this.agentIds = [...new Set(all.flatMap((workspace) => workspace.rows.map((row) => row.agentId).filter(Boolean)))];
      this.dirty = true;
    }
    return all;
  }

  // Looking at an agent that finished turns "done" into "idle". The agent
  // keeps its place until you go to another tab, so that it does not move
  // away under the pointer.
  hold(rows) {
    const current = rows.find((row) => row.focused && row.kind === "agent");
    if (!current) {
      this.held = null;
      return;
    }
    if (this.held?.key !== current.key || current.state !== "idle") this.held = { key: current.key, state: current.state };
  }

  rank(row) {
    if (this.held?.key === row.key && row.state === "idle") return RANK[this.held.state] ?? RANK.idle;
    return RANK[row.state] ?? 0;
  }

  mark(t, row) {
    const box = div().w(14).flex_none().flex().justify_center();
    if (row.label === "working" || this.busy === row.key) return box.text_color(t.working).frames(SPINNER, 80);
    return box.text_color(t[row.tone] ?? t.dim).child(row.mark);
  }

  workspaces(t) {
    const rail = narrow();
    const now = Date.now();
    const all = this.scan();
    this.hold(all.flatMap((workspace) => workspace.rows));
    // Rows do not move while the pointer is on the list.
    const list = all.map((workspace) => {
      const rows = this.hovering
        ? keepOrder(workspace.rows, this.order.get(workspace.id))
        : orderRows(workspace.rows, (row) => this.rank(row));
      this.order.set(workspace.id, rows.map((row) => row.key));
      return { ...workspace, rows };
    });
    setAgentOrder(list.flatMap((workspace) => workspace.rows.filter((row) => row.address).map((row) => row.address)));

    const row = (r) => {
      const quiet = r.kind === "terminal" || r.state === "idle";
      let label = r.state === "idle" ? "" : r.label;
      if (r.label === "working") label = r.workingSince ? duration(now - r.workingSince) : "";
      if (this.busy === r.key) label = "closing";
      const note = this.notice?.key === r.key ? this.notice.text : "";
      const line = h_flex().id(`galpon-row-${r.key}`).h(ROW).flex_none().gap(8).items_center().cursor_pointer()
        .px(rail ? 0 : 12)
        .when(r.focused, (el) => el.bg(t.selected))
        .hover((s) => s.bg(t.hover))
        .tooltip(r.label ? `${r.title} · ${r.label}` : r.title)
        .on_click((_e, cx) => {
          this.notice = null;
          if (r.pane) run("pane.focus", { pane: r.pane });
          cx.notify();
        })
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu(menuTarget(r));
        })
        .when(rail, (el) => el.justify_center().child(this.mark(t, r)))
        .when(!rail, (el) => el
          .child(div().w(8).flex_none())
          .child(this.mark(t, r))
          .child(div().flex_1().min_w(0).truncate().text_color(quiet && !r.focused ? t.muted : t.foreground).child(r.title))
          .when(Boolean(label), (el) => el.child(div().flex_none().text_size(11.5).text_color(t[r.tone] ?? t.dim).child(label))));
      if (!note || rail) return line;
      return v_flex().flex_none().child(line).child(div().pl(42).pr(12).pb(4).text_size(11.5).text_color(t.blocked).child(note));
    };

    const header = (w) => {
      const open = !this.folded.has(w.id);
      const counts = summary(w.rows);
      if (rail) return div().h(9).flex_none().mx(8).border_b(1).border_color(t.border);
      const target = w.workspaceId
        ? { type: "workspace", kind: "workspace", id: w.workspaceId, title: w.title }
        : { type: "space", spaceId: w.id, title: w.title };
      return h_flex().id(`galpon-space-${w.id}`).h(30).flex_none().px(10).gap(6).items_center().cursor_pointer()
        .text_size(12)
        .hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => {
          this.fold(w.id);
          cx.notify();
        })
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu(target);
        })
        .child(div().w(10).flex_none().text_color(t.dim).child(open ? "▾" : "▸"))
        .child(div().flex_1().min_w(0).truncate().font_semibold().text_color(w.active ? t.foreground : t.muted).child(w.title))
        .when(Boolean(counts.text), (el) => el.child(div().flex_none().text_size(11.5).text_color(t[counts.tone] ?? t.dim).child(counts.text)))
        .when(!open && !counts.text, (el) => el.child(div().flex_none().text_size(11.5).text_color(t.dim).child(String(w.rows.length))));
    };

    const rows = list.flatMap((w) => [header(w), ...(this.folded.has(w.id) && !rail ? [] : w.rows.map(row))]);
    return v_flex().id("galpon-agents").size_full().min_h(0).bg(t.chrome).text_size(13)
      .on_hover((on, cx) => {
        this.hovering = on;
        if (!on) cx.notify();
      })
      .when(!rail, (el) => el.child(h_flex().h(30).flex_none().px(14).items_center()
        .text_size(11).font_semibold().text_color(t.dim).child("WORKSPACES")))
      .when(list.length === 0 && !rail, (el) => el.child(div().px(14).py(4).text_size(12).text_color(t.dim)
        .child("No agent is open. Ctrl+K opens the command center, and Ctrl+Alt+N starts a new agent.")))
      .child(v_flex().id("galpon-agents-list").flex_1().min_h(0).overflow_y_scrollbar().children(rows))
      .when(Boolean(this.error) && !rail, (el) => el.child(div().flex_none().px(14).py(6).text_size(11.5).text_color(t.dim)
        .child(this.error)));
  }

  // --- Pull requests

  browse(url, cx) {
    if (!url) return;
    const opener = appWindow()?.platform === "macos" ? "open" : "xdg-open";
    cx.spawn(async (cx) => {
      try {
        await exec(opener, [url]);
      } catch (error) {
        this.notice = { key: "", text: sentence(message(error)) };
        cx.notify();
      }
    });
  }

  // A click opens the agent of the pull request: its tab when it is open in
  // the app, else Galpon opens it. A pull request without an agent opens on
  // GitHub. An idle agent that is open in Herdr moves after a second click,
  // because its process restarts.
  openPullRequest(item, open, cx) {
    if (!item.agentId) return this.browse(item.url, cx);
    const pane = open.get(item.agentId);
    if (pane) {
      this.notice = null;
      run("pane.focus", { pane });
      return;
    }
    if (this.busy) return;
    const move = this.pendingMove === item.agentId;
    this.busy = item.id;
    this.notice = null;
    cx.notify();
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["open", item.agentId, ...(move ? ["--move"] : [])]);
      } catch (error) {
        out = { error: message(error) };
      }
      this.busy = "";
      this.pendingMove = out.code === "open-elsewhere" ? item.agentId : "";
      if (out.code === "open-elsewhere") this.notice = { key: item.id, text: `${sentence(out.error)}. Click again to move it.` };
      else if (out.error) this.notice = { key: item.id, text: sentence(out.error) };
      cx.notify();
    });
  }

  refresh(cx) {
    this.refreshAsked = Date.now();
    this.notice = null;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        const out = await this.galpon(["refresh"]);
        if (out.error) this.notice = { key: "", text: sentence(out.error) };
      } catch (error) {
        this.notice = { key: "", text: sentence(message(error)) };
      }
      this.dirty = true;
      cx.notify();
    });
  }

  pullRequests(t) {
    const rail = narrow();
    const facts = this.facts;
    const sections = pullRequestSections(facts);
    const open = openAgentPanes(panes(), terminals());
    const attention = sections.filter((section) => section.group <= 1).reduce((count, section) => count + section.items.length, 0);
    const refreshing = Boolean(facts?.refreshing) || this.refreshAsked > 0;
    const age = facts?.pullRequestsAt ? duration(Date.now() - facts.pullRequestsAt) : "";

    const mark = (item) => {
      const box = div().w(14).flex_none().flex().justify_center();
      if (this.busy === item.id) return box.text_color(t.working).frames(SPINNER, 80);
      return box.text_color(item.color || t.dim).child(item.mark || "○");
    };
    const row = (item) => {
      const note = this.notice?.key === item.id ? this.notice.text : "";
      const detail = [item.ref, item.label, item.agentTitle].filter(Boolean).join(" · ");
      const base = v_flex().id(`galpon-pr-${item.id}`).flex_none().cursor_pointer()
        .hover((s) => s.bg(t.hover))
        .tooltip(item.agentId ? `${item.ref} · click opens ${item.agentTitle}` : `${item.ref} · click opens GitHub`)
        .on_click((_e, cx) => this.openPullRequest(item, open, cx))
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu({
            type: "pull-request", key: item.id, title: `${item.ref} · ${item.title}`, url: item.url,
            agentId: item.agentId ?? "", agentTitle: item.agentTitle ?? "",
          });
        });
      if (rail) return base.h(ROW).items_center().justify_center().child(mark(item));
      return base.px(12).py(5).gap(1)
        .child(h_flex().gap(8).items_center()
          .child(div().w(8).flex_none())
          .child(mark(item))
          .child(div().flex_1().min_w(0).truncate().text_color(t.foreground).child(item.title)))
        .child(div().pl(38).truncate().text_size(11.5).text_color(t.dim).child(detail))
        .when(Boolean(note), (el) => el.child(div().pl(38).text_size(11.5).text_color(t.blocked).child(note)));
    };
    const header = (section) => {
      const key = `pr:${section.group}`;
      const open = !this.folded.has(key);
      if (rail) return div().h(9).flex_none().mx(8).border_b(1).border_color(t.border);
      return h_flex().id(`galpon-pr-group-${section.group}`).h(28).flex_none().px(10).gap(6).items_center().cursor_pointer()
        .text_size(11).hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => {
          this.fold(key);
          cx.notify();
        })
        .child(div().w(10).flex_none().text_color(t.dim).child(open ? "▾" : "▸"))
        .child(div().flex_1().min_w(0).truncate().font_semibold().text_color(t.muted).child(section.title))
        .child(div().flex_none().text_color(t.dim).child(String(section.items.length)));
    };

    let empty = "";
    if (!facts && !this.error) empty = "Reading Galpon…";
    else if (facts && !facts.github) empty = "No Galpon repository has a GitHub remote.";
    else if (facts && sections.length === 0) empty = facts.pullRequestsAt ? "No pull request needs anyone." : "No GitHub data yet. The refresh button gets it.";
    const problem = this.notice?.key === "" ? this.notice.text : this.error || facts?.pullRequestError || "";
    const rows = sections.flatMap((section) => [header(section), ...(this.folded.has(`pr:${section.group}`) && !rail ? [] : section.items.map(row))]);

    return v_flex().id("galpon-pull-requests").size_full().min_h(0).bg(t.chrome).text_size(13)
      .when(!rail, (el) => el.child(h_flex().h(30).flex_none().pl(14).pr(6).gap(6).items_center().text_size(11)
        .child(div().font_semibold().text_color(t.dim).child("PULL REQUESTS"))
        .when(attention > 0, (el) => el.child(div().text_color(t.blocked).child(String(attention))))
        .child(div().flex_1())
        .when(Boolean(age) && !refreshing, (el) => el.child(div().text_color(t.dim).child(age)))
        .child(div().id("galpon-pr-refresh").size(24).flex_none().flex().items_center().justify_center().cursor_pointer()
          .text_size(13).text_color(t.muted).hover((s) => s.bg(t.hover).text_color(t.foreground))
          .tooltip(refreshing ? "Galpon gets the pull requests from GitHub" : "Get the pull requests from GitHub now")
          .on_click((_e, cx) => !refreshing && this.refresh(cx))
          .when(refreshing, (el) => el.text_color(t.working).frames(SPINNER, 80))
          .when(!refreshing, (el) => el.child("↻")))))
      .when(Boolean(problem) && !rail, (el) => el.child(div().flex_none().px(14).pb(6).text_size(11.5).text_color(t.blocked).child(problem)))
      .when(Boolean(empty) && !rail, (el) => el.child(div().px(14).py(4).text_size(12).text_color(t.dim).child(empty)))
      .child(v_flex().id("galpon-pull-requests-list").flex_1().min_h(0).overflow_y_scrollbar().children(rows));
  }
}
