// data.js: the sample records of the demo. Nothing here is real: no model,
// no daemon and no file. A reload starts again from these records.

const MIN = 60 * 1000;
const now = Date.now();

export const MODELS = [
  { id: "gpt-6-sol", name: "GPT-6 Sol", provider: "OpenAI", context: "400k" },
  { id: "claude-opus-5-5", name: "Claude Opus 5.5", provider: "Anthropic", context: "1M" },
  { id: "claude-sonnet-5", name: "Claude Sonnet 5", provider: "Anthropic", context: "1M" },
  { id: "gemini-3-5-pro", name: "Gemini 3.5 Pro", provider: "Google", context: "2M" },
];
export const EFFORTS = ["Low", "Medium", "High", "Max"];

export const WORKSPACES = [
  { id: "galpon", title: "Galpon" },
  { id: "engines", title: "Cloud Engines" },
  { id: "docs", title: "Docs" },
];

// A turn of an agent: what it said, the steps of its work and the facts of
// the turn. md is markdown (paragraphs, lists, `code`, **bold**, links and
// fenced code).
const meet = [
  { kind: "user", text: "What is Galpon?", at: now - 9 * MIN },
  {
    kind: "log", steps: [
      { icon: "brain", text: "Thought", detail: "Start with the parts that you see first." },
      { icon: "search", text: "Search", detail: "README.md · \"Herdr app\"" },
      { icon: "file-text", text: "Read", detail: "README.md" },
    ],
  },
  {
    kind: "text", md: `Galpon is a workstation for **durable coding agents**. It runs Pi, Claude Code and Codex agents in the [Herdr desktop app](https://github.com/herdrdev/herdr-app), and it keeps each agent, its files and its conversation in the correct place.

- **Agents stay.** Close a tab today and open it tomorrow: the same agent, the same conversation, the same files.
- **Each agent has its own worktree.** Galpon makes a private Git worktree for each agent, so agents do not change each other's files.
- **Workspaces** group the agents and the terminals of one project. This sidebar shows them, with the agents that need you first.
- **One command center.** Press \`Ctrl K\` to find any agent, pull request or workspace, and to act on it.
- **Agents work together.** An agent can delegate work to other agents and get their results. The **Agents** panel on the right shows that work.
- **Pull requests** show at the bottom of the sidebar, grouped by the next step, with the agent that opened each one.
- **The Changes view** shows the Git work of an agent beside its conversation, as a semantic diff. Press \`Alt C\`.

Everything here is a demo in your browser. Click around: open other agents, send a message, or press \`Ctrl K\`.`,
  },
  { kind: "meta", model: "GPT-6 Sol", effort: "High", took: "14s", tokens: "↑48k ↓1.2k", cost: "$0.09", at: now - 9 * MIN },
  { kind: "user", text: "How do I install it?", at: now - 6 * MIN },
  {
    kind: "text", md: `You need Git 2.45 or newer, Go 1.26.5 or newer and Pi. Claude Code and Codex are optional.

**1. Install the Herdr app and Pi.**

\`\`\`bash
curl -fsSL https://releases.herdr.dev/app/install.sh | sh
npm install -g --ignore-scripts @earendil-works/pi-coding-agent
\`\`\`

**2. Install Galpon** from a source checkout:

\`\`\`bash
git clone https://github.com/matipan/galpon.git
cd galpon
go install ./cmd/galpon
\`\`\`

**3. Add Galpon to the app.** Open the app once, then run:

\`\`\`bash
galpon herdr-app install
\`\`\`

In the app, press \`Ctrl Shift L\` to go to the Galpon layout, and \`Ctrl Alt N\` to start your first agent. The [README](https://github.com/matipan/galpon#readme) has the full guide.`,
  },
  { kind: "meta", model: "GPT-6 Sol", effort: "High", took: "6s", tokens: "↑51k ↓420", cost: "$0.03", at: now - 6 * MIN },
];

const flaky = [
  { kind: "user", text: "The Codex e2e test fails one run in ten. Find out why and fix it.", at: now - 4 * MIN },
];

// The live run of the flaky test agent: it plays when the demo starts.
export const FLAKY_RUN = {
  steps: [
    { icon: "brain", text: "Thought", detail: "A race between a delivery and a question is likely." },
    { icon: "search", text: "Search", detail: "internal/runtime · \"deliver(\"" },
    { icon: "file-text", text: "Read", detail: "internal/runtime/queue.go" },
    { icon: "terminal", text: "Run", detail: "go test ./e2e -run Codex -count=20" },
    { icon: "square-pen", text: "Edit", detail: "internal/runtime/queue.go", added: 6, removed: 2 },
    { icon: "square-pen", text: "Edit", detail: "e2e/codex_test.go", added: 3, removed: 1 },
    { icon: "terminal", text: "Run", detail: "go test ./e2e -run Codex -count=20 · 20 passed" },
  ],
  answer: `The test was flaky because a delivery could arrive while the agent still waited for an answer from you.

- \`deliver\` now queues a message while the agent asks a question.
- The queue drains when the answer arrives.
- The e2e test waits for the question before it delegates.

\`\`\`go
if r.waiting || r.asking {
    return r.queue(msg)
}
\`\`\`

\`go test ./e2e -run Codex -count=20\` passes 20 of 20 runs. The **Changes** button above shows the diff.`,
  meta: { model: "Claude Opus 5.5", effort: "High", took: "2m 41s", tokens: "↑312k ↓2.0k", cost: "$0.42" },
};

const release = [
  { kind: "user", text: "Draft the release notes for 0.9 from the merged pull requests.", at: now - 22 * MIN },
  {
    kind: "log", steps: [
      { icon: "terminal", text: "Run", detail: "gh pr list --state merged --limit 40" },
      { icon: "file-text", text: "Read", detail: "CHANGELOG.md" },
    ],
  },
  { kind: "text", md: "I found 14 merged pull requests since 0.8. Before I write, I need one decision from you." },
];

export const RELEASE_QUESTION = {
  title: "Which pull requests go in the notes?",
  options: [
    { label: "Only user-facing changes", detail: "Skip refactors, tests and CI" },
    { label: "All 14 pull requests", detail: "One line each, grouped by area" },
  ],
};

const scheduler = [
  { kind: "delivery", type: "request", sender: "Platform lead", body: "Find out why the scheduler gives too few engines to large organizations at peak time.", at: now - 3 * 60 * MIN },
  {
    kind: "log", steps: [
      { icon: "file-text", text: "Read", detail: "scheduler/capacity.go" },
      { icon: "bot", text: "Delegate", detail: "Load test · Replay last week's peak" },
      { icon: "bot", text: "Delegate", detail: "Metrics · Collect engine wait times" },
    ],
  },
  { kind: "text", md: "I gave two parts to other agents: **Load test** replays last week's peak, and **Metrics** collects the wait times. I continue when their results arrive." },
  { kind: "meta", model: "GPT-6 Sol", effort: "Max", took: "1m 12s", tokens: "↑96k ↓1.8k", cost: "$0.21", at: now - 3 * 60 * MIN },
  { kind: "delivery", type: "result", sender: "Metrics", body: "Large organizations wait 41s on average at peak; small ones wait 6s. The capacity cap per organization is the cause.", at: now - 2 * 60 * MIN },
  { kind: "text", md: "The cap per organization is the cause. **Load test** still runs; I write the fix when it confirms the numbers." },
  { kind: "meta", model: "GPT-6 Sol", effort: "Max", took: "38s", tokens: "↑120k ↓640", cost: "$0.12", at: now - 2 * 60 * MIN },
];

const docs = [
  { kind: "user", text: "Add a page about agent placements to the docs site.", at: now - 26 * 60 * MIN },
  { kind: "log", steps: [{ icon: "square-pen", text: "Write", detail: "docs/placements.md", added: 84, removed: 0 }] },
  { kind: "text", md: "The new page `docs/placements.md` explains private worktrees, forks of another agent's placement, managed directories and shared worktrees, with one example each." },
  { kind: "meta", model: "Claude Sonnet 5", effort: "Medium", took: "52s", tokens: "↑40k ↓3.1k", cost: "$0.08", at: now - 26 * 60 * MIN },
];

// Agents, in the order of the sidebar inside their workspaces.
export const AGENTS = [
  {
    id: "meet", title: "Meet Galpon", workspace: "galpon", harness: "Pi", model: "GPT-6 Sol", effort: "High", state: "ready",
    at: now - 6 * MIN, folder: "~/galpon/agents/meet-galpon/galpon", branch: "main", cost: "$0.12", context: 6, transcript: meet,
    todos: [
      { subject: "Explain what Galpon is", done: true },
      { subject: "Explain how to install it", done: true },
      { subject: "Show you around", active: true },
    ],
  },
  {
    id: "flaky", title: "Fix the flaky Codex e2e test", workspace: "galpon", harness: "Claude Code", model: "Claude Opus 5.5", effort: "High", state: "working",
    at: now - 4 * MIN, folder: "~/galpon/agents/fix-flaky/galpon", branch: "fix/codex-e2e-race", cost: "$0.31", context: 18, transcript: flaky, live: true, changes: true,
    todos: [
      { subject: "Reproduce the failure", done: true },
      { subject: "Find the race", active: true },
      { subject: "Fix it and run the test 20 times" },
    ],
  },
  {
    id: "release", title: "Draft the release notes", workspace: "galpon", harness: "Codex", model: "GPT-6 Sol", effort: "Medium", state: "needs",
    at: now - 20 * MIN, folder: "~/galpon/agents/release-notes/galpon", branch: "docs/release-0.9", cost: "$0.05", context: 9, transcript: release, question: true,
  },
  {
    id: "scheduler", title: "Engine scheduler capacity", workspace: "engines", harness: "Pi", model: "GPT-6 Sol", effort: "Max", state: "idle",
    at: now - 2 * 60 * MIN, folder: "~/galpon/agents/scheduler/engines", branch: "fix/org-capacity-cap", cost: "$1.84", context: 41, transcript: scheduler,
    delegations: [
      { title: "Load test", state: "working", note: "Replays last week's peak", age: "12m" },
      { title: "Metrics", state: "done", note: "Large organizations wait 41s at peak", age: "2h" },
    ],
  },
  {
    id: "docs", title: "Docs site", workspace: "docs", harness: "Claude Code", model: "Claude Sonnet 5", effort: "Medium", state: "idle",
    at: now - 26 * 60 * MIN, folder: "~/galpon/agents/docs-site/docs", branch: "docs/placements", cost: "$0.08", context: 4, transcript: docs,
  },
];

// Terminals of a workspace that are not agents.
export const TERMINALS = [{ id: "term-galpon", title: "zsh", workspace: "galpon" }];

export const PULL_REQUESTS = [
  { id: "galpon#212", ref: "galpon#212", title: "Queue deliveries while an agent asks a question", group: "needs", agent: "flaky", age: "5m" },
  { id: "engines#488", ref: "engines#488", title: "Raise the per-organization capacity cap at peak", group: "needs", agent: "scheduler", age: "2h" },
  { id: "galpon#209", ref: "galpon#209", title: "docs: explain the Changes view", group: "agent", agent: "docs", age: "1d", failed: true },
  { id: "galpon#207", ref: "galpon#207", title: "Release notes for 0.8", group: "ready", agent: "release", age: "3d" },
];
export const PR_GROUPS = [
  { id: "needs", title: "Needs you" },
  { id: "agent", title: "Agent's turn" },
  { id: "ready", title: "Ready to merge" },
];

// The Git work that the Changes view shows: a branch of the flaky test agent
// with two commits and uncommitted work. Rows: [kind, old, new, text], kind
// c (context), d (removed), a (added), h (scope), f (fold of n lines). Words
// in {+...+} or {-...-} are the changed words.
export const CHANGES = {
  branch: "fix/codex-e2e-race",
  base: "main",
  files: [
    {
      path: "internal/runtime/queue.go", status: "M", added: 6, removed: 2, entities: ["~ func (*Runner).deliver", "+ func (*Runner).drainAfterAnswer"],
      rows: [
        ["f", 0, 0, "41 unchanged lines · func NewRunner, func (*Runner).Start"],
        ["h", 0, 0, "func (r *Runner) deliver(msg Message) error {"],
        ["c", 42, 42, "\tr.mu.Lock()"],
        ["c", 43, 43, "\tdefer r.mu.Unlock()"],
        ["d", 44, 0, "\tif {-r.waiting-} {"],
        ["a", 0, 44, "\tif {+r.waiting || r.asking+} {"],
        ["c", 45, 45, "\t\treturn r.queue(msg)"],
        ["c", 46, 46, "\t}"],
        ["d", 47, 0, "\treturn r.send(msg)"],
        ["a", 0, 47, "\t{+r.last = msg.ID+}"],
        ["a", 0, 48, "\treturn r.send(msg)"],
        ["c", 48, 49, "}"],
        ["c", 49, 50, ""],
        ["a", 0, 51, "// drainAfterAnswer sends the queued messages when the answer arrives."],
        ["a", 0, 52, "func (r *Runner) drainAfterAnswer() {"],
        ["a", 0, 53, "\tr.asking = false"],
        ["a", 0, 54, "\tr.drain()"],
        ["a", 0, 55, "}", true],
        ["f", 0, 0, "88 unchanged lines · func (*Runner).queue, func (*Runner).drain"],
      ],
    },
    {
      path: "e2e/codex_test.go", status: "M", added: 3, removed: 1, entities: ["~ func TestCodexDelegatesWhileAsking"],
      rows: [
        ["f", 0, 0, "212 unchanged lines"],
        ["h", 0, 0, "func TestCodexDelegatesWhileAsking(t *testing.T) {"],
        ["c", 213, 213, "\tagent := startCodex(t, env)"],
        ["d", 214, 0, "\ttime.Sleep({-500 * time.Millisecond-})"],
        ["a", 0, 214, "\t{+waitFor(t, agent, \"question\")+}"],
        ["a", 0, 215, "\tagent.answer(t, \"yes\")"],
        ["a", 0, 216, "\twaitFor(t, agent, \"delegated\")"],
        ["c", 215, 217, "\tassertDelivered(t, env, 1)"],
        ["c", 216, 218, "}"],
      ],
    },
    {
      path: "README.md", status: "M", added: 2, removed: 0, entities: ["~ section Delegation"],
      rows: [
        ["f", 0, 0, "1318 unchanged lines · section Agent tools and delegation"],
        ["c", 1319, 1319, "A delivery that arrives while the agent works waits in its queue."],
        ["a", 0, 1320, "A delivery that arrives while the agent asks you a question also waits."],
        ["a", 0, 1321, "It goes to the agent after your answer.", true],
        ["c", 1320, 1322, ""],
      ],
    },
  ],
};

// The scripted run that a message plays in any agent.
export function demoRun(prompt) {
  const words = prompt.toLowerCase();
  if (/(install|setup|set up|start)/.test(words)) {
    return {
      steps: [{ icon: "file-text", text: "Read", detail: "README.md · Quick start" }],
      answer: "Install the Herdr app, Pi and Galpon, then run `galpon herdr-app install`. Open **Meet Galpon** in the sidebar: its second answer has the commands.\n\nThis is a demo run: no model was called.",
    };
  }
  return {
    steps: [
      { icon: "brain", text: "Thought", detail: "Find where this lives before I change it." },
      { icon: "search", text: "Search", detail: "internal · \"" + prompt.split(/\s+/).slice(0, 3).join(" ") + "\"" },
      { icon: "file-text", text: "Read", detail: "internal/app/app.go" },
      { icon: "square-pen", text: "Edit", detail: "internal/app/app.go", added: 12, removed: 3 },
      { icon: "terminal", text: "Run", detail: "go test ./internal/app · ok" },
    ],
    answer: "Done. In Galpon, the agent does this work in its own worktree, and the **Changes** view shows the diff beside the conversation.\n\nThis is a demo run: no model was called and no file changed.",
  };
}
