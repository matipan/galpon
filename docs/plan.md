# Native Plan mode

## User flow

1. Enter `/plan`. Galpon enables planning without sending a model prompt.
2. Enter your task. The model can inspect files, search, and ask questions.
3. The model submits a complete plan through `plan_mode_complete`. Galpon saves
   its exact text, revision ID, and SHA-256 hash in the agent's Pi session.
4. After the turn settles, that revision opens in native Neovim Review.
5. Select text and add comments. Press Esc, then Enter to save a comment.
   Prepare feedback to put it in the Pi editor. Review never sends it.
6. Send the feedback when ready. New input makes the previous plan unready.
   The model must submit a complete replacement, not a partial update.
7. Choose `/plan do` or `/plan delegate` when the revision is ready.

A chat reply alone is not a saved plan. The completion tool accepts at most
48 KiB and 2048 lines. It must be the only tool call in its response.

Review remains optional and needs explicit `galpon review setup` on Linux with
Neovim 0.11 or newer. Missing setup does not discard the plan or install packages.
After setup, use `/plan review`. In RPC and print modes, plans are saved but no
terminal Review opens automatically. Foreground delegation requires a terminal.

## Tools and authority

Plan mode changes the tools sent to the model. It permits `read`, `grep`,
`find`, `ls`, the configured web search/fetch tools, Galpon tools, and the Plan
completion tool. General shell, editing, TODO, MCP execution, and other custom
execution tools are excluded. A tool-call check also rejects unavailable tools.
Normal tool selection is restored on exit or in-place implementation.

Galpon tools can change agent state. Plan mode is not a security sandbox.
The model must not use other agents to bypass planning restrictions. Agent
creation, contact, delegated work, and cleanup still need explicit user requests.
Entering Plan mode does not grant that permission. Questions use normal chat;
there is no separate Plan question form or ready menu.

## Implementation

`/plan do` restores normal tools and queues the exact approved revision as a
user message in the current agent. It keeps the durable agent and Pi session.
The command uses a revision-specific message key so retries do not queue the
same work twice. Approval is saved before dispatch. If the response is lost,
normal tools stay active because the work may already be queued. Retry
`/plan do`, also after reload, to confirm the same request without duplicating it.

`/plan delegate` opens the existing foreground New Agent form. Defaults are:

- The planning agent's workspace.
- All primary and secondary repositories, in their existing order.
- Each repository's configured `main` or `master`; otherwise `main`.
- The configured default remote, fetched before private worktree creation.
- A fresh conversation, not a context fork.
- A title from the plan, which you can edit.

The form keeps normal placement choices. No planner branch, dirty files,
untracked files, or external directory is copied implicitly. A source without
repository worktrees must select a placement. A missing remote branch fails;
Galpon must not substitute a stale local branch. Canceling before you select
Create keeps Plan mode and creates no agent. After creation starts, it can
finish even if you close the form; retry to recover that launch.

The implementer is a normal durable foreground agent. It has no delegated
request, creator relationship, or obligation to send a result to the planner.
The exact plan is queued before its view opens. It reports to the user in its
own conversation. The planner keeps the revision and implementer identity.
Both implementation commands refuse to discard any unsent editor text,
including whitespace-only text.

## Recovery

Plan state uses owner-scoped `galpon:plan:v1` Pi entries. Reload and session-tree
changes restore the matching state and tools. A context fork does not activate
another agent's saved Plan state. Reload does not reopen an old Review by itself.
Use `/plan review` to reopen it. Review drafts keep their exact revision identity
and use the normal Review recovery rules.

Foreground launch reservations live in the Galpon database and checkpoints.
Checkpoint format 4 includes these reservations and prevents older readers
from silently dropping them. Formats 1–3 remain readable.
Creation and its saved marker commit together. If the terminal closes or a
response is lost, retry `/plan delegate`. A completed creation reuses its agent
and initial message; it does not create another agent. Placement edits on that
retry do not replace an agent that was already created. A deleted target is not
silently recreated: restore it or submit a new plan revision.

`/plan exit` leaves planning and keeps the saved revision. It does not approve
or implement it. New plans clear the previous implementer link.

## Migration

Galpon no longer provisions `@narumitw/pi-plan-mode`. Package setup removes its
registered installation, including filtered entries, to keep one `/plan` owner.
Existing third-party configuration files and session entries are preserved.
Native Plan does not import or execute their saved plans. Re-submit a plan in
the native workflow if needed. Other Pi packages and settings remain unchanged.
Existing Pi sessions need `/reload` after the update.
