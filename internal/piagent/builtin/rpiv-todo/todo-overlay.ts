/**
 * todo-overlay.ts — Persistent widget showing todo list above the editor.
 *
 * Lifecycle controller for Pi's `setWidget` contract: factory-form
 * registration in widgetContainerAbove, register-once + requestRender()
 * refresh, configurable collapse-not-scroll (default 12 content rows via
 * getMaxWidgetLines(); plus a trailing spacer row so the widget renders up
 * to 13 lines), Pi tool-output expansion awareness, auto-hide when empty.
 *
 * Reads live state via `getRenderState()` (the ctx-less foreground slot) at render
 * time — NEVER `replayFromBranch` from `tool_execution_end` (branch is stale;
 * `message_end` runs after).
 */

import type { ExtensionUIContext, Theme } from "@earendil-works/pi-coding-agent";
import { type TUI, truncateToWidth } from "@earendil-works/pi-tui";
import { COLLAPSE_KEY_OFF, getMaxWidgetLines, resolveCollapseKey } from "./config.js";
import { getWorkSnapshot, isWorkSnapshotTruncated, type WorkDockItem, type WorkState } from "./integrations/work.js";
import { formatStatusLabel, t } from "./state/i18n-bridge.js";
import { selectHasActive, selectOverlayLayout, selectShowTaskIds, selectTodoCounts } from "./state/selectors.js";
import { getRenderState } from "./state/store.js";
import { sanitizeTerminalText } from "./tool/sanitize.js";
import { formatOverlayTaskLine } from "./view/format.js";

const WIDGET_KEY = "rpiv-todos";
const WORK_DOCK_HEADING = "Work Dock";
const DELEGATIONS_HEADING = "Delegations";

// English fallbacks for localized overlay chrome strings.
const OVERLAY_HEADING = "Todos";
const OVERLAY_MORE = "more";
const OVERLAY_EXPAND_HINT = "{key} to expand";
const OVERLAY_COLLAPSED = "collapsed";

type WorkDockRow = { item: WorkDockItem; depth: number; ancestors: WorkDockItem[] };

function isActiveWork(item: WorkDockItem): boolean {
	return item.observation.state === "queued" || item.observation.state === "started";
}

function prioritizeWorkRows(work: WorkDockItem[]): WorkDockRow[] {
	const activeBranches = new Map<WorkDockItem, boolean>();
	const markActiveBranches = (item: WorkDockItem): boolean => {
		let active = isActiveWork(item);
		for (const child of item.children ?? []) {
			if (markActiveBranches(child)) active = true;
		}
		activeBranches.set(item, active);
		return active;
	};
	for (const item of work) markActiveBranches(item);

	const rows: WorkDockRow[] = [];
	const visit = (items: WorkDockItem[], depth: number, ancestors: WorkDockItem[]) => {
		const ordered = items
			.map((item, index) => ({ item, index, active: activeBranches.get(item) === true }))
			.sort((left, right) => Number(right.active) - Number(left.active) || left.index - right.index);
		for (const { item } of ordered) {
			rows.push({ item, depth, ancestors });
			visit(item.children ?? [], depth + 1, [...ancestors, item]);
		}
	};
	visit(work, 0, []);
	return rows;
}

function selectCompactWorkRows(rows: WorkDockRow[], budget: number): WorkDockRow[] {
	if (budget <= 0) return [];
	if (rows.length <= budget) return rows;
	const selected = new Set<WorkDockItem>();
	const addWithContext = (row: WorkDockRow): boolean => {
		const required = [...row.ancestors, row.item].filter((item) => !selected.has(item));
		if (selected.size + required.length > budget) return false;
		for (const item of required) selected.add(item);
		return true;
	};

	let activeHidden = false;
	for (const row of rows) {
		if (isActiveWork(row.item) && !addWithContext(row)) activeHidden = true;
	}
	if (!activeHidden) {
		for (const row of rows) {
			if (selected.size >= budget) break;
			addWithContext(row);
		}
	}
	return rows.filter(({ item }) => selected.has(item));
}

function hiddenWorkLabel(count: number, truncated: boolean): string {
	const quantity = truncated ? `At least ${count}` : String(count);
	return `${quantity} delegated ${count === 1 ? "item" : "items"} hidden`;
}

export class TodoOverlay {
	private uiCtx: ExtensionUIContext | undefined;
	private widgetRegistered = false;
	private tui: TUI | undefined;
	private completedTaskIdsPendingHide = new Set<number>();
	private hiddenCompletedTaskIds = new Set<number>();
	private completedWorkIdsPendingHide = new Set<string>();
	private hiddenCompletedWorkIds = new Set<string>();
	private lastNextId: number | undefined;
	private collapsed = false;

	setUICtx(ctx: ExtensionUIContext): void {
		// Identity-compare so repeat session_start handlers are idempotent;
		// on identity change (/reload) invalidate so update() re-registers.
		if (ctx !== this.uiCtx) {
			this.uiCtx = ctx;
			this.widgetRegistered = false;
			this.tui = undefined;
		}
	}

	update(): void {
		if (!this.uiCtx) return;
		const snapshot = this.getSnapshot();
		const visible = this.selectOverlayTasks(snapshot);
		const work = this.selectVisibleWork();

		if (visible.length === 0 && work.length === 0) {
			if (this.widgetRegistered) {
				this.uiCtx.setWidget(WIDGET_KEY, undefined);
				this.widgetRegistered = false;
				this.tui = undefined;
			}
			return;
		}

		if (!this.widgetRegistered) {
			this.uiCtx.setWidget(
				WIDGET_KEY,
				(tui, factoryTheme) => {
					this.tui = tui;
					return {
						render: (width: number) => this.renderWidget(this.uiCtx?.theme ?? factoryTheme, width),
						invalidate: () => {
							// No rendered strings are cached. Pi invalidates on theme changes;
							// the next render reads uiCtx.theme.
						},
					};
				},
				{ placement: "aboveEditor" },
			);
			this.widgetRegistered = true;
		} else {
			this.tui?.requestRender();
		}
	}

	resetCompletedDisplayState(): void {
		this.completedTaskIdsPendingHide.clear();
		this.hiddenCompletedTaskIds.clear();
		this.completedWorkIdsPendingHide.clear();
		this.hiddenCompletedWorkIds.clear();
		this.lastNextId = undefined;
	}

	hideCompletedTasksFromPreviousTurn(): void {
		if (this.completedTaskIdsPendingHide.size === 0 && this.completedWorkIdsPendingHide.size === 0) return;
		for (const taskId of this.completedTaskIdsPendingHide) this.hiddenCompletedTaskIds.add(taskId);
		for (const workId of this.completedWorkIdsPendingHide) this.hiddenCompletedWorkIds.add(workId);
		this.completedTaskIdsPendingHide.clear();
		this.completedWorkIdsPendingHide.clear();
		this.tui?.requestRender();
	}

	toggleCollapse(): void {
		this.collapsed = !this.collapsed;
		// Forced full redraw on the collapsed↔expanded height step, mirroring the
		// lane-dock's requestRender(shapeChanged); distinct from the non-forced
		// requestRender() refresh paths in update()/hideCompletedTasksFromPreviousTurn().
		this.tui?.requestRender(true);
	}

	isRegistered(): boolean {
		return this.widgetRegistered;
	}

	private getSnapshot() {
		const state = getRenderState();
		if (this.lastNextId !== undefined && state.nextId < this.lastNextId) {
			this.resetCompletedDisplayState();
		}
		this.lastNextId = state.nextId;
		const completedTaskIds = new Set(
			state.tasks.filter((task) => task.status === "completed").map((task) => task.id),
		);
		for (const taskId of this.completedTaskIdsPendingHide) {
			if (!completedTaskIds.has(taskId)) this.completedTaskIdsPendingHide.delete(taskId);
		}
		for (const taskId of this.hiddenCompletedTaskIds) {
			if (!completedTaskIds.has(taskId)) this.hiddenCompletedTaskIds.delete(taskId);
		}
		return { tasks: [...state.tasks], nextId: state.nextId };
	}

	private selectOverlayTasks(snapshot: ReturnType<TodoOverlay["getSnapshot"]>) {
		return snapshot.tasks.filter((task) => task.status !== "deleted" && !this.shouldHideCompletedTask(task));
	}

	private shouldHideCompletedTask(task: ReturnType<TodoOverlay["getSnapshot"]>["tasks"][number]): boolean {
		return task.status === "completed" && this.hiddenCompletedTaskIds.has(task.id);
	}

	private selectVisibleWork(): WorkDockItem[] {
		const work = getWorkSnapshot();
		const currentIds = new Set<string>();
		const filter = (items: readonly WorkDockItem[]): WorkDockItem[] => items.flatMap((item) => {
			currentIds.add(item.id);
			const children = filter(item.children ?? []);
			if (item.observation.state === "completed" && this.hiddenCompletedWorkIds.has(item.id) && children.length === 0) return [];
			return [{ ...item, children }];
		});
		const visible = filter(work);
		for (const id of this.completedWorkIdsPendingHide) if (!currentIds.has(id)) this.completedWorkIdsPendingHide.delete(id);
		for (const id of this.hiddenCompletedWorkIds) if (!currentIds.has(id)) this.hiddenCompletedWorkIds.delete(id);
		return visible;
	}

	private renderWidget(theme: Theme, width: number): string[] {
		const snapshot = this.getSnapshot();
		const overlayTasks = this.selectOverlayTasks(snapshot);
		const work = this.selectVisibleWork();
		if (overlayTasks.length === 0 && work.length === 0) return [];
		if (work.length > 0) return this.renderWorkDock(theme, width, snapshot, overlayTasks, work);

		const overlayState = { tasks: overlayTasks, nextId: snapshot.nextId };
		const truncate = (line: string): string => truncateToWidth(line, width, "…");
		const counts = selectTodoCounts(overlayState);
		const hasActive = selectHasActive(overlayState);
		const showIds = selectShowTaskIds(overlayState);

		const headingColor = hasActive ? "accent" : "dim";
		const headingIcon = hasActive ? "●" : "○";
		const headingText = `${t("overlay.heading", OVERLAY_HEADING)} (${counts.completed}/${counts.total})`;
		const heading = truncate(`${theme.fg(headingColor, headingIcon)} ${theme.fg(headingColor, headingText)}`);

		// Collapsed view: just the heading + a dim "└─" expand hint, then the
		// trailing spacer. Short-circuit before the budget math and the completed-
		// display tracking — nothing is shown to track, and skipping the tracking
		// when nothing is rendered is correctness, not optimization. The hint splices
		// the resolved key into the {key} placeholder (per-render, like the row
		// budget); a config edit needs /reload to re-bind the actual shortcut. The
		// "off" sentinel is reachable here mid-session (config edited after the
		// shortcut was bound and the overlay collapsed) — render a static collapsed
		// label instead of splicing the sentinel into the placeholder.
		if (this.collapsed) {
			const key = resolveCollapseKey();
			const hint =
				key === COLLAPSE_KEY_OFF
					? t("overlay.collapsed", OVERLAY_COLLAPSED)
					: t("overlay.expandHint", OVERLAY_EXPAND_HINT).replace("{key}", key);
			return this.withTrailingSpacer([heading, truncate(`${theme.fg("dim", "└─")} ${theme.fg("dim", hint)}`)]);
		}

		const lines: string[] = [heading];
		// Budget for content rows (heading + tasks/summary). The rendered widget is
		// one line taller — withTrailingSpacer() appends a blank row below the panel.
		// Pi's global tool-output expansion mode is read on every render so its
		// expand/collapse shortcut also expands this live widget. Optional chaining
		// preserves compatibility with hosts predating getToolsExpanded().
		const bodyBudget = this.uiCtx?.getToolsExpanded?.() === true ? overlayTasks.length : getMaxWidgetLines() - 1;
		const layout = selectOverlayLayout(overlayState, bodyBudget);
		for (const task of layout.visible) {
			lines.push(truncate(`${theme.fg("dim", "├─")} ${formatOverlayTaskLine(task, theme, showIds)}`));
		}

		const newlyDisplayedCompletedTaskIds = overlayTasks
			.filter(
				(task) =>
					task.status === "completed" &&
					!this.completedTaskIdsPendingHide.has(task.id) &&
					!this.hiddenCompletedTaskIds.has(task.id),
			)
			.map((task) => task.id);
		for (const taskId of newlyDisplayedCompletedTaskIds) {
			this.completedTaskIdsPendingHide.add(taskId);
		}

		if (layout.hiddenCompleted === 0 && layout.truncatedTail === 0) {
			const last = lines.length - 1;
			lines[last] = lines[last].replace("├─", "└─");
			return this.withTrailingSpacer(lines);
		}

		const totalHidden = layout.hiddenCompleted + layout.truncatedTail;
		const overflowParts: string[] = [];
		if (layout.hiddenCompleted > 0) overflowParts.push(`${layout.hiddenCompleted} ${formatStatusLabel("completed")}`);
		if (layout.truncatedTail > 0) overflowParts.push(`${layout.truncatedTail} ${formatStatusLabel("pending")}`);
		const more = t("overlay.more", OVERLAY_MORE);
		const summary =
			overflowParts.length > 0 ? `+${totalHidden} ${more} (${overflowParts.join(", ")})` : `+${totalHidden} ${more}`;
		lines.push(truncate(`${theme.fg("dim", "└─")} ${theme.fg("dim", summary)}`));
		return this.withTrailingSpacer(lines);
	}

	private renderWorkDock(
		theme: Theme,
		width: number,
		snapshot: ReturnType<TodoOverlay["getSnapshot"]>,
		tasks: ReturnType<TodoOverlay["selectOverlayTasks"]>,
		work: WorkDockItem[],
	): string[] {
		const truncate = (line: string): string => truncateToWidth(line, width, "…");
		const flatWork = prioritizeWorkRows(work);
		const activeWork = flatWork.filter(({ item }) => isActiveWork(item)).length;
		const todoCounts = selectTodoCounts({ tasks, nextId: snapshot.nextId });
		const active = activeWork > 0 || tasks.some((task) => task.status === "pending" || task.status === "in_progress");
		const color = active ? "accent" : "dim";
		const workCountLabel = `${flatWork.length}${isWorkSnapshotTruncated() ? "+" : ""}`;
		const todoLabel = tasks.length === 1 ? "todo" : "todos";
		const delegationLabel = flatWork.length === 1 && !isWorkSnapshotTruncated() ? "delegation" : "delegations";
		const heading = truncate(`${theme.fg(color, active ? "●" : "○")} ${theme.fg(color, `${WORK_DOCK_HEADING} · ${tasks.length} ${todoLabel} · ${workCountLabel} ${delegationLabel}`)}`);
		if (this.collapsed) {
			const key = resolveCollapseKey();
			const hint = key === COLLAPSE_KEY_OFF
				? t("overlay.collapsed", OVERLAY_COLLAPSED)
				: t("overlay.expandHint", OVERLAY_EXPAND_HINT).replace("{key}", key);
			return this.withTrailingSpacer([heading, truncate(`${theme.fg("dim", "└─")} ${theme.fg("dim", hint)}`)]);
		}

		for (const task of tasks) {
			if (task.status === "completed" && !this.completedTaskIdsPendingHide.has(task.id) && !this.hiddenCompletedTaskIds.has(task.id)) {
				this.completedTaskIdsPendingHide.add(task.id);
			}
		}
		const headings = (tasks.length > 0 ? 1 : 0) + (flatWork.length > 0 ? 1 : 0);
		const expanded = this.uiCtx?.getToolsExpanded?.() === true;
		const totalBudget = getMaxWidgetLines();
		const itemBudget = Math.max(0, totalBudget - 1 - headings);
		const both = tasks.length > 0 && flatWork.length > 0;
		const workBudget = expanded
			? Math.min(flatWork.length, 8)
			: Math.min(flatWork.length, itemBudget, both ? Math.min(4, Math.max(1, Math.floor(itemBudget / 3))) : itemBudget);
		const workVisibleBudget = !expanded && flatWork.length > workBudget && workBudget > 0 ? workBudget - 1 : workBudget;
		const todoRowBudget = expanded ? tasks.length : Math.max(0, itemBudget - workBudget);
		const todoLayout = todoRowBudget > 0
			? selectOverlayLayout({ tasks, nextId: snapshot.nextId }, todoRowBudget)
			: { visible: [], hiddenCompleted: tasks.filter((task) => task.status === "completed").length, truncatedTail: tasks.filter((task) => task.status !== "completed").length };
		const visibleWork = selectCompactWorkRows(flatWork, workVisibleBudget);
		const hiddenWork = flatWork.length - visibleWork.length;

		const lines: string[] = [heading];
		if (tasks.length > 0) {
			const connector = both ? "├─" : "└─";
			lines.push(truncate(`${theme.fg("dim", connector)} ${theme.fg("muted", `${OVERLAY_HEADING} (${todoCounts.completed}/${todoCounts.total})`)}`));
			for (const task of todoLayout.visible) {
				lines.push(truncate(`${theme.fg("dim", both ? "│  ├─" : "   ├─")} ${formatOverlayTaskLine(task, theme, selectShowTaskIds({ tasks, nextId: snapshot.nextId }))}`));
				if (task.status === "completed" && !this.hiddenCompletedTaskIds.has(task.id)) this.completedTaskIdsPendingHide.add(task.id);
			}
			const hiddenTodos = todoLayout.hiddenCompleted + todoLayout.truncatedTail;
			if (hiddenTodos > 0 && todoRowBudget > 0) lines.push(truncate(`${theme.fg("dim", both ? "│  └─" : "   └─")} ${theme.fg("dim", `+${hiddenTodos} ${OVERLAY_MORE}`)}`));
		}
		if (flatWork.length > 0) {
			const hiddenInHeading = hiddenWork > 0 && workBudget === 0
				? ` · ${hiddenWorkLabel(hiddenWork, isWorkSnapshotTruncated())}`
				: "";
			lines.push(truncate(`${theme.fg("dim", "└─")} ${theme.fg("muted", `${DELEGATIONS_HEADING} (${activeWork}/${workCountLabel} active${hiddenInHeading})`)}`));
			for (const { item, depth } of visibleWork) {
				lines.push(truncate(`${theme.fg("dim", `   ${"  ".repeat(depth)}├─`)} ${this.formatWorkLine(item, theme)}`));
				if (item.observation.state === "completed" && !this.hiddenCompletedWorkIds.has(item.id)) this.completedWorkIdsPendingHide.add(item.id);
			}
			if (hiddenWork > 0 && workBudget > 0) {
				lines.push(truncate(`${theme.fg("dim", "   └─")} ${theme.fg("dim", hiddenWorkLabel(hiddenWork, isWorkSnapshotTruncated()))}`));
			}
		}
		return this.withTrailingSpacer(lines);
	}

	private formatWorkLine(item: WorkDockItem, theme: Theme): string {
		const glyphs: Record<WorkState, [string, "dim" | "warning" | "success" | "error"]> = {
			queued: ["○", "dim"], started: ["◐", "warning"], completed: ["✓", "success"],
			failed: ["✗", "error"], canceled: ["✗", "error"], expired: ["✗", "error"],
		};
		const [glyph, glyphColor] = glyphs[item.observation.state];
		const titleColor = item.observation.state === "started" ? "accent" : item.observation.state === "completed" ? "muted" : "text";
		let title = theme.fg(titleColor, sanitizeTerminalText(item.title));
		if (item.observation.state === "completed") title = theme.strikethrough(title);
		let line = `${theme.fg(glyphColor, glyph)} ${title} ${theme.fg("muted", `[${item.observation.state} · observed]`)}`;
		if (item.checkpoint) {
			line += ` ${theme.fg("muted", `(${sanitizeTerminalText(item.checkpoint.phase)} · ${sanitizeTerminalText(item.checkpoint.summary)} · reported)`)}`;
			if (item.checkpoint.blocker) line += ` ${theme.fg("warning", `⛓ ${sanitizeTerminalText(item.checkpoint.blocker)}`)}`;
		}
		const age = (timestamp: number): string => {
			const elapsed = Math.max(0, Date.now() - timestamp);
			if (elapsed < 60_000) return "now";
			if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)}m`;
			if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)}h`;
			return `${Math.floor(elapsed / 86_400_000)}d`;
		};
		line += ` ${theme.fg("dim", `elapsed ${age(item.createdAt)}`)}`;
		if (item.observation.lease === "stale") line += ` ${theme.fg("warning", "stale observation")}`;
		else if (item.observation.lease === "fresh") line += ` ${theme.fg("dim", "active lease")}`;
		return line;
	}

	/**
	 * Append a trailing blank line so the overlay isn't flush against the
	 * editor box. Pi's host adds a leading spacer above the widget but none
	 * below, which leaves the last "└─" row (or the "+N more" summary) glued
	 * to the input box. The empty string gives the "Todos" panel a little
	 * breathing room.
	 */
	private withTrailingSpacer(lines: string[]): string[] {
		if (lines.length === 0) return lines;
		lines.push("");
		return lines;
	}

	dispose(): void {
		if (this.uiCtx) this.uiCtx.setWidget(WIDGET_KEY, undefined);
		this.widgetRegistered = false;
		this.tui = undefined;
		this.uiCtx = undefined;
		this.collapsed = false;
		this.resetCompletedDisplayState();
	}
}
