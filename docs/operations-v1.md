# Workspace Operations projection v1

`GET /v1/workspaces/{workspaceID}/operations` returns the server-owned,
read-only workspace Operations projection. The Companion adapter exposes the
same projection at `GET /api/v1/workspaces/{workspaceID}/operations`.

Before communication protocol v2 cutover, the endpoint keeps this v1 behavior.
After cutover, `version` is `2`. Existing v1 fields stay present, each work
item can include the bounded `coordination` facts that are specified in
`communication-v2.md`, and the response can include aggregated safe direct Pi
operation facts. Before cutover, the optional v2 fields are absent from JSON.

The projection contains:

- a workspace title and summary;
- an agent runtime matrix with safe status and current delivery facts;
- prioritized causal delegated-work roots;
- observed delivery state and a separate current agent-reported checkpoint;
- an optional versioned safe-activity lane;
- inbound queue and result-notification stages;
- bounded recent timeline facts; and
- explicit bounds, omission counts, and source truncation state.

Priority order is reported blocker, active work, queued work, stale
observation, recent failure, then recent completion. The server applies this
order to roots and children before it applies bounds. Work priority is global
and is applied before the 128-agent runtime matrix bound. If one causal item is
also visible through another delegator, the workspace projection keeps only the
outer root. Ancestors remain visible when a prioritized descendant is active.
All source reads use one read transaction and one captured time value.
Protocol v2 reads start from the bounded message set. Message-first indexes
cover target operations, joins, results, receipts, and TODO facts. Join,
receipt, and TODO rows are aggregated in SQL before the 24-fact item bound is
applied. Direct operation rows are also aggregated in SQL.

The bounds are 128 agents, 64 roots, 256 work items, and 128 timeline facts.
The projection reports each bound. It also reports known omitted counts. A
source projection can be truncated before an exact omission count is known. In
that case, `sourceTruncated` and `truncated` are true. Each omission field has a
matching exactness field, so zero never means both none and unknown. The safe
activity lane has a separate 64-fact bound and truncation record.

Observed state comes from durable agent, message, attempt, and lease facts. A
checkpoint is current only while its request is started on the current attempt
and has a fresh lease. Queued, stale, and terminal reports are historical
reported timeline facts. They do not set blocker priority. `currentDelivery`
also requires a fresh started lease. `observedDelivery` keeps the latest
queued, stale, or terminal lease fact separate for runtime rows.

A fresh started delivery can have a subtle liveness cue. The cue shows
lease renewal only. It does not prove useful progress. The view shows the age
of the last observed lease or safe activity fact. The agent-reported checkpoint
remains the best statement of actual work. A stale lease is
`stale_observation`. It is not a stuck-state inference. Clients display the
server priority and do not reclassify work.

A safe activity fact can use an already normalized Pi event. At ingestion, the
server fences the derived fact to every active delivery in the Pi batch by
message, current attempt, runtime, and claim time. The public lane emits only an
approved category or tool name, its status, and its observed time. It never
emits arguments, output, prompts, paths, IDs, commands, secrets, or reasoning.
Old facts are labeled as last activity.

Queue counts and result stages are durable daemon observations. `result_ready`,
`result_projected`, `delivery_queued`, and `delivery_claimed` do not claim that
Pi injected, handled, or consumed a result. The views say when Pi handling is
not observed. After v2 cutover, the queue also gives claimed, presented, and
acknowledged receipt counts. The summary gives waiting work, queued resumes,
pending and applied TODO work, and legacy suppression with unknown state.
Waiting and resume values count distinct operation IDs internally, including
safe direct Pi operation state, but no protocol ID enters the response. TODO
link and settlement facts stay separate from the Pi-local TODO list.

The projection does not contain prompts, conversation bodies, tool input or
output, paths, session IDs, runtime IDs, secrets, private reasoning, model
summaries, percentages, or ETA values. Titles, validated checkpoint labels,
and approved activity categories are the only descriptive text.

The CLI supports one-shot text and JSON output with `galpon operations`. It does
not provide watch mode because the local CLI does not have a matching event
contract. The terminal cockpit loads once and has an explicit data refresh.
Companion uses its existing invalidation path. The derived safe-activity table
adds no mutation control or orchestration state.

The Operations surfaces have no mutation controls. The user TODO list remains
Pi-local through the documented TODO event contract. The Pi-local readiness
selector requires incomplete state, complete blockers, an empty owner, no
unsettled linked delegation, and no associated active Pi operation. The
selector does not claim or schedule work. Protocol v2 projects only the
daemon-owned link and settlement application state. It does not copy TODO
subjects or snapshots, and it does not combine TODO status with delegated-work
status.
