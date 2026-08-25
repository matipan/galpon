import { TodoOverlay } from "../builtin/rpiv-todo/todo-overlay.ts";
import { registerWorkDockIntegration } from "../builtin/rpiv-todo/integrations/work.ts";

function equal(actual: unknown, expected: unknown, label: string) {
	if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`${label}: ${JSON.stringify(actual)}`);
}

function item(index: number, state = "started") {
	return {
		id: `work-${index}`,
		title: `Worker ${index}`,
		createdAt: Date.now(),
		updatedAt: Date.now(),
		observation: { state, source: "observed", lease: "fresh" },
		checkpoint: state === "started" ? { phase: "working", summary: "Safe checkpoint", source: "reported", reportedAt: 180 } : undefined,
		children: [],
	};
}

export default function () {
	let listener: (value: unknown) => void = () => {};
	const fakePi = { events: { on: (_name: string, callback: (value: unknown) => void) => { listener = callback; return () => {}; } } } as any;
	registerWorkDockIntegration(fakePi, async () => {});
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
	const overlay = new TodoOverlay();
	const headless = new TodoOverlay();
	headless.update();
	overlay.setUICtx(ui);
	overlay.update();
	const component = factory(tui, theme);
	equal(component.render(120), [
		"● Work Dock · 0 todos · 1 delegations",
		"└─ Delegations (1/1 active)",
		"   ├─ ◐ Worker 1 [started · observed] (working · Safe checkpoint · reported) elapsed now fresh now",
		"",
	], "exact expanded layout");
	component.invalidate();
	(ui as any).theme = { fg: (color: string, text: string) => `<${color}>${text}`, strikethrough: (text: string) => text };
	if (!component.render(240)[0].includes("<accent>")) throw new Error("theme invalidation did not use the replacement theme");
	(ui as any).theme = theme;
	equal(component.render(120).at(-1), "", "theme invalidation keeps trailing spacing");

	overlay.toggleCollapse();
	equal(component.render(120), [
		"● Work Dock · 0 todos · 1 delegations",
		"└─ ctrl+shift+t to expand",
		"",
	], "collapsed layout");
	overlay.toggleCollapse();
	if (renders < 2) throw new Error("collapse did not request a shape render");

	listener({ schemaVersion: 1, work: Array.from({ length: 30 }, (_, index) => item(index)) });
	overlay.update();
	if (component.render(100).length > 13) throw new Error("normal row budget exceeded");
	expanded = true;
	if (component.render(100).length > 25) throw new Error("expanded row budget exceeded");
	expanded = false;

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
}
