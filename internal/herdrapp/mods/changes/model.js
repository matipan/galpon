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

// buildItems makes the items of the diff list. view holds the folded files,
// the opened noise changes, the comments and the file of one-file mode;
// layout holds the columns of a line, split or unified, and wrap.
export function buildItems(diff, view, layout) {
  const items = [];
  const sizes = [];
  const push = (item, size) => {
    items.push(item);
    sizes.push(size);
  };
  const files = diff?.files ?? [];
  const comments = view.comments ?? [];
  files.forEach((file, fileIndex) => {
    if (view.oneFile && file.path !== view.oneFile) return;
    if (items.length) push({ t: "gap", key: `${file.path}|gap` }, H.gap);
    push({ t: "file", key: `${file.path}|file`, file, fileIndex }, H.file);
    const collapsed = view.collapsed?.has(file.path) || (file.viewed && !view.expanded?.has(file.path));
    if (collapsed) return;
    if (file.entities?.length) push({ t: "entities", key: `${file.path}|entities`, file }, H.entities);
    for (const [index, notice] of (file.notices ?? []).entries()) push({ t: "note", key: `${file.path}|note|${index}`, file, text: notice }, H.note);
    if (file.deferred) {
      push({ t: "load", key: `${file.path}|load`, file }, H.note);
      return;
    }
    const rows = file.rows ?? [];
    const noise = noiseBlocks(rows);
    const shownNoise = new Set();
    const anchored = new Map();
    for (const comment of comments) {
      if (comment.path !== file.path) continue;
      const key = `${comment.side}|${comment.to}`;
      anchored.set(key, [...(anchored.get(key) ?? []), comment]);
    }
    const addComments = (row) => {
      const side = row.k === "d" ? "old" : "new";
      const line = row.k === "d" ? row.o : row.n;
      for (const comment of anchored.get(`${side}|${line}`) ?? []) {
        const count = parts(comment.text.length + 2, layout.cols, true);
        for (let part = 0; part < count; part++) push({ t: "comment", key: `comment|${comment.id}|${part}`, comment, part, parts: count }, H.comment);
      }
    };
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
      const length = rowText(row).length;
      const count = parts(length, layout.cols, layout.wrap);
      for (let part = 0; part < count; part++) {
        push({ t: "row", key: part ? `${rowKey(file.path, row)}|${part}` : rowKey(file.path, row), file, row, index, part, parts: count }, H.row);
      }
      addComments(row);
    };
    if (layout.split) {
      for (const pair of splitPairs(rows)) {
        if (pair.full) {
          if (pair.full.k === "c") {
            push({ t: "split", key: rowKey(file.path, pair.full), file, left: pair.full, right: pair.full, leftIndex: pair.index, rightIndex: pair.index }, H.row);
            addComments(pair.full);
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
        if (pair.left) addComments(pair.left);
        if (pair.right) addComments(pair.right);
      }
    } else {
      rows.forEach((row, index) => addRow(row, index));
    }
  });
  const index = new Map();
  items.forEach((item, at) => index.set(item.key, at));
  return { items, sizes, index };
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
  if (built.index.has(key)) return built.index.get(key);
  const [path, kind, line] = key.split("|");
  const target = Number(line);
  let best = -1;
  let distance = Infinity;
  built.items.forEach((item, at) => {
    if (item.file?.path !== path || item.part) return;
    const row = item.row ?? item.right ?? item.left;
    if (!row) return;
    const value = kind === "d" ? row.o : row.n;
    if (!Number.isFinite(target) || !value) return;
    if (Math.abs(value - target) < distance) {
      distance = Math.abs(value - target);
      best = at;
    }
  });
  if (best >= 0) return best;
  return built.index.get(`${path}|file`) ?? -1;
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

// commentExcerpt gives the lines of a file that a comment is about.
export function commentExcerpt(file, side, from, to) {
  const out = [];
  for (const row of file?.rows ?? []) {
    const line = side === "old" ? (row.k === "d" || row.k === "c" ? row.o : null) : (row.k === "a" || row.k === "c" ? row.n : null);
    if (line && line >= from && line <= to) out.push(rowText(row));
  }
  return out;
}

// composeReview gives the message that carries the comments to the agent:
// one quote of the lines and the comment for each, in file order.
export function composeReview(comments, context) {
  const sorted = [...comments].sort((a, b) => (a.path === b.path ? a.from - b.from : a.path.localeCompare(b.path)));
  const lines = [`Review comments on ${context}:`];
  for (const comment of sorted) {
    const where = comment.from === comment.to ? `${comment.from}` : `${comment.from}-${comment.to}`;
    lines.push("", `${comment.path}:${where} (${comment.side === "old" ? "old side" : "new side"})`);
    for (const line of comment.excerpt ?? []) lines.push(`> ${line}`);
    lines.push(comment.text.trim());
  }
  return lines.join("\n");
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
