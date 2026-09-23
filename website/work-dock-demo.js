// A fixed browser-only snapshot. Messages do not change task or work states.
export function createWorkDockSample() {
  return {
    collapsed: false,
    history: true,
    coordinatorStatus: "idle",
    todos: [
      { id: 1, subject: "Agree on key bindings", status: "completed", blockedBy: [] },
      { id: 2, subject: "Add workspace expansion", status: "in_progress", owner: "Interface", blockedBy: [1] },
      { id: 3, subject: "Draft help text", status: "pending", blockedBy: [1] },
      { id: 4, subject: "Check keyboard flow", status: "pending", blockedBy: [2] },
    ],
    delegations: [{
      id: "work-task-2",
      title: "Interface",
      taskId: 2,
      taskSubject: "Add workspace expansion",
      status: "started",
      lease: "fresh",
      observedAt: Date.now(),
      checkpoint: { phase: "working", summary: "Adding Tab expansion" },
      activity: { category: "tool: edit", status: "started" },
      coordination: [],
      children: [],
    }],
  };
}
