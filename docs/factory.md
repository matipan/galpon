# Galpon Factory

Factory is a separate full-screen interface for low-touch feature work. Start it with:

```sh
galpon factory
```

Factory starts its durable sidecar when needed. You can also manage it directly:

```sh
galpon factory start
galpon factory status
galpon factory restart
galpon factory stop
```

## Operator console

Factory uses the same flat console design as Galpon Control. It sorts work into **Needs You**, **Active**, **Queued**, and **Recently Shipped** groups. Canceled work appears separately and is not shown as shipped. Each queue row shows one state, title, next action, and age. Work that needs a human decision is first.

Terminals that are 168 columns or wider show the queue, selected feature, and live agent together. Terminals from 108 through 167 columns show equal queue and detail panes. Press `v` to replace detail with the live-agent view. Smaller terminals show one readable surface at a time. The live-agent view shows observed agent state, reported work, and changed files with added and removed line counts. The file summary refreshes every five seconds.

The footer changes with the selected feature. Press `/` to filter feature titles, `l` to open the collapsed semantic history, `?` for all actions, and `Esc` to move outward. Background refresh does not move queue rows. If a feature changes groups, press `Ctrl-R` to apply the new grouping.

## Workflow

1. Press `n` and describe the mission in the large feature brief editor. `Enter` adds a new line. Press `Tab`, select **Start**, and press `Enter` to launch. You can also press `Alt-Enter` from the brief.
2. Factory immediately adds the feature to **Active** and starts an ordinary background Galpon planner. Use the configuration surface only when you must change the repository, workspace, base ref, optional GitHub issue, or generated title.
3. When the plan is ready, press `e` to open it in native Neovim Review. Add annotations and press `s` to prepare them.
4. Factory shows the number of prepared annotations. Press `r` to send them to the same planner, or press `a` to approve a plan that does not need changes.
5. Factory starts an ordinary developer agent in a private worktree.
6. After the developer commits the result, Factory asks that same agent to prepare and verify a ready-to-use test environment for the exact commit. The developer installs project-local dependencies, builds artifacts, performs safe local setup, starts required development services, and verifies the test target when these steps apply. The handoff gives the operator a ready local or preview URL, a prebuilt CLI path, minimal feature-check steps, expected results, and cleanup commands. It does not ask the operator to build the project or start its services. Factory does not enable the test decision until this handoff is ready.
7. Perform only the feature checks in the handoff. Press `p` if the test passed or `f` to report a failure.
8. Factory starts three independent reviews for the same commit: general quality, simplicity, and cybersecurity.
9. Factory sends requested fixes to the developer. A new commit invalidates all earlier approvals and causes Factory to prepare and verify an updated test environment.
10. After all reviews approve the same commit, Factory pushes the branch, opens or finds a pull request, and watches GitHub checks.
11. After checks pass, press `m`, review the confirmation, and press `Enter` to approve the merge. Factory marks the work complete when GitHub reports the pull request as merged.

Press `o` to inspect the current agent through the existing Herdr flow. Press `x` to delete the selected feature, then press `Enter` to confirm. Factory stops active work before it removes the feature brief, plan, runs, and history. The normal Galpon agent and its worktree remain available. You can close the Factory screen at any time. The service and normal Galpon agents continue their work. Factory does not embed a terminal.

## GitHub

Factory uses the authenticated `gh` command. It does not store a GitHub token. Sign in before you use issue, pull request, check, or merge functions:

```sh
gh auth status
```

## Native Review

Native Review is optional. Prepare it once:

```sh
galpon review setup
```

Factory review runs use the existing pinned, offline Neovim Review runtime. Their isolated files are under `factory/review-runs/`.

## State and recovery

Factory state is separate from the Galpon database:

- `factory/factory.db`
- `factory/factory.sock`
- `factory/factory.pid`
- `factory/factory.lock`
- `factory/factory.log`
- `factory/review-runs/`

Factory metadata is not part of Galpon checkpoints. On restart, the reconciler reads the Factory database, inspects normal Galpon agent messages and repository state, and continues active features. Failed external operations put a feature in a blocked state. The detail surface explains the cause and the required correction. Press `e` to fix an empty brief. Press `r` to retry after you correct the cause. `Enter` only opens the selected feature; it does not retry or approve work.

## Boundaries

Factory does not change the Ctrl-K command center, communication protocol, agent roles, Plan or Review commands, Herdr bindings, cleanup behavior, checkpoint format, or dashboard format. Its only Galpon API extension is the namespaced `POST /v1/integrations/factory/agents` bridge. The bridge creates an ordinary background agent, queues its initial prompt, and starts it with one idempotent request.
