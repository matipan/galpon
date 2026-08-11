# Galpon

Galpon is a single-user, terminal-first workstation for durable coding agents.
The durable service owns repositories, named Git remotes, workspaces, worktrees, agents, and their
cross-agent message queue. Pi owns agent conversations and sessions. Terminal
renderers such as Herdr are replaceable view adapters.

## Product boundaries

- Keep the product terminal-only. Do not add a browser UI, browser panes, a
  terminal emulator, an editor, or a diff viewer.
- Opening a project tool means opening a real terminal at the managed worktree.
- A workspace and its agent remain durable when a terminal view closes.
- Herdr IDs are view references. They are not durable Galpon identities.
- A captain is not a special type. It is a normal agent that uses the normal
  cross-agent tools.
- This is a single-user system. Do not add multi-user or tenant logic.

## Interface

- The active Neovim `tokyonight-moon` visual system in `internal/tui/theme.go`
  is the base style for every terminal surface. Use the flat Telescope prompt,
  result, and selection bands plus the blue Mini statusline style. Do not add
  generic rounded cards.
- Search human-facing titles and labels only. Do not search conversation bodies,
  file contents, IDs, or paths.
- Keep switcher result groups clear and stable.

## Verification

Run `go test ./...` and `go test ./e2e -count=1`. The end-to-end suite uses the
real Pi and Herdr binaries with a local mock model endpoint. It must not call a
paid model.
