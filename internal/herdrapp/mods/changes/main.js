// The Changes view of Galpon: the Git work of one agent, in a pane beside
// its agent tab. `galpon herdr-app install` writes this mod.
//
// The view reads Galpon through the galpon command: `herdr-app changes work`
// gives the checkouts of the placement, their branches and commits;
// `herdr-app changes diff` gives the diff of a change set as rows that Go
// made ready to draw; `herdr-app changes lines` gives the lines of a fold;
// `herdr-app changes mark` saves viewed marks; and `herdr-app changes
// fingerprint` tells when a worktree changed. JavaScript only lays out and
// paints the rows that show, so a large diff stays inside the render time of
// Herdr. The view only reads the work. To change code, the person writes
// comments, and the view puts them in the composer of the agent tab.
//
// props { agent, tab, file }: the agent, the tab of the pane, and a file to
// show first. The command "galpon-changes.key:<keys>" does what the keys do.

import { View, div } from "gpui-kit";
import { h_flex, v_flex, v_virtual_list, Scrollbar, VirtualListScrollHandle, InputState, Input } from "gpui-base";
import { exec, focused, nextCommand, notify, panes, props, run, setShared, settings, shared, terminals, window as appWindow } from "herdr";
import { MONO, S, SANS, icon, kbd, keepSelection, mix, palette, pulse, spinner } from "./look.js";
import { deleteWord } from "./words.js";
import {
  EMPHASIS, FLAG, H, ago, anchorIndex, baseFor, branchFacts, branchLabel, buildItems, changeStart, commentExcerpt, composeReview,
  fileTree, findBranch, fuzzy, isStop, jumpEntries, nextIndex, rangeSet, rowText, setFor, setLabel, shortSha, sliceSegs,
  statusLetter, statusWord, workGroups,
} from "./model.js";

const GALPON = __GALPON__;
// HOST is the herdr-core machine name of the computer that runs Galpon, or
// null when Galpon runs on this computer.
const HOST = __HOST__;

// REVIEW is the shared value that gives review comments to the agent tab,
// and FOCUS the one that shows a file in an open Changes pane.
const REVIEW = "galpon.changes.review";
const FOCUS = "galpon.changes.focus";
const CONTEXT = "GalponChanges";
const POLL_WORKING = 2000;
const POLL_IDLE = 8000;
const WIDE = 1000;
const TREE_W = 260;
const SPLIT_WIDE = 880;
const CODE = 12;
// MAX_SIZES is the most item heights that a virtual list takes. A longer
// list gives every item one height.
const MAX_SIZES = 10000;
const MENU_ROW = 30;

const zoom = () => Number(settings()?.zoom) || 1;
const message = (error) => String(error?.message ?? error ?? "unknown error");

const STATE_WORDS = { live: "Live", review: "In review", local: "Local", unfinished: "Unfinished", merged: "Merged", closed: "Closed" };
const CLASS_WORDS = {
  generated: "Generated", vendored: "Vendored", lockfile: "Lockfile", binary: "Binary", large: "Large", format: "Format only",
  moved: "Moved code", uncommitted: "Uncommitted", untracked: "Untracked", staged: "Staged", submodule: "Submodule",
};

export default class GalponChanges extends View {
  init(_props, cx) {
    const p = props() ?? {};
    this.agentId = typeof p.agent === "string" ? p.agent : "";
    this.tabId = typeof p.tab === "string" ? p.tab : "";
    this.pendingFile = typeof p.file === "string" ? p.file : "";
    this.handle = cx.focus_handle();
    this.scroller = VirtualListScrollHandle.new();
    this.treeScroller = VirtualListScrollHandle.new();
    this.menuScroller = VirtualListScrollHandle.new();
    this.jumpInput = InputState.new({ placeholder: "Jump to a file or a function" });
    this.jumpInput.on("change", (_e, cx) => {
      if (this.popover?.kind === "jump") this.popover.cursor = 0;
      cx.notify();
    });
    this.jumpInput.on("submit", (_e, cx) => this.chooseMenu(cx));
    this.commentInput = InputState.new({ placeholder: "Write a comment for the agent. Enter saves it, Esc cancels." });
    this.commentInput.on("submit", (_e, cx) => this.saveComment(cx));
    this.work = null;
    this.diff = null;
    this.error = "";
    this.loading = "";
    this.sel = null;
    this.onlyNew = false;
    this.view = { collapsed: new Set(), expanded: new Set(), noise: new Set(), comments: [], oneFile: null };
    this.layout = { split: false, wrap: true, tree: true };
    this.built = { items: [], sizes: [], index: new Map() };
    this.cursor = -1;
    this.anchor = null;
    this.popover = null;
    this.commenting = null;
    this.flash = null;
    this.width = 0;
    this.height = 0;
    this.visible = { start: 0, end: 0 };
    this.fingerprints = {};
    this.lastPoll = 0;
    this.closed = false;
    this.focusSeen = Number(shared(FOCUS)?.at ?? 0);
    this.restore();
    if (!this.agentId) {
      this.error = "This pane names no Galpon agent. Open the Changes view from an agent.";
      return;
    }
    cx.spawn(async (cx) => {
      await this.load(cx, { first: true });
    });
    cx.timer.after(120, () => this.handle.focus());
    cx.timer.every(1000, (cx) => this.tick(cx));
    cx.spawn(async (cx) => {
      for (;;) {
        const command = await nextCommand();
        if (this.closed) return;
        if (command === "focus") this.handle.focus();
        // "key:<keys>" does what the keys do:
        // `herdr-app do galpon-changes.key:shift-r`.
        else if (command.startsWith("key:")) this.keyDown({ keystroke: command.slice(4), key: command.slice(4) }, cx);
        cx.notify();
      }
    });
  }

  async close() {
    this.closed = true;
    this.save();
  }

  // --- saved choices: the selection, the layout and the draft comments of
  // this agent stay in the app for the next pane.

  restore() {
    try {
      const saved = JSON.parse(localStorage.getItem(`changes:${this.agentId}`) ?? "null");
      if (!saved) return;
      if (saved.sel) this.sel = saved.sel;
      if (saved.layout) Object.assign(this.layout, saved.layout);
      if (Array.isArray(saved.comments)) this.view.comments = saved.comments;
      this.onlyNew = Boolean(saved.onlyNew);
    } catch {
      // A new pane starts with the default selection.
    }
  }

  save() {
    if (!this.agentId) return;
    try {
      localStorage.setItem(`changes:${this.agentId}`, JSON.stringify({ sel: this.sel, layout: this.layout, comments: this.view.comments, onlyNew: this.onlyNew }));
    } catch {
      // The choices last until the window closes.
    }
  }

  // --- data

  async galpon(args, timeout = 60000) {
    const result = HOST
      ? await exec(GALPON, ["herdr-app", "--screen", ...args], { on: HOST, timeout })
      : await exec(GALPON, ["herdr-app", ...args], { timeout });
    const text = (result.stdout ?? "").trim();
    if (!text) throw new Error((result.stderr ?? "").trim() || `galpon exited with code ${result.code}`);
    const out = JSON.parse(text);
    if (out.error) throw new Error(out.error);
    return out;
  }

  // load reads the work, keeps or picks the selection, and reads its diff.
  // prints are the fingerprints that the poll read before: they go with the
  // diff, so a change after the poll starts another load.
  async load(cx, { first = false, prints = null } = {}) {
    this.loading = this.work ? "Updating" : "Reading the work of the agent";
    cx.notify();
    try {
      this.work = await this.galpon(["changes", "work", this.agentId]);
      this.agentTitle = this.work.agentTitle ?? "";
      const found = this.sel && findBranch(this.work, this.sel.checkout, this.sel.branch);
      const live = found?.checkout?.branches?.find((branch) => branch.live);
      if (!found?.branch) this.sel = this.work.default ? { ...this.work.default, follow: true } : null;
      else if (this.sel.follow && live && live.key !== this.sel.branch) {
        // The checkout shows another branch now: the view follows the work.
        this.sel = { checkout: this.sel.checkout, branch: live.key, set: setFor(live), follow: true };
        this.say(`${found.repository.title} shows ${branchLabel(live)} now.`, "info");
      } else if (first && this.sel.set !== "uncommitted" && !this.sel.set.startsWith("commit:") && !this.sel.set.startsWith("range:")) this.sel.set = setFor(found.branch);
      this.error = "";
    } catch (error) {
      this.error = message(error);
      this.loading = "";
      cx.notify();
      return;
    }
    await this.loadDiff(cx, { prints: prints ?? (await this.readFingerprints()) });
  }

  selected() {
    if (!this.sel || !this.work) return null;
    return findBranch(this.work, this.sel.checkout, this.sel.branch);
  }

  diffArgs(extra = []) {
    const found = this.selected();
    const args = ["changes", "diff", this.agentId, "--checkout", this.sel.checkout, "--set", this.sel.set];
    const base = baseFor(this.sel.set, found?.branch);
    if (base) args.push("--base", base);
    if (this.onlyNew) args.push("--only-new");
    return args.concat(extra);
  }

  async loadDiff(cx, { keep = true, prints = null } = {}) {
    if (!this.sel) {
      this.diff = null;
      this.loading = "";
      this.rebuild();
      cx.notify();
      return;
    }
    const anchor = keep ? this.anchorKey() : null;
    this.loading = this.diff ? "Updating" : "Reading the diff";
    cx.notify();
    try {
      const diff = await this.galpon(this.diffArgs());
      // Files that the person opened keep their rows.
      for (const file of diff.files ?? []) {
        const before = this.diff?.files?.find((old) => old.path === file.path && old.loaded);
        if (file.deferred && before && before.content === file.content) Object.assign(file, { rows: before.rows, deferred: false, loaded: true });
      }
      this.diff = diff;
      this.error = "";
      if (prints) this.fingerprints = prints;
    } catch (error) {
      this.error = message(error);
    }
    this.loading = "";
    this.rebuild(anchor);
    this.save();
    if (this.pendingFile) {
      this.jumpToFile(this.pendingFile);
      this.pendingFile = "";
    }
    cx.notify();
  }

  checkoutPaths() {
    const out = [];
    for (const repository of this.work?.repositories ?? []) {
      for (const checkout of repository.checkouts ?? []) {
        if (!checkout.missing) out.push(checkout.path);
      }
    }
    return out;
  }

  async readFingerprints() {
    const paths = this.checkoutPaths();
    if (!paths.length) return {};
    try {
      return (await this.galpon(["changes", "fingerprint", ...paths], 20000)).fingerprints ?? {};
    } catch {
      return {};
    }
  }

  // tick polls the worktrees while the pane shows: often while the agent
  // works, less often while it waits. A change reads the work and the diff
  // again and keeps the place of the person.
  tick(cx) {
    if (this.flash && this.flash.until < Date.now()) {
      this.flash = null;
      cx.notify();
    }
    const asked = shared(FOCUS);
    if (asked && asked.agent === this.agentId && Number(asked.at) > this.focusSeen) {
      this.focusSeen = Number(asked.at);
      if (asked.file) {
        if (this.diff) this.jumpToFile(asked.file);
        else this.pendingFile = asked.file;
      }
      this.handle.focus();
      cx.notify();
    }
    if (!this.shown() || this.loading || this.busy || !this.work) return;
    const period = this.agentWorking() ? POLL_WORKING : POLL_IDLE;
    if (Date.now() - this.lastPoll < period) return;
    this.lastPoll = Date.now();
    this.busy = true;
    cx.spawn(async (cx) => {
      try {
        const now = await this.readFingerprints();
        const changed = Object.keys(now).some((path) => now[path] !== this.fingerprints[path]);
        if (changed) await this.load(cx, { prints: now });
      } finally {
        this.busy = false;
      }
    });
  }

  shown() {
    const at = focused();
    if (!at) return false;
    if (!this.tabId) {
      // A pane of its own tab learns its tab when it gets the keyboard.
      if (this.handle.is_focused()) this.tabId = at.tab;
      else return true;
    }
    return at.tab === this.tabId && appWindow()?.active !== false;
  }

  agentWorking() {
    const terminal = terminals().find((t) => t.labels?.["galpon.agent"] === this.agentId);
    return terminal?.status === "working";
  }

  // --- items of the list

  // measure computes the sizes that every row uses, once per layout: the
  // digits of the line numbers, the width of the gutter and the characters
  // of a line. A row reads them; it never scans the diff.
  measure() {
    let most = 0;
    for (const file of this.diff?.files ?? []) {
      for (const row of file.rows ?? []) most = Math.max(most, row.o ?? 0, row.n ?? 0);
    }
    const digits = Math.max(3, String(most).length);
    const listW = this.listWidth();
    const narrow = listW < 560;
    const numberW = digits * CODE * 0.6 + 10;
    const gutterW = (narrow ? numberW : numberW * 2) + 18;
    const cols = Math.max(20, Math.floor((listW - gutterW - 28) / (CODE * 0.6)));
    this.metrics = { digits, listW, narrow, numberW, gutterW, cols, split: this.splitShown() };
    return this.metrics;
  }

  cols() {
    return (this.metrics ?? this.measure()).cols;
  }

  listWidth() {
    const full = this.width || 900;
    return this.treeShown() ? full - TREE_W - 1 : full;
  }

  treeShown() {
    const full = this.width || 0;
    // Side by side takes the room of the tree when both do not fit.
    if (this.layout.split && full >= SPLIT_WIDE && full - TREE_W - 1 < SPLIT_WIDE) return false;
    return this.layout.tree && full >= WIDE && (this.diff?.files?.length ?? 0) > 1 && !this.view.oneFile;
  }

  splitShown() {
    return this.layout.split && this.listWidth() >= SPLIT_WIDE;
  }

  gutterWidth() {
    return (this.metrics ?? this.measure()).gutterW;
  }

  narrow() {
    return (this.metrics ?? this.measure()).narrow;
  }

  // rebuild makes the items of the list again. It runs in event handlers,
  // never while the view draws: a large diff takes longer than a frame.
  rebuild(anchor = null) {
    const cursorKey = this.built.items[this.cursor]?.key ?? null;
    const selectionKey = this.anchor !== null ? this.built.items[this.anchor]?.key ?? null : null;
    const m = this.measure();
    this.built = buildItems(this.diff, this.view, { cols: m.cols, wrap: this.layout.wrap && !m.split, split: m.split });
    this.uniform = this.built.items.length > MAX_SIZES;
    this.builtFor = this.layoutKey();
    if (anchor) {
      const at = anchorIndex(this.built, anchor);
      if (at >= 0) this.scroller.scroll_to_item(at, "top");
    }
    const cursor = cursorKey ? anchorIndex(this.built, cursorKey) : -1;
    this.cursor = cursor >= 0 ? cursor : nextIndex(this.built.items, -1, 1, isStop);
    // A selection keeps its first line when the rows come again.
    const selection = selectionKey ? anchorIndex(this.built, selectionKey) : -1;
    this.anchor = selection >= 0 ? selection : null;
  }

  layoutKey() {
    return `${Math.round(this.listWidth())}:${this.layout.wrap}:${this.splitShown()}`;
  }

  anchorKey() {
    const item = this.built.items[this.visible.start];
    return item?.key ?? null;
  }

  // --- moves of the cursor

  moveTo(index, cx, how = "nearest") {
    if (index < 0 || index >= this.built.items.length) return;
    this.cursor = index;
    if (how === "top") this.scroller.scroll_to_item(index, "top");
    else if (how === "center") this.scroller.scroll_to_item(index, "center");
    else if (index < this.visible.start + 1 || index >= this.visible.end - 1) this.scroller.scroll_to_item(index, "center");
    cx?.notify();
  }

  step(direction, cx, test = isStop) {
    const next = nextIndex(this.built.items, this.cursor, direction, test);
    if (next >= 0) this.moveTo(next, cx);
  }

  nextFile(direction, cx) {
    this.step(direction, cx, (item) => item.t === "file");
  }

  nextChange(direction, cx) {
    const items = this.built.items;
    const next = nextIndex(items, this.cursor, direction, (_item, i) => changeStart(items, i));
    if (next >= 0) this.moveTo(next, cx, "center");
  }

  nextUnviewed(cx) {
    const items = this.built.items;
    let next = nextIndex(items, this.cursor, 1, (item) => item.t === "file" && !item.file.viewed);
    if (next < 0) next = nextIndex(items, -1, 1, (item) => item.t === "file" && !item.file.viewed);
    if (next >= 0) this.moveTo(next, cx, "top");
    else this.say("Every file is viewed.", "done");
  }

  // resolvePath finds the file of the diff that a path names: the same path,
  // or a path that ends with it, as an absolute path of the worktree does.
  resolvePath(path) {
    const files = this.diff?.files ?? [];
    if (files.some((file) => file.path === path)) return path;
    const found = files.find((file) => path.endsWith(`/${file.path}`) || file.path.endsWith(`/${path}`));
    return found?.path ?? path;
  }

  jumpToFile(asked, line = 0) {
    const path = this.resolvePath(asked);
    const at = this.built.index.get(`${path}|file`);
    if (at === undefined) return;
    if (this.view.collapsed.has(path)) {
      this.view.collapsed.delete(path);
      this.rebuild();
    }
    let target = this.built.index.get(`${path}|file`);
    if (line) {
      const found = this.built.items.findIndex((item) => item.file?.path === path && !item.part && (item.row?.n === line || item.right?.n === line));
      if (found >= 0) target = found;
    }
    this.moveTo(target, null, line ? "center" : "top");
  }

  currentFile() {
    const item = this.built.items[this.cursor] ?? this.built.items[this.visible.start];
    return item?.file ?? null;
  }

  topFile() {
    for (let i = this.visible.start; i >= 0; i--) {
      const item = this.built.items[i];
      if (item?.file) return item.file;
    }
    return null;
  }

  // --- actions

  say(text, tone = "info") {
    this.flash = { text, tone, until: Date.now() + 5000 };
  }

  toggleFile(file, cx) {
    if (file.deferred && !file.loaded) {
      this.openDeferred(file, cx);
      return;
    }
    const folded = this.view.collapsed.has(file.path) || (file.viewed && !this.view.expanded.has(file.path));
    if (folded) {
      this.view.collapsed.delete(file.path);
      if (file.viewed) this.view.expanded.add(file.path);
    } else {
      this.view.collapsed.add(file.path);
      this.view.expanded.delete(file.path);
    }
    this.rebuild();
    const at = this.built.index.get(`${file.path}|file`);
    if (at !== undefined) this.cursor = at;
    cx.notify();
  }

  async openDeferred(file, cx) {
    this.loading = `Reading ${file.path}`;
    cx.notify();
    try {
      const out = await this.galpon(this.diffArgs(["--open", file.path]));
      const fresh = (out.files ?? []).find((candidate) => candidate.path === file.path);
      if (fresh) Object.assign(file, fresh, { deferred: false, loaded: true });
    } catch (error) {
      this.say(message(error), "error");
    }
    this.loading = "";
    this.rebuild();
    cx.notify();
  }

  async openFold(item, cx) {
    const fold = item.row.x;
    const file = item.file;
    this.loading = "Reading lines";
    cx.notify();
    try {
      const args = ["changes", "lines", this.agentId, "--checkout", this.sel.checkout, "--set", this.sel.set];
      const base = baseFor(this.sel.set, this.selected()?.branch);
      if (base) args.push("--base", base);
      args.push("--file", file.path, "--from", String(fold.n), "--old", String(fold.o), "--count", String(fold.c));
      const out = await this.galpon(args);
      const rows = file.rows ?? [];
      const at = rows.indexOf(item.row);
      if (at >= 0) {
        // Pairs point at rows by index, so the rows after the fold move.
        const added = (out.rows ?? []).length - 1;
        for (const row of rows) if (row.p && row.p - 1 > at) row.p += added;
        rows.splice(at, 1, ...(out.rows ?? []));
      }
    } catch (error) {
      this.say(message(error), "error");
    }
    this.loading = "";
    this.rebuild(item.key);
    cx.notify();
  }

  async openAllFolds(file, cx) {
    const folds = this.built.items.filter((item) => item.t === "fold" && item.file === file);
    for (const fold of folds.reverse()) await this.openFold(fold, cx);
  }

  async setViewed(file, viewed, cx) {
    if (!file?.content) {
      this.say("This file has no version to mark.", "warning");
      return;
    }
    file.viewed = viewed;
    if (viewed) {
      file.changed = false;
      this.view.expanded.delete(file.path);
    }
    this.rebuild();
    if (viewed) this.nextUnviewed(cx);
    cx.notify();
    try {
      await this.galpon(["changes", "mark", this.agentId, JSON.stringify([{ checkout: this.sel.checkout, path: file.path, content: file.content, viewed }])]);
    } catch (error) {
      file.viewed = !viewed;
      this.rebuild();
      this.say(`The viewed mark failed: ${message(error)}`, "error");
      cx.notify();
    }
  }

  choose(checkoutKey, branch, set, cx) {
    this.sel = { checkout: checkoutKey, branch: branch.key, set, follow: Boolean(branch.live) && set === setFor(branch) };
    this.popover = null;
    this.view.oneFile = null;
    this.diff = null;
    this.rebuild();
    this.save();
    cx.spawn(async (cx) => this.loadDiff(cx, { keep: false }));
    cx.notify();
  }

  toggleOnlyNew(cx) {
    this.onlyNew = !this.onlyNew;
    this.say(this.onlyNew ? "Only files that changed after you viewed them." : "All the files of the change set.", "info");
    cx.spawn(async (cx) => this.loadDiff(cx, { keep: false }));
    cx.notify();
  }

  // --- comments

  selectionRange() {
    const items = this.built.items;
    const a = this.anchor ?? this.cursor;
    const b = this.cursor;
    const from = Math.min(a, b);
    const to = Math.max(a, b);
    const file = items[b]?.file;
    let side = null;
    let first = Infinity;
    let last = -Infinity;
    for (let i = from; i <= to; i++) {
      const item = items[i];
      if (!item || item.file !== file) continue;
      const row = item.t === "split" ? (item.right ?? item.left) : item.row;
      if (!row || (row.k !== "a" && row.k !== "d" && row.k !== "c")) continue;
      const rowSide = row.k === "d" ? "old" : "new";
      side = side ?? rowSide;
      if (rowSide !== side) continue;
      const line = side === "old" ? row.o : row.n;
      first = Math.min(first, line);
      last = Math.max(last, line);
    }
    if (!file || side === null) return null;
    return { path: file.path, side, from: first, to: last, file };
  }

  startComment(cx) {
    const range = this.selectionRange();
    if (!range) {
      this.say("Put the cursor on a line of a file to comment on it.", "warning");
      cx.notify();
      return;
    }
    this.commenting = range;
    this.commentInput.set_value("");
    cx.timer.after(0, () => this.commentInput.focus());
    cx.notify();
  }

  saveComment(cx) {
    const range = this.commenting;
    const text = this.commentInput.value().trim();
    this.commenting = null;
    this.anchor = null;
    if (range && text) {
      this.view.comments.push({
        id: `c${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`,
        path: range.path, side: range.side, from: range.from, to: range.to, text,
        excerpt: commentExcerpt(range.file, range.side, range.from, range.to).slice(0, 12),
      });
      this.save();
      this.rebuild(this.anchorKey());
      this.say(`${this.view.comments.length} comment${this.view.comments.length === 1 ? "" : "s"}. Ctrl+Enter puts them in the composer.`, "info");
    }
    this.handle.focus();
    cx.notify();
  }

  removeComment(comment, cx) {
    this.view.comments = this.view.comments.filter((candidate) => candidate.id !== comment.id);
    this.save();
    this.rebuild(this.anchorKey());
    cx.notify();
  }

  // sendComments gives the comments to the agent tab as one review chip in
  // its composer. The person adds words there and sends it.
  sendComments(cx) {
    const comments = this.view.comments;
    if (!comments.length) {
      this.say("There are no comments. Press C on a line to write one.", "warning");
      cx.notify();
      return;
    }
    const found = this.selected();
    const where = `${found?.repository?.title ?? ""} ${branchLabel(found?.branch)} (${setLabel(this.sel?.set, found?.branch).toLowerCase()})`.trim();
    const text = composeReview(comments, where);
    const agentPane = this.agentPane();
    if (!agentPane) {
      cx.write_to_clipboard(text);
      this.say("The agent has no tab here, so the comments are on the clipboard.", "warning");
      cx.notify();
      return;
    }
    setShared(REVIEW, { agent: this.agentId, id: `review-${Date.now().toString(36)}`, text, count: comments.length, at: Date.now() });
    this.view.comments = [];
    this.save();
    this.rebuild(this.anchorKey());
    run("pane.focus", { pane: agentPane.pane });
    notify({ title: "Review comments", body: `${comments.length === 1 ? "1 comment is" : `${comments.length} comments are`} in the composer of ${this.agentTitle || "the agent"}.`, level: "info", timeout: 4000 });
    cx.notify();
  }

  // agentPane finds the agent tab of the agent: Galpon names its tab
  // galpon-<agent>.
  agentPane() {
    const tab = `${HOST ? `${HOST}/` : ""}galpon-${this.agentId}`;
    return panes().find((pane) => pane.mod === "galpon-agent" && pane.tab === tab) ?? null;
  }

  copyReference(cx) {
    const range = this.selectionRange();
    if (!range) return;
    const where = range.from === range.to ? `${range.from}` : `${range.from}-${range.to}`;
    cx.write_to_clipboard(`${range.path}:${where}`);
    this.say(`Copied ${range.path}:${where}`, "done");
    cx.notify();
  }

  openInTerminal(cx) {
    const range = this.selectionRange();
    const found = this.selected();
    if (!range || !found?.checkout) return;
    if (HOST) {
      this.say("Open the file in a terminal on the host computer.", "warning");
      cx.notify();
      return;
    }
    run("terminal.open", {
      argv: ["sh", "-lc", 'exec "${EDITOR:-vi}" +"$1" "$2"', "sh", String(range.from), range.path],
      folder: found.checkout.path,
      place: "tab",
    });
  }

  zoomPane() {
    const at = focused();
    if (at?.pane) run("pane.zoom", { pane: at.pane });
  }

  // --- keys

  keyDown(event, cx) {
    const key = event.keystroke;
    if (this.commenting) {
      if (key === "escape") {
        this.commenting = null;
        this.handle.focus();
        cx.stop_propagation?.();
        cx.notify();
      } else if (key === "ctrl-w") {
        deleteWord(this.commentInput);
        cx.stop_propagation?.();
      }
      return;
    }
    if (this.popover) {
      this.menuKey(event, cx);
      return;
    }
    const items = this.built.items;
    const page = Math.max(1, Math.floor((this.height - 120) / H.row) - 2);
    const handled = () => {
      cx.stop_propagation?.();
      cx.notify();
    };
    switch (key) {
      case "j": case "down": this.step(1, cx); return handled();
      case "k": case "up": this.step(-1, cx); return handled();
      case "shift-j": case "shift-down":
        if (this.anchor === null) this.anchor = this.cursor;
        this.step(1, cx);
        return handled();
      case "shift-k": case "shift-up":
        if (this.anchor === null) this.anchor = this.cursor;
        this.step(-1, cx);
        return handled();
      case "n": this.nextChange(1, cx); return handled();
      case "shift-n": case "p": this.nextChange(-1, cx); return handled();
      case "f": this.nextFile(1, cx); return handled();
      case "shift-f": this.nextFile(-1, cx); return handled();
      case "u": this.nextUnviewed(cx); return handled();
      case "space": case "pagedown": case "ctrl-d":
        this.moveTo(Math.min(items.length - 1, this.cursor + page), cx, "nearest");
        return handled();
      case "shift-space": case "pageup": case "ctrl-u":
        this.moveTo(Math.max(0, this.cursor - page), cx, "nearest");
        return handled();
      case "g": case "home": this.moveTo(nextIndex(items, -1, 1, isStop), cx, "top"); return handled();
      case "shift-g": case "end": this.moveTo(nextIndex(items, items.length, -1, isStop), cx, "center"); return handled();
      case "enter": this.activate(items[this.cursor], cx); return handled();
      case "v": {
        const file = this.currentFile();
        if (file) this.setViewed(file, !file.viewed, cx);
        return handled();
      }
      case "a": this.toggleOnlyNew(cx); return handled();
      case "o":
        this.view.oneFile = this.view.oneFile ? null : this.currentFile()?.path ?? null;
        this.rebuild();
        this.moveTo(nextIndex(this.built.items, -1, 1, isStop), cx, "top");
        return handled();
      case "s":
        this.layout.split = !this.layout.split;
        if (this.layout.split && this.listWidth() < SPLIT_WIDE) this.say("Side by side needs a wider pane. Z zooms the pane.", "warning");
        this.rebuild(this.anchorKey());
        this.save();
        return handled();
      case "w":
        this.layout.wrap = !this.layout.wrap;
        this.rebuild(this.anchorKey());
        this.save();
        return handled();
      case "t":
        this.layout.tree = !this.layout.tree;
        this.rebuild(this.anchorKey());
        this.save();
        return handled();
      case "e": {
        const file = this.currentFile();
        if (file) cx.spawn(async (cx) => this.openAllFolds(file, cx));
        return handled();
      }
      case "b": this.openMenu("nav", cx); return handled();
      case "r": this.openMenu("set", cx); return handled();
      case "/": case "ctrl-p": this.openMenu("jump", cx); return handled();
      case "?": case "shift-/": this.openMenu("help", cx); return handled();
      case "x":
        this.anchor = this.anchor === null ? this.cursor : null;
        return handled();
      case "c": this.startComment(cx); return handled();
      case "ctrl-enter": case "cmd-enter": this.sendComments(cx); return handled();
      case "y": this.copyReference(cx); return handled();
      case "shift-o": this.openInTerminal(cx); return handled();
      case "z": this.zoomPane(); return handled();
      case "alt-c": {
        const agentPane = this.agentPane();
        if (agentPane) run("pane.focus", { pane: agentPane.pane });
        return handled();
      }
      case "shift-r": cx.spawn(async (cx) => this.load(cx)); return handled();
      case "escape":
        if (this.anchor !== null) this.anchor = null;
        else if (this.view.oneFile) {
          this.view.oneFile = null;
          this.rebuild();
        }
        return handled();
      default:
    }
  }

  activate(item, cx) {
    if (!item) return;
    switch (item.t) {
      case "file": this.toggleFile(item.file, cx); break;
      case "load": this.openDeferred(item.file, cx); break;
      case "fold": cx.spawn(async (cx) => this.openFold(item, cx)); break;
      case "noise":
        this.view.noise.add(`${item.file.path}|${item.block}`);
        this.rebuild(item.key);
        cx.notify();
        break;
      case "move": {
        const link = item.row.l;
        if (link && this.built.index.has(`${link.file}|file`)) this.jumpToFile(link.file, link.line);
        else if (link) this.say(`${link.file} is not in this change set.`, "info");
        cx.notify();
        break;
      }
      case "comment": this.removeComment(item.comment, cx); break;
      case "entities": this.openMenu("jump", cx, item.file.path.split("/").pop()); break;
      default: this.startComment(cx);
    }
  }

  // --- menus: the work navigator, the change sets of a branch, the jump
  // list and the keys

  openMenu(kind, cx, query = "") {
    this.popover = { kind, cursor: 0, open: new Set(), mark: null };
    if (kind === "nav") {
      const rows = this.menuRows();
      const at = rows.findIndex((row) => row.type === "branch" && row.checkout.key === this.sel?.checkout && row.branch.key === this.sel?.branch);
      this.popover.cursor = Math.max(0, at >= 0 ? at : rows.findIndex((row) => row.type === "branch"));
    }
    if (kind === "set") {
      const rows = this.menuRows();
      const at = rows.findIndex((row) => row.set === this.sel?.set);
      this.popover.cursor = Math.max(0, at);
    }
    if (kind === "jump") {
      this.jumpInput.set_value(query);
      cx.timer.after(0, () => this.jumpInput.focus());
    }
    cx.timer.after(0, () => this.menuScroller.scroll_to_item(this.popover?.cursor ?? 0, "center"));
    cx.notify();
  }

  closeMenu(cx) {
    this.popover = null;
    this.handle.focus();
    cx.notify();
  }

  menuRows() {
    const kind = this.popover?.kind;
    if (kind === "nav") return this.navRows();
    if (kind === "set") return this.setRows();
    if (kind === "jump") return this.jumpRows();
    if (kind === "help") return this.helpRows();
    return [];
  }

  navRows() {
    const groups = workGroups(this.work);
    const rows = [{ type: "head", key: "head:ongoing", text: "Ongoing" }];
    const numbers = new Map();
    for (const repository of this.work?.repositories ?? []) {
      (repository.checkouts ?? []).forEach((checkout, index) => numbers.set(checkout.key, repository.checkouts.length > 1 ? index + 1 : 0));
    }
    for (const group of groups.ongoing) {
      const number = numbers.get(group.checkout.key);
      const where = group.checkout.external ? group.checkout.path.split("/").pop() : number ? `checkout ${number}` : "checkout";
      const facts = [];
      if (group.checkout.primary) facts.push("primary");
      if (group.checkout.sharedWith) facts.push(`shared with ${group.checkout.sharedWith} agent${group.checkout.sharedWith === 1 ? "" : "s"}`);
      if (group.checkout.operation) facts.push(`${group.checkout.operation} in progress`);
      if (group.checkout.missing) facts.push("missing on disk");
      rows.push({ type: "checkout", key: `checkout:${group.checkout.key}`, text: `${group.repository.title} · ${where}`, detail: facts.join(" · ") });
      for (const entry of group.branches) rows.push({ type: "branch", key: `branch:${group.checkout.key}:${entry.branch.key}`, ...entry });
    }
    if (!groups.ongoing.length) rows.push({ type: "empty", key: "empty", text: (this.work?.notices ?? []).join(" ") || "No ongoing work." });
    for (const [name, list] of [["unfinished", groups.unfinished], ["done", groups.done]]) {
      if (!list.length) continue;
      const open = this.popover.open.has(name);
      rows.push({ type: "fold", key: `fold:${name}`, name, text: `${name === "done" ? "Done" : "Unfinished"} (${list.length})`, open });
      if (open) for (const entry of list) rows.push({ type: "branch", key: `branch:${entry.checkout.key}:${entry.branch.key}`, grouped: true, ...entry });
    }
    return rows;
  }

  setRows() {
    const found = this.selected();
    const branch = found?.branch;
    if (!branch) return [{ type: "empty", key: "empty", text: "Choose a branch first: press B." }];
    const rows = [{ type: "set", key: "set:total", set: setFor(branch), text: "Branch total", detail: branch.detached ? "the work of the detached checkout" : `the work of ${branch.name} after ${branch.base?.stackedOn || shortRef(branch.base?.ref) || "its base"}` }];
    const dirty = found.checkout.dirty ?? {};
    const uncommitted = (dirty.staged ?? 0) + (dirty.unstaged ?? 0) + (dirty.untracked ?? 0) + (dirty.conflicted ?? 0);
    if (branch.live) rows.push({ type: "set", key: "set:uncommitted", set: "uncommitted", text: "Uncommitted", detail: uncommitted ? `${uncommitted} file${uncommitted === 1 ? "" : "s"}` : "nothing yet" });
    rows.push({ type: "toggle", key: "toggle:new", text: "Only new since you viewed", detail: "A", on: this.onlyNew });
    if (branch.commits?.length) rows.push({ type: "head", key: "head:commits", text: `Commits${branch.moreCommits ? " (newest 50)" : ""}` });
    for (const commit of branch.commits ?? []) {
      rows.push({ type: "commit", key: `commit:${commit.sha}`, set: `commit:${commit.sha}`, commit, marked: this.popover?.mark === commit.sha });
    }
    return rows;
  }

  jumpRows() {
    const query = this.jumpInput.value().trim();
    const scored = jumpEntries(this.diff).map((entry) => ({ entry, score: fuzzy(query, entry.label) })).filter((item) => item.score >= 0);
    if (query) scored.sort((a, b) => b.score - a.score);
    return scored.slice(0, 200).map(({ entry }, index) => ({ type: "jump", key: `jump:${index}:${entry.kind}:${entry.label}`, entry }));
  }

  helpRows() {
    const keys = [
      ["J K", "Next or previous line"], ["N Shift+N", "Next or previous change"], ["F Shift+F", "Next or previous file"],
      ["U", "Next file that is not viewed"], ["V", "Mark the file viewed, then go to the next one"], ["A", "Only files new since you viewed them"],
      ["Enter", "Open a fold, a folded file or a move"], ["E", "Open every fold of the file"], ["O", "One file at a time"],
      ["S", "Side by side or one column"], ["W", "Wrap long lines"], ["T", "File tree"],
      ["B", "Branches and checkouts"], ["R", "Change sets of the branch"], ["/", "Jump to a file or a function"],
      ["X, Shift+J/K", "Select lines"], ["C", "Comment on the lines"], ["Ctrl+Enter", "Put the comments in the composer"],
      ["Y", "Copy the file and the lines"], ["Shift+O", "Open the file in a terminal"], ["Z", "Zoom the pane"], ["Shift+R", "Read the work again"],
    ];
    return keys.map(([keysText, text]) => ({ type: "help", key: `help:${keysText}`, keysText, text }));
  }

  menuKey(event, cx) {
    const key = event.keystroke;
    const rows = this.menuRows();
    const pick = (row) => row && row.type !== "head" && row.type !== "checkout" && row.type !== "empty" && row.type !== "help";
    const stop = () => {
      cx.stop_propagation?.();
      cx.notify();
    };
    const move = (step) => {
      let at = this.popover.cursor;
      for (let n = 0; n < rows.length; n++) {
        at = (at + step + rows.length) % rows.length;
        if (pick(rows[at]) || this.popover.kind === "help") break;
      }
      this.popover.cursor = at;
      this.menuScroller.scroll_to_item(at);
    };
    switch (key) {
      case "escape": this.closeMenu(cx); return stop();
      case "down": case "ctrl-n": move(1); return stop();
      case "up": case "ctrl-p": move(-1); return stop();
      case "enter": this.chooseMenu(cx); return stop();
      default:
    }
    if (this.popover.kind !== "jump") {
      if (key === "j") {
        move(1);
        return stop();
      }
      if (key === "k") {
        move(-1);
        return stop();
      }
      if (key === "space" && this.popover.kind === "set") {
        const row = rows[this.popover.cursor];
        if (row?.type === "commit") this.popover.mark = this.popover.mark === row.commit.sha ? null : row.commit.sha;
        return stop();
      }
      if (key === "a" && this.popover.kind === "set") {
        this.closeMenu(cx);
        this.toggleOnlyNew(cx);
        return stop();
      }
      if ((key === "b" && this.popover.kind === "nav") || (key === "r" && this.popover.kind === "set") || ((key === "?" || key === "shift-/") && this.popover.kind === "help")) {
        this.closeMenu(cx);
        return stop();
      }
    } else if (key === "ctrl-w") {
      deleteWord(this.jumpInput);
      return stop();
    }
  }

  chooseMenu(cx, index = null) {
    const rows = this.menuRows();
    if (index !== null) this.popover.cursor = index;
    const row = rows[this.popover?.cursor ?? 0];
    if (!row) return;
    switch (row.type) {
      case "branch": this.choose(row.checkout.key, row.branch, setFor(row.branch), cx); break;
      case "fold":
        if (row.open) this.popover.open.delete(row.name);
        else this.popover.open.add(row.name);
        cx.notify();
        break;
      case "set": {
        const found = this.selected();
        this.choose(this.sel.checkout, found.branch, row.set, cx);
        break;
      }
      case "commit": {
        const found = this.selected();
        const mark = this.popover.mark;
        const set = mark ? rangeSet(found.branch, mark, row.commit.sha) : row.set;
        this.choose(this.sel.checkout, found.branch, set, cx);
        break;
      }
      case "toggle":
        this.closeMenu(cx);
        this.toggleOnlyNew(cx);
        break;
      case "jump": {
        const entry = row.entry;
        this.closeMenu(cx);
        this.jumpToFile(entry.file.path, entry.entity?.line ?? 0);
        break;
      }
      case "help":
        this.closeMenu(cx);
        break;
      default:
    }
  }

  // --- drawing

  render(cx) {
    const P = palette();
    this.P = P;
    keepSelection(P, cx);
    const root = v_flex().id("ch-root").size_full().relative().track_focus(this.handle).key_context(CONTEXT)
      .bg(P.bg).text_color(P.text).font_family(SANS).text_size(S.ui)
      .on_key_down((event, cx) => this.keyDown(event, cx))
      .on_size_change((size, cx) => {
        const width = size.width / zoom();
        const height = size.height / zoom();
        if (Math.abs(width - this.width) < 1 && Math.abs(height - this.height) < 1) return;
        this.width = width;
        this.height = height;
        if (this.builtFor !== this.layoutKey()) this.rebuild(this.anchorKey());
        cx.notify();
      })
      .child(this.header())
      .when(Boolean(this.strip()), (el) => el.child(this.strip()))
      .child(this.body())
      .child(this.footer());
    if (this.popover) return root.child(this.backdrop()).child(this.menu());
    return root;
  }

  header() {
    const P = this.P;
    const found = this.selected();
    const totals = this.diff?.totals;
    const chip = (id, iconName, text, onClick, tip) => h_flex().id(id).h(26).min_w(0).px(8).gap(6).flex_shrink_1().items_center().rounded(6)
      .bg(P.surface).border_1().border_color(P.line).cursor_pointer().hover((s) => s.bg(P.raised))
      .tooltip(tip)
      .on_click((_e, cx) => onClick(cx))
      .child(icon(iconName, 13).text_color(P.text3))
      .child(div().min_w(0).truncate().text_size(S.meta).child(text))
      .child(icon("chevron-down", 12).text_color(P.text3));
    const branchText = found?.branch ? `${found.repository.title} · ${branchLabel(found.branch)}` : this.work ? "No branch" : "…";
    return h_flex().id("ch-header").h(40).flex_none().w_full().px(10).gap(8).items_center().border_b(1).border_color(P.line)
      .child(icon("git-compare", 15).text_color(P.accent))
      .child(div().flex_none().font_weight(600).child("Changes"))
      .when(Boolean(this.agentTitle) && (this.width || 0) > 720, (el) => el.child(div().flex_none().max_w(160).truncate().text_color(P.text3).child(this.agentTitle)))
      .child(chip("ch-branch", "git-branch", branchText, (cx) => this.openMenu("nav", cx), "Branches and checkouts · B"))
      .when(Boolean(found?.branch), (el) => el.child(chip("ch-set", "layers", `${setLabel(this.sel.set, found.branch)}${this.onlyNew ? " · new only" : ""}`, (cx) => this.openMenu("set", cx), "Change sets · R")))
      .child(div().flex_1())
      .when(Boolean(totals), (el) => el
        .child(h_flex().flex_none().gap(6).font_family(MONO).text_size(S.small)
          .child(div().text_color(P.green).child(`+${totals.added}`))
          .child(div().text_color(P.red).child(`−${totals.removed}`))
          .when(totals.generatedAdded + totals.generatedRemoved > 0, (el) => el.child(div().text_color(P.text3).tooltip("Lines of generated files, vendored files and lockfiles").child(`(+${totals.generatedAdded} −${totals.generatedRemoved})`))))
        .when((this.width || 0) > 560, (el) => el.child(div().flex_none().text_size(S.small).text_color(P.text3).child(`${(this.diff?.files ?? []).filter((file) => file.viewed).length}/${totals.files} viewed`))))
      .child(this.liveMark());
  }

  liveMark() {
    const P = this.P;
    if (this.loading) return h_flex().flex_none().gap(6).items_center().text_size(S.small).text_color(P.text3).child(spinner(P.text3, 13)).when((this.width || 0) > 640, (el) => el.child(this.loading));
    if (this.agentWorking()) return h_flex().flex_none().gap(4).items_center().text_size(S.small).text_color(P.text3).tooltip("The agent works. The view updates while it changes files.").child(pulse(P.purple, 12)).child("live");
    return div().flex_none();
  }

  // strip shows the facts of the branch that matter now: its pull request,
  // a Git operation in progress, a shared checkout, a missing base.
  strip() {
    const found = this.selected();
    if (!found?.branch) return null;
    const P = this.P;
    const { checkout, branch } = found;
    const parts = [];
    const pr = branch.pullRequest;
    if (pr) {
      const state = pr.state === "OPEN" ? (pr.draft ? "draft" : "open") : pr.state.toLowerCase();
      const why = pr.state === "OPEN" ? pr.reason || pr.attention?.toLowerCase() : "";
      parts.push({ text: `PR #${pr.number} ${state}${why ? ` · ${why}` : ""}`, color: pr.attention === "NEEDS YOU" ? P.yellow : pr.attention === "AGENT'S TURN" ? P.purple : P.text2 });
    }
    if (checkout.operation) parts.push({ text: `${checkout.operation[0].toUpperCase()}${checkout.operation.slice(1)} in progress${checkout.dirty?.conflicted ? ` · ${checkout.dirty.conflicted} conflicts` : ""}`, color: P.yellow });
    if (branch.base?.missing) parts.push({ text: "No base branch: the total is every commit of the branch", color: P.yellow });
    else if (branch.base?.behind) parts.push({ text: `${branch.base.behind} behind ${shortRef(branch.base.ref)}`, color: P.text3 });
    if (branch.base?.stackedOn) parts.push({ text: `on top of ${branch.base.stackedOn}`, color: P.text3 });
    if (checkout.sharedWith) parts.push({ text: `Shared with ${checkout.sharedWith} agent${checkout.sharedWith === 1 ? "" : "s"}: their work shows too`, color: P.yellow });
    if (branch.reason && !pr) parts.push({ text: branch.reason, color: P.text3 });
    if (!parts.length) return null;
    return h_flex().id("ch-strip").h(26).flex_none().w_full().px(12).gap(14).items_center().border_b(1).border_color(P.line).bg(P.surface).text_size(S.small)
      .children(parts.map((part, index) => div().id(`ch-strip-${index}`).flex_none().max_w(Math.max(120, (this.width || 600) / 2)).truncate().text_color(part.color).child(part.text)));
  }

  body() {
    const P = this.P;
    let content;
    if (this.error && !this.diff) content = this.message(this.error, "alert", P.red, "Shift+R reads again.");
    else if (!this.work) content = this.message("Reading the work of the agent…", "spinner", P.text3);
    else if (!this.sel) content = this.message((this.work.notices ?? []).join(" ") || "This agent has no ongoing work.", "git-branch", P.text3, "B shows done and unfinished branches.");
    else if (!this.diff) content = this.message("Reading the diff…", "spinner", P.text3);
    else if (!this.built.items.length) content = this.message(this.onlyNew ? "Nothing changed since you viewed the files." : "No changes in this change set.", "circle-check", P.green, this.onlyNew ? "A shows all the files again." : "");
    else content = this.list();
    const row = h_flex().id("ch-body").flex_1().min_h(0).w_full().items_stretch();
    if (this.treeShown() && this.diff) return row.child(this.tree()).child(div().w(1).h_full().bg(P.line)).child(v_flex().flex_1().min_w(0).h_full().child(this.fileBar()).child(content));
    return row.child(v_flex().flex_1().min_w(0).h_full().when(Boolean(this.diff?.files?.length), (el) => el.child(this.fileBar())).child(content));
  }

  message(text, iconName, color, hint = "") {
    const P = this.P;
    const mark = iconName === "spinner" ? spinner(color, 18) : icon(iconName, 18).text_color(color);
    return v_flex().flex_1().items_center().justify_center().gap(10).px(24)
      .child(mark)
      .child(div().max_w(520).text_size(S.ui).text_color(P.text2).child(text))
      .when(Boolean(hint), (el) => el.child(div().text_size(S.small).text_color(P.text3).child(hint)));
  }

  // fileBar is the bar of the file at the top of the list, with the actions
  // that a row of the list cannot have.
  fileBar() {
    const P = this.P;
    const file = this.topFile() ?? this.diff?.files?.[0];
    if (!file) return div();
    const button = (id, iconName, text, onClick, active = false, tip = "") => h_flex().id(id).h(22).px(6).gap(5).flex_none().items_center().rounded(5).cursor_pointer()
      .text_size(S.small).text_color(active ? P.green : P.text3).hover((s) => s.bg(P.raised).text_color(P.text))
      .when(Boolean(tip), (el) => el.tooltip(tip))
      .on_click((_e, cx) => onClick(cx))
      .child(icon(iconName, 12)).child(text);
    const index = (this.diff?.files ?? []).indexOf(file) + 1;
    return h_flex().id("ch-filebar").h(30).flex_none().w_full().px(10).gap(8).items_center().border_b(1).border_color(P.line).bg(P.side)
      .child(div().flex_none().text_size(S.small).text_color(P.text3).child(`${index}/${this.diff.files.length}`))
      .child(this.pathText(file.path, S.meta).flex_1().min_w(0))
      .child(button("ch-viewed", file.viewed ? "circle-check" : "circle", file.viewed ? "Viewed" : "Mark viewed", (cx) => this.setViewed(file, !file.viewed, cx), file.viewed, "V"))
      .child(button("ch-one", this.view.oneFile ? "layers" : "file-text", this.view.oneFile ? "All files" : "This file", (cx) => {
        this.view.oneFile = this.view.oneFile ? null : file.path;
        this.rebuild();
        this.moveTo(nextIndex(this.built.items, -1, 1, isStop), cx, "top");
      }, false, "O"));
  }

  pathText(path, size) {
    const P = this.P;
    const cut = path.replace(/\/$/, "").lastIndexOf("/");
    const folder = cut >= 0 ? path.slice(0, cut + 1) : "";
    const name = cut >= 0 ? path.slice(cut + 1) : path;
    return h_flex().min_w(0).overflow_hidden().whitespace_nowrap().font_family(MONO).text_size(size)
      .child(div().min_w(0).flex_shrink_1().truncate().text_color(P.text3).child(folder))
      .child(div().flex_none().text_color(P.text).font_weight(600).child(name));
  }

  tree() {
    const P = this.P;
    const rows = fileTree(this.diff?.files ?? []);
    const current = this.topFile()?.path;
    const list = v_virtual_list("ch-tree", rows.length, 24, (index) => rows[index].key, (range) => rows.slice(range.start, range.end).map((row) => {
      const pad = 10 + row.depth * 12;
      if (row.t === "folder") {
        return h_flex().h(24).pl(pad).pr(8).gap(6).items_center().text_size(S.small).text_color(P.text3)
          .child(icon("folder", 12)).child(div().min_w(0).truncate().child(row.name));
      }
      const file = row.file;
      const active = file.path === current;
      return h_flex().h(24).pl(pad).pr(8).gap(6).items_center().rounded(5).text_size(S.small)
        .when(active, (el) => el.bg(P.raised2))
        .child(div().w(12).flex_none().font_family(MONO).text_color(this.statusColor(file)).child(statusLetter(file)))
        .child(div().flex_1().min_w(0).truncate().text_color(file.viewed ? P.text3 : P.text).when(file.viewed, (el) => el.line_through()).child(row.name))
        .when(file.changed, (el) => el.child(div().w(6).h(6).flex_none().rounded_full().bg(P.accent)))
        .child(div().flex_none().font_family(MONO).text_size(10).text_color(P.text3).child(file.classes?.includes("folder") ? `${file.count}` : `${file.added}/${file.removed}`));
    })).track_scroll(this.treeScroller).on_item_click((key, cx) => {
      const row = rows.find((candidate) => candidate.key === key);
      if (row?.file) this.jumpToFile(row.file.path);
      cx.notify();
    }).size_full();
    return v_flex().id("ch-tree-col").w(TREE_W).flex_none().h_full().bg(P.side).relative().py(6).px(4)
      .child(list).child(Scrollbar.vertical("ch-tree").absolute().inset_0());
  }

  statusColor(file) {
    const P = this.P;
    switch (file.status) {
      case "added": case "untracked": return P.green;
      case "deleted": return P.red;
      case "renamed": case "copied": return P.blue;
      case "conflict": return P.yellow;
      default: return P.yellow;
    }
  }

  // hh gives the height of an item of a kind. A list with more items than
  // a virtual list takes sizes for gives every item the row height.
  hh(kind) {
    return this.uniform ? H.row : H[kind];
  }

  list() {
    const built = this.built;
    const list = v_virtual_list("ch-list", built.items.length, this.uniform ? H.row : built.sizes, (index) => built.items[index].key, (range) => {
      this.visible = { start: range.start, end: range.end };
      const out = [];
      for (let i = range.start; i < range.end; i++) out.push(this.item(built.items[i], i));
      return out;
    }).track_scroll(this.scroller).on_item_click((key, cx) => {
      const at = this.built.index.get(key);
      if (at === undefined) return;
      const item = this.built.items[at];
      const moved = this.cursor !== at;
      this.cursor = at;
      if (["file", "load", "fold", "noise", "move", "entities"].includes(item.t) || !moved) this.activate(item, cx);
      cx.notify();
    }).size_full();
    return v_flex().id("ch-list-box").flex_1().min_h(0).w_full().relative()
      .child(list).child(Scrollbar.vertical("ch-list").absolute().inset_0());
  }

  // --- the items of the list

  item(item, index) {
    const selected = this.inSelection(index);
    const cursor = index === this.cursor;
    const el = this.itemBody(item, index, cursor, selected);
    if (!cursor || item.t === "gap") return el;
    // The cursor is one mark on every kind of row: a bar of the accent at
    // the left edge.
    return div().relative().w_full().child(el).child(div().absolute().left(0).top(0).bottom(0).w(3).bg(this.P.accent));
  }

  itemBody(item, index, cursor, selected) {
    switch (item.t) {
      case "gap": return div().h(this.hh("gap"));
      case "file": return this.fileRow(item, cursor);
      case "entities": return this.entitiesRow(item, cursor);
      case "note": return this.noteRow(item.text, cursor, "info");
      case "load": return this.noteRow(`${statusWord(item.file)} · folded (${(item.file.classes ?? []).map((c) => CLASS_WORDS[c] ?? c).join(", ") || "large change set"}). Enter loads its diff.`, cursor, "load");
      case "fold": return this.foldRow(item, cursor);
      case "hunk": return this.hunkRow(item, cursor);
      case "move": return this.moveRow(item, cursor);
      case "noise": return this.noiseRow(item, cursor);
      case "comment": return this.commentRow(item, cursor);
      case "split": return this.splitRow(item, cursor, selected);
      default: return this.codeRow(item, cursor, selected);
    }
  }

  inSelection(index) {
    if (this.anchor === null) return false;
    return index >= Math.min(this.anchor, this.cursor) && index <= Math.max(this.anchor, this.cursor);
  }

  fileRow(item, cursor) {
    const P = this.P;
    const file = item.file;
    const folded = this.view.collapsed.has(file.path) || (file.viewed && !this.view.expanded.has(file.path)) || (file.deferred && !file.loaded);
    const classes = (file.classes ?? []).filter((c) => c !== "uncommitted" || this.sel?.set !== "uncommitted");
    return h_flex().h(this.hh("file")).w_full().px(10).gap(8).items_center().bg(cursor ? P.raised2 : P.surface).border_t(1).border_b(1).border_color(P.line)
      .child(icon(folded ? "chevron-right" : "chevron-down", 13).text_color(P.text3))
      .child(div().w(14).flex_none().font_family(MONO).font_weight(700).text_size(S.small).text_color(this.statusColor(file)).child(statusLetter(file)))
      .child(this.pathText(file.oldPath ? `${file.oldPath} → ${file.path}` : file.path, S.meta).flex_1().min_w(0))
      .children(classes.slice(0, 3).map((c) => div().flex_none().h(18).px(6).rounded(4).bg(P.raised).text_size(10).text_color(c === "uncommitted" ? P.yellow : P.text3).child(CLASS_WORDS[c] ?? c)))
      .when(file.changed, (el) => el.child(h_flex().flex_none().gap(4).items_center().text_size(10).text_color(P.accent).child(div().w(6).h(6).rounded_full().bg(P.accent)).child("new")))
      .child(h_flex().flex_none().gap(5).font_family(MONO).text_size(S.small)
        .when(file.classes?.includes("folder"), (el) => el.child(div().text_color(P.text3).child(`${file.count} files`)))
        .when(!file.classes?.includes("folder") && !file.binary, (el) => el.child(div().text_color(P.green).child(`+${file.added}`)).child(div().text_color(P.red).child(`−${file.removed}`))))
      .child(icon(file.viewed ? "circle-check" : "circle", 13).text_color(file.viewed ? P.green : P.text3));
  }

  entitiesRow(item, cursor) {
    const P = this.P;
    const marks = { added: ["+", P.green], removed: ["−", P.red], modified: ["~", P.yellow], renamed: ["→", P.blue], moved: ["↷", P.blue], format: ["≈", P.text3] };
    const shown = item.file.entities.slice(0, 8);
    const more = item.file.entities.length - shown.length;
    return h_flex().h(this.hh("entities")).w_full().pl(36).pr(10).gap(12).items_center().overflow_hidden().when(cursor, (el) => el.bg(P.raised))
      .children(shown.map((entity) => {
        const [mark, color] = marks[entity.change] ?? ["·", P.text3];
        return h_flex().flex_none().gap(4).items_center().text_size(S.small)
          .child(div().font_family(MONO).text_color(color).child(mark))
          .child(div().text_color(P.text2).child(entity.change === "renamed" ? `${entity.oldName} → ${entity.name}` : entity.name));
      }))
      .when(more > 0, (el) => el.child(div().flex_none().text_size(S.small).text_color(P.text3).child(`+${more} more`)));
  }

  noteRow(text, cursor, kind) {
    const P = this.P;
    return h_flex().h(this.hh("note")).w_full().pl(36).pr(10).gap(6).items_center().text_size(S.small).text_color(kind === "load" ? P.text2 : P.text3)
      .when(cursor, (el) => el.bg(P.raised))
      .child(icon(kind === "load" ? "layers" : "info", 12))
      .child(div().min_w(0).truncate().child(text));
  }

  foldRow(item, cursor) {
    const P = this.P;
    const fold = item.row.x;
    const names = (fold.e ?? []).join(", ");
    return h_flex().h(this.hh("fold")).w_full().px(10).gap(8).items_center().bg(cursor ? P.raised2 : mix(P.bg, P.fg, 0.03)).text_size(S.small).text_color(P.text3)
      .child(div().w(this.gutterWidth() - 10).flex_none().font_family(MONO).child("⋯"))
      .child(div().flex_none().child(`${fold.c} unchanged line${fold.c === 1 ? "" : "s"}`))
      .when(Boolean(names), (el) => el.child(div().min_w(0).truncate().child(`· ${names}`)))
      .when(cursor, (el) => el.child(div().flex_1()).child(kbd(P, "↵")));
  }

  hunkRow(item, cursor) {
    const P = this.P;
    return h_flex().h(this.hh("hunk")).w_full().px(10).gap(8).items_center().text_size(S.small).text_color(P.text3).when(cursor, (el) => el.bg(P.raised))
      .child(div().w(this.gutterWidth() - 10).flex_none())
      .child(div().min_w(0).truncate().font_family(MONO).child(`── ${item.row.t}`));
  }

  moveRow(item, cursor) {
    const P = this.P;
    return h_flex().h(this.hh("move")).w_full().px(10).gap(8).items_center().text_size(S.small).text_color(P.blue).bg(cursor ? P.raised2 : mix(P.bg, P.blue, 0.06))
      .child(div().w(this.gutterWidth() - 10).flex_none().font_family(MONO).child("↷"))
      .child(div().min_w(0).truncate().child(item.row.t))
      .when(cursor, (el) => el.child(div().flex_1()).child(kbd(P, "↵")));
  }

  noiseRow(item, cursor) {
    const P = this.P;
    return h_flex().h(this.hh("noise")).w_full().px(10).gap(8).items_center().text_size(S.small).text_color(P.text3).bg(cursor ? P.raised2 : P.bg)
      .child(div().w(this.gutterWidth() - 10).flex_none().font_family(MONO).child("≈"))
      .child(div().min_w(0).truncate().child(`${item.kind === "format" ? "Format only" : "Whitespace only"} · ${item.count} line${item.count === 1 ? "" : "s"}`))
      .when(cursor, (el) => el.child(div().flex_1()).child(kbd(P, "↵")));
  }

  commentRow(item, cursor) {
    const P = this.P;
    const cols = this.cols();
    const text = item.comment.text.slice(item.part * cols, (item.part + 1) * cols);
    return h_flex().h(this.hh("comment")).w_full().pl(this.gutterWidth()).pr(10).gap(6).items_center().bg(cursor ? mix(P.bg, P.accent, 0.22) : P.accentSoft)
      .child(div().w(14).flex_none().when(item.part === 0, (el) => el.child(icon("message", 12).text_color(P.accent))))
      .child(div().min_w(0).whitespace_nowrap().overflow_hidden().text_size(S.meta).text_color(P.text).child(text))
      .when(cursor && item.part === 0, (el) => el.child(div().flex_1()).child(div().flex_none().text_size(S.small).text_color(P.text3).child("Enter removes it")));
  }

  // tint gives the colors of a row: its background, the background of its
  // changed tokens, and its marker.
  tint(row) {
    const P = this.P;
    const flags = row.f ?? 0;
    const noise = flags & (FLAG.whitespace | FLAG.format);
    if (row.k === "c") return { bg: P.bg, emph: P.bg, mark: " ", markColor: P.text3 };
    if (flags & FLAG.moved) return { bg: mix(P.bg, P.blue, 0.1), emph: mix(P.bg, P.blue, 0.28), mark: "↷", markColor: P.blue };
    const color = row.k === "a" ? P.green : P.red;
    if (noise) return { bg: P.bg, emph: P.bg, mark: row.k === "a" ? "+" : "−", markColor: P.text3, dim: true };
    return { bg: mix(P.bg, color, 0.11), emph: mix(P.bg, color, 0.3), mark: row.k === "a" ? "+" : "−", markColor: color };
  }

  syntaxColor(style) {
    const P = this.P;
    switch (style & 31) {
      case 1: return P.purple;
      case 2: return P.green;
      case 3: return P.text3;
      case 4: return P.yellow;
      case 5: return P.blue;
      case 6: return P.cyan;
      case 7: return P.yellow;
      case 8: return P.text2;
      case 9: return P.cyan;
      case 11: return P.accent;
      case 12: return P.red;
      default: return P.text;
    }
  }

  segments(segs, tint) {
    const P = this.P;
    return (segs ?? []).map(([text, style]) => {
      let el = div().flex_none().text_color(tint.dim ? P.text3 : this.syntaxColor(style)).child(text);
      if (style & EMPHASIS) el = el.bg(tint.emph).rounded(2);
      if ((style & 31) === 3) el = el.italic();
      return el;
    });
  }

  // gutter draws the line numbers and the mark of a row: both numbers, or
  // the number of one side ("old" or "new").
  gutter(row, part, tint, width, side = "both") {
    const P = this.P;
    const number = (value) => div().w(width).flex_none().text_right().pr(6).text_color(P.text3).child(part === 0 && value ? String(value) : "");
    let el = h_flex().flex_none().h_full().items_center().font_family(MONO).text_size(10.5);
    if (side === "both") el = el.child(number(row.k === "a" ? null : row.o)).child(number(row.k === "d" ? null : row.n));
    else if (side === "old") el = el.child(number(row.o));
    else if (side === "new") el = el.child(number(row.n));
    else el = el.child(number(row.k === "d" ? row.o : row.n));
    return el.child(div().w(14).flex_none().text_color(tint.markColor).child(part === 0 ? tint.mark : ""));
  }

  codeRow(item, cursor, selected) {
    const P = this.P;
    const row = item.row;
    const tint = this.tint(row);
    const m = this.metrics;
    const cols = m.cols;
    const segs = this.layout.wrap ? sliceSegs(row.s, item.part * cols, (item.part + 1) * cols) : row.s;
    const width = m.numberW;
    const flags = row.f ?? 0;
    let bg = tint.bg;
    if (selected) bg = mix(bg, P.accent, 0.22);
    else if (cursor) bg = mix(bg, P.accent, 0.12);
    return h_flex().h(this.hh("row")).w_full().items_center().bg(bg).font_family(MONO).text_size(CODE).whitespace_nowrap().overflow_hidden()
      .child(div().w(3).h_full().flex_none().bg(flags & FLAG.uncommitted ? P.yellow : flags & FLAG.fresh ? P.accent : bg))
      .child(this.gutter(row, item.part, tint, width, this.narrow() ? "one" : "both"))
      .child(h_flex().min_w(0).flex_1().overflow_hidden().children(this.segments(segs, tint)))
      .when((flags & FLAG.cut) !== 0 && item.part === item.parts - 1, (el) => el.child(div().flex_none().px(4).text_color(P.text3).child("…")));
  }

  splitRow(item, cursor, selected) {
    const P = this.P;
    const width = this.metrics.numberW;
    const half = (row, side) => {
      if (!row) return h_flex().flex_1().min_w(0).h_full().bg(mix(P.bg, P.fg, 0.025));
      const tint = this.tint(row);
      let bg = tint.bg;
      if (selected) bg = mix(bg, P.accent, 0.22);
      else if (cursor) bg = mix(bg, P.accent, 0.12);
      return h_flex().flex_1().min_w(0).h_full().items_center().bg(bg).overflow_hidden()
        .child(this.gutter(row, 0, tint, width, side))
        .child(h_flex().min_w(0).flex_1().overflow_hidden().children(this.segments(row.s, tint)));
    };
    return h_flex().h(this.hh("row")).w_full().items_center().font_family(MONO).text_size(CODE).whitespace_nowrap()
      .child(half(item.left, "old"))
      .child(div().w(1).h_full().bg(P.line))
      .child(half(item.right, "new"));
  }

  footer() {
    const P = this.P;
    if (this.commenting) {
      const range = this.commenting;
      const where = range.from === range.to ? `${range.from}` : `${range.from}-${range.to}`;
      return h_flex().id("ch-comment").h(40).flex_none().w_full().px(10).gap(8).items_center().border_t(1).border_color(P.line).bg(P.side)
        .child(icon("message", 14).text_color(P.accent))
        .child(div().flex_none().font_family(MONO).text_size(S.small).text_color(P.text2).child(`${range.path.split("/").pop()}:${where}`))
        .child(div().flex_1().min_w(0).border_b(1).border_color(P.accentLine).child(Input.new(this.commentInput).w_full().h(26).text_size(S.ui).text_color(P.text)));
    }
    const comments = this.view.comments.length;
    const flash = this.flash;
    const hints = [["J K", "move"], ["N", "change"], ["V", "viewed"], ["C", "comment"], ["?", "keys"]];
    return h_flex().id("ch-footer").h(26).flex_none().w_full().gap(8).items_center().border_t(1).border_color(P.line).bg(P.side).pr(10).text_size(S.small)
      .child(h_flex().h_full().px(10).flex_none().items_center().bg(P.blue).text_color(P.bg).font_weight(700).child("CHANGES"))
      .when(Boolean(flash), (el) => el.child(div().min_w(0).truncate().text_color(flash.tone === "error" ? P.red : flash.tone === "warning" ? P.yellow : flash.tone === "done" ? P.green : P.text2).child(flash.text)))
      .when(!flash && Boolean(this.error) && Boolean(this.diff), (el) => el.child(div().min_w(0).truncate().text_color(P.red).child(this.error)))
      .when(!flash && comments > 0, (el) => el.child(h_flex().flex_none().gap(6).items_center().text_color(P.accent)
        .child(icon("message", 12)).child(`${comments} comment${comments === 1 ? "" : "s"}`).child(kbd(P, "Ctrl ↵")).child(div().text_color(P.text3).child("to the composer"))))
      .when(this.anchor !== null, (el) => el.child(div().flex_none().text_color(P.text2).child("Selecting lines · C comments · Esc stops")))
      .child(div().flex_1())
      .when((this.width || 0) > 620, (el) => el.children(hints.map(([keysText, text]) => h_flex().flex_none().gap(4).items_center().text_color(P.text3).child(kbd(P, keysText)).child(text))));
  }

  backdrop() {
    return div().id("ch-backdrop").absolute().inset_0().on_mouse_down("left", (_e, cx) => {
      this.closeMenu(cx);
      cx.stop_propagation?.();
    });
  }

  menu() {
    const P = this.P;
    const rows = this.menuRows();
    const kind = this.popover.kind;
    const width = Math.min(620, Math.max(320, (this.width || 600) - 24));
    const titles = { nav: "Branches and checkouts", set: "Change sets", jump: "Jump", help: "Keys" };
    const heights = rows.map((row) => (row.type === "head" || row.type === "checkout" ? 28 : MENU_ROW));
    const total = heights.reduce((sum, h) => sum + h, 0);
    const listH = Math.min(total, Math.max(160, (this.height || 500) * 0.6));
    const list = rows.length
      ? v_virtual_list("ch-menu", rows.length, heights, (index) => rows[index].key, (range) => rows.slice(range.start, range.end).map((row, offset) => this.menuRow(row, range.start + offset === this.popover.cursor)))
        .track_scroll(this.menuScroller).on_item_click((key, cx) => {
          const index = rows.findIndex((row) => row.key === key);
          if (index >= 0) this.chooseMenu(cx, index);
        }).size_full()
      : div().p(12).text_color(P.text3).child("No match.");
    return v_flex().id("ch-menu-box").absolute().top(44).left(12).w(width).rounded(10).bg(P.raised).border_1().border_color(P.line2).shadow_lg().overflow_hidden()
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .child(h_flex().h(34).px(12).gap(8).items_center().border_b(1).border_color(P.line)
        .child(div().flex_none().font_weight(600).text_size(S.meta).child(titles[kind]))
        .when(kind === "jump", (el) => el.child(div().flex_1().min_w(0).child(Input.new(this.jumpInput).w_full().h(26).text_size(S.ui).text_color(P.text))))
        .when(kind !== "jump", (el) => el.child(div().flex_1()))
        .child(div().flex_none().text_size(S.small).text_color(P.text3).child(kind === "set" ? "Space marks a range · Esc" : "↵ · Esc")))
      .child(v_flex().relative().h(listH).w_full().px(4).py(4).child(list).child(Scrollbar.vertical("ch-menu").absolute().inset_0()));
  }

  menuRow(row, active) {
    const P = this.P;
    const base = h_flex().h(MENU_ROW).w_full().px(10).gap(8).items_center().rounded(6).when(active, (el) => el.bg(P.raised2));
    switch (row.type) {
      case "head": return h_flex().h(28).px(10).items_end().pb(4).text_size(S.small).font_weight(600).text_color(P.text3).child(row.text.toUpperCase());
      case "checkout": return h_flex().h(28).px(10).gap(8).items_end().pb(4).text_size(S.small).text_color(P.text2)
        .child(icon("folder", 12).text_color(P.text3)).child(div().flex_none().font_weight(600).child(row.text))
        .child(div().min_w(0).truncate().text_color(P.text3).child(row.detail));
      case "empty": return h_flex().h(MENU_ROW).px(10).items_center().text_size(S.small).text_color(P.text3).child(row.text);
      case "fold": return base.text_size(S.small).text_color(P.text2).child(icon(row.open ? "chevron-down" : "chevron-right", 12)).child(row.text);
      case "branch": {
        const branch = row.branch;
        const current = this.sel?.checkout === row.checkout.key && this.sel?.branch === branch.key;
        const stateColor = { live: P.purple, review: P.yellow, local: P.text2, unfinished: P.text3, merged: P.green, closed: P.text3 }[branch.state] ?? P.text3;
        return base
          .child(div().w(8).h(8).flex_none().rounded_full().when(branch.live, (el) => el.bg(stateColor)).when(!branch.live, (el) => el.border_1().border_color(stateColor)))
          .child(div().flex_none().max_w(Math.round(this.menuWidth() * 0.5)).truncate().font_family(MONO).text_size(S.meta).text_color(current ? P.accent : P.text).child(branchLabel(branch)))
          .child(div().flex_none().text_size(S.small).text_color(stateColor).child(STATE_WORDS[branch.state] ?? branch.state))
          .child(div().flex_1().min_w(0).truncate().text_size(S.small).text_color(P.text3).child(branchFacts(branch).join(" · ")))
          .when(Boolean(row.grouped), (el) => el.child(div().flex_none().text_size(10).text_color(P.text3).child(row.repository.title)));
      }
      case "set": return base.child(icon(row.set === this.sel?.set ? "check" : "circle", 12).text_color(row.set === this.sel?.set ? P.accent : P.text3))
        .child(div().flex_none().text_size(S.meta).child(row.text)).child(div().min_w(0).truncate().text_size(S.small).text_color(P.text3).child(row.detail));
      case "toggle": return base.child(icon(row.on ? "circle-check" : "circle", 12).text_color(row.on ? P.accent : P.text3))
        .child(div().flex_none().text_size(S.meta).child(row.text)).child(div().flex_1()).child(kbd(P, row.detail));
      case "commit": {
        const commit = row.commit;
        const current = this.sel?.set === `commit:${commit.sha}`;
        return base.when(row.marked, (el) => el.bg(P.accentSoft))
          .child(div().w(8).h(8).flex_none().rounded_full().when(commit.pushed, (el) => el.bg(P.text3)).when(!commit.pushed, (el) => el.border_1().border_color(P.text3)))
          .child(div().flex_none().font_family(MONO).text_size(S.small).text_color(current ? P.accent : P.text3).child(shortSha(commit.sha)))
          .child(div().flex_1().min_w(0).truncate().text_size(S.meta).child(commit.subject))
          .child(div().flex_none().font_family(MONO).text_size(10).text_color(P.green).child(`+${commit.added}`))
          .child(div().flex_none().font_family(MONO).text_size(10).text_color(P.red).child(`−${commit.removed}`))
          .child(div().flex_none().w(28).text_right().text_size(S.small).text_color(P.text3).child(ago(commit.time)));
      }
      case "jump": {
        const entry = row.entry;
        return base.child(icon(entry.kind === "file" ? "file-text" : "sparkle", 12).text_color(P.text3))
          .child(div().flex_none().max_w(Math.round(this.menuWidth() * 0.6)).truncate().font_family(MONO).text_size(S.meta).child(entry.label))
          .child(div().min_w(0).truncate().text_size(S.small).text_color(P.text3).child(entry.detail));
      }
      case "help": return h_flex().h(MENU_ROW).px(10).gap(10).items_center()
        .child(div().w(110).flex_none().child(kbd(P, row.keysText))).child(div().text_size(S.meta).text_color(P.text2).child(row.text));
      default: return base;
    }
  }

  menuWidth() {
    return Math.min(620, Math.max(320, (this.width || 600) - 24));
  }
}

function shortRef(ref) {
  return String(ref ?? "").replace(/^refs\/remotes\//, "").replace(/^refs\/heads\//, "");
}
