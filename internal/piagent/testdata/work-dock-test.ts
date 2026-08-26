import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { TodoOverlay, WORK_LIVENESS_FRAMES, WORK_LIVENESS_INTERVAL_MS } from "../builtin/rpiv-todo/todo-overlay.ts";
import { registerWorkDockIntegration } from "../builtin/rpiv-todo/integrations/work.ts";
import { replaceState, setActiveRenderSession } from "../builtin/rpiv-todo/state/store.ts";

function equal(actual: unknown, expected: unknown, label: string) {
	if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`${label}: ${JSON.stringify(actual)}`);
}

function setMaxWidgetLines(lines: number) {
	const root = process.env.XDG_CONFIG_HOME;
	if (!root) throw new Error("XDG_CONFIG_HOME is required for the Work Dock harness");
	const directory = join(root, "rpiv-todo");
	mkdirSync(directory, { recursive: true });
	writeFileSync(join(directory, "config.json"), JSON.stringify({ maxWidgetLines: lines }), { mode: 0o600 });
}

function item(index: number, state = "started") {
	return {
		id: `work-${index}`,
		title: `Worker ${index}`,
		createdAt: Date.now(),
		updatedAt: Date.now(),
		observation: { state, source: "observed", lease: "fresh", leaseObservedAt: Date.now(), freshnessAt: Date.now() + 60_000 },
		checkpoint: state === "started" ? { phase: "working", summary: "Safe checkpoint", source: "reported", reportedAt: 180 } : undefined,
		children: [],
	};
}

function runWorkDockTest() {
	let listener: (value: unknown) => void = () => {};
	let listenerDisposed = 0;
	const fakePi = { events: { on: (_name: string, callback: (value: unknown) => void) => { listener = callback; return () => { listenerDisposed++; }; } } } as any;
	const unregisterWork = registerWorkDockIntegration(fakePi, async () => {});
	listener({ schemaVersion: 1, work: [item(1)] });

	let factory: any;
	let clears = 0;
	let renders = 0;
	let expanded = false;
	const tui = { requestRender: () => { renders++; } };
	const theme = { fg: (_color: string, text: string) => text, strikethrough: (text: string) => text };
	const ui = {
		theme,
		getToolsExpanded: () => expanded,
		setWidget: (_key: string, value: any) => { if (value) factory = value; else clears++; },
	} as any;
	const expectedFrames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
	equal(WORK_LIVENESS_FRAMES, expectedFrames, "Pi Working spinner frame sequence");
	if (WORK_LIVENESS_INTERVAL_MS !== 80) throw new Error(`Pi Working spinner interval was ${WORK_LIVENESS_INTERVAL_MS}ms`);
	let timerSequence = 0;
	const activeTimers = new Map<object, () => void>();
	const timerDelays: number[] = [];
	let clearedTimers = 0;
	const clock = {
		setInterval: (callback: () => void, delay: number) => {
			const timer = { id: ++timerSequence };
			activeTimers.set(timer, callback);
			timerDelays.push(delay);
			return timer;
		},
		clearInterval: (timer: object) => {
			if (activeTimers.delete(timer)) clearedTimers++;
		},
	};
	const overlay = new TodoOverlay(clock as any);
	const headless = new TodoOverlay();
	headless.update();
	overlay.setUICtx(ui);
	overlay.update();
	const component = factory(tui, theme);
	equal(component.render(120), [
		"● Work Dock · 0 todos · 1 delegation",
		"└─ Delegations (1/1 active)",
		"   ├─ ⠋ Worker 1 [started · observed] (working · Safe checkpoint · reported) lease observed now",
		"",
	], "exact expanded layout");
	if (!(overlay as any).livenessTimer || activeTimers.size !== 1 || timerDelays[0] !== 80) throw new Error("a fresh started delegation did not start one 80ms render-only timer");
	for (const frame of expectedFrames) {
		const line = component.render(120).find((value: string) => value.includes("Worker 1")) || "";
		if (!line.includes(`├─ ${frame} Worker 1`)) throw new Error(`spinner frame ${frame} was not rendered: ${line}`);
		for (const callback of activeTimers.values()) callback();
	}
	if (!component.render(120).some((line: string) => line.includes("├─ ⠋ Worker 1"))) throw new Error("spinner did not wrap to its first frame");
	listener({ schemaVersion: 1, work: [{ ...item(1), observation: { state: "started", source: "observed", lease: "fresh", leaseObservedAt: Date.now() - 60_000, freshnessAt: Date.now() - 1 } }] });
	overlay.update();
	if ((overlay as any).livenessTimer) throw new Error("an expired lease timestamp kept its animation timer");
	listener({ schemaVersion: 1, work: [{ ...item(1), checkpoint: undefined, timeline: [{ kind: "checkpoint", label: "Prior safe report", source: "reported", createdAt: Date.now() - 60_000 }], observation: { state: "started", source: "observed", lease: "stale", leaseObservedAt: Date.now() - 60_000, freshnessAt: Date.now() - 1 } }] });
	overlay.update();
	if ((overlay as any).livenessTimer) throw new Error("a stale delegation kept its animation timer");
	if (!component.render(160).some((line: string) => line.includes("historical report · Prior safe report"))) throw new Error("stale checkpoint was not rendered as historical");
	listener({ schemaVersion: 1, work: [item(1)] });
	overlay.update();
	component.invalidate();
	(ui as any).theme = { fg: (color: string, text: string) => `<${color}>${text}`, strikethrough: (text: string) => text };
	const themed = component.render(240);
	if (!themed[0].includes("<accent>")) throw new Error("theme invalidation did not use the replacement theme");
	if (!themed.some((line: string) => line.includes("<accent>⠋"))) throw new Error("fresh spinner did not use the accent color");
	(ui as any).theme = theme;
	equal(component.render(120).at(-1), "", "theme invalidation keeps trailing spacing");

	overlay.toggleCollapse();
	equal(component.render(120), [
		"● Work Dock · 0 todos · 1 delegation",
		"└─ ctrl+shift+t to expand",
		"",
	], "collapsed layout");
	overlay.toggleCollapse();
	if (renders < 2) throw new Error("collapse did not request a shape render");

	listener({ schemaVersion: 1, work: Array.from({ length: 30 }, (_, index) => item(index)) });
	overlay.update();
	if (activeTimers.size !== 1) throw new Error("visible fresh delegations created more than one shared timer");
	setActiveRenderSession("dock-test");
	replaceState("dock-test", {
		nextId: 21,
		tasks: Array.from({ length: 20 }, (_, index) => ({ id: index + 1, subject: `Todo ${index + 1}`, status: index < 10 ? "completed" : "pending" })),
	});
	overlay.update();
	const normal = component.render(100);
	if (normal.length > 13) throw new Error("normal row budget exceeded");
	if (!normal.some((line: string) => line.includes("Todo 11"))) throw new Error("completed TODOs hid active TODOs");
	expanded = true;
	const expandedLines = component.render(100);
	if (!expandedLines.some((line: string) => line.includes("Todo 20"))) throw new Error("expanded mode did not show all TODOs");
	if (expandedLines.filter((line: string) => line.includes("Worker ")).length > 8) throw new Error("expanded delegations exceeded their separate bound");
	expanded = false;
	const completedRoot = {
		...item(200, "completed"),
		title: "Final multi-harness security review",
		children: [
			...Array.from({ length: 5 }, (_, index) => ({ ...item(201 + index, "completed"), title: `Completed sibling ${index + 1}` })),
			{ ...item(206), title: "Companion Work Dock redesign" },
		],
	};
	listener({ schemaVersion: 1, work: [completedRoot] });
	replaceState("dock-test", { nextId: 37, tasks: [{ id: 36, subject: "Finish Work Dock", status: "in_progress" }] });
	overlay.update();
	const heading = "● Work Dock · 1 todo · 7 delegations";
	const todosHeading = "├─ Todos (0/1)";
	const todoRow = "│  ├─ ◐ Finish Work Dock";
	const delegationsHeading = "└─ Delegations (1/7 active)";
	const hiddenRow = "   └─ 7 delegated items hidden";
	const table: Array<{ budget: number; expected: string[] }> = [
		{ budget: 3, expected: [heading, todosHeading, "└─ Delegations (1/7 active · 7 delegated items hidden)", ""] },
		{ budget: 4, expected: [heading, todosHeading, delegationsHeading, hiddenRow, ""] },
		{ budget: 5, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 6, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 7, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 8, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 9, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 10, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 11, expected: [heading, todosHeading, todoRow, delegationsHeading, hiddenRow, ""] },
		{ budget: 12, expected: [
			heading,
			todosHeading,
			todoRow,
			delegationsHeading,
			"   ├─ ✓ Final multi-harness security review [completed · observed] observed now",
			"     ├─ ⠋ Companion Work Dock redesign [started · observed] (working · Safe checkpoint · reported) lease observed now",
			"   └─ 5 delegated items hidden",
			"",
		] },
	];
	for (const testCase of table) {
		setMaxWidgetLines(testCase.budget);
		const actual = component.render(160);
		equal(actual, testCase.expected, `active descendant exact layout at budget ${testCase.budget}`);
		if (actual.length > testCase.budget + 1) throw new Error(`budget ${testCase.budget} exceeded the shared row budget`);
	}

	replaceState("dock-test", { tasks: [], nextId: 1 });
	listener({ schemaVersion: 1, work: [{
		...item(96, "waiting"),
		coordination: { version: 2, facts: [
			{ kind: "source_operation", state: "waiting", observedAt: Date.now() },
			{ kind: "result_delivery", state: "ready", observedAt: Date.now() },
			{ kind: "result_receipt", state: "presented", observedAt: Date.now() },
			{ kind: "secret", state: "pending", observedAt: Date.now() },
		] },
	}] });
	overlay.update();
	const v2 = component.render(240).join("\n");
	for (const label of ["[waiting · observed]", "operation waiting", "result ready", "result receipt presented"]) {
		if (!v2.includes(label)) throw new Error(`Work Dock omitted protocol v2 label: ${label}`);
	}
	if (v2.includes("secret")) throw new Error("Work Dock rendered an unsafe protocol v2 fact");

	listener({ schemaVersion: 1, work: [{ ...item(97), title: `Unsafe\u202e${"x".repeat(400)}`, activity: { category: "tool: read private path", status: "started", source: "observed", observedAt: Date.now() } }, { invalid: true }, item(98)] });
	overlay.update();
	const normalized = component.render(240);
	if (!normalized.some((line: string) => line.includes("Worker 98"))) throw new Error("one invalid work item froze the complete snapshot");
	if (normalized.some((line: string) => line.includes("\u202e"))) throw new Error("work title format control was not removed");
	if (normalized.some((line: string) => line.includes("private path"))) throw new Error("unsafe activity category was rendered");

	listener({ schemaVersion: 1, work: [item(99, "completed")] });
	overlay.update();
	component.render(100);
	overlay.hideCompletedTasksFromPreviousTurn();
	equal(component.render(100), [], "completed item did not hide on the next turn");
	overlay.update();
	if (clears < 1) throw new Error("empty Work Dock did not auto-hide");

	let replacementRegistrations = 0;
	const replacement = { ...ui, setWidget: (_key: string, value: any) => { if (value) replacementRegistrations++; } } as any;
	listener({ schemaVersion: 1, work: [item(100)] });
	overlay.setUICtx(replacement);
	overlay.update();
	if (replacementRegistrations !== 1) throw new Error("session replacement did not register once");
	overlay.dispose();
	if ((overlay as any).livenessTimer || activeTimers.size !== 0 || clearedTimers === 0) throw new Error("dispose leaked the shared liveness timer");
	unregisterWork();
	if (listenerDisposed !== 1) throw new Error("dispose leaked the Work Dock snapshot listener");
}

export default function () {
	const resultPath = process.env.GALPON_WORK_DOCK_TEST_RESULT;
	const record = (value: unknown) => {
		if (resultPath) writeFileSync(resultPath, JSON.stringify(value), { mode: 0o600 });
	};
	try {
		runWorkDockTest();
		if (process.env.GALPON_WORK_DOCK_FORCE_FAILURE === "1") throw new Error("forced Work Dock assertion failure");
		record({ ok: true });
	} catch (error) {
		const message = error instanceof Error ? error.message : String(error);
		record({ ok: false, error: message });
		throw error;
	}
}
