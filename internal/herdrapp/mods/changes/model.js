// The pure logic of the Changes view: the groups of the work navigator, the
// labels of change sets, the visual rows of the diff list, wrapping, the
// file tree, the jump list and the text of review comments. It imports
// nothing from Herdr, so Node tests it (internal/herdrapp/tests).

// Row heights of the diff list, in pixels.
export const H = { row: 20, file: 32, entities: 22, note: 22, fold: 24, hunk: 22, move: 22, noise: 22, gap: 12, comment: 20 };

// Row flags of the engine (internal/changes/diff).
export const FLAG = { whitespace: 1, format: 2, moved: 4, uncommitted: 8, fresh: 16, cut: 32 };
// EMPHASIS marks a changed token in a segment style.
export const EMPHASIS = 32;

const ONGOING = new Set(["live", "review", "local"]);
// EXCERPT_LINES is the most lines of one place that a review quotes. The
// review names all the lines, so the agent reads the others in the file.
const EXCERPT_LINES = 12;
const DAY = 24 * 60 * 60 * 1000;

// shortSha gives the first seven characters of a commit.
export const shortSha = (sha) => String(sha ?? "").slice(0, 7);

// ago gives a short age: "now", "5m", "3h", "2d", "4w".
export function ago(ms, now = Date.now()) {
  const seconds = Math.max(0, Math.round((now - ms) / 1000));
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  if (seconds < 14 * 86400) return `${Math.floor(seconds / 86400)}d`;
  return `${Math.floor(seconds / (7 * 86400))}w`;
}

// branchLabel names a branch, or a detached checkout by its commit.
export function branchLabel(branch) {
  if (!branch) return "";
  return branch.detached ? `Detached at ${shortSha(branch.tip)}` : branch.name;
}

// workGroups puts the branches of the work in three groups: ongoing work by
// repository and checkout, unfinished work, and done work. Each entry keeps
// its repository and checkout.
export function workGroups(work) {
  const ongoing = [];
  const unfinished = [];
  const done = [];
  for (const repository of work?.repositories ?? []) {
    for (const checkout of repository.checkouts ?? []) {
      const group = { repository, checkout, branches: [] };
      for (const branch of checkout.branches ?? []) {
        const entry = { repository, checkout, branch };
        if (ONGOING.has(branch.state)) group.branches.push(entry);
        else if (branch.state === "unfinished") unfinished.push(entry);
        else done.push(entry);
      }
      if (group.branches.length || checkout.missing || checkout.error) ongoing.push(group);
    }
  }
  const newest = (a, b) => (b.branch.lastActivity ?? 0) - (a.branch.lastActivity ?? 0);
  unfinished.sort(newest);
  done.sort(newest);
  return { ongoing, unfinished, done };
}

// findBranch finds a checkout and a branch of the work by their keys.
export function findBranch(work, checkoutKey, branchKey) {
  for (const repository of work?.repositories ?? []) {
    for (const checkout of repository.checkouts ?? []) {
      if (checkout.key !== checkoutKey) continue;
      const branch = (checkout.branches ?? []).find((candidate) => candidate.key === branchKey) ?? null;
      return { repository, checkout, branch };
    }
  }
  return null;
}

// setFor gives the change set of the total of a branch.
export function setFor(branch) {
  return branch?.detached ? "detached" : `branch:${branch?.name ?? ""}`;
}

// setLabel names a change set for the header.
export function setLabel(set, branch) {
  if (!set) return "";
  if (set === "uncommitted") return "Uncommitted";
  if (set === "detached" || set.startsWith("branch:")) return "Branch total";
  if (set.startsWith("commit:")) {
    const sha = set.slice(7);
    const commit = branch?.commits?.find((candidate) => candidate.sha === sha);
    return commit ? `${shortSha(sha)} ${commit.subject}` : `Commit ${shortSha(sha)}`;
  }
  if (set.startsWith("range:")) {
    const [from, to] = set.slice(6).split("..");
    const commits = branch?.commits ?? [];
    const a = commits.findIndex((commit) => commit.sha === from);
    const b = commits.findIndex((commit) => commit.sha === to);
    if (a >= 0 && b >= 0) return `${Math.abs(a - b) + 1} commits`;
    return `${shortSha(from)}..${shortSha(to)}`;
  }
  return set;
}

// baseFor gives the commit that a branch set starts from.
export function baseFor(set, branch) {
  if (set === "detached" || set?.startsWith("branch:")) return branch?.base?.mergeBase ?? "";
  return "";
}

// rangeSet gives the set of the commits between two commits of a branch,
// oldest first.
export function rangeSet(branch, a, b) {
  const commits = branch?.commits ?? [];
  const i = commits.findIndex((commit) => commit.sha === a);
  const j = commits.findIndex((commit) => commit.sha === b);
  if (i < 0 || j < 0) return "";
  if (i === j) return `commit:${a}`;
  // Commits are newest first.
  const oldest = commits[Math.max(i, j)].sha;
  const newest = commits[Math.min(i, j)].sha;
  return `range:${oldest}..${newest}`;
}

// rowText gives the text of a row from its segments.
export function rowText(row) {
  let text = "";
  for (const seg of row?.s ?? []) text += seg[0];
  return text;
}

// rowLength gives the characters of a row. Every build of the list needs
// it, so it stays on the row.
export function rowLength(row) {
  if (row._length === undefined) {
    let length = 0;
    for (const seg of row.s ?? []) length += seg[0].length;
    row._length = length;
  }
  return row._length;
}

// sliceSegs gives the segments of the characters [start, end) of a row.
export function sliceSegs(segs, start, end) {
  const out = [];
  let at = 0;
  for (const [text, style] of segs ?? []) {
    const from = at;
    const to = at + text.length;
    at = to;
    if (to <= start || from >= end) continue;
    out.push([text.slice(Math.max(0, start - from), Math.min(text.length, end - from)), style]);
  }
  return out;
}

// parts gives how many visual lines a text of a length takes at cols
// characters each. Without wrap, a line takes one visual line.
export function parts(length, cols, wrap) {
  if (!wrap || cols <= 0) return 1;
  return Math.max(1, Math.ceil(length / cols));
}

// noiseBlocks finds the changes of a file whose rows are all noise: only
// whitespace, or only format. They fold to one row each.
export function noiseBlocks(rows) {
  const blocks = new Map();
  for (const row of rows ?? []) {
    if (!row.b || (row.k !== "d" && row.k !== "a")) continue;
    const noise = (row.f ?? 0) & (FLAG.whitespace | FLAG.format);
    const block = blocks.get(row.b) ?? { noise: true, kind: "whitespace", count: 0 };
    block.count++;
    if (!noise) block.noise = false;
    else if (row.f & FLAG.format) block.kind = "format";
    blocks.set(row.b, block);
  }
  for (const [key, block] of blocks) if (!block.noise) blocks.delete(key);
  return blocks;
}

// rowKey gives a key of a row that stays the same when the diff loads again,
// so the view can keep its place: the kind of the row and its line numbers.
export function rowKey(path, row) {
  switch (row.k) {
    case "d": return `${path}|d|${row.o}`;
    case "a": return `${path}|a|${row.n}`;
    case "c": return `${path}|c|${row.n}`;
    case "f": return `${path}|f|${row.n}`;
    case "h": return `${path}|h|${row.n}|${row.t ?? ""}`;
    case "m": return `${path}|m|${row.o ?? ""}|${row.n ?? ""}`;
    default: return `${path}|${row.k}|${row.o ?? ""}|${row.n ?? ""}`;
  }
}

// splitPairs puts the rows of a file side by side: an unchanged row on both
// sides, a removed row on the left beside its paired added row, and an
// unpaired row alone on its side.
export function splitPairs(rows) {
  const out = [];
  let i = 0;
  while (i < rows.length) {
    const row = rows[i];
    if (row.k !== "d" && row.k !== "a") {
      out.push({ full: row, index: i });
      i++;
      continue;
    }
    // A change: its removed rows, then its added rows, maybe with move rows.
    const removed = [];
    const added = [];
    let j = i;
    while (j < rows.length && (rows[j].k === "d" || rows[j].k === "a" || rows[j].k === "m") && rows[j].b === row.b) {
      if (rows[j].k === "d") removed.push(j);
      else if (rows[j].k === "a") added.push(j);
      j++;
    }
    let next = 0;
    for (const r of removed) {
      const pair = rows[r].p ? rows[r].p - 1 : -1;
      const at = pair >= 0 ? added.indexOf(pair, next) : -1;
      if (at >= 0) {
        for (; next < at; next++) out.push({ right: rows[added[next]], rightIndex: added[next] });
        out.push({ left: rows[r], leftIndex: r, right: rows[added[at]], rightIndex: added[at] });
        next = at + 1;
      } else {
        out.push({ left: rows[r], leftIndex: r });
      }
    }
    for (; next < added.length; next++) out.push({ right: rows[added[next]], rightIndex: added[next] });
    i = j;
  }
  return out;
}

// commentTargets gives what a comment is about: ranges of lines of one
// side of a file, or whole files ({ path } without lines). A comment of an
// older view kept its one range in its own fields.
export function commentTargets(comment) {
  if (Array.isArray(comment?.targets)) return comment.targets;
  if (!comment?.path) return [];
  return [{ path: comment.path, side: comment.side, from: comment.from, to: comment.to, excerpt: comment.excerpt ?? [] }];
}

// normalizeComment gives a comment in the shape of this view: { id, text,
// targets }.
export function normalizeComment(comment) {
  return { id: String(comment?.id ?? ""), text: String(comment?.text ?? ""), targets: commentTargets(comment) };
}

// commentLabel gives the text of the rows of a comment. A comment on more
// than one place, or on whole files, names its files first.
export function commentLabel(comment) {
  const targets = commentTargets(comment);
  if (targets.length === 1 && targets[0].from) return comment.text;
  const names = targets.map((target) => target.path.replace(/\/$/, "").split("/").pop());
  const shown = names.length > 3 ? `${names.slice(0, 3).join(", ")} +${names.length - 3}` : names.join(", ");
  return `${shown} · ${comment.text}`;
}

// commentAnchors puts each comment after its last target: after its last
// line, or after the header of its file. It gives path → anchor → comments,
// where an anchor is "old|N", "new|N" or "file".
function commentAnchors(comments) {
  const out = new Map();
  for (const comment of comments) {
    const last = commentTargets(comment).at(-1);
    if (!last) continue;
    const anchor = last.from ? `${last.side}|${last.to}` : "file";
    if (!out.has(last.path)) out.set(last.path, new Map());
    const own = out.get(last.path);
    own.set(anchor, [...(own.get(anchor) ?? []), comment]);
  }
  return out;
}

// fileSignature names everything that the items of one file depend on, so
// that a build keeps the items of a file that did not change.
function fileSignature(file, view, shape, anchors) {
  const collapsed = view.collapsed?.has(file.path) || (file.viewed && !view.expanded?.has(file.path));
  let noise = "";
  for (const key of view.noise ?? []) if (key.startsWith(`${file.path}|`)) noise += `${key};`;
  let notes = "";
  for (const [anchor, list] of anchors ?? []) for (const comment of list) notes += `${anchor}:${comment.id}:${comment.text.length};`;
  return `${shape}|${collapsed ? 1 : 0}|${file.deferred ? 1 : 0}|${file.version ?? 0}|${file.rows?.length ?? 0}|${noise}|${notes}`;
}

// buildItems makes the items of the diff list: their heights, where each
// starts, and where each file starts and ends. view holds the folded
// files, the opened noise changes, the comments and the file of one-file
// mode; layout holds the columns of a line, split or unified, and wrap.
// cache, a Map, keeps the items of each file between builds: a build makes
// again only the files whose items changed.
export function buildItems(diff, view, layout, cache = null) {
  const items = [];
  const sizes = [];
  const files = new Map();
  const anchors = commentAnchors(view.comments ?? []);
  const shape = `${layout.cols}|${layout.wrap ? 1 : 0}|${layout.split ? 1 : 0}`;
  (diff?.files ?? []).forEach((file, fileIndex) => {
    if (view.oneFile && file.path !== view.oneFile) return;
    if (items.length) {
      items.push({ t: "gap", key: `${file.path}|gap` });
      sizes.push(H.gap);
    }
    const own = anchors.get(file.path);
    const sig = fileSignature(file, view, shape, own);
    let part = cache?.get(file);
    if (!part || part.sig !== sig) {
      part = { sig, ...fileItems(file, fileIndex, view, layout, own) };
      cache?.set(file, part);
    }
    files.set(file.path, { start: items.length, end: items.length + part.items.length });
    for (let i = 0; i < part.items.length; i++) {
      items.push(part.items[i]);
      sizes.push(part.sizes[i]);
    }
  });
  const origins = new Array(items.length);
  let total = 0;
  for (let i = 0; i < items.length; i++) {
    origins[i] = total;
    total += sizes[i];
  }
  return { items, sizes, origins, total, files };
}

// fileItems makes the items of one file: its header, its entities and
// notices, and its rows with their comments.
function fileItems(file, fileIndex, view, layout, anchors) {
  const items = [];
  const sizes = [];
  const push = (item, size) => {
    items.push(item);
    sizes.push(size);
  };
  const addComments = (anchor) => {
    for (const comment of anchors?.get(anchor) ?? []) {
      const text = commentLabel(comment);
      const count = parts(text.length + 2, layout.cols, true);
      for (let part = 0; part < count; part++) push({ t: "comment", key: `comment|${comment.id}|${part}`, file, comment, text, part, parts: count }, H.comment);
    }
  };
  push({ t: "file", key: `${file.path}|file`, file, fileIndex }, H.file);
  addComments("file");
  const collapsed = view.collapsed?.has(file.path) || (file.viewed && !view.expanded?.has(file.path));
  if (collapsed) return { items, sizes };
  if (file.entities?.length) push({ t: "entities", key: `${file.path}|entities`, file }, H.entities);
  for (const [index, notice] of (file.notices ?? []).entries()) push({ t: "note", key: `${file.path}|note|${index}`, file, text: notice }, H.note);
  if (file.deferred) {
    push({ t: "load", key: `${file.path}|load`, file }, H.note);
    return { items, sizes };
  }
  const rows = file.rows ?? [];
  const noise = noiseBlocks(rows);
  const shownNoise = new Set();
  const rowComments = (row) => addComments(row.k === "d" ? `old|${row.o}` : `new|${row.n}`);
  const addRow = (row, index) => {
    const block = row.b && (row.k === "d" || row.k === "a") ? noise.get(row.b) : null;
    if (block && !view.noise?.has(`${file.path}|${row.b}`)) {
      if (!shownNoise.has(row.b)) {
        shownNoise.add(row.b);
        push({ t: "noise", key: `${file.path}|noise|${row.b}`, file, block: row.b, kind: block.kind, count: block.count }, H.noise);
      }
      return;
    }
    if (row.k === "f") return push({ t: "fold", key: rowKey(file.path, row), file, row, index }, H.fold);
    if (row.k === "h") return push({ t: "hunk", key: rowKey(file.path, row), file, row, index }, H.hunk);
    if (row.k === "m") return push({ t: "move", key: rowKey(file.path, row), file, row, index }, H.move);
    const count = parts(rowLength(row), layout.cols, layout.wrap);
    for (let part = 0; part < count; part++) {
      push({ t: "row", key: part ? `${rowKey(file.path, row)}|${part}` : rowKey(file.path, row), file, row, index, part, parts: count }, H.row);
    }
    rowComments(row);
  };
  if (layout.split) {
    for (const pair of splitPairs(rows)) {
      if (pair.full) {
        if (pair.full.k === "c") {
          push({ t: "split", key: rowKey(file.path, pair.full), file, left: pair.full, right: pair.full, leftIndex: pair.index, rightIndex: pair.index }, H.row);
          rowComments(pair.full);
        } else {
          addRow(pair.full, pair.index);
        }
        continue;
      }
      const row = pair.left ?? pair.right;
      const block = row.b ? noise.get(row.b) : null;
      if (block && !view.noise?.has(`${file.path}|${row.b}`)) {
        addRow(row, pair.leftIndex ?? pair.rightIndex);
        continue;
      }
      const key = pair.left ? rowKey(file.path, pair.left) : rowKey(file.path, pair.right);
      push({ t: "split", key, file, left: pair.left ?? null, right: pair.right ?? null, leftIndex: pair.leftIndex, rightIndex: pair.rightIndex }, H.row);
      if (pair.left) rowComments(pair.left);
      if (pair.right) rowComments(pair.right);
    }
  } else {
    rows.forEach((row, index) => addRow(row, index));
  }
  return { items, sizes };
}

// itemAt gives the item at a height of the list: the last item that starts
// at or above it, or -1 for an empty list.
export function itemAt(origins, y) {
  let lo = 0;
  let hi = origins.length - 1;
  if (hi < 0) return -1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (origins[mid] <= y) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

// scrollLimit gives the most that the list scrolls: to its end, or further
// when the last file is short, so that its header can come to the top.
export function scrollLimit(built, viewH) {
  let last = 0;
  for (const range of built.files?.values() ?? []) last = Math.max(last, built.origins[range.start] ?? 0);
  return Math.max(0, built.total - viewH, last);
}

// scrollFor gives the scroll position that shows an item: "top" puts it at
// the top, "center" in the middle, and "nearest" moves only as far as the
// item and margin pixels around it need.
export function scrollFor(built, index, how, top, viewH, margin = 0) {
  const y = built.origins[index] ?? 0;
  const h = built.sizes[index] ?? 0;
  let next = top;
  if (how === "top") next = y;
  else if (how === "center") next = y + h / 2 - viewH / 2;
  else if (y - margin < top) next = y - margin;
  else if (y + h + margin > top + viewH) next = y + h + margin - viewH;
  return Math.min(Math.max(0, next), scrollLimit(built, viewH));
}

// selectionTargets gives the targets of a comment on the items from..to,
// one for each file in that range: its selected lines, with the range on
// the new side when it has new lines, else on the old side, and the lines
// quoted as a unified diff (removed lines before the added lines of a
// change, also from the split view), with the count of lines after the
// quote in more; or the whole file when the range holds none of its lines.
export function selectionTargets(items, from, to) {
  const order = [];
  const byFile = new Map();
  const quote = (entry, line) => {
    if (entry.excerpt.length < EXCERPT_LINES) entry.excerpt.push(line);
    else entry.more++;
  };
  const flush = (entry) => {
    for (const line of entry.added) quote(entry, line);
    entry.added = [];
  };
  for (let i = Math.max(0, from); i <= to && i < items.length; i++) {
    const item = items[i];
    if (!item?.file || item.part || item.t === "comment") continue;
    let entry = byFile.get(item.file.path);
    if (!entry) {
      entry = { path: item.file.path, old: null, new: null, excerpt: [], added: [], more: 0 };
      byFile.set(item.file.path, entry);
      order.push(entry);
    }
    let marked = [];
    if (item.t === "row") marked = [[item.row, item.row.k === "d" ? "-" : item.row.k === "a" ? "+" : " "]];
    else if (item.t === "split" && item.left === item.right) marked = [[item.left, " "]];
    else if (item.t === "split") marked = [[item.left, "-"], [item.right, "+"]];
    for (const [row, mark] of marked) {
      if (!row || (row.k !== "a" && row.k !== "d" && row.k !== "c")) continue;
      for (const side of row.k === "c" ? ["old", "new"] : [row.k === "d" ? "old" : "new"]) {
        const line = side === "old" ? row.o : row.n;
        if (!line) continue;
        const span = entry[side] ?? { from: line, to: line };
        span.from = Math.min(span.from, line);
        span.to = Math.max(span.to, line);
        entry[side] = span;
      }
      // A split row holds a removed and an added line: the added lines wait
      // until the removed lines of the change are quoted.
      if (item.t === "split" && mark === "+") entry.added.push(`+${rowText(row)}`);
      else {
        if (mark !== "-") flush(entry);
        quote(entry, `${mark}${rowText(row)}`);
      }
    }
  }
  return order.map((entry) => {
    flush(entry);
    const side = entry.new ? "new" : entry.old ? "old" : null;
    if (!side) return { path: entry.path };
    const target = { path: entry.path, side, from: entry[side].from, to: entry[side].to, excerpt: entry.excerpt };
    if (entry.more) target.more = entry.more;
    return target;
  });
}

// filesBetween gives the paths of the files of the tree from one file to
// another, both included, in the order of the tree.
export function filesBetween(treeRows, a, b) {
  const paths = treeRows.filter((row) => row.file).map((row) => row.file.path);
  const i = paths.indexOf(a);
  const j = paths.indexOf(b);
  if (i < 0 || j < 0) return paths.filter((path) => path === a || path === b);
  return paths.slice(Math.min(i, j), Math.max(i, j) + 1);
}

// stops are the items that the cursor stops on: the first visual line of a
// row, and every other item but gaps and comment lines after the first.
export function isStop(item) {
  if (!item || item.t === "gap") return false;
  if ((item.t === "row" || item.t === "comment") && item.part) return false;
  return true;
}

// nextIndex finds the next item after at (or before it, with step -1) that
// a test accepts.
export function nextIndex(items, at, step, test) {
  for (let i = at + step; i >= 0 && i < items.length; i += step) {
    if (test(items[i], i)) return i;
  }
  return -1;
}

// changeStart tells if an item starts a change: a removed or added row after
// a row that is not part of the same change.
export function changeStart(items, i) {
  const item = items[i];
  if (!item) return false;
  if (item.t === "noise" || item.t === "move") return true;
  const row = item.t === "split" ? (item.left ?? item.right) : item.row;
  if (!row || (row.k !== "d" && row.k !== "a") || item.part) return false;
  for (let j = i - 1; j >= 0; j--) {
    const before = items[j];
    if (before.t === "comment" || (before.t === "row" && before.part)) continue;
    const prior = before.t === "split" ? (before.left ?? before.right) : before.row;
    if (before.t === "move" && before.row.b === row.b) return false;
    return !prior || prior.b !== row.b || (prior.k !== "d" && prior.k !== "a");
  }
  return true;
}

// anchorIndex finds where an anchor key went after a reload: the same key,
// else the nearest row of the same file, else the same file header.
export function anchorIndex(built, key) {
  if (!key) return -1;
  if (key.startsWith("comment|")) return built.items.findIndex((item) => item.key === key);
  const cut = key.indexOf("|");
  const range = built.files?.get(key.slice(0, cut));
  if (!range) return -1;
  for (let i = range.start; i < range.end; i++) if (built.items[i].key === key) return i;
  const [kind, line] = key.slice(cut + 1).split("|");
  const target = Number(line);
  let best = -1;
  let distance = Infinity;
  for (let at = range.start; at < range.end && Number.isFinite(target); at++) {
    const item = built.items[at];
    if (item.part) continue;
    const row = item.row ?? item.right ?? item.left;
    const value = row ? (kind === "d" ? row.o : row.n) : 0;
    if (value && Math.abs(value - target) < distance) {
      distance = Math.abs(value - target);
      best = at;
    }
  }
  return best >= 0 ? best : range.start;
}

// fileTree gives the rows of the file tree: folders with one child folder
// join into one row, as GitHub shows them.
export function fileTree(files) {
  const root = { name: "", folders: new Map(), files: [] };
  for (const file of files ?? []) {
    const names = file.path.replace(/\/$/, "").split("/");
    let node = root;
    for (const name of names.slice(0, -1)) {
      if (!node.folders.has(name)) node.folders.set(name, { name, folders: new Map(), files: [] });
      node = node.folders.get(name);
    }
    node.files.push(file);
  }
  const rows = [];
  const walk = (node, depth, prefix) => {
    for (const folder of [...node.folders.values()].sort((a, b) => a.name.localeCompare(b.name))) {
      let name = folder.name;
      let current = folder;
      while (current.files.length === 0 && current.folders.size === 1) {
        current = [...current.folders.values()][0];
        name = `${name}/${current.name}`;
      }
      rows.push({ t: "folder", key: `folder|${prefix}${name}`, name, depth });
      walk(current, depth + 1, `${prefix}${name}/`);
    }
    for (const file of node.files) {
      rows.push({ t: "file", key: `tree|${file.path}`, name: file.path.replace(/\/$/, "").split("/").pop() + (file.path.endsWith("/") ? "/" : ""), depth, file });
    }
  };
  walk(root, 0, "");
  return rows;
}

// fuzzy scores how well a query matches a label: a higher score is better,
// and -1 is no match. The letters of the query must come in order.
export function fuzzy(query, label) {
  const q = String(query ?? "").toLowerCase();
  const text = String(label ?? "").toLowerCase();
  if (!q) return 0;
  let score = 0;
  let at = 0;
  let run = 0;
  for (const c of q) {
    const found = text.indexOf(c, at);
    if (found < 0) return -1;
    run = found === at ? run + 1 : 0;
    score += 1 + run * 2 - Math.min(3, found - at) * 0.1;
    if (found === 0 || "/._- ".includes(text[found - 1])) score += 2;
    at = found + 1;
  }
  return score - text.length * 0.01;
}

// jumpEntries lists the files and the changed entities of a diff for the
// jump list.
export function jumpEntries(diff) {
  const out = [];
  for (const file of diff?.files ?? []) {
    out.push({ kind: "file", label: file.path, detail: statusWord(file), file });
    for (const entity of file.entities ?? []) {
      out.push({ kind: "entity", label: entity.name, detail: `${entity.change} ${entity.kind} · ${file.path.split("/").pop()}`, file, entity });
    }
  }
  return out;
}

// statusWord names the status of a file for people.
export function statusWord(file) {
  if (file?.classes?.includes("folder")) return `${file.count} untracked files`;
  return { added: "Added", deleted: "Deleted", renamed: "Renamed", copied: "Copied", untracked: "Untracked", conflict: "Conflict", type: "Type changed" }[file?.status] ?? "Modified";
}

// statusLetter gives the one-letter mark of a file status.
export function statusLetter(file) {
  return { added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "U", conflict: "!", type: "T" }[file?.status] ?? "M";
}

// placeWords names the lines of a target: "line 4" or "lines 4-9", which
// are numbers in the file before the change for removed lines. A target
// without lines is the whole file.
export function placeWords(target) {
  if (!target?.from) return "whole file";
  const lines = target.from === target.to ? `line ${target.from}` : `lines ${target.from}-${target.to}`;
  return target.side === "old" ? `${lines} before the change` : lines;
}

// targetLabel names the place of a target: its file and its lines.
export function targetLabel(target) {
  return `${target.path} · ${placeWords(target)}`;
}

// reviewSubject names the work of a review for the agent: the change set,
// the branch and the repository.
export function reviewSubject(set, branch, repository) {
  const where = repository ? ` in ${repository}` : "";
  const on = branch?.detached ? ` on the checkout at ${shortSha(branch.tip)}` : branch?.name ? ` on \`${branch.name}\`` : "";
  if (set === "uncommitted") return `the uncommitted work${on}${where}`;
  if (set?.startsWith("commit:")) {
    const sha = set.slice(7);
    const subject = branch?.commits?.find((commit) => commit.sha === sha)?.subject;
    return `commit ${shortSha(sha)}${subject ? ` (${subject})` : ""}${on}${where}`;
  }
  if (set?.startsWith("range:")) {
    const [from, to] = set.slice(6).split("..");
    return `the commits ${shortSha(from)} to ${shortSha(to)}${on}${where}`;
  }
  if (branch?.detached) return `the detached checkout at ${shortSha(branch.tip)}${where}`;
  return branch?.name ? `the branch \`${branch.name}\`${where}` : `the work${where}`;
}

// diffBlock quotes the lines of a target as a fenced diff. The fence is
// longer than each run of backticks in the lines, so that no line ends it.
function diffBlock(target) {
  const body = [...(target.excerpt ?? [])];
  if (!body.length) return [];
  if (target.more) body.push(`… ${target.more} more ${target.more === 1 ? "line" : "lines"}`);
  let longest = 0;
  for (const line of body) for (const run of line.match(/`+/g) ?? []) longest = Math.max(longest, run.length);
  const fence = "`".repeat(Math.max(3, longest + 1));
  return [`${fence}diff`, ...body, fence];
}

// sortComments puts comments in the order of their first places: by file,
// then by line.
function sortComments(comments) {
  const first = (comment) => comment.targets[0] ?? { path: "", from: 0 };
  return comments.map(normalizeComment).sort((a, b) => {
    const x = first(a);
    const y = first(b);
    return x.path === y.path ? (x.from ?? 0) - (y.from ?? 0) : x.path.localeCompare(y.path);
  });
}

// commentPlaces names the places of a comment: one label for each target
// with lines, then one label for all its whole files. code formats a path.
function commentPlaces(targets, code = (path) => path) {
  const out = targets.filter((target) => target.from).map((target) => ({ target, label: `${code(target.path)} · ${placeWords(target)}` }));
  const whole = targets.filter((target) => !target.from).map((target) => code(target.path));
  if (whole.length) out.push({ target: null, label: `${whole.join(", ")} · ${whole.length === 1 ? "whole file" : "whole files"}` });
  return out;
}

// composeReview gives the message that carries the comments to the agent,
// in markdown as the conversation shows it: a line that names the work,
// then each comment in file order: its places, each with its lines in a
// diff block, then its text.
export function composeReview(comments, subject) {
  const lines = [`Review comments on ${subject}:`];
  for (const comment of sortComments(comments)) {
    lines.push("");
    for (const place of commentPlaces(comment.targets, (path) => `\`${path}\``)) {
      lines.push(place.label, ...(place.target ? diffBlock(place.target) : []));
    }
    lines.push(comment.text.trim());
  }
  return lines.join("\n");
}

// reviewSummary gives the places and the words of each comment of a review,
// in the order of the review, for the short preview of the composer chip.
export function reviewSummary(comments) {
  return sortComments(comments).map((comment) => ({ places: commentPlaces(comment.targets).map((place) => place.label), text: comment.text.trim() }));
}

// branchFacts gives the short facts of a branch for the navigator.
export function branchFacts(branch, now = Date.now()) {
  const facts = [];
  const pr = branch.pullRequest;
  if (pr) facts.push(`PR #${pr.number}${pr.attention && pr.state === "OPEN" ? ` · ${pr.attention}` : pr.state !== "OPEN" ? ` ${pr.state.toLowerCase()}` : ""}`);
  if (branch.ahead) facts.push(`${branch.ahead} commit${branch.ahead === 1 ? "" : "s"}`);
  if (branch.unpushed && branch.unpushed < branch.ahead) facts.push(`${branch.unpushed} unpushed`);
  else if (branch.unpushed && branch.ahead) facts.push("unpushed");
  if (branch.base?.stackedOn) facts.push(`on ${branch.base.stackedOn}`);
  if (branch.reason && !pr) facts.push(branch.reason);
  if (branch.lastActivity && now - branch.lastActivity > DAY) facts.push(ago(branch.lastActivity, now));
  return facts;
}
