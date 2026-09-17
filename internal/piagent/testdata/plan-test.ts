import { writeFileSync } from "node:fs";
import { registerPlan, planEvent, maxPlanBytes } from "../galpon-plan.ts";

const assert = (condition: unknown, message: string) => { if (!condition) throw new Error(message); };
async function rejects(fn: () => Promise<unknown>, text: string) {
	try { await fn(); } catch (error) { assert(String(error).includes(text), `wrong error: ${error}`); return; }
	throw new Error(`Expected rejection: ${text}`);
}
function fixture(entries: any[] = [], owner = "planner") {
	const hooks = new Map<string, any[]>(), commands = new Map<string, any>(), tools = new Map<string, any>();
	const normal = ["read", "bash", "edit", "write", "todo", "mcp", "galpon_send_agent", "web_search"];
	let active = [...normal], editor = "", busy = false, implementationError = false, delegated = "";
	const notices: string[] = [], sent: any[] = [], reviews: any[] = [], implementations: any[] = [], completions: string[] = [];
	const ctx: any = { mode: "tui", isIdle: () => !busy, sessionManager: { getBranch: () => entries }, ui: {
		notify: (text: string) => notices.push(text), setStatus: () => {}, getEditorText: () => editor,
	} };
	const pi: any = {
		on: (name: string, hook: any) => hooks.set(name, [...hooks.get(name) ?? [], hook]),
		registerCommand: (name: string, command: any) => commands.set(name, command),
		registerTool: (tool: any) => tools.set(tool.name, tool),
		getActiveTools: () => [...active], getAllTools: () => [...normal, "grep", "find", "ls", ...tools.keys()].map(name => ({ name })),
		setActiveTools: (names: string[]) => { active = [...names]; },
		appendEntry: (customType: string, data: any) => entries.push({ type: "custom", customType, data: structuredClone(data) }),
		sendUserMessage: (text: string, options: any) => sent.push({ text, options }),
	};
	const controller = registerPlan(pi, { agentId: owner, ready: () => !busy, wake: () => {},
		review: async source => { reviews.push(source); }, completed: text => { completions.push(text); },
		implement: async revision => { if (implementationError) throw new Error("offline"); implementations.push(revision); },
		delegate: async revision => { implementations.push(revision); return delegated || undefined; },
	});
	const emit = async (name: string, event: any = {}) => {
		let result: any;
		for (const hook of hooks.get(name) ?? []) result = await hook(event, ctx) ?? result;
		return result;
	};
	const command = (args: string) => commands.get("plan").handler(args, ctx);
	const submit = async (plan = "# Example plan\n\n1. Inspect.\n2. Implement.\n3. Verify.  \n") => {
		entries.push({ type: "message", message: { role: "assistant", content: [{ type: "toolCall", name: "plan_mode_complete" }] } });
		return tools.get("plan_mode_complete").execute("call", { plan }, undefined, undefined, ctx);
	};
	return { entries, normal, active: () => active, notices, sent, reviews, implementations, completions, emit, command, submit, controller, ctx, commands,
		setEditor: (value: string) => { editor = value; }, setBusy: (value: boolean) => { busy = value; }, failImplementation: () => { implementationError = true; }, setDelegate: (value: string) => { delegated = value; } };
}
async function run() {
	const f = fixture(); await f.emit("session_start");
	assert(JSON.stringify(f.active()) === JSON.stringify(f.normal), "startup changed normal tools");
	await rejects(() => f.submit(), "not active");
	await f.command("");
	assert(f.sent.length === 0, "/plan started a model turn");
	for (const name of ["read", "grep", "find", "ls", "galpon_send_agent", "web_search", "plan_mode_complete"]) assert(f.active().includes(name), `Plan omitted ${name}`);
	for (const name of ["write", "edit", "bash", "todo", "mcp"]) {
		assert(!f.active().includes(name), `Plan exposed ${name}`);
		assert((await f.emit("tool_call", { toolName: name })).block, `Plan did not block ${name}`);
	}
	assert(!await f.emit("tool_call", { toolName: "galpon_send_agent" }), "coordination was blocked");
	const prompt = await f.emit("before_agent_start", { systemPrompt: "BASE" });
	assert(prompt.systemPrompt.startsWith("BASE") && prompt.systemPrompt.includes("explicitly requested planning coordination") && prompt.systemPrompt.includes("main/master"), "Plan prompt lost constraints");
	await f.command("do"); assert(f.implementations.length === 0, "started without a plan");
	await rejects(() => f.submit("x".repeat(maxPlanBytes + 1)), "48 KiB");
	await rejects(() => f.submit(" \n"), "48 KiB");
	await rejects(() => f.submit("# Test\n\x1b[2J"), "48 KiB");
	const result = await f.submit();
	assert(result.terminate === true && f.completions[0] === result.content[0].text, "completion did not terminate with exact plan text");
	const first = f.controller.source()!;
	assert(first.text.endsWith("  \n"), "submission trimmed exact plan text");
	f.setBusy(true); assert(!f.controller.dispatchReview(), "opened Review before settlement");
	f.setBusy(false); assert(f.controller.dispatchReview() && !f.controller.dispatchReview(), "Review dispatch was not once-only");
	assert(f.sent[0].options.expandPromptTemplates === true, "Review command was sent to the model");
	await f.commands.get("galpon-plan-review").handler(first.entryId.slice(5), f.ctx);
	assert(f.reviews[0].hash === first.hash && f.reviews[0].entryId === first.entryId, "Review selected a different response");
	f.setEditor(" \n"); await f.command("do"); assert(f.implementations.length === 0, "replaced whitespace editor draft"); f.setEditor("");
	await f.emit("input", { text: "Change step two" });
	await f.command("do"); assert(f.implementations.length === 0, "implemented stale plan after feedback");
	await f.submit("# Replacement\n\nUse the new requirement.");
	assert(f.controller.source()?.entryId !== first.entryId, "revision ID was reused");
	await f.command("delegate"); assert(f.active().includes("plan_mode_complete"), "cancelled delegation exited Plan mode");
	await f.command("do");
	assert(JSON.stringify(f.active()) === JSON.stringify(f.normal), "implementation did not restore exact normal tools");
	assert((await f.emit("tool_call", { toolName: "plan_mode_complete" })).block, "completion tool was callable in normal mode");
	assert(!(await f.emit("tool_call", { toolName: "write" }))?.block, "normal writing remained blocked");
	await f.command(""); await f.submit(); f.failImplementation(); await f.command("do");
	assert(!f.active().includes("plan_mode_complete") && f.notices.some(text => text.includes("Retry /plan do")), "ambiguous dispatch re-enabled planning restrictions on admitted work");
	const restored = fixture(structuredClone(f.entries)); await restored.emit("session_start");
	assert(!restored.active().includes("plan_mode_complete"), "reload lost approved normal mode");
	await restored.command("do");
	assert(restored.implementations.length === 1 && restored.implementations[0].id === f.entries.at(-1).data.revision.id, "reload could not retry the same approved revision");
	await restored.command("");
	const planningReload = fixture(structuredClone(restored.entries)); await planningReload.emit("session_start");
	assert(planningReload.active().includes("plan_mode_complete") && planningReload.controller.source()?.text === restored.controller.source()?.text, "reload lost mode or revision");
	assert(!restored.controller.dispatchReview(), "reload automatically reopened old Review");
	const foreign = fixture(structuredClone(f.entries), "new-agent"); await foreign.emit("session_start");
	assert(!foreign.active().includes("plan_mode_complete") && !foreign.controller.source(), "fresh/forked agent inherited another agent's Plan mode");
	await restored.command("exit");
	assert(restored.entries.some(entry => entry.customType === planEvent && entry.data.revision), "exit deleted the saved plan");
}
export default async function () {
	try { await run(); writeFileSync(process.env.GALPON_PLAN_TEST_RESULT!, JSON.stringify({ ok: true })); }
	catch (error) { writeFileSync(process.env.GALPON_PLAN_TEST_RESULT!, JSON.stringify({ ok: false, error: String(error) })); throw error; }
}
