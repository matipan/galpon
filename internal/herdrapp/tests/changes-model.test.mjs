import { test } from "node:test";
import assert from "node:assert/strict";
import {
  anchorIndex, buildItems, changeStart, composeReview, fileTree, fuzzy, isStop, nextIndex, rangeSet, setLabel, splitPairs, workGroups,
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
