# Agent harness contract

An agent's `kind` is `pi`, `claude`, or `codex`. Pi is the default. The value is
set at creation and cannot be changed. A different harness requires a new agent.
Placements can be copied or shared across harnesses. Native session context must
not be passed to another harness.

## Requirements and use

Pi is the default for CLI and tool creation, Plan, and Factory. Select `claude`
or `codex` through `galpon agent create --harness`, the command center creation
form, Companion, or the `harness` field of `galpon_create_agent`.
A context fork inherits the source harness. A copied file placement does not.

The native adapters are tested with Claude Code 2.1.278 and Codex 0.155.1. Their
channel, queue, remote terminal, and app-server interfaces are preview APIs.
Install and authenticate these CLIs yourself. Galpon uses their own model and
permission settings. Galpon resolves `pi`, `claude`, and `codex` through `PATH`
at launch. It does not install, pin, upgrade, downgrade, or replace these binaries.
You can upgrade any harness independently. An incompatible API produces an error;
Galpon must not change the binary or select another harness to work around it.
Version pins apply only to isolated test containers, not to user installations.
Galpon does not change harness account settings. Its Pi extension package setup
is separate from management of the Pi executable.

Background launches use the daemon's inherited `PATH`. Foreground launches use
the renderer pane's `PATH`. If you change `PATH`, restart the relevant component
with that environment. Replacing an executable at the same path affects later
launches, not processes that are already running. A Codex runtime reuses its
resolved executable path for the app-server and its terminal client.

Claude foreground operation requires its local development channel confirmation
and the account's channel feature. Do not disable privacy controls or bypass
organization policy to obtain this feature. A headless Claude agent does not
need channels. Queued image deliveries are supported in headless Claude and
Codex; Claude's foreground channel accepts text only. Paste an image into the
native terminal, or use a background agent.

Native harness task lists remain local. Pi Work Dock, `/plan`, `/review`,
`/operations`, and Pi extension tools are not installed into another harness.
Claude Code and Codex expose the durable Galpon tools over MCP. The daemon
preapproves only its own Codex MCP tools; other approvals remain native. A
background Codex approval request fails with an instruction to open the native
terminal. It does not silently approve a command or change the sandbox.

Use Galpon to create, fork, import, or open another agent. Native session-switch
commands are not supported inside a managed agent. Blocking Claude Stop hooks
and Stop-hook continuation are not supported by the foreground adapter. Do not
configure them for a managed Claude agent: the adapter correlates Stop with the
saved final response of that prompt. Native subagent histories and external
harness databases are not part of the managed conversation archive.

## Ownership

Galpon owns agent identity, placements, durable operations, messages, results,
and progress. The harness owns authentication, models, coding tools, permissions,
and its terminal interface. Claude and Codex must not receive Pi provider or
model settings. Missing executables and unavailable integration capabilities are
errors; they must not cause a fallback to another harness.

Pi uses its extension and keeps its existing TODO integration. Claude and Codex
use an agent-scoped MCP bridge. Their local task lists do not create Galpon TODO
ownership links. A Pi sender can still link its own TODO to work done by another
harness.

## Runtime contract

Only one runtime can write an agent's native session. A runtime uses the existing
prepared registration and protocol-generation fence. Each active operation also
has a runtime and attempt fence. Tools inherit these identities from the runtime;
the model cannot choose them.

A runtime must persist its delivery identity before it submits a prompt. Native
prompt or turn IDs identify the corresponding reply. An unrelated human reply
must not complete a delegated assignment. Interruptions and errors are not
successful replies.

Sending a channel notification or receiving a tool result is not a persistence
acknowledgement. Receipt presentation and explicit result observation require
matching evidence in the saved native conversation. Final replies must be saved
before the runtime submits them to Galpon. Runtime recovery must use the current
attempt fence, not an earlier runtime's identity. Delivery remains at least once
across process failure.

Claude's foreground integration uses MCP channels and lifecycle hooks. Channels
are a research preview and require feature availability, interactive consent for
the local development channel, and any applicable organization approval. Galpon
must not change privacy settings or bypass organization policy to enable them.
The headless integration uses streaming JSON. Saved channel entries are marked
as metadata by Claude; only entries from Galpon's own channel can open a delivery
turn. Prompt IDs and saved UUID ancestry separate human and delegated replies.
Compaction summaries do not open user turns. A compaction boundary uses its
saved parent or prompt identity, or one unambiguous unfinished turn. An ambiguous
final response fails after a bounded evidence wait; it cannot renew forever.
Codex retains all saved user inputs in a turn so steering cannot erase delivery
evidence.

A delivery that has no saved input is withdrawn after the evidence deadline.
If the adapter cannot prove withdrawal, it stops the writer before reporting
failure. Expired channel payloads cannot become direct operations. A blocked
Claude user prompt with no saved input releases its provisional ownership.

Codex uses app-server for structured turns and the native remote terminal for
interaction. Both connections refer to the same thread. The runtime must not
run a second independent Codex writer for that thread. Deliveries enter Codex's
thread queue with stable client message IDs. Starting a queued entry does not
steer a human turn already in progress. New threads use complete JSONL history
so that the managed snapshot can resume without the source host's history database.

The controller and a separate supervisor hold an exclusive session lock. The
native writer and its tool children never inherit that lock. A start pipe holds
the writer until the supervisor saves its process-group identity. Controller exit
closes a private pipe; the supervisor then stops the writer's process group.
Detached tool processes can remain alive without holding session ownership.

Linux also sends a parent-death signal to the writer launcher. On all supported
systems, a replacement checks the saved writer group while it holds the session
lock. If a supervisor failure leaves that group alive, startup fails with its
group ID instead of admitting a second writer. Galpon does not signal a possibly
reused group ID. Inspect or stop the reported group before reopening the agent.
Normal handoff waits up to eight seconds for lock and group release.
The journal and managed transcript contain only complete, synced records. A replacement controller reclaims work with current runtime and
attempt fences. A saved final response can complete recovered work without a
second model turn. Missing saved input causes redelivery, not invented evidence.

Conversation export/import preserves the harness and creates a separate native
session on first start. Claude restores a compatible complete-line prefix and
rejects divergent histories. Codex resumes or forks from its managed rollout.
Checkpoints include these managed snapshots under each agent's `sessions`
directory; they do not include native authentication or user configuration.
Companion receives saved messages and tool events, not token-by-token native
streaming.

## Verification boundary

Runtime tests use local model fixtures and isolated state. They must not use a
paid model or the user's running Galpon service. Tests must exercise actual
message admission, persistence, reply correlation, interruption, recovery, and
cross-harness results. A final text string alone is not proof that the requested
tool ran or that its result was saved.
