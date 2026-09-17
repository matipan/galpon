import { EventEmitter } from "node:events";
import { mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { launchPlanAgent } from "../galpon-plan-launch.ts";

const assert = (value: unknown, message: string) => { if (!value) throw new Error(message); };

export default async function () {
	const root = mkdtempSync(join(tmpdir(), "galpon-plan-launch-test-"));
	const inputTTY = Object.getOwnPropertyDescriptor(process.stdin, "isTTY"), outputTTY = Object.getOwnPropertyDescriptor(process.stdout, "isTTY");
	const originalWrite = process.stdout.write;
	try {
		Object.defineProperty(process.stdin, "isTTY", { configurable: true, value: true });
		Object.defineProperty(process.stdout, "isTTY", { configurable: true, value: true });
		process.stdout.write = (() => true) as any;
		for (const scenario of ["success", "cancel", "invalid", "null", "overflow", "error", "throw", "stop"]) {
			let stopped = 0, started = 0, rendered = 0, stop: (() => Promise<void>) | undefined;
			const agentId = "00000000-0000-4000-8000-000000000001";
			const revision = { id: "00000000-0000-4000-8000-000000000002", text: "# Exact plan\n\nKeep whitespace.  \n", hash: "hash" };
			const ctx = { mode: "tui", ui: { custom: (factory: any) => new Promise(resolve => factory({ stop: () => stopped++, start: () => started++, requestRender: () => rendered++ }, {}, {}, resolve)) } };
			const spawn = (executable: string, args: string[], options: any) => {
				assert(executable === "galpon" && args.slice(0, 3).join(" ") === "plan delegate --request", "launch bypassed the foreground form");
				assert(JSON.stringify(options.stdio) === JSON.stringify(["inherit", "inherit", "inherit", "pipe"]), "form did not inherit the real terminal");
				const request = JSON.parse(readFileSync(args[3], "utf8"));
				assert(request.sourceAgentId === agentId && request.revisionId === revision.id && request.plan === revision.text, "handoff lost exact source data");
				assert((statSync(args[3]).mode & 0o777) === 0o600, "request permissions are not private");
				if (scenario === "throw") throw new Error("spawn failure");
				const child = new EventEmitter() as any;
				child.pid = 12345;
				child.stdio = [null, null, null, new EventEmitter()];
				child.kill = (signal: string) => { child.emit("close", null, signal); return true; };
				queueMicrotask(() => {
					if (scenario === "stop") { void stop!(); return; }
					if (scenario === "error") { child.emit("error", new Error("spawn error")); return; }
					const output = scenario === "overflow" ? " ".repeat(5000) : scenario === "invalid" ? "{" : scenario === "null" ? "null" : JSON.stringify({ agentId: scenario === "cancel" ? "" : agentId });
					child.stdio[3].emit("data", Buffer.from(output));
					child.emit("close", 0, null);
				});
				return child;
			};
			let value: string | undefined, failed = false;
			try { value = await launchPlanAgent(ctx, root, agentId, revision, next => { stop = next; }, spawn as any); }
			catch { failed = true; }
			assert(failed === !["success", "cancel"].includes(scenario), `${scenario}: unexpected result`);
			if (scenario === "success") assert(value === agentId, "agent identity was lost");
			if (scenario === "cancel") assert(value === undefined, "cancel returned an agent");
			assert(stopped === 1 && started === 1 && rendered === 1 && !stop, `${scenario}: terminal ownership was not restored exactly once`);
			assert(readdirSync(root).length === 0, `${scenario}: handoff files were retained`);
		}
		writeFileSync(process.env.GALPON_PLAN_LAUNCH_TEST_RESULT!, JSON.stringify({ ok: true }));
	} catch (error) {
		writeFileSync(process.env.GALPON_PLAN_LAUNCH_TEST_RESULT!, JSON.stringify({ ok: false, error: String(error) }));
		throw error;
	} finally {
		process.stdout.write = originalWrite;
		if (inputTTY) Object.defineProperty(process.stdin, "isTTY", inputTTY); else delete (process.stdin as any).isTTY;
		if (outputTTY) Object.defineProperty(process.stdout, "isTTY", outputTTY); else delete (process.stdout as any).isTTY;
		rmSync(root, { recursive: true, force: true });
	}
}
