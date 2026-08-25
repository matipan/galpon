export function agentCountText({ visible, total, query = "", filter = "all" }) {
  if (String(query).trim()) return `${visible} MATCHES · ${total} AGENTS`;
  if (filter === "attention") return `${visible} NEED YOU · ${total} AGENTS`;
  if (filter === "active") return `${visible} ACTIVE · ${total} AGENTS`;
  return `${visible} AGENT${visible === 1 ? "" : "S"}`;
}

export function launchIsReady({ workspaceId, harness, startMode, repositoryId, sourceAgentId, title, prompt }) {
  const hasStartingPoint = startMode === "agent"
    ? Boolean(String(sourceAgentId || "").trim())
    : Boolean(String(repositoryId || "").trim());
  return Boolean(
    String(workspaceId || "").trim()
    && String(harness || "").trim()
    && hasStartingPoint
    && String(title || "").trim()
    && String(prompt || "").trim()
  );
}
