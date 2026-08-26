import { writeFileSync } from "node:fs";
import { visibleWidth } from "@earendil-works/pi-tui";
import { renderOperationsCockpit } from "../extension.ts";

const projection = {
	version: 1,
	workspace: { id: "workspace", title: "Operations" },
	summary: { agents: 2, activeWork: 1, queuedWork: 0, reportedBlockers: 1, staleObservations: 1 },
	agents: [{ id: "worker", title: "Worker", status: "running", currentDelivery: { observation: { state: "started", source: "observed" } } }],
	work: [{
		id: "root", title: "Worker", priority: "reported_blocker",
		observation: { state: "started", source: "observed", lease: "stale", attempt: 2 },
		checkpoint: { phase: "blocked", summary: "Waiting for a choice", blocker: "Choose the safe option", source: "reported" },
		children: [],
	}],
	truncation: { truncated: true },
};
const theme = {
	fg: (_color: string, text: string) => text,
	bold: (text: string) => text,
};

function run() {
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
			for (const lane of ["TODOs stay in the Work Dock", "Delegations stay in this read-only view"]) {
				if (!view.includes(lane)) throw new Error(`${width} omitted ${lane}`);
			}
		}
		if (view.includes("runtimeId") || view.includes("sessionId")) throw new Error("private runtime identity entered cockpit");
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
