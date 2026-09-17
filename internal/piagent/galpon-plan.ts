import { createHash, randomUUID } from "node:crypto";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionCommandContext, ExtensionContext } from "@earendil-works/pi-coding-agent";
const reviewTextHash = (text: string) => createHash("sha256").update(text).digest("hex");

export const planEvent = "galpon:plan:v1";
export const maxPlanBytes = 48 * 1024;
export type PlanRevision = { id: string; text: string; hash: string };
type PlanState = {
	version: 1; owner: string; enabled: boolean; ready: boolean;
	normalTools: string[]; revision?: PlanRevision; implementer?: string; approvedHere?: string;
};
type PlanHost = {
	agentId: string;
	ready(): boolean;
	wake(): void;
	review(source: { entryId: string; text: string; hash: string }, ctx: ExtensionCommandContext): Promise<void>;
	implement(revision: PlanRevision): Promise<void>;
	delegate(revision: PlanRevision, ctx: ExtensionCommandContext): Promise<string | undefined>;
	completed(text: string): void;
};
const readers = new Set(["read", "grep", "find", "ls", "web_search", "fetch_content", "source_check", "get_search_content"]);
const planTool = "plan_mode_complete";

export function validPlan(text: unknown): text is string {
	return typeof text === "string" && text.trim().length > 0 && Buffer.byteLength(text) <= maxPlanBytes
		&& text.split("\n").length <= 2048 && !/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/.test(text);
}
export function planSource(revision: PlanRevision) {
	return { entryId: `plan:${revision.id}`, text: revision.text, hash: revision.hash };
}
export function planningTools(active: string[], configured: string[]): string[] {
	// Native search tools are normally inactive in Pi. Enable them for exploration
	// without exposing a general shell or arbitrary custom execution tools.
	return [...new Set([...active.filter(name => readers.has(name) || name.startsWith("galpon_")),
		...configured.filter(name => ["read", "grep", "find", "ls"].includes(name)), planTool])];
}
function restore(entries: any[], owner: string): PlanState | undefined {
	for (const entry of [...entries].reverse()) {
		if (entry?.type !== "custom" || entry.customType !== planEvent) continue;
		const value = entry.data;
		if (value?.version !== 1 || value.owner !== owner || typeof value.enabled !== "boolean" || typeof value.ready !== "boolean"
			|| !Array.isArray(value.normalTools) || !value.normalTools.every((name: unknown) => typeof name === "string")) continue;
		const revision = value.revision;
		if (revision && (!/^[a-f0-9-]{36}$/.test(revision.id) || !validPlan(revision.text) || reviewTextHash(revision.text) !== revision.hash)) continue;
		if (value.ready && !revision) continue;
		if (value.approvedHere !== undefined && value.approvedHere !== revision?.id) continue;
		return { ...value, normalTools: [...value.normalTools] };
	}
}

export function registerPlan(pi: ExtensionAPI, host: PlanHost) {
	let state: PlanState = { version: 1, owner: host.agentId, enabled: false, ready: false, normalTools: [] };
	let pendingReview = "";
	let context: ExtensionContext | undefined;
	let stopped = false;
	const save = (next = state) => { pi.appendEntry(planEvent, structuredClone(next)); state = next; };
	let shownStatus: string | undefined;
	const status = () => {
		const next = state.enabled ? `Plan${state.ready ? " · ready" : ""} · /plan review · /plan do · /plan delegate` : undefined;
		if (context && next !== shownStatus) { context.ui.setStatus("galpon-plan", next); shownStatus = next; }
	};
	const applyTools = () => {
		const configured = pi.getAllTools().map(tool => tool.name);
		pi.setActiveTools(state.enabled ? planningTools(state.normalTools, configured) : state.normalTools.filter(name => name !== planTool && configured.includes(name)));
		status();
	};
	const current = (allowApproved = false): PlanRevision => {
		const approved = allowApproved && state.revision && state.approvedHere === state.revision.id;
		if ((!state.enabled && !approved) || !state.ready || !state.revision) throw new Error("No completed current plan is ready. Ask for a complete plan first.");
		return state.revision;
	};
	const review = async (ctx: ExtensionCommandContext) => {
		if (!state.revision) throw new Error("No saved plan is available.");
		await host.review(planSource(state.revision), ctx);
	};
	pi.registerTool({
		name: planTool, label: "Submit plan", description: "Only use while /plan mode is active. Submit the complete decision-ready plan for native Review. Call alone as the final action. Do not implement the plan.",
		parameters: Type.Object({ plan: Type.String({ minLength: 1, maxLength: maxPlanBytes, description: "Complete Markdown plan, including decisions, constraints, implementation steps, and verification." }) }),
		async execute(_id, params, signal, _update, ctx) {
			if (!state.enabled) throw new Error("Plan mode is not active. The user must enter /plan first.");
			if (signal?.aborted) throw new Error("Plan submission cancelled.");
			if (ctx.hasPendingMessages?.()) throw new Error("New input is queued. Read it before submitting the complete plan.");
			if (!validPlan(params.plan)) throw new Error("The plan must be nonempty Markdown of at most 48 KiB and 2048 lines, without terminal control characters.");
			const last = [...ctx.sessionManager.getBranch()].reverse().find((entry: any) => entry.type === "message" && entry.message.role === "assistant") as any;
			const calls = last?.message.content?.filter((part: any) => part.type === "toolCall") ?? [];
			if (calls.length !== 1 || calls[0].name !== planTool) throw new Error("Submit the plan alone, with no other tool calls in the same batch.");
			save({ ...state, ready: true, implementer: undefined, approvedHere: undefined, revision: { id: randomUUID(), text: params.plan, hash: reviewTextHash(params.plan) } });
			pendingReview = ctx.mode === "tui" ? state.revision!.id : "";
			host.completed(params.plan);
			status();
			host.wake();
			return { content: [{ type: "text", text: params.plan }], details: { revision: state.revision }, terminate: true };
		},
	});
	pi.registerCommand("plan", {
		description: "Enter Plan mode; review the saved plan, do it here, delegate to a foreground agent, or exit",
		getArgumentCompletions: prefix => ["review", "do", "delegate", "exit"].filter(value => value.startsWith(prefix)).map(value => ({ value, label: value })),
		handler: async (args, ctx) => {
			context = ctx;
			if (!ctx.isIdle() || !host.ready()) { ctx.ui.notify("Finish the current work before changing Plan mode.", "warning"); return; }
			try {
				const command = args.trim();
				if (command === "") {
					if (!state.enabled) save({ ...state, enabled: true, normalTools: pi.getActiveTools().filter(name => name !== planTool) });
					applyTools();
					ctx.ui.notify("Plan mode is on. Enter your task. No model turn has started.", "info");
				} else if (command === "exit") {
					if (state.enabled) { save({ ...state, enabled: false }); pendingReview = ""; applyTools(); }
					ctx.ui.notify("Plan mode is off. The saved plan was kept.", "info");
				} else if (command === "review") {
					await review(ctx);
				} else if (command === "do" || command === "delegate") {
					const revision = current(command === "do");
					if (ctx.ui.getEditorText().length > 0) throw new Error("Send or clear the unsent editor text before starting the plan. It was not changed.");
					pendingReview = "";
					if (command === "delegate") {
						const agent = await host.delegate(revision, ctx);
						if (agent) { save({ ...state, implementer: agent }); ctx.ui.notify(`Plan sent to foreground agent ${agent}.`, "info"); }
					} else {
						// A lost response can follow a committed queue entry. Keep the
						// approved normal mode so that work cannot restart under Plan
						// restrictions. The saved approval permits an idempotent retry.
						save({ ...state, enabled: false, approvedHere: revision.id }); applyTools();
						try { await host.implement(revision); }
						catch { throw new Error("Plan dispatch could not be confirmed. Normal tools are active. Retry /plan do; the same work will not be queued twice."); }
					}
				} else throw new Error("Use /plan, /plan review, /plan do, /plan delegate, or /plan exit.");
			} catch (error) { ctx.ui.notify(error instanceof Error ? error.message : String(error), "error"); }
		},
	});
	pi.registerCommand("galpon-plan-review", {
		description: "Open the exact completed Plan revision after Galpon settles",
		handler: async (revision, ctx) => {
			if (!state.enabled || !state.ready || state.revision?.id !== revision || !host.ready()) return;
			await review(ctx);
		},
	});
	pi.on("session_start", (_event, ctx) => {
		context = ctx;
		state = restore(ctx.sessionManager.getBranch(), host.agentId) ?? { ...state, normalTools: pi.getActiveTools().filter(name => name !== planTool) };
		applyTools();
	});
	pi.on("session_tree", (_event, ctx) => {
		context = ctx;
		pendingReview = "";
		state = restore(ctx.sessionManager.getBranch(), host.agentId) ?? { version: 1, owner: host.agentId, enabled: false, ready: false, normalTools: state.normalTools };
		applyTools();
	});
	pi.on("input", (_event, ctx) => {
		context = ctx;
		// Do not append before Galpon records the direct-input identity. A failed
		// input must retain the same leaf on retry instead of creating new work.
		if (state.enabled) { pendingReview = ""; state = { ...state, ready: false }; status(); }
	});
	pi.on("before_agent_start", event => {
		if (!state.enabled) return { systemPrompt: event.systemPrompt + "\n\nGalpon Plan mode is off. Earlier Plan restrictions in conversation history no longer apply. Do not call Plan completion tools." };
		save();
		applyTools();
		return { systemPrompt: event.systemPrompt + "\n\nGalpon /plan mode is active. Plan the work; do not implement it. Explore with read, grep, find, and ls. General shell, editing, TODO, and other execution tools are unavailable. Galpon coordination tools remain available only for explicitly requested planning coordination; do not use another agent to bypass planning restrictions. Ask concise questions in chat when decisions remain. When ready, call plan_mode_complete alone as the final action with the full Markdown plan: goal, decisions and constraints, implementation steps, and verification. Galpon opens native Review after the turn settles. Review feedback is not sent automatically. Only the user's /plan do or /plan delegate command starts implementation. Include all context an implementer needs in the plan and use repository-relative paths. The delegate starts from fresh main/master worktrees, not this branch." + (state.revision ? `\n\nSaved plan (${state.revision.id}); use feedback to submit a complete replacement:\n${state.revision.text}` : "") };
	});
	pi.on("tool_call", event => {
		if (!state.enabled) {
			if (event.toolName === planTool) return { block: true, reason: "Plan mode is not active." };
			return;
		}
		if (!planningTools(state.normalTools, pi.getAllTools().map(tool => tool.name)).includes(event.toolName)) return { block: true, reason: "Plan mode blocks this tool. Use read/search tools, or ask the user to exit Plan mode." };
	});
	pi.on("session_shutdown", () => { stopped = true; pendingReview = ""; });
	return {
		source: () => state.enabled && state.revision ? planSource(state.revision) : undefined,
		dispatchReview() {
			if (stopped || !pendingReview || !host.ready()) return false;
			const revision = pendingReview;
			pendingReview = "";
			pi.sendUserMessage(`/galpon-plan-review ${revision}`, { expandPromptTemplates: true });
			return true;
		},
	};
}
