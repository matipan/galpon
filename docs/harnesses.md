# Agent harness contract

An agent's `kind` is `pi`, `claude`, or `codex`. Pi is the default. The value is
set at creation and cannot be changed. A different harness requires a new agent.
Placements can be copied or shared across harnesses. A cross-harness fork converts
saved history into a new native session; it never gives the target an incompatible
source session file.

## Cross-harness forks

Use the normal fork action and select a different harness in the new-agent form.
The context source stays selected when the harness changes. CLI and tool callers
can also combine an explicit harness with a context source:

```sh
galpon agent create "Codex fork" --workspace <workspace-id> \
  --context-agent <source-agent-id> --harness codex
```

A cross-harness source can stay active. Galpon copies a fixed prefix of complete
saved records before the new agent starts. A partial final record is not included.
Later source writes stay with the source. Galpon checks the copied bytes and file
identity and retries if the source rewrites them. A same-harness source must be
idle or stopped. Normal placement options still control whether working files
are copied, shared, or kept separate from the context source.

Saved history and active model context are separate. Pi compaction, context edits,
branch summaries, Claude compaction and retained messages, and Codex replacement
history and rollbacks select the target's effective context. Native checkpoints
or Pi model-only edits keep archived messages out of that context. The selected
conversation ancestry is shown in the target's native history. Claude shows a
read-only archive followed by a separate active-context section; retained
messages can appear in both sections. Its archive notices label attachments
rather than providing native attachment widgets. A complete source snapshot is also kept
in the new agent's `history/source.jsonl`, including other branches and records
that the target does not interpret.

User and assistant text stays in conversation order. Tool calls and results become
labeled historical text, including unfinished calls and unmatched results. They
are not executable calls or native tool widgets. An unanswered user message does
not prevent a fork. Readable reasoning is retained as historical text, not as
provider-signed reasoning. The target gets fresh session and entry identities and
uses its own model, instructions, permissions, and tools. Source runtime ownership,
queues, approvals, task state, and source system/developer instructions are not
inherited.

Images, including tool-result images, are embedded in the new session. Local and
URL-backed attachments are copied while the snapshot is built. URL reads do not
use source harness credentials. Other file types are saved under the new agent's
`history` directory and linked from its messages; Claude also receives PDF content
as native document blocks. Missing files, unreadable URLs, provider-only file IDs,
and invalid attachment data produce a visible unavailable-attachment marker. The
original record remains in the source snapshot. These cases do not prevent a fork.
Copied file links are local to this installation; conversation export currently
carries native session content, not the separate `history` directory.

A provider-encrypted Codex checkpoint cannot be decoded by another provider.
Galpon preserves its original record and explicitly marks a fallback to the saved
readable history. This fallback is not an equivalent compacted context and can
require compaction in the target harness. Plaintext compaction checkpoints do not
restore pre-compaction messages to the model.

There is no fixed conversation size cap. Galpon stores the snapshot and converted
content on disk and indexes record offsets; memory use still depends on the
largest individual record and the native harness. The target model's context
limit still applies. Invalid JSON, cyclic ancestry, unreadable session files, and
a source that keeps rewriting its selected prefix can still prevent conversion.
No database migration or change to existing agents is required.

Converted histories depend on the harnesses' native session formats. Compatibility
checks use real harnesses with local mock models. New native record formats can
require an adapter update.

## Requirements and use

Pi is the default unless `default_harness` is set in the global
`galpon/config.toml` under `$XDG_CONFIG_HOME` or `~/.config`. The daemon's active
default applies to CLI and tool creation, Factory, and Companion. Creation
forms preselect it and permit an explicit choice. Select a harness through
`galpon agent create --harness`, the creation form, Companion, or the `harness`
field of `galpon_create_agent`. A context fork inherits the source harness when
the harness is omitted. An explicit different harness converts its saved conversation
history. An import retains its exported harness. A copied file placement does not inherit
a harness. See [Configuration](../README.md#configuration) for file location
and restart requirements.

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

Claude keeps the inherited `CLAUDE_CONFIG_DIR`, including when it is unset.
Galpon does not redirect its global config, credentials, or plugins. The adapter
finds the session file by ID under Claude's effective `projects` directory and
resumes from its exact path. A native conversation can be recovered before its
managed copy has been registered. If there is no agent-owned file and multiple
native files match, startup fails rather than selecting an uncertain history.

Claude foreground operation requires its local development channel confirmation
and the account's channel feature. Do not disable privacy controls or bypass
organization policy to obtain this feature. A headless Claude agent does not
need channels. Queued image deliveries are supported in headless Claude and
Codex; Claude's foreground channel accepts text only. Paste an image into the
native terminal, or use a background agent.

Native harness task lists remain local. Pi Work Dock, `/review`,
`/operations`, and Pi extension tools are not installed into another harness.
Claude Code and Codex expose the durable Galpon tools over MCP. The daemon
preapproves only its own Codex MCP tools; other approvals remain native. A
background Codex approval request fails with an instruction to open the native
terminal. It does not silently approve a command or change the sandbox mode.

The agent's Galpon folder holds its extra worktrees and its `scratch` folder,
outside the start directory. Galpon starts Claude Code with `--add-dir` for this
folder. For Codex, Galpon adds the folder to the writable roots of the
workspace-write sandbox for the thread and keeps the configured roots. The
sandbox mode and other permission settings do not change.

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

Claude can move a long MCP call into a local background task. Galpon binds the
saved task ID to the original result-observation tool call. A completed native
task notification can acknowledge that result only after its attachment is saved
in the owning turn. A queued notification, another task's output, or a marker
quoted inside result text is not sufficient evidence.

Claude's foreground integration uses MCP channels and lifecycle hooks. Channels
are a research preview and require feature availability, interactive consent for
the local development channel, and any applicable organization approval. Galpon
must not change privacy settings or bypass organization policy to enable them.
The headless integration uses streaming JSON. Saved channel entries are marked
as metadata by Claude; only entries from Galpon's own channel can open a delivery
turn. Prompt IDs and saved UUID ancestry separate human and delegated replies.
Compaction summaries do not open user turns. A compaction boundary uses its
saved parent or prompt identity, or the newest unfinished input turn. Local
command records do not open user turns. Older failed or interrupted turns cannot
supply a fallback identity. An unmatched final response fails after a bounded
evidence wait; it cannot renew forever.
Codex retains all saved user inputs in a turn so steering cannot erase delivery
evidence.

A delivery that has no saved input is withdrawn after the evidence deadline.
If the adapter cannot prove withdrawal, a background writer stops before Galpon
reports failure. A foreground writer stays open with the operation unsettled and
its lease renewed. Galpon reports a blocker in the agent status and terminal,
and claims no more deliveries. Human input remains available. Saved delivery
input clears the blocker; closing the writer fails the held delivery. Recovery
after a controller crash also fails that held delivery after writer fencing.
Normal runtime and operation authority checks still apply.

The journal keeps a bounded list of expired delivery tokens. Claude's input hook
rejects those tokens without closing the terminal. Other pasted delivery markers
are ordinary human input. A blocked Claude user prompt with no saved input
releases its provisional ownership.

Codex uses app-server for structured turns and the native remote terminal for
interaction. Both connections refer to the same thread. The runtime must not
run a second independent Codex writer for that thread. Deliveries enter Codex's
thread queue with stable client message IDs. Starting a queued entry does not
steer a human turn already in progress. New threads use complete JSONL history
so that the managed snapshot can resume without the source host's history database.
Before opening an empty foreground thread, Galpon saves the agent title through
Codex's metadata API and waits for its saved session identity. This permits
terminal resume without a synthetic prompt or model call. An empty terminal can
be closed and reopened with the same thread ID.

The controller and a separate supervisor hold an exclusive session lock. The
native writer and its tool children never inherit that lock. A start pipe holds
the writer until the supervisor saves its process-group identity. Controller exit
closes a private pipe; the supervisor then stops the writer's process group.
Detached tool processes can remain alive without holding session ownership.

Linux also sends a parent-death signal to the writer launcher. On all supported
systems, a replacement checks the saved writer group while it holds the session
lock. If a supervisor failure leaves that group alive, startup fails with its
group ID instead of admitting a second writer. Galpon does not signal a possibly
reused group ID. Inspect the reported group and its owner record before reopening
the agent. On normal exit, the supervisor removes the owner record only after the
group has gone, while it still holds the lock. Normal handoff waits up to eight
seconds for lock and group release.
The journal and managed transcript contain only complete, synced records. A replacement controller reclaims work with current runtime and
attempt fences. A saved final response can complete recovered work without a
second model turn. Missing saved input normally causes redelivery, not invented
evidence. A delivery held for uncertain foreground admission fails instead.

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
