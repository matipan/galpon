# Workspace Operations projection v1

`GET /v1/workspaces/{workspaceID}/operations` returns the server-owned,
read-only workspace Operations projection. The Companion adapter exposes the
same projection at `GET /api/v1/workspaces/{workspaceID}/operations`.

The projection contains:

- a workspace title and summary;
- an agent runtime matrix with safe status and current delivery facts;
- prioritized causal delegated-work roots;
- observed delivery state and a separate current agent-reported checkpoint;
- bounded recent timeline facts; and
- explicit bounds, omission counts, and source truncation state.

Priority order is reported blocker, active work, queued work, stale
observation, recent failure, then recent completion. The server applies this
order to roots and children before it applies bounds. If one causal item is also
visible through another delegator, the workspace projection keeps only the
outer root. Ancestors remain visible when a prioritized descendant is active.

The bounds are 128 agents, 64 roots, 256 work items, and 128 timeline facts.
The projection reports each bound. It also reports known omitted counts. A
source projection can be truncated before an exact omission count is known. In
that case, `sourceTruncated` and `truncated` are true.

Observed state comes from durable agent, message, attempt, and lease facts.
Reported checkpoints come only from the current attempt's validated progress
events. A fresh started delivery can have a subtle liveness cue. The cue shows
lease renewal only. It does not prove useful progress. The view shows the age
of the last observed lease or safe activity fact. The agent-reported checkpoint
remains the best statement of actual work. A stale lease is
`stale_observation`. It is not a stuck-state inference. Clients display the
server priority and do not reclassify work.

A safe activity fact can use an already normalized Pi event. The server emits
only an approved category or tool name, its status, and its observed time. It
never emits arguments, output, prompts, paths, IDs, commands, secrets, or
reasoning in this fact. Old facts are labeled as last activity.

The projection does not contain prompts, conversation bodies, tool input or
output, paths, session IDs, runtime IDs, secrets, private reasoning, model
summaries, percentages, or ETA values. Titles, validated checkpoint labels,
and approved activity categories are the only descriptive text.

The CLI supports one-shot text and JSON output with `galpon operations`. It does
not provide watch mode because the local CLI does not have a matching event
contract. The command center, Pi cockpit, and Companion use their existing safe
refresh paths. No new durable state is used.

The Operations surfaces have no mutation controls. TODO state remains Pi-local
through the documented TODO event contract. The daemon projection does not copy
TODO state and does not combine TODO status with delegated-work status.
