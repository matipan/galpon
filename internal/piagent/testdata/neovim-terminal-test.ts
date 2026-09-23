import { renameSync, writeFileSync } from "node:fs";
import { createHash, randomUUID } from "node:crypto";
import { planEvent } from "../galpon-plan.ts";
import { join } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import galpon from "../extension.ts";

// Run the actual /review handler and actual Pi UI without a daemon, tools, or
// agent dispatch. The Go harness provides a local-only mock model endpoint.
export default function (pi: ExtensionAPI) {
	const root = process.env.GALPON_NATIVE_TERMINAL_TEST_DIR!;
	const colorSource = process.env.GALPON_NATIVE_TERMINAL_COLORS
		? '\n## Colors\n\nA [link](https://example.test) and `inline code`.\n\n```go\nfmt.Println("hello")\n```\n'
		: "";
	const source = {
		type: "message", id: "native-source", timestamp: new Date().toISOString(),
		message: { role: "assistant", content: [{ type: "text", text: "# Native review\n\nA **bold** idea with é and 👨‍👩‍👧‍👦.\n\nKeep all source text.\n" + colorSource }], stopReason: "stop", timestamp: Date.now() },
	};
	const planText = source.message.content[0].text;
	const planRevision = { id: randomUUID(), text: planText, hash: createHash("sha256").update(planText).digest("hex") };
	if (process.env.GALPON_NATIVE_TERMINAL_PLAN) source.message.content[0].text = "# Not the plan\n\nThis later assistant response must not be reviewed.";
	let trial = 0;
	let sent = 0;
	const save = (name: string, value: unknown) => {
		const path = join(root, name);
		writeFileSync(path + ".tmp", JSON.stringify(value));
		renameSync(path + ".tmp", path);
	};
	pi.on("session_start", () => save("ready.json", { ready: true }));
	pi.registerShortcut("ctrl+q", { description: "Close the isolated test", handler: async ctx => { ctx.ui.setEditorText(""); ctx.shutdown(); } });
	pi.registerCommand("native-trial", {
		description: "Run one isolated native Review test",
		handler: async (_args, ctx) => {
			trial++;
			const commands = new Map<string, any>();
			const hooks = new Map<string, any[]>();
			const entries = () => [source, ...ctx.sessionManager.getBranch()];
			const fake = {
				events: { on: () => () => {}, emit: () => {} },
				on: (name: string, callback: any) => hooks.set(name, [...hooks.get(name) ?? [], callback]),
				getActiveTools: () => ["read", "bash", "edit", "write"],
				getAllTools: () => ["read", "bash", "edit", "write", "grep", "find", "ls", "plan_mode_complete"].map(name => ({ name })),
				setActiveTools: () => {},
				registerMessageRenderer: () => {},
				registerTool: () => {}, registerCommand: (name: string, command: any) => commands.set(name, command),
				appendEntry: (customType: string, data: any) => {
					pi.appendEntry(customType, data);
					save("trace.json", { trial, entries: entries() });
				},
				exec: pi.exec.bind(pi),
				sendUserMessage: () => { sent++; throw new Error("Review must not send a prompt"); },
				sendMessage: () => { sent++; throw new Error("Review must not send a message"); },
			};
			galpon(fake as any);
			if (process.env.GALPON_NATIVE_TERMINAL_PLAN) {
				if (trial === 1) pi.appendEntry(planEvent, { version: 1, owner: process.env.GALPON_AGENT_ID ?? "", enabled: true, ready: true, normalTools: ["read", "bash", "edit", "write"], revision: planRevision });
				// Restore the Plan controller only (after the console hook). This
				// adapter does not register a daemon runtime or dispatch agent work.
				await hooks.get("session_start")![1]({ reason: "reload" }, { ...ctx, sessionManager: { getBranch: entries } });
			}
			if (trial === 1 && process.env.GALPON_NATIVE_TERMINAL_UNSENT) {
				ctx.ui.setEditorText(process.env.GALPON_NATIVE_TERMINAL_UNSENT === "whitespace" ? " \n " : "Keep my unsent editor text.");
			}
			try {
				await commands.get("review").handler("", {
					...ctx,
					sessionManager: { getBranch: entries, getSessionId: () => ctx.sessionManager.getSessionId() },
					ui: {
						...ctx.ui,
						custom: async (factory: any, options: any) => {
							let tick: NodeJS.Timeout | undefined;
							try {
								return await ctx.ui.custom((tui, theme, keys, done) => {
									save("tui-api.json", { stop: tui.stop.toString(), render: tui.requestRender.toString() });
									const component = factory(tui, theme, keys, done);
									tick = setInterval(() => ctx.ui.setStatus("native-test", `BACKGROUND-RENDER-MARKER:${trial}`), 25);
									return component;
								}, options);
							} finally {
								if (tick) clearInterval(tick);
								ctx.ui.setStatus("native-test", undefined);
							}
						},
						confirm: async (title: string, body: string) => {
							save("confirm.json", { title, body });
							return ctx.ui.confirm(title, body);
						},
					},
				});
			} finally {
				// Stop this test adapter's timers, not a live Galpon runtime.
				for (const hook of hooks.get("session_shutdown") ?? []) await hook({ reason: "reload" });
			}
			save(`trial-${trial}.json`, { sent, editor: ctx.ui.getEditorText(), entries: entries() });
		},
	});
}
