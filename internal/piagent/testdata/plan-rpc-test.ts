import { writeFileSync } from "node:fs";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { registerPlan, planEvent } from "../galpon-plan.ts";

export default function (pi: ExtensionAPI) {
	let context: any;
	const completed: string[] = [], implemented: string[] = [];
	pi.registerTool({ name: "galpon_test_insight", label: "Test insight", description: "Inert test tool; does not contact an agent", parameters: Type.Object({}),
		execute: async () => ({ content: [{ type: "text", text: "Test only" }], details: {} }),
	});
	registerPlan(pi, {
		agentId: "test-planner", ready: () => context?.isIdle() ?? true, wake: () => {},
		review: async () => { throw new Error("RPC must not open the terminal Review"); },
		delegate: async () => { throw new Error("This test does not create agents"); },
		implement: async revision => { implemented.push(revision.text); },
		completed: text => { completed.push(text); },
	});
	const report = () => writeFileSync(process.env.GALPON_PLAN_RPC_RESULT!, JSON.stringify({
		active: pi.getActiveTools(), completed, implemented,
		states: context?.sessionManager.getBranch().filter((entry: any) => entry.customType === planEvent).map((entry: any) => entry.data),
	}));
	pi.on("session_start", (_event, ctx) => { context = ctx; report(); });
	pi.on("agent_settled", () => report());
	pi.registerCommand("plan-probe", { description: "Save isolated test state", handler: async (_args, ctx) => { context = ctx; report(); } });
}
