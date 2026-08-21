function timestamp(value) {
  if (typeof value === "number") return Number.isFinite(value) ? value : 0;
  const numeric = Number(value);
  if (String(value || "").trim() && Number.isFinite(numeric)) return numeric;
  const parsed = new Date(value).getTime();
  return Number.isFinite(parsed) ? parsed : 0;
}

export function agentTreeActivity(agent) {
  let latest = timestamp(agent?.updatedAt || agent?.createdAt);
  for (const child of agent?.delegatedAgents || []) {
    latest = Math.max(latest, agentTreeActivity(child));
  }
  return latest;
}

function compareLabels(left, right) {
  const title = String(left?.title || "").localeCompare(String(right?.title || ""));
  if (title) return title;
  return String(left?.id || "").localeCompare(String(right?.id || ""));
}

function compareAgents(left, right) {
  return agentTreeActivity(right) - agentTreeActivity(left) || compareLabels(left, right);
}

function compareEntries(left, right) {
  return compareAgents(left.agent, right.agent);
}

function insertNewByActivity(retained, additions, compare) {
  const output = [...retained];
  for (const item of [...additions].sort(compare)) {
    const index = output.findIndex((current) => compare(item, current) < 0);
    output.splice(index < 0 ? output.length : index, 0, item);
  }
  return output;
}

function orderDelegatedAgents(agents, previousAgents, recompute) {
  const values = Array.isArray(agents) ? agents : [];
  if (recompute) {
    return values
      .map((agent) => ({
        ...agent,
        delegatedAgents: orderDelegatedAgents(agent.delegatedAgents, [], true),
      }))
      .sort(compareAgents);
  }

  const previous = Array.isArray(previousAgents) ? previousAgents : [];
  const currentByID = new Map(values.map((agent) => [String(agent?.id || ""), agent]));
  const previousByID = new Map(previous.map((agent) => [String(agent?.id || ""), agent]));
  const retained = previous.flatMap((oldAgent) => {
    const agent = currentByID.get(String(oldAgent?.id || ""));
    if (!agent) return [];
    return [{
      ...agent,
      delegatedAgents: orderDelegatedAgents(agent.delegatedAgents, oldAgent.delegatedAgents, false),
    }];
  });
  const additions = values
    .filter((agent) => !previousByID.has(String(agent?.id || "")))
    .map((agent) => ({
      ...agent,
      delegatedAgents: orderDelegatedAgents(agent.delegatedAgents, [], true),
    }));
  return insertNewByActivity(retained, additions, compareAgents);
}

function topLevelEntries(workspaces) {
  const entries = [];
  for (const workspace of Array.isArray(workspaces) ? workspaces : []) {
    for (const agent of Array.isArray(workspace?.agents) ? workspace.agents : []) {
      entries.push({
        workspace: { id: String(workspace?.id || ""), title: String(workspace?.title || "Unknown workspace") },
        agent,
      });
    }
  }
  return entries;
}

export function orderTopLevelAgentsByActivity(workspaces, previousEntries = [], { recompute = false } = {}) {
  const values = topLevelEntries(workspaces);
  if (recompute) {
    return values
      .map((entry) => ({
        ...entry,
        agent: {
          ...entry.agent,
          delegatedAgents: orderDelegatedAgents(entry.agent.delegatedAgents, [], true),
        },
      }))
      .sort(compareEntries);
  }

  const previous = Array.isArray(previousEntries) ? previousEntries : [];
  const currentByID = new Map(values.map((entry) => [String(entry.agent?.id || ""), entry]));
  const previousIDs = new Set(previous.map((entry) => String(entry.agent?.id || "")));
  const retained = previous.flatMap((oldEntry) => {
    const entry = currentByID.get(String(oldEntry.agent?.id || ""));
    if (!entry) return [];
    return [{
      ...entry,
      agent: {
        ...entry.agent,
        delegatedAgents: orderDelegatedAgents(
          entry.agent.delegatedAgents,
          oldEntry.agent?.delegatedAgents,
          false,
        ),
      },
    }];
  });
  const additions = values
    .filter((entry) => !previousIDs.has(String(entry.agent?.id || "")))
    .map((entry) => ({
      ...entry,
      agent: {
        ...entry.agent,
        delegatedAgents: orderDelegatedAgents(entry.agent.delegatedAgents, [], true),
      },
    }));
  return insertNewByActivity(retained, additions, compareEntries);
}
