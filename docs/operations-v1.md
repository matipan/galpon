# Agent Operations projection

`GET /v1/agents/{agentID}/operations` returns the server-owned, read-only
Operations projection for one selected agent. Companion exposes the same view
at `GET /api/v1/agents/{agentID}/operations`.

The view includes:

- the selected agent and its workspace;
- current work received by the agent and delegated by the agent;
- work that needs attention, including reported blockers, stale observations,
  failures, cancellations, and expirations;
- recent results and failures;
- bounded recent coordination facts;
- the selected agent's inbound and result queue counts;
- safe direct Pi work facts and current safe activity; and
- explicit bounds and omission counts.

Each work item has a `direction` value of `received` or `delegated`. Current work
is ordered first. Attention and recent results use separate bounded sections.
One item can qualify for more than one server section. Clients remove duplicate
rows and keep the first section, so current work stays first.

The section bounds are 64 current items, 32 attention items, 32 recent results,
and 64 recent coordination facts. The source scan is also bounded. The
projection reports source truncation and exact known section omissions. Older
completed work can be outside the recent-results bound.

Observed state comes from durable agent, message, operation, attempt, result,
receipt, and lease facts. Agent checkpoints remain reported facts. A stale
lease is an attention fact. It is not a stuck-state inference. A started item
with a stale lease is not in the current section.

Protocol-v2 diagnostics can remain in stored work facts for compatibility, but
Operations clients do not make them a primary surface. They show work state,
attention, results, and recent coordination first. The messaging protocol and
its mutation paths are unchanged.

The projection does not contain prompts, conversation bodies, tool input or
output, paths, session IDs, runtime IDs, secrets, private reasoning, model
summaries, percentages, or ETA values. Titles, validated checkpoint labels,
and approved activity categories are the only descriptive text.

The CLI uses `galpon operations <agent-id-or-title>`. The Go TUI opens Operations
only for a selected agent. Companion opens Operations from the selected agent
detail. All Operations surfaces are read-only.
