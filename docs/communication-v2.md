# Galpón communication protocol v2

Status: implementation contract

## Purpose

Protocol v2 makes cross-agent result delivery durable across Pi turns, daemon restarts, extension reloads, and closed terminal views. It removes timing-based joined-result suppression.

An operation is one Pi objective. It is not a workflow, a DAG, a Work Run, or a harness-neutral abstraction. Pi remains the only supported operation runtime.

## Authoritative objects

### Message

A message is one `request`, `query`, or `inform` send. It keeps sender, target, causal lineage, deadlines, idempotency, and terminal status.

A reply-bearing message has one immutable result. The request row remains a compatibility projection during migration.

### Operation

An operation is one direct-user objective or inbound request. An operation can use more than one Pi invocation when it waits for joined results.

States:

```text
ready -> claimed -> running -> waiting -> ready
running -> settling -> settled
```

A nonterminal operation can become `failed`, `canceled`, or `expired`.

`waiting` has no runtime lease and no model invocation. It does not block the agent from other ready work.

### Result

A result is the immutable terminal outcome of one reply-bearing message. It contains either response text or an error.

### Inbox receipt

A receipt is the durable duty for one agent to handle a request, result, blocker, or control event.

States:

```text
pending -> claimed -> presented -> acknowledged
claimed -> pending
presented -> pending
```

Lease expiry or restart returns an unacknowledged receipt to `pending`. Only an explicit transition can move it to `abandoned`.

### Join

A join is a durable dependency from an operation to a child message.

States:

```text
open -> ready -> acknowledged
open -> failed
open -> expired
open -> detached
open -> canceled
```

## Result modes

### join

A join creates a durable dependency from the current operation to the child message.

When Pi settles with an open join, the daemon parks the operation in `waiting`. It does not complete the parent message. Child completion creates a receipt for the same operation and changes it to `ready`.

A join does not hold an HTTP request, database transaction, runtime lease, message lease, tool call, model call, or agent lock.

The daemon rejects a join that creates a dependency cycle. Cycle checks include explicit waits and implicit joins.

A join can end without a result only through visible failure, deadline expiry, explicit detach, cancel, or abandon. Protocol v2 never suppresses a late joined result.

### notify

Notify never blocks the current operation. Child completion creates an independent pending result receipt for the sender. The durable ready receipt continues to request a wake until Pi acknowledges it.

The daemon does not steer a notify result into an unrelated active direct-user operation.

### none

None creates no sender receipt. It keeps the target-side completion state. `inform` always uses none.

## Send

Send admission is one daemon transaction. It creates:

- the logical message;
- the target request receipt;
- the join edge, when required;
- the durable TODO link intent, when required;
- the idempotency receipt.

A changed retry with the same idempotency key fails. An exact retry returns the first response.

A request or query sent from an active operation defaults to `join`. A send outside an active operation defaults to `notify`.

## Take and acknowledge

`take` atomically binds a ready result receipt to the current operation and stable Pi tool request ID. An exact retry returns the saved immutable result. Take does not delete the result.

Pi records the presented receipt in its session before it acknowledges presentation. If Pi crashes after take, the same result is replayed.

A receipt becomes acknowledged only when the handling operation settles or records an explicit outcome.

## Settle and resume

Settle is attempt-fenced and idempotent.

If an operation has open joins, settle changes the operation to `waiting` and returns a parked response. For an inbound request, the message remains nonterminal without an active delivery lease.

If no joins are open, settle commits the operation and its parent message result in one transaction.

A child terminal transition resolves its join in the same transaction. A ready join changes the parent operation to `ready`. Ready operation state is the durable wake condition.

Resume claims the existing operation with a new attempt. It does not create a new causal root.

## Wake

Wake is level-triggered. An agent needs a wake when it has an unowned ready operation or an eligible pending receipt.

The dispatcher retries wake after daemon restart, runtime restart, pane closure, and extension reload. A one-time signal is never authoritative.

## Batching

One Pi invocation handles one work request or one operation resume. It does not mix unrelated operations.

A resume can present at most four result or blocker receipts for the same operation. A byte limit also applies. More receipts cause more resumes of the same operation.

## TODO links

Message admission stores the TODO link intent in the daemon transaction. Pi must apply and acknowledge the link before the child request becomes eligible to claim.

Child completion records the result receipt and TODO settlement event in one transaction. Pi applies the idempotent TODO snapshot and acknowledges it. Result handling through await and normal resume uses the same settlement path.

## Direct user operations

Terminal, Companion, and CLI user input registers an operation before Galpón tools can send child work. Terminal input uses the stable Pi session user-entry ID.

All child sends from one direct-user operation use the same causal run. A direct operation can park and resume, but the agent can process other ready work while it waits.

## Deadlock prevention

- Store all join and wait edges durably.
- Reject a transaction that adds a graph cycle.
- Release all leases when an operation waits.
- Give each join a visible deadline and terminal path.
- Do not steer work into an active await tool call.
- Fence claim, present, acknowledge, settle, and resume by operation attempt.

## Coordinated upgrade

Protocol v2 uses a coordinated cutover. Long-term mixed extension operation is not required.

1. Enter maintenance mode and reject new mutations.
2. Wait until every agent reaches a safe idle point.
3. Verify that no tool call, completion, injection, acknowledgement, or model invocation is active.
4. Create and verify a complete state backup.
5. Pause runtimes.
6. Run additive schema migrations and backfill in one transaction.
7. Materialize the new extension.
8. Let the existing extension watcher reload every idle running Pi process.
9. Require registration with the new protocol generation.
10. Start stopped agents with the new extension on their next wake.
11. Rebuild durable ready work from database state.
12. Leave maintenance mode.

Calls from an old runtime generation fail after cutover. If any running agent does not register the new generation, the daemon stays in maintenance mode.

Existing queued work stays queued. Existing completed work stays terminal. Existing suppressed results become `legacy_suppressed_unknown`; migration does not wake them automatically. Known unsettled TODO links can use a specific repair receipt.

All first-release schema changes are additive. Existing state remains available. A verified pre-upgrade backup is required before cutover.

## Read-only projections

Work Dock and Operations project authoritative operations, messages, joins, results, and receipts. They show:

- active delegated work;
- waiting operations;
- results ready;
- resume queued;
- receipt claimed, presented, and acknowledged;
- TODO application pending and applied;
- legacy suppressed results.

They do not expose prompts, result bodies, reasoning, tool arguments, paths, secrets, or durable IDs.
