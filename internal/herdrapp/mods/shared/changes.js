// Opening the Changes view of an agent, the same way from every place that
// offers it: the agent tab, the command center and the sidebar menu.

import { panes, run, setShared, spaces } from "herdr";

// CHANGES_MOD is the mod of the Changes view.
export const CHANGES_MOD = "galpon-changes";
// CHANGES_FOCUS is the shared value that shows a file in an open Changes
// view: { agent, file, at }.
export const CHANGES_FOCUS = "galpon.changes.focus";

// showChanges opens the Changes view of an agent beside the pane of the
// agent, or gives the keyboard to the open one. agent is { id, title,
// workspaceId, pane }: pane is the pane of the agent when the caller knows
// it. host is the herdr-core machine of Galpon, or null when Galpon runs on
// this computer. A file shows at once. An agent without a pane here gets the
// view in a tab of the space of its workspace.
export function showChanges(agent, host, file = "") {
  const prefix = host ? `${host}/` : "";
  const tab = `${prefix}galpon-${agent.id}`;
  const all = panes();
  const open = all.find((pane) => pane.mod === CHANGES_MOD && pane.tab === tab);
  if (open) {
    if (file) setShared(CHANGES_FOCUS, { agent: agent.id, file, at: Date.now() });
    run("pane.focus", { pane: open.pane });
    return;
  }
  const beside = agent.pane ?? all.find((pane) => pane.mod === "galpon-agent" && pane.tab === tab)?.pane;
  if (beside) {
    run("pane.split", { direction: "right", pane: beside, mod: CHANGES_MOD, props: { agent: agent.id, tab, file } });
    return;
  }
  const space = spaces().find((candidate) => candidate.id === `${prefix}galpon-${agent.workspaceId}`);
  run("tab.new", { name: `${agent.title} · changes`, ...(space ? { space: space.id } : {}), mod: CHANGES_MOD, props: { agent: agent.id, file } });
}
