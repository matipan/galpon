import { spawn, type ChildProcess } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { PlanRevision } from "./galpon-plan.ts";

export async function launchPlanAgent(ctx: any, root: string, agentId: string, revision: PlanRevision, onStop: (stop: (() => Promise<void>) | undefined) => void, spawnProcess: typeof spawn = spawn): Promise<string | undefined> {
	if (ctx.mode !== "tui" || !process.stdin.isTTY || !process.stdout.isTTY) throw new Error("Plan delegation requires a foreground terminal.");
	mkdirSync(root, { recursive: true, mode: 0o700 });
	const directory = mkdtempSync(join(root, "launch-"));
	try {
		const path = join(directory, "request.json");
		writeFileSync(path, JSON.stringify({ sourceAgentId: agentId, revisionId: revision.id, plan: revision.text }), { mode: 0o600 });
		const result = await ctx.ui.custom((tui: any, _theme: any, _keys: any, done: (result: any) => void) => {
			let child: ChildProcess | undefined;
			let output = "";
			let overflow = false;
			let ended = false;
			let terminalStopped = false;
			let resolveClosed: () => void = () => {};
			const closed = new Promise<void>(resolve => { resolveClosed = resolve; });
			const finish = (error?: string) => {
				if (ended) return;
				ended = true;
				onStop(undefined);
				try { if (terminalStopped) { tui.start(); tui.requestRender(true); } }
				finally { resolveClosed(); done({ error: overflow ? "The agent form returned too much data. The plan was kept." : error, output }); }
			};
			let stopping: Promise<void> | undefined;
			const stop = () => stopping ??= (async () => {
				child?.kill("SIGTERM");
				const timer = setTimeout(() => child?.kill("SIGKILL"), 1500);
				try { await closed; } finally { clearTimeout(timer); }
			})();
			try {
				terminalStopped = true;
				tui.stop();
				process.stdout.write("\x1b[2J\x1b[H");
				child = spawnProcess("galpon", ["plan", "delegate", "--request", path], { stdio: ["inherit", "inherit", "inherit", "pipe"] });
				child.stdio[3]?.on("data", (data: Buffer) => {
					if (ended || overflow) return;
					if (Buffer.byteLength(output) + data.length > 4096) { overflow = true; output = ""; void stop(); return; }
					output += data.toString("utf8");
				});
				child.once("error", () => finish("The foreground agent form could not start. The plan was kept."));
				child.once("close", (code, signal) => finish(code === 0 && !signal ? undefined : "The agent form stopped. Retry /plan delegate; any created agent will be reused."));
				onStop(stop);
			} catch {
				if (child?.pid) void stop(); else finish("The foreground agent form could not start. The plan was kept.");
			}
			return { render: () => [], invalidate: () => {} };
		});
		if (result?.error) throw new Error(result.error);
		let value: any;
		try { value = JSON.parse(result?.output ?? ""); } catch { throw new Error("The agent form returned no valid result. Retry /plan delegate to recover the saved launch."); }
		if (value?.agentId === "") return undefined;
		if (typeof value?.agentId !== "string" || !/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(value.agentId)) throw new Error("The agent form returned an invalid agent identity.");
		return value.agentId;
	} finally { rmSync(directory, { recursive: true, force: true }); }
}
