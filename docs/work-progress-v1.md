# Delegated work progress protocol v1

Galpón projects delegated work from its durable causal message tree. A work item is a request delivery, not an agent process. The projection is harness-neutral. Pi uses the runtime tool contract in this change. Other harness adapters can integrate with the same contract in separate changes.

## Report progress

Call the runtime tool `report_progress` while an active delivery is claimed. The runtime envelope must contain the registered `agentId`, `runtimeId`, a unique `requestId`, `currentMessageId`, and the claimed `currentAttempt`. Galpón compares the supplied attempt with the active message runtime and attempt as a fence. A runtime that lost its lease cannot report progress for a later attempt.

Request:

```json
{
  "version": 1,
  "event_id": "stable-update-id",
  "phase": "working",
  "summary": "Implementing the durable projection",
  "milestones": [
    {"label": "Schema", "state": "completed"},
    {"label": "Projection", "state": "active"}
  ],
  "counts": [
    {"label": "tests", "completed": 4, "total": 9}
  ]
}
```

Fields:

- `version`: Must be `1`.
- `event_id`: Required. It is an idempotency key of 1 to 100 visible characters.
- `phase`: One of `planning`, `working`, `verifying`, `waiting`, `blocked`, or `finishing`.
- `summary`: Required safe checkpoint text of 1 to 240 Unicode characters.
- `milestones`: Optional. At most 8 items. A label has at most 80 characters. State is `pending`, `active`, `completed`, or `blocked`.
- `blocker`: Optional. It has at most 240 characters. It is required when phase is `blocked` and forbidden for other phases.
- `counts`: Optional. At most 8 factual counters. A label has at most 40 characters. `completed` and `total` are integers from 0 to 1,000,000,000 and `completed` cannot exceed `total`.

Text must use a small one-line grammar. Galpón rejects controls, bidirectional format characters, paths, percentages, ETA text, reasoning text, long opaque strings, and bounded patterns for common AWS, GitHub, JWT, PEM, Slack, bearer, API-key, password, and token forms. This reduces accidental disclosure. It cannot prove that arbitrary text contains no secret. Do not report chain-of-thought, private reasoning, prompts, tool arguments, tool output, credentials, runtime IDs, or session IDs.

A repeated `event_id` with the same body and attempt is successful and does not add a second event, including after response loss and lease expiry. A stale attempt cannot add a new event. Reuse with a different body or attempt fails. Pi retries one transport failure with the same tool-call and event IDs. Galpón keeps at most 64 reports for one delivery and 100,000 reports in total. Live insertion and checkpoint restore enforce both limits. It does not persist noisy tool activity as progress.

## Projection

`GET /v1/agents/{agentID}/work` returns work delegated by the selected agent. Runtime clients use `POST /v1/runtime/agents/{agentID}/work` with `{"runtimeId":"...","includeSettled":false}`. The runtime endpoint requires current runtime ownership. The response contains `work`, `returnedRoots`, `returnedItems`, and `truncated`. It returns at most 128 roots and 256 total request items, with at most 128 children per item and 15 nested child levels. The projection uses scoped indexed message queries. `galpon work [agent]` reports nested item and root counts.

Before communication protocol v2 cutover, lifecycle state is an observed system
fact derived from the message row:

- `queued`
- `started`
- `completed`
- `failed`
- `canceled`
- `expired`

After cutover, lifecycle state comes from the target operation. The projection
also uses `waiting` for an operation that has parked on an open join. Waiting
work is active, but it has no runtime lease. The item `coordination` section
shows safe message, operation, join, result, receipt, resume, and TODO
application facts. The section does not include private payloads or protocol
object IDs.

`canceled` and `expired` require a durable structured terminal reason. Free-text errors never select these states; an unclassified failure is `failed`. Lease freshness is `fresh`, `stale`, or `none`. A stale lease only states that Galpón did not observe a recent renewal. It is not proof that an agent is stuck.

A reported checkpoint has `source: "reported"`. Derived lifecycle and lease values have `source: "observed"`. Galpón does not create model-generated percentages, ETA values, or hidden reasoning summaries.

The projection includes nested request children from the causal tree. Result rows remain structural causal parents but are not rendered as work. Durable child rows add observed `child delegated` and `child settled` timeline facts. Inform, join, and notify requests keep their original protocol fields. Only the current message attempt can supply the current checkpoint. Old-attempt reports remain durable history but do not appear as current progress. Old clients that do not report progress continue to work and receive observed lifecycle data only.

## Retention and recovery

Progress reports are in SQLite and commit with Companion invalidation. Checkpoints include reports. Restore keeps reports, but any delivered message becomes queued and loses runtime ownership. A restored or retried runtime must claim a new attempt before it can report again.

Message pruning removes reports through a foreign key cascade. Branch and Pi compaction do not change the Galpón causal message tree or its progress projection.

Normal reports update the Work Dock and Companion through refresh and invalidation. They do not create an agent message and do not wake a parent model. Existing message result rules continue to notify completion and failure. The first transition into `blocked` in one delivery attempt atomically creates one blocker notification for the delegator. Later blocker reports in that attempt update the work views but do not wake the delegator again. A reclaimed attempt can create one new blocker notification. Notifications are labeled as agent-reported and are not completed results. Exact replay does not create a second notification.
