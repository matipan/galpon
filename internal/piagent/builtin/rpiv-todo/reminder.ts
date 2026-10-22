/**
 * Open-task reminder. Each agent run starts with a hidden custom message that
 * lists unfinished tasks, so the model can close or delete old tasks when the
 * user changes direction or after compaction removed the earlier todo calls.
 */

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { selectOpenTasks } from "./state/selectors.js";
import type { TaskState } from "./state/state.js";
import { getState, sid } from "./state/store.js";
import { formatOpenTasks } from "./tool/response-envelope.js";
import { TOOL_NAME } from "./tool/types.js";

export const OPEN_TASKS_REMINDER_TYPE = "rpiv-todo:open-tasks";

export function openTasksReminder(state: TaskState): string | undefined {
	if (selectOpenTasks(state).length === 0) return undefined;
	return [
		"Your todo list still has open tasks from earlier work.",
		formatOpenTasks(state),
		"",
		"Do not start a task only because it is in this list. Before your final reply, mark finished tasks completed. Delete tasks that are obsolete, that a change of plan replaced, or that the user no longer wants. Keep open only work that you or a delegated agent will still do.",
	].join("\n");
}

export function registerOpenTasksReminder(pi: ExtensionAPI): void {
	pi.on("before_agent_start", (_event, ctx) => {
		// The model cannot act on the reminder while the todo tool is inactive,
		// for example in Galpón Plan mode.
		if (!pi.getActiveTools().includes(TOOL_NAME)) return undefined;
		const content = openTasksReminder(getState(sid(ctx)));
		if (!content) return undefined;
		return { message: { customType: OPEN_TASKS_REMINDER_TYPE, content, display: false } };
	});
}
