import { keyHint, type Theme } from "@earendil-works/pi-coding-agent";
import { Text } from "@earendil-works/pi-tui";
import { consoleActive, consoleIcon } from "./tool-frame.js";
import { formatStatusLabel } from "../state/i18n-bridge.js";
import { selectTaskSubjectById } from "../state/selectors.js";
import type { TaskState } from "../state/state.js";
import { sanitizeTerminalText } from "../tool/sanitize.js";
import type { Task, TaskAction, TaskDetails, TaskMutationParams, TaskStatus } from "../tool/types.js";

// Re-export so legacy import paths (todo.ts, tests) continue to resolve;
// the canonical definition lives in the i18n bridge.
export { formatStatusLabel };

// ---------------------------------------------------------------------------
// Status presentation tables — the single source of truth for glyph/color.
// ---------------------------------------------------------------------------

export const STATUS_GLYPH: Record<TaskStatus, string> = {
	pending: consoleIcon("pending"),
	in_progress: "◐",
	completed: consoleIcon("success"),
	deleted: "",
};

/**
 * Color palette for the renderResult status echo. `deleted` uses `muted` so a
 * successful delete is visually distinct from an error. The action is text,
 * not the failure or cancellation symbol.
 */
export const STATUS_COLOR: Record<TaskStatus, "dim" | "warning" | "success" | "muted"> = {
	pending: "dim",
	in_progress: "warning",
	completed: "success",
	deleted: "muted",
};

/**
 * Per-action prefix glyph for renderCall. `+` create, `→` update, `×` delete,
 * `›` get, `☰` list, `∅` clear. Pre-refactor `todo.ts:457-464`.
 */
export const ACTION_GLYPH: Record<TaskAction, string> = {
	create: "+",
	update: "→",
	delete: "×",
	get: "›",
	list: "☰",
	clear: "∅",
};

/**
 * Glyph for the persistent overlay's per-task row. Completion uses the same
 * check as tool results. Deleted items are neutral, not failed work.
 */
export function overlayStatusGlyph(status: TaskStatus, theme: Theme): string {
	switch (status) {
		case "pending":
			return theme.fg("dim", consoleIcon("pending"));
		case "in_progress":
			return theme.fg("warning", "◐");
		case "completed":
			return theme.fg("success", consoleIcon("success"));
		case "deleted":
			return " ";
	}
}

/**
 * Format a single task row for the persistent overlay. The subject color
 * reflects task state while IDs and supporting metadata stay visually quiet.
 */
export function formatOverlayTaskLine(t: Task, theme: Theme, showId: boolean): string {
	const glyph = overlayStatusGlyph(t.status, theme);
	const subjectColor = t.status === "completed" || t.status === "deleted" ? "muted" : "text";
	let subject = theme.fg(subjectColor, sanitizeTerminalText(t.subject));
	if (t.status === "completed" || t.status === "deleted") {
		subject = theme.strikethrough(subject);
	}
	let line = `${glyph}`;
	if (showId) line += ` ${theme.fg("dim", `#${t.id}`)}`;
	line += ` ${subject}`;
	if (t.status === "in_progress" && t.activeForm) {
		line += ` ${theme.fg("muted", `(${sanitizeTerminalText(t.activeForm)})`)}`;
	}
	if (t.blockedBy && t.blockedBy.length > 0) {
		line += ` ${theme.fg("muted", `blocked by ${t.blockedBy.map((id) => `#${id}`).join(", ")}`)}`;
	}
	return line;
}

/**
 * Format a single task line for the `/todos` slash command (no glyph color,
 * indented bullet prefix). Pre-refactor `todo.ts:670-674`.
 */
export function formatCommandTaskLine(t: Task, glyph: string): string {
	const form = t.status === "in_progress" && t.activeForm ? ` (${sanitizeTerminalText(t.activeForm)})` : "";
	const block = t.blockedBy?.length ? `    blocked by ${t.blockedBy.map((id) => `#${id}`).join(", ")}` : "";
	return `  ${glyph} #${t.id} ${sanitizeTerminalText(t.subject)}${form}${block}`;
}

// ---------------------------------------------------------------------------
// Tool render hooks — wrapped so `todo.ts` becomes a thin call-site.
// ---------------------------------------------------------------------------

/**
 * `renderCall` body. Receives the parsed args, the theme, and the live
 * `TaskState` (resolved by the caller via `getState()`). Returns a `Text`
 * node identical to pre-refactor `todo.ts:507-525`.
 */
export function renderTodoCall(
	args: TaskMutationParams & { action: TaskAction },
	theme: Theme,
	state: TaskState,
): Text {
	const glyph = consoleActive() ? (args.action === "create" ? consoleIcon("add") : args.action) : ACTION_GLYPH[args.action] ?? args.action;
	let text = theme.fg("toolTitle", theme.bold("todo ")) + theme.fg("muted", glyph);

	if (args.action === "create" && args.subject) {
		text += ` ${theme.fg("dim", sanitizeTerminalText(args.subject))}`;
	} else if (
		(args.action === "update" || args.action === "get" || args.action === "delete") &&
		args.id !== undefined
	) {
		const subject = selectTaskSubjectById(state, args.id);
		text += ` ${theme.fg("accent", subject ? sanitizeTerminalText(subject) : `#${args.id}`)}`;
	} else if (args.action === "list" && args.status) {
		text += ` ${theme.fg("muted", formatStatusLabel(args.status))}`;
	}
	return new Text(text, 0, 0);
}

/**
 * `renderResult` body. Inspects `details` to pick the per-action status echo
 * (only `create`/`update`/`delete` advertise a status; `list`/`get`/`clear`
 * fall back to plain `✓`). Identical visual output to pre-refactor
 * `todo.ts:533-565`.
 */
export function renderTodoResult(result: { details?: unknown; content?: readonly { type: string; text?: string }[] }, theme: Theme, expanded = false): Text {
	const details = result.details as TaskDetails | undefined;
	const content = (result.content ?? []).filter(part => part.type === "text").map(part => part.text ?? "").join("\n");
	if (details?.error) return new Text(theme.fg("error", sanitizeTerminalText(details.error)), 0, 0);
	if (expanded || details?.action === "list" || details?.action === "get") {
		const all = content.split("\n");
		const lines = expanded ? all : all.slice(0, 4);
		if (lines.length < all.length) lines.push(keyHint("app.tools.expand", `${all.length - lines.length} more lines`));
		return new Text(theme.fg("toolOutput", lines.join("\n")), 0, 0);
	}
	let status: TaskStatus | undefined;
	if (details) {
		const params = details.params as TaskMutationParams;
		switch (details.action) {
			case "create":
				status = details.tasks[details.tasks.length - 1]?.status;
				break;
			case "update":
				status = params.status ?? details.tasks.find((t) => t.id === params.id)?.status;
				break;
			case "delete":
				status = details.tasks.find((t) => t.id === params.id)?.status;
				break;
			case "clear":
				break;
		}
	}
	if (status) {
		return new Text(theme.fg(STATUS_COLOR[status], `${STATUS_GLYPH[status]} ${formatStatusLabel(status)}`), 0, 0);
	}
	return new Text(theme.fg("success", consoleIcon("success")), 0, 0);
}
