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

## Ask and wait in one tool call

`galpon_ask_agent` accepts `agent`, `prompt`, optional `act` (`request` or
`query`), `todo_id`, `todo_policy`, and `timeout_seconds`. It does not accept
`inform`. This tool requires the same explicit user permission as send.

The Pi extension calls the existing fenced send endpoint with the original
tool-call ID, stores the returned message ID in a `galpon-ask` session entry,
then calls the existing await endpoint. It does not hold admission locks while
waiting. Transport retries use the same send identity. The model does not
copy a message ID between admission and observation.

The default wait budget is 600 seconds; the accepted range is 1 through 1800
seconds. The extension uses sequential await requests of at most 300 seconds,
with one overall deadline. This does not change the explicit await APIs.
The tool returns the await outcome and complete `messageId`. If observation
fails after admission, it returns `interrupted` with the accepted handle.
Timeout and cancellation return that handle too. They stop only the wait.
They do not cancel or resend the assignment.

A terminal ask result enters the same persisted-tool-result observation path
as an explicit await. A wait timeout is not terminal result evidence.

## Results during active work

Pi 0.87.0 provides actionable `turn_end` and `agent_before_settle` hooks.
Galpon uses these hooks to take bounded receipt batches for the exact active
operation, runtime, attempt, and protocol generation. It does not take another
objective's receipts or interrupt tools. Explicit result observations are
flushed first, so their saved tool results suppress duplicate notifications.

The extension proposes a model-visible `custom_message` session entry and
`continue: true`. It preserves entries from earlier extensions. Pi persists the
entry before the next provider request. Natural continuation satisfies the
request; it does not add an extra model request after each tool step.
Aborted or failed boundaries do not propose delivery or continuation. A late
result with an idle parent still uses the existing durable operation scheduler.

Receipt presentation is separate from proposal. At the next turn start or
boundary, Galpon verifies that the custom message exists on the session branch
before it records presentation. A rejected proposal remains unpresented. A
lost take response or uncommitted proposal retains the same claim identity for
retry. Each boundary has a two-second network budget; failure leaves the result
in durable storage. Existing receipt and attempt recovery still apply after a
restart. Delivery remains at least once, not exactly once across process failure.

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

Reading and notification bookkeeping are separate actions. An ask, read, or await
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

If an observation loses its attempt fence, Pi stops retrying that attempt. It
retains the active model's context until the final response is saved, then claims
the same operation under a new attempt and replays the saved observations. If
no unread results remain, it submits the saved final response without another
model turn. Late responses from the old attempt cannot change the new attempt.
Other conflicts and server errors remain retryable; they do not discard ownership.

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

This result does not store progress or wake the parent. A real storage error
remains an error.
The input `version` and `event_id` fields are optional. The extension uses
version `1` and a stable SHA-256 ID derived from the Pi tool-call ID when they
are absent. The hash uses four colon-separated groups so it passes the progress
text validator. The daemon uses the same default. Explicit event IDs remain
unchanged and must pass validation.

## Compatibility and upgrade

The tested Pi version is 0.87.0. Earlier Pi versions do not provide the
continuation-entry hooks. They retain idle-only result delivery. The ask tool
uses existing generation-3 endpoints; these additions do not change the database
schema or require a new protocol generation.

Generation 3 is the installed runtime contract after the automatic offline
upgrade. It does not expose a permanent choice between the generation 2 and
generation 3 tool contracts. Existing durable message IDs, operations, results,
receipts, TODO links, and causal resume state remain valid. Immutable results
retain their original creation generation; checkpoint restore accepts those
historical results without changing their contents.

Stop all agent runtimes and the daemon, pull and reinstall, then start Galpon.
Startup performs the verified backup and migration automatically. It refuses
cutover if an agent process for that daemon socket is still running. A failed
upgrade stops daemon startup and retains recoverable state and the backup.
