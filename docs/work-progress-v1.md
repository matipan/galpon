# Delegated work progress protocol v1

Galpón projects delegated work from its durable causal message tree. A work item is a request delivery, not an agent process. The projection is harness-neutral. Pi, Codex, Claude, and other MCP clients use the same runtime tool contract.

## Report progress

Call the runtime tool `report_progress` while an active delivery is claimed. The runtime envelope must contain the registered `agentId`, `runtimeId`, a unique `requestId`, and `currentMessageId`. Galpón uses the active message runtime and attempt as a fence. A runtime that lost its lease cannot report progress for a later attempt.

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

Text must be one line. Galpón rejects terminal control characters and common secret forms. Do not report chain-of-thought, private reasoning, prompts, tool arguments, tool output, secrets, paths, runtime IDs, session IDs, percentages, or estimates. Do not report an ETA.

A repeated `event_id` with the same body is successful and does not add a second event. Reuse with a different body fails. Galpón keeps at most 64 reports for one delivery and 100,000 reports in total. It does not persist noisy tool activity as progress.

## Projection

`GET /v1/agents/{agentID}/work` returns work delegated by the selected agent. Runtime clients use `POST /v1/runtime/agents/{agentID}/work` with `{"runtimeId":"...","includeSettled":false}`. The runtime endpoint requires current runtime ownership. `galpon work [agent]` is the terminal inspection path.

Lifecycle state is an observed system fact derived from the message row:

- `queued`
- `started`
- `completed`
- `failed`
- `canceled`
- `expired`

Lease freshness is `fresh`, `stale`, or `none`. A stale lease only states that Galpón did not observe a recent renewal. It is not proof that an agent is stuck.

A reported checkpoint has `source: "reported"`. Derived lifecycle and lease values have `source: "observed"`. Galpón does not create model-generated percentages, ETA values, or hidden reasoning summaries.

The projection includes nested request children from the causal tree. Inform, join, and notify requests keep their original protocol fields. Old clients that do not report progress continue to work and receive observed lifecycle data only.

## Retention and recovery

Progress reports are in SQLite and commit with Companion invalidation. Checkpoints include reports. Restore keeps reports, but any delivered message becomes queued and loses runtime ownership. A restored or retried runtime must claim a new attempt before it can report again.

Message pruning removes reports through a foreign key cascade. Branch and Pi compaction do not change the Galpón causal message tree or its progress projection.

Normal reports update the Work Dock and Companion through refresh and invalidation. They do not create an agent message and do not wake a parent model. Existing message result rules continue to notify completion and failure. A new blocked report atomically creates one coalesced blocker notification for the delegator. The notification is labeled as agent-reported and is not treated as a completed result. Replaying the same event does not create a second notification.
