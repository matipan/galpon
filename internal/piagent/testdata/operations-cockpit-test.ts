import { writeFileSync } from "node:fs";
import { visibleWidth } from "@earendil-works/pi-tui";
import { renderOperationsCockpit, renderOperationsEmergency } from "../extension.ts";

const now = Date.now();
const projection = {
	version: 1,
	workspace: { id: "workspace", title: "Operations" },
	summary: { agents: 3, activeWork: 1, waitingWork: 1, queuedWork: 0, resumeQueued: 1, todoPending: 1, legacySuppressedUnknown: 1, reportedBlockers: 1, staleObservations: 1 },
	queue: { inboundQueued: 2, inboundClaimed: 1, resultsReady: 1, receiptsPresented: 1 },
	directOperations: [{ title: "Direct Pi work", state: "waiting", source: "observed", lease: "none", count: 1, observedAt: now - 2_000, operationId: "private-operation" }],
	agents: [
		{ id: "worker", title: "Worker", status: "running", currentDelivery: { observation: { state: "started", source: "observed", lease: "fresh", leaseObservedAt: now - 2_000 }, checkpoint: { summary: "Waiting for a choice", source: "reported" } } },
		{ id: "reviewer", title: "Reviewer", status: "idle", observedDelivery: { observation: { state: "started", source: "observed", lease: "stale", leaseObservedAt: now - 40_000 } } },
		{ id: "quiet", title: "Quiet agent", status: "idle" },
	],
	work: [{
		id: "root", title: "Worker", targetTitle: "Worker", priority: "reported_blocker",
		observation: { state: "started", source: "observed", lease: "fresh", leaseObservedAt: now - 4_000, freshnessAt: now + 20_000, attempt: 2 },
		result: { stage: "delivery_queued", label: "Durable result delivery queued; Pi handling is not observed", source: "observed", observedAt: now - 1_000 },
		checkpoint: { phase: "blocked", summary: "Waiting for a choice", blocker: "Choose the safe option", source: "reported" },
		coordination: { version: 2, facts: [
			{ kind: "source_operation", state: "waiting", count: 1 },
			{ kind: "result_delivery", state: "ready", count: 1 },
			{ kind: "result_receipt", state: "presented", count: 1 },
		] },
		children: [],
	}],
	activity: { version: 1, facts: [{ category: "tool: read", status: "completed", source: "observed", observedAt: now - 3_000 }], truncation: { truncated: false } },
	truncation: { truncated: true },
};
const theme = {
	fg: (_color: string, text: string) => text,
	bold: (text: string) => text,
};

function run() {
	for (const kind of ["loading", "error"] as const) {
		const emergency = renderOperationsEmergency(kind, 12, theme);
		if (emergency.length > 5 || emergency.some((line: string) => visibleWidth(line) > 12)) throw new Error(`${kind} emergency state exceeded 12x5`);
	}
	for (const width of [12, 28, 72, 120]) {
		const lines = renderOperationsCockpit(projection, width, 0, theme);
		if (lines.length > 24) throw new Error(`height bound failed at ${width}`);
		for (const line of lines) {
			if (visibleWidth(line) > width) throw new Error(`width bound failed at ${width}: ${line}`);
		}
		const view = lines.join("\n");
		if (width >= 28) {
			for (const label of ["WORK OUTLINE", "SELECTED DETAIL", "AGENT RUNTIME", "Observed", "Reported"]) {
				if (!view.includes(label)) throw new Error(`${width} omitted ${label}`);
			}
		}
		if (width >= 120) {
			for (const fact of ["lease observed", "tool: read · completed", "Observed result", "Reported · blocked", "Protocol v2", "source operation waiting", "Direct Pi work · 1 direct Pi operation · waiting", "latest observed started delivery · stale lease", "no observed delivery · no lease"]) {
				if (!view.includes(fact)) throw new Error(`${width} omitted ${fact}`);
			}
			for (const lane of ["TODOs stay in the Work Dock", "Delegations stay in this read-only view"]) {
				if (!view.includes(lane)) throw new Error(`${width} omitted ${lane}`);
			}
		}
		if (view.includes("runtimeId") || view.includes("sessionId") || view.includes("private-operation") || view.includes("operationId")) throw new Error("private runtime or operation identity entered cockpit");
	}
}

export default function () {
	const resultPath = process.env.GALPON_OPERATIONS_COCKPIT_TEST_RESULT;
	try {
		run();
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: true }), { mode: 0o600 });
	} catch (error) {
		const message = error instanceof Error ? error.message : String(error);
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: false, error: message }), { mode: 0o600 });
		throw error;
	}
}
