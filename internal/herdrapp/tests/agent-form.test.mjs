import test from "node:test";
import assert from "node:assert/strict";
import {createForm} from "../mods/galpon/forms.js";
import {repositoryURL, repositorySourceKey} from "../mods/galpon/sources.js";

const data = () => ({
  catalog: {
    defaultHarness: "pi-app", harnesses: [{value:"pi-app", label:"Pi app"}],
    workspaces: [{id:"workspace", title:"Platform", status:"active"}],
    repositories: [{id:"repository", title:"Widgets", defaultBranch:"trunk", defaultRemote:"origin", remotes:[
      {name:"origin", fetchUrl:"https://example.test/fork/widgets.git"},
      {name:"upstream", fetchUrl:"git@github.com:Acme/Widgets.git"},
    ]}], agents:[], worktrees:[],
  },
  agent: {name:"", harness:"pi-app", workspaceId:"workspace", placement:0, worktrees:[
    {repositoryId:"repository", remote:"origin", ref:"trunk", fetchFirst:true},
  ]},
});
function select(form, key, query, index = 0) {
  const choice = form.choice(key);
  const option = choice.filter(query)[index];
  assert.ok(option, `no choice for ${query}`);
  form.choose(key, option.value, option);
  return option;
}

test("existing title searches and exact repository URLs retain selected resources", () => {
  const form = createForm("agent", data());
  assert.equal(select(form, "workspace", "plat").value, "workspace");
  assert.equal(select(form, "repository:0", "wid").value, "repository");
  assert.equal(select(form, "repository:0", "https://github.com/acme/widgets").value, "repository");
  const payload = form.payload();
  assert.equal(payload.workspaceId, "workspace");
  assert.equal(payload.workspaceTitle, "");
  assert.deepEqual(payload.worktrees[0], {
    repositoryId:"repository", repositoryUrl:"", remote:"upstream", ref:"trunk", fetchFirst:true,
  });
  assert.equal(form.choice("repository:0").filter("example.test").length, 0);
});

test("new values stay in the draft and can be edited or replaced by selections", () => {
  const form = createForm("agent", data());
  assert.equal(select(form, "workspace", "  Research  ").input, true);
  assert.equal(select(form, "repository:0", "git@github.com:acme/new-project.git").input, true);
  let payload = form.payload();
  assert.equal(payload.workspaceTitle, "Research");
  assert.equal(payload.workspaceId, "");
  assert.equal(payload.worktrees[0].repositoryUrl, "git@github.com:acme/new-project.git");
  assert.equal(payload.worktrees[0].repositoryId, "");
  assert.equal(payload.worktrees[0].ref, "");
  assert.equal(form.choice("workspace").input, "Research");
  assert.equal(form.choice("repository:0").input, payload.worktrees[0].repositoryUrl);
  assert.equal(form.fields().find(f=>f.key==="repository:0").value, payload.worktrees[0].repositoryUrl);
  select(form, "workspace", "platform");
  select(form, "repository:0", "widgets");
  payload = form.payload();
  assert.equal(payload.workspaceTitle, "");
  assert.equal(payload.worktrees[0].repositoryUrl, "");
  assert.equal(payload.worktrees[0].remote, "origin");
  assert.equal(payload.worktrees[0].ref, "trunk");
});

test("empty catalogs allow workspace names and primary and secondary URLs", () => {
  const fixture = data();
  fixture.catalog.workspaces = [];
  fixture.catalog.repositories = [];
  fixture.agent.workspaceId = "";
  fixture.agent.worktrees = [{repositoryId:"", remote:"", ref:"", fetchFirst:true}];
  const form = createForm("agent", fixture);
  select(form, "workspace", "My work");
  select(form, "repository:0", "https://example.test/first.git");
  assert.equal(form.add(), "");
  select(form, "repository:1", "https://example.test/second.git");
  assert.deepEqual(form.payload().worktrees.map(w=>w.repositoryUrl), ["https://example.test/first.git", "https://example.test/second.git"]);
  assert.equal(form.remove("repository:1"), true);
  assert.equal(form.payload().worktrees.length, 1);
});

test("workspace suggestions preserve partial matches and do not create exact matches", () => {
  const form = createForm("agent", data());
  assert.deepEqual(form.choice("workspace").filter("plat").map(o=>[o.value,Boolean(o.input)]), [["workspace",false],["plat",true]]);
  assert.deepEqual(form.choice("workspace").filter(" PLATFORM ").map(o=>o.value), ["workspace"]);
  assert.equal(form.choice("workspace").filter("   ").some(o=>o.input), false);
  assert.equal(form.choice("repository:0").filter("ext::false").length, 0);
});

test("source identity keeps hosts, accounts and local paths distinct", () => {
  assert.equal(repositorySourceKey("git@github.com:Acme/Widgets.git"), repositorySourceKey("https://github.com/acme/widgets/"));
  assert.equal(repositorySourceKey("file:///home/user/code/widgets"), repositorySourceKey("/home/user/code/widgets"));
  assert.notEqual(repositorySourceKey("file:///home/user/code/widgets.git"), repositorySourceKey("/home/user/code/widgets"));
  assert.notEqual(repositorySourceKey("https://notgithub.com/acme/widgets"), repositorySourceKey("https://github.com/acme/widgets"));
  assert.notEqual(repositorySourceKey("ssh://git@example.test/a"), repositorySourceKey("ssh://other@example.test/a"));
  for (const value of ["https://example.test/a.git", "git@example.test:a.git", "file:///home/code/a"]) assert.equal(repositoryURL(value), true);
  for (const value of ["a", "ext::false", "/home/code/a", "https://example.test/a?token=x"]) assert.equal(repositoryURL(value), false);
});
