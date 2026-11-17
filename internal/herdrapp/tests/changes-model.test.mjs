import { test } from "node:test";
import assert from "node:assert/strict";
import {
  anchorIndex, buildItems, changeStart, composeReview, fileTree, filesBetween, fuzzy, isStop, itemAt, nextIndex, rangeSet, scrollFor,
  selectionTargets, setLabel, splitPairs, workGroups,
} from "../mods/changes/model.js";

const seg = (text) => [[text, 0]];
const file = (path, rows, extra = {}) => ({ path, status: "modified", rows, ...extra });

// A change: two removed lines, the first paired with the second added line.
const rows = [
  { k: "c", o: 1, n: 1, s: seg("package a") },
  { k: "d", o: 2, s: seg("x := 1"), b: 1, p: 5 },
  { k: "d", o: 3, s: seg("y := 2"), b: 1 },
  { k: "a", n: 2, s: seg("// new"), b: 1 },
  { k: "a", n: 3, s: seg("x := 10"), b: 1, p: 2 },
  { k: "c", o: 4, n: 4, s: seg("}") },
];

test("split pairs keep a paired line beside its pair and others alone", () => {
  const pairs = splitPairs(rows);
  const shape = pairs.map((p) => (p.full ? "full" : `${p.left ? p.left.o : "-"}/${p.right ? p.right.n : "-"}`));
  assert.deepEqual(shape, ["full", "-/2", "2/3", "3/-", "full"]);
});

test("unified items wrap long rows and keep keys that survive a reload", () => {
  const long = { k: "a", n: 9, s: seg("y".repeat(25)), b: 2 };
  const built = buildItems({ files: [file("a.go", [...rows, long])] }, { comments: [] }, { cols: 10, wrap: true, split: false });
  const wrapped = built.items.filter((item) => item.row === long);
  assert.equal(wrapped.length, 3);
  assert.equal(wrapped[0].key, "a.go|a|9");
  assert.equal(built.items.length, built.sizes.length);
  const again = buildItems({ files: [file("a.go", [{ k: "c", o: 1, n: 1, s: seg("package a") }, long])] }, { comments: [] }, { cols: 10, wrap: true, split: false });
  assert.equal(again.items[anchorIndex(again, "a.go|a|9")].row, long);
});

test("a change that is only format folds to one row until opened", () => {
  const noisy = [
    { k: "d", o: 1, s: seg("call(a, b)"), b: 1, f: 2 },
    { k: "a", n: 1, s: seg("call("), b: 1, f: 2 },
    { k: "a", n: 2, s: seg("  a, b)"), b: 1, f: 2 },
  ];
  const folded = buildItems({ files: [file("a.go", noisy)] }, { comments: [], noise: new Set() }, { cols: 80, wrap: true });
  assert.deepEqual(folded.items.map((item) => item.t), ["file", "noise"]);
  assert.equal(folded.items[1].count, 3);
  const opened = buildItems({ files: [file("a.go", noisy)] }, { comments: [], noise: new Set(["a.go|1"]) }, { cols: 80, wrap: true });
  assert.equal(opened.items.filter((item) => item.t === "row").length, 3);
});

test("a viewed file folds to its header and a comment follows its line", () => {
  const comment = { id: "c1", path: "a.go", side: "new", from: 3, to: 3, text: "Keep ten." };
  const built = buildItems({ files: [file("a.go", rows), file("b.go", rows, { viewed: true })] }, { comments: [comment] }, { cols: 80, wrap: true });
  const at = built.items.findIndex((item) => item.t === "comment");
  assert.equal(built.items[at - 1].row.n, 3);
  assert.equal(built.items.at(-1).t, "file");
  assert.equal(built.items.at(-1).file.path, "b.go");
});

test("next change stops on the first row of each change", () => {
  const built = buildItems({ files: [file("a.go", rows)] }, { comments: [] }, { cols: 80, wrap: true });
  const first = nextIndex(built.items, -1, 1, (_item, i) => changeStart(built.items, i));
  assert.equal(built.items[first].row.o, 2);
  assert.equal(nextIndex(built.items, first, 1, (_item, i) => changeStart(built.items, i)), -1);
  assert.equal(isStop(built.items[first]), true);
});

test("the file tree joins folders with one child folder", () => {
  const rowsOfTree = fileTree([{ path: "internal/changes/diff/a.go" }, { path: "internal/changes/b.go" }, { path: "README.md" }]);
  assert.deepEqual(rowsOfTree.map((row) => `${row.depth}:${row.name}`), ["0:internal/changes", "1:diff", "2:a.go", "1:b.go", "0:README.md"]);
});

test("fuzzy matching wants the letters in order and likes word starts", () => {
  assert.equal(fuzzy("xyz", "changes.go"), -1);
  assert.ok(fuzzy("chg", "changes.go") > 0);
  assert.ok(fuzzy("rows", "internal/changes/diff/rows.go") > fuzzy("rows", "internal/changes/diff/errorsx.go"));
});

test("a range of commits names the oldest and the newest", () => {
  const branch = { commits: [{ sha: "c3" }, { sha: "c2" }, { sha: "c1" }] };
  assert.equal(rangeSet(branch, "c3", "c1"), "range:c1..c3");
  assert.equal(rangeSet(branch, "c2", "c2"), "commit:c2");
  assert.equal(setLabel("range:c1..c3", branch), "3 commits");
});

test("review comments quote their lines in file order", () => {
  const text = composeReview([
    { path: "b.go", side: "new", from: 4, to: 5, text: "Second.", excerpt: ["x", "y"] },
    { path: "a.go", side: "old", from: 2, to: 2, text: "First.", excerpt: ["z"] },
  ], "galpon fix/x (branch total)");
  assert.equal(text, "Review comments on galpon fix/x (branch total):\n\na.go:2 (old side)\n> z\nFirst.\n\nb.go:4-5 (new side)\n> x\n> y\nSecond.");
});

test("work groups put ongoing branches first and done branches last", () => {
  const work = { repositories: [{ id: "r", title: "app", checkouts: [{ key: "w", branches: [
    { key: "live", name: "live", state: "live", lastActivity: 3 },
    { key: "old", name: "old", state: "unfinished", lastActivity: 1 },
    { key: "done", name: "done", state: "merged", lastActivity: 2 },
  ] }] }] };
  const groups = workGroups(work);
  assert.equal(groups.ongoing[0].branches[0].branch.key, "live");
  assert.equal(groups.unfinished[0].branch.key, "old");
  assert.equal(groups.done[0].branch.key, "done");
});

test("a file opens with its header at the top, also below the screen and at the end", () => {
  const files = ["a.go", "b.go", "c.go"].map((path) => file(path, rows));
  const built = buildItems({ files }, { comments: [] }, { cols: 80, wrap: true });
  const viewH = 100;
  for (const path of ["c.go", "a.go", "b.go"]) {
    const header = built.files.get(path).start;
    const top = scrollFor(built, header, "top", 0, viewH);
    assert.equal(top, built.origins[header]);
    assert.equal(itemAt(built.origins, top), header);
  }
  // Nearest moves only as far as the row needs.
  const last = built.items.length - 1;
  assert.equal(scrollFor(built, last, "nearest", 0, viewH), built.origins[last] + built.sizes[last] - viewH);
  assert.equal(scrollFor(built, 1, "nearest", 0, viewH), 0);
});

test("a selection across files comments on the lines of each file, or on a whole file", () => {
  const files = [file("a.go", rows), file("b.go", rows), file("c.go", rows)];
  const built = buildItems({ files }, { comments: [] }, { cols: 80, wrap: true });
  const from = built.items.findIndex((item) => item.file?.path === "a.go" && item.row?.k === "a" && item.row.n === 3);
  const to = built.files.get("c.go").start;
  const targets = selectionTargets(built.items, from, to);
  assert.deepEqual(targets.map((target) => [target.path, target.side, target.from, target.to]), [
    ["a.go", "new", 3, 4], ["b.go", "new", 1, 4], ["c.go", undefined, undefined, undefined],
  ]);
  assert.deepEqual(targets[0].excerpt, ["+ x := 10", "  }"]);
  const removedOnly = built.items.findIndex((item) => item.file?.path === "a.go" && item.row?.k === "d" && item.row.o === 3);
  assert.deepEqual(selectionTargets(built.items, removedOnly, removedOnly), [{ path: "a.go", side: "old", from: 3, to: 3, excerpt: ["- y := 2"] }]);
});

test("a comment on several files names each place and quotes its lines", () => {
  const text = composeReview([
    { id: "c1", text: "Use one helper for both.", targets: [
      { path: "b.go", side: "new", from: 2, to: 3, excerpt: ["+ x := 10"] },
      { path: "c.go" },
    ] },
  ], "galpon fix/x (branch total)");
  assert.equal(text, "Review comments on galpon fix/x (branch total):\n\nb.go:2-3 (new side)\n> + x := 10\nc.go (whole file)\nUse one helper for both.");
});

test("a build with the file cache gives the items of a build without it", () => {
  const files = [file("a.go", rows.map((row) => ({ ...row }))), file("b.go", rows.map((row) => ({ ...row })))];
  const diff = { files };
  const view = { comments: [], collapsed: new Set(), expanded: new Set(), noise: new Set() };
  const layout = { cols: 80, wrap: true };
  const cache = new Map();
  const shape = (built) => built.items.map((item, i) => `${item.key}:${built.sizes[i]}`);
  buildItems(diff, view, layout, cache);
  view.collapsed.add("a.go");
  view.comments = [{ id: "c1", text: "Why?", targets: [{ path: "b.go", side: "new", from: 3, to: 3 }] }];
  files[1].rows.splice(0, 1);
  files[1].version = 1;
  assert.deepEqual(shape(buildItems(diff, view, layout, cache)), shape(buildItems(diff, view, layout)));
  assert.deepEqual(shape(buildItems(diff, view, { cols: 4, wrap: true }, cache)), shape(buildItems(diff, view, { cols: 4, wrap: true })));
});

test("Shift picks the files of the tree between two files", () => {
  const tree = fileTree([{ path: "internal/a.go" }, { path: "internal/b.go" }, { path: "README.md" }]);
  assert.deepEqual(filesBetween(tree, "README.md", "internal/b.go"), ["internal/b.go", "README.md"]);
  assert.deepEqual(filesBetween(tree, "internal/a.go", "internal/a.go"), ["internal/a.go"]);
});
