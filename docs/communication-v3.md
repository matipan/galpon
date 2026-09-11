# Galpón communication protocol v3

Status: implementation contract

Protocol v3 makes Pi communication simpler. It keeps durable operations, joins,
results, receipts, causal resume, and TODO settlement. It removes result-mode
selection and parked waits from the Pi tool contract.

## Send and update

`galpon_create_agent` and `galpon_send_agent` do not expose `result_mode`.
Galpón attaches each new reply-bearing message to the active Pi operation. An
`inform` message is one-way and does not have a reply.

A `todo_id` adds TODO ownership. It does not change message delivery or result
observation. The message ID remains the stable handle for read, await, and
update calls.

`galpon_update_agent` accepts:

```json
{
  "message_id": "message-id",
  "prompt": "additional instructions"
}
```

It appends instructions to a queued assignment that no runtime claimed. It
preserves the original assignment and does not create a new message. Its status is one of:

- `updated`
- `already_started`
- `already_completed`

A running assignment is not changed.

## Read and await

`galpon_read_message`, `galpon_await_agent`, and `galpon_await_agents` are
repeatable observations. They do not consume a result. They do not change a
join, TODO, or operation result. A registered runtime can call them without an
active operation.

An await uses one bounded HTTP request. The default timeout is 60 seconds. The
accepted range is 1 through 300 seconds. `galpon_await_agents` accepts 1 through
16 unique message IDs and keeps their input order. Its `return_when` value is
`any` or `all`.

Terminal outcomes have a `waitStatus` of `completed` or `failed`. Unfinished
outcomes are `pending` when an `any` wait returns, or `timeout` when the budget
ends. Cancellation can return `canceled`. A timeout does not cancel work. An await never returns `parked` or `receiptId`.
`messageStatus` and read `status` are projections of the durable operation and
result state.

## Durable result observation

Reading and notification bookkeeping are separate actions. A read or await
first returns the current durable state. It does not acknowledge a notification.

If a successful tool result contains terminal message results, the Pi extension
stores a pending observation entry. It waits until the matching Pi tool-result
entry is durable. It then sends:

```text
POST /v1/runtime/agents/{agentId}/operations/{operationId}/observe-results
```

with this body:

```json
{
  "runtimeId": "runtime-id",
  "operationId": "operation-id",
  "operationAttempt": 1,
  "attempt": 1,
  "protocolGeneration": 3,
  "requestId": "observe-results:{original-tool-call-id}",
  "messageIds": ["message-id"],
  "toolCallId": "original-tool-call-id"
}
```

The endpoint is idempotent and attempt-fenced. It marks only result receipts
that belong to this operation, or receipts that are not bound to another
operation. It does not consume a receipt that belongs to another operation.

After success, Pi stores a presented observation entry. On restart, the
extension reloads observations until operation settlement is confirmed. It
replays them only after the same operation is claimed under its current attempt,
and only when the matching successful tool-result entry exists. Thus, a failed tool call does not suppress
a notification, and a repeated endpoint call is safe.

## Progress

`galpon_report_progress` is available only during an active inbound delegated
request. Without that context, it returns:

```json
{
  "accepted": false,
  "recorded": false,
  "reason": "no_active_delegated_request"
}
```

This safe result does not call storage. A real storage error remains an error.
The input `version` and `event_id` fields are optional. The extension uses
version `1` and the Pi tool-call ID when they are absent.

## Compatibility and upgrade

Generation 3 is the installed runtime contract after the automatic offline
upgrade. It does not expose a permanent choice between the generation 2 and
generation 3 tool contracts. Existing durable message IDs, operations, results,
receipts, TODO links, and causal resume state remain valid.
