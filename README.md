# Galpon

Galpon is a terminal-first workstation for durable coding agents. A local
service owns workspaces, repository mirrors, worktrees, agent identities, and
the cross-agent message queue. Pi owns each agent conversation and session.
Herdr is the first terminal renderer. It can be replaced without changing
durable Galpon state.

Galpon does not contain a browser, terminal emulator, editor, or diff viewer.
It opens your real terminal tools in the correct managed worktree.

## Current vertical slice

- Durable SQLite state under `~/.local/state/galpon`.
- Native Git mirrors and managed worktrees.
- Empty coordination workspaces that can contain many independent agent
  placements.
- One ordered placement for each agent: one primary worktree and optional
  secondary repository worktrees.
- Private placement copies by default, with explicit exact sharing.
- Optional agent roles and Pi context forks that do not change file placement.
- One interactive Pi session for each durable agent.
- Exact Pi session resume after a pane or process stops.
- General Pi tools for repositories, workspaces, agents, delegation, message
  status, and waiting for another agent. A captain is a normal agent with a
  suitable prompt.
- A flat `tokyonight-moon` command center and a matching Pi theme, based on
  Erik's Neovim Telescope and statusline highlights.
- A title-only fuzzy switcher grouped by workspaces, agents, worktrees, and repositories.
- Durable soft deletion from the switcher, with explicit permanent cleanup.
- Herdr workspace and named tab creation for agents, shells, and `$EDITOR`.
- A direct Ctrl-K Herdr popup binding.

## Build and install

```bash
go build ./cmd/galpon
go install ./cmd/galpon
galpon herdr install
herdr server reload-config
```

`galpon herdr install` only adds a marked Galpon block. It does not replace
other Herdr settings. Ctrl-K opens an 88% by 88% popup.

Run `galpon` from any terminal to open the same command center without Herdr.
The daemon starts automatically and continues after the TUI closes.

## First use

You can do all common setup in the TUI:

- `r` adds a local Git repository or a Git SSH/HTTPS URL.
- `R` adds a named remote to the selected repository.
- `w` creates an empty coordination workspace.
- `a` opens the complete agent form. It selects the role, context, and
  placement. A new placement can contain a primary and secondary repositories.
- `Enter` opens the selected Pi agent. On a workspace, it selects an agent
  placement for a terminal.
- `t` opens a terminal in the selected agent placement. When the placement has
  secondary repositories, Galpon asks which worktree to use.
- `e` opens `$EDITOR` through the same placement selection.
- `x` hides the selected item. Workspace, repository, worktree, and agent
  dependencies are hidden together.

Pi provides the conversation UI, tool call UI, input handling, and approval UI.
Galpon loads its Pi extension and its `tokyonight-moon` theme for every agent.
It uses your existing Pi provider login. Galpon does not copy or store your Pi
credentials. The default provider is `openai-codex`.

The CLI also exposes setup operations for scripts:

```bash
galpon repo add /path/to/repository
galpon repo add git@github.com:owner/repository.git
galpon repo add git@github.com:upstream/project --remote fork=git@github.com:you/project --push-remote fork
galpon repo remote add <repository-id-or-title> fork git@github.com:you/project --push-default
galpon repo remote list <repository-id-or-title>
galpon workspace create "Search redesign"
galpon agent create "Implementation A" --workspace <workspace-id> --role implementer --repo <repository-id>
galpon agent create "Implementation B" --workspace <workspace-id> --context-agent <agent-a> --repo <repository-id>
galpon agent create "Reviewer" --workspace <workspace-id> --placement-agent <agent-a> --share
galpon agent create "Coordinator" --workspace <workspace-id> --cwd /path/to/directory
galpon agent open <agent-id>
galpon agent send <agent-id> "Implement the approved design"
galpon cleanup
```

Galpon imports every remote when you add an existing local checkout. For a Git
URL, it creates `origin`. Named remotes are stored on the Galpon repository and
configured on its shared bare mirror, so every managed worktree sees the same
fetch URLs, push URLs, and `remote.pushDefault` value.

## State and process life

Closing a Herdr workspace does not archive the Galpon workspace or delete its
agent placements. Closing an agent pane stops that Pi process. The next open
action starts Pi with the same Galpon agent ID, placement, and Pi session.

Stop the service with:

```bash
galpon daemon stop
```

The next `galpon` command starts the service again. Pi processes in Herdr panes
can stay active while the service restarts. Their extension reconnects to the
same Unix socket.

Deleting an item with `x` only hides durable state. `galpon cleanup`
permanently removes hidden database records, managed worktrees, agent session
directories, and hidden repository mirrors. It never removes the original
source checkout. Cleanup asks you to stop any hidden Pi process that is still
active before it removes files.

For isolated development or tests, set `GALPON_STATE_DIR`. Set
`GALPON_PI_BIN` or `GALPON_HERDR_BIN` to select specific binaries. Set
`GALPON_PI_PROVIDER` and `GALPON_PI_MODEL` to override the default Pi model.

## Verification

```bash
go test ./...
go test ./e2e -count=1
```

The end-to-end test runs the real Pi and Herdr binaries against a local mock
Responses API. It tests launch, durable resume, and cross-agent communication.
It does not use a paid model endpoint.
