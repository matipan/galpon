<p align="center">
  <img src="assets/galpon-logo.png" alt="Galpon" width="760">
</p>

# Galpon

**A terminal-first workstation for durable coding agents.**

Galpon gives each [Pi](https://github.com/earendil-works/pi) coding agent a
persistent identity, session, and Git worktree. It groups agents into
workspaces and gives them tools to exchange messages. Close a view, open the
agent again, and Galpon resumes the same Pi session.

Use Galpon to:

- manage several coding agents from one command center;
- give each agent a private Git worktree;
- group related agents in a workspace;
- fork an existing agent context without sharing its files;
- let agents send work and results to each other;
- open your real terminal and `$EDITOR` in the correct worktree.

Galpon does not include a browser, terminal emulator, editor, or diff viewer.

## Galpon in action

<p align="center">
  <img src="assets/galpon-herdr.png" alt="A Galpon Pi agent running in Herdr">
  <br>
  <em>A durable Galpon Pi agent in its Herdr workspace.</em>
</p>

<p align="center">
  <img src="assets/galpon-command-center.png" alt="The Galpon command center opened as a Herdr popup">
  <br>
  <em>The command center opened with Ctrl-K over the active agent.</em>
</p>

## Quick start

### 1. Install the requirements

You need Git, Go 1.26.5 or newer, Pi, and
[Herdr](https://herdr.dev/). Galpon uses Pi for agent conversations and Herdr
to open terminal workspaces.

Install Pi:

```bash
npm install -g --ignore-scripts @earendil-works/pi-coding-agent
pi
```

In Pi, run `/login` and sign in to OpenAI Codex. This is the default provider
in Galpon. You can select a different provider later with environment
variables.

Install Herdr on macOS or Linux:

```bash
curl -fsSL https://herdr.dev/install.sh | sh
```

See the [Herdr installation guide](https://herdr.dev/docs/install/) for other
installation methods.

### 2. Install Galpon

Install Galpon from a source checkout:

```bash
git clone https://github.com/matipan/galpon.git
cd galpon
go install ./cmd/galpon
```

Make sure that `$(go env GOPATH)/bin` is in your `PATH`, then verify the
installation:

```bash
galpon --version
```

### 3. Add Galpon to Herdr

```bash
galpon herdr install
herdr
```

The install command adds a marked Galpon block to your Herdr configuration. It
does not replace your other Herdr settings. It binds <kbd>Ctrl</kbd>+<kbd>K</kbd>
to an 88% by 88% Galpon popup.

If Herdr was already running, reload its configuration:

```bash
herdr server reload-config
```

You can also run `galpon` in any terminal to open the command center directly.

### 4. Create your first agent

Open the Galpon command center with <kbd>Ctrl</kbd>+<kbd>K</kbd>, then:

1. Press <kbd>r</kbd>. Add a local Git repository path or an SSH/HTTPS Git URL.
2. Press <kbd>w</kbd>. Create a workspace for the task.
3. Select the workspace and press <kbd>a</kbd>.
4. Enter an agent name and select its repository placement.
5. Press <kbd>Ctrl</kbd>+<kbd>S</kbd> to create the agent and open Pi.

Galpon creates a private managed worktree for the agent. Work in Pi as usual.
To resume the agent, open the command center, select the agent, and press
<kbd>Enter</kbd>.

## Command center keys

Start typing to search workspace, agent, worktree, and repository titles.

| Key | Action |
| --- | --- |
| <kbd>Enter</kbd> | Open the selected item |
| <kbd>r</kbd> | Add a repository |
| <kbd>R</kbd> | Add a named Git remote |
| <kbd>w</kbd> | Create a workspace |
| <kbd>a</kbd> | Create an agent in the selected workspace |
| <kbd>t</kbd> | Open a terminal in the selected placement |
| <kbd>e</kbd> | Open `$EDITOR` in the selected placement |
| <kbd>x</kbd> | Hide the selected item and its dependent items |
| <kbd>q</kbd> or <kbd>Esc</kbd> | Close the command center |

The footer in each form shows the keys that are available for that form.

## Main concepts

- **Repository:** A local Git checkout or remote Git URL. Galpon imports its
  remotes and creates a shared bare mirror. It does not change or delete the
  original checkout.
- **Workspace:** A group of agents that work on one task or project. A
  workspace can use one or more repositories.
- **Agent:** A durable Pi conversation with a file placement. An agent has one
  primary worktree and can also have secondary repository worktrees.
- **Placement:** The files that an agent can use. New placements are private by
  default. You can explicitly share another agent's exact placement.
- **Context fork:** A new Pi conversation that starts from another agent's
  context. A context fork does not change or share file placement.

Agents receive Galpon tools in Pi. These tools can create agents, delegate
work, send messages, check message state, wait for another agent, and clean up
agents that they created. Galpon records recursive creator lineage. On an
explicit cleanup request, the creator can close its descendants' Herdr tabs and
permanently remove their private worktrees and Pi sessions. A coordinator or
captain is a normal agent with instructions to coordinate the other agents.

## CLI examples

The TUI supports the usual workflow. The CLI supports scripts and repeatable
setup:

```bash
galpon repo add ~/code/my-project --title "My project"
galpon workspace create "Feature work"
galpon agent create "Implementer" \
  --workspace "Feature work" \
  --repo "My project" \
  --role implementer
galpon agent send <agent-id> "Implement the approved design"
galpon agent show <agent-id>
```

Use `galpon help` to see all commands. Repository and workspace commands accept
an ID or an exact title where applicable.

## State and cleanup

Galpon starts its local daemon when needed. The daemon continues to run after
the command center closes. State is stored in `~/.local/state/galpon` by
default.

Closing an agent pane stops that Pi process, but it does not delete the agent.
The next open action starts Pi with the same Galpon agent and Pi session.
Closing a Herdr workspace also does not delete the Galpon workspace.

Pressing <kbd>x</kbd> hides durable state. It does not immediately remove files.
To permanently remove hidden records, worktrees, sessions, and mirrors, run:

```bash
galpon cleanup
```

Cleanup never removes the original source checkout. It will ask you to stop a
hidden Pi process before it removes that agent's files. An agent can also use
`galpon_cleanup_created_agents` after an explicit user request. This cleanup is
recursive, never removes the calling agent, closes each managed Herdr tab, and
removes only descendant agents and worktrees that no surviving agent uses.

Stop the daemon with `galpon daemon stop`. The next `galpon` command starts it
again.

## Configuration

| Variable | Purpose | Default |
| --- | --- | --- |
| `GALPON_STATE_DIR` | State, database, socket, logs, and managed files | `~/.local/state/galpon` |
| `GALPON_PI_BIN` | Pi executable | `pi` |
| `GALPON_PI_PROVIDER` | Pi provider | `openai-codex` |
| `GALPON_PI_MODEL` | Pi model override | Provider default |
| `GALPON_HERDR_BIN` | Herdr executable | `herdr` |

Galpon uses your existing Pi provider login. It does not copy or store your Pi
credentials.

## Development

Build from a source checkout:

```bash
go build ./cmd/galpon
```

Run the test suites:

```bash
go test ./...
go test ./e2e -count=1
```

The end-to-end suite uses the real Pi and Herdr binaries with a local mock model
endpoint. It does not call a paid model.

## Terminal frontends

Herdr is a terminal frontend for Galpon, and tmux can be another frontend.
These frontends only present Galpon workspaces, agents, and terminals. Galpon
currently implements the Herdr frontend only. It does not implement a tmux
frontend yet.
