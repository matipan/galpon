import { writeFileSync } from "node:fs";
import { registerOpenTasksReminder } from "../builtin/rpiv-todo/reminder.ts";
import { replaceState } from "../builtin/rpiv-todo/state/store.ts";
import { registerTodoTool } from "../builtin/rpiv-todo/todo.ts";

function text(result: any): string {
	return (result?.content ?? []).map((part: any) => part.text ?? "").join("\n");
}

function includes(value: string, expected: string, label: string) {
	if (!value.includes(expected)) throw new Error(`${label} omitted ${JSON.stringify(expected)}:\n${value}`);
}

function excludes(value: string, unexpected: string, label: string) {
	if (value.includes(unexpected)) throw new Error(`${label} included ${JSON.stringify(unexpected)}:\n${value}`);
}

async function runOpenTasksTest() {
	let todoTool: any;
	let beforeAgentStart: any;
	let activeTools = ["read", "todo"];
	const pi = {
		registerTool: (tool: any) => { todoTool = tool; },
		on: (name: string, handler: any) => { if (name === "before_agent_start") beforeAgentStart = handler; },
		getActiveTools: () => activeTools,
		appendEntry: () => {},
		events: { on: () => () => {}, emit: () => {} },
	} as any;
	registerTodoTool(pi);
	registerOpenTasksReminder(pi);
	const ctx = { sessionManager: { getSessionId: () => "open-tasks" } } as any;
	replaceState("open-tasks", {
		nextId: 6,
		tasks: [
			{ id: 1, subject: "Old plan step", status: "pending" },
			{ id: 2, subject: "Finished export", status: "completed" },
			{ id: 3, subject: "Dropped approach", status: "deleted" },
			{
				id: 4,
				subject: "Reviewer check",
				status: "in_progress",
				metadata: { galponDelegations: { child: { messageId: "child", policy: "complete_on_success" } } },
			},
			{ id: 5, subject: "Current step", status: "pending" },
		],
	});

	const update = text(await todoTool.execute("update", { action: "update", id: 5, status: "in_progress" }, undefined, undefined, ctx));
	for (const subject of ["Old plan step", "Reviewer check", "Current step"]) includes(update, subject, "update result");
	for (const subject of ["Finished export", "Dropped approach"]) excludes(update, subject, "update result");
	const delegated = update.split("\n").find((line) => line.includes("Reviewer check")) ?? "";
	includes(delegated, "delegated", "delegated task line");

	const reminder = await beforeAgentStart({ prompt: "next request" }, ctx);
	const message = reminder?.message;
	if (!message || message.display !== false) throw new Error(`open tasks did not produce a hidden reminder: ${JSON.stringify(reminder)}`);
	for (const subject of ["Old plan step", "Reviewer check", "Current step"]) includes(String(message.content), subject, "reminder");
	excludes(String(message.content), "Finished export", "reminder");

	activeTools = ["read"];
	if (await beforeAgentStart({ prompt: "plan request" }, ctx) !== undefined) throw new Error("reminder was sent while the todo tool was inactive");
	activeTools = ["read", "todo"];

	await todoTool.execute("close-old", { action: "delete", id: 1 }, undefined, undefined, ctx);
	await todoTool.execute("close-current", { action: "update", id: 5, status: "completed" }, undefined, undefined, ctx);
	const last = text(await todoTool.execute("close-delegated", { action: "update", id: 4, status: "completed" }, undefined, undefined, ctx));
	for (const subject of ["Old plan step", "Reviewer check", "Current step"]) excludes(last, subject, "clean list result");
	if (await beforeAgentStart({ prompt: "clean request" }, ctx) !== undefined) throw new Error("reminder was sent for a list without open tasks");
}

export default async function () {
	const resultPath = process.env.GALPON_TODO_OPEN_TASKS_TEST_RESULT;
	const record = (value: unknown) => {
		if (resultPath) writeFileSync(resultPath, JSON.stringify(value), { mode: 0o600 });
	};
	try {
		await runOpenTasksTest();
		record({ ok: true });
	} catch (error) {
		record({ ok: false, error: error instanceof Error ? error.message : String(error) });
		throw error;
	}
}
