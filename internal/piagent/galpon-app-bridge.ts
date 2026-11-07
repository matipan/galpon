// Galpon app bridge: a Pi extension that gives the agent tab of the Herdr app
// the facts that Galpon's terminal console draws itself. `galpon pi run --app`
// loads it. Pi in RPC mode sends string widgets to its client, so each fact
// goes out as one JSON line in a widget whose key starts with "galpon-app:".
// The app host (`galpon herdr-app host`) keeps these widgets for itself.
//
//   galpon-app:galpon  the Galpon agent: ID, title, workspace, placement and folder
//   galpon-app:work    the delegated work of the Work Dock (Galpon's snapshot)
//   galpon-app:tasks   the background commands and monitors

const WORK_EVENT = "galpon:work:snapshot:v1";
const COUNTS_EVENT = "galpon:background-counts";
const TASKS = Symbol.for("galpon.pi.background-tasks");
const TAIL = 8;

const firstLine = (value: unknown) => String(value ?? "").split("\n").find((line) => line.trim())?.trim().slice(0, 160) ?? "";

export default function (pi: any) {
	let ui: any;
	let timer: ReturnType<typeof setTimeout> | undefined;
	const waiting = new Map<string, string>();
	const sent = new Map<string, string>();

	const publish = (key: string, value: unknown) => {
		const line = JSON.stringify(value);
		if (sent.get(key) === line) return;
		if (!ui) {
			waiting.set(key, line);
			return;
		}
		try {
			ui.setWidget(`galpon-app:${key}`, [line]);
			sent.set(key, line);
		} catch {
			waiting.set(key, line);
		}
	};

	const identity = () => publish("galpon", {
		agentId: process.env.GALPON_AGENT_ID ?? "",
		title: process.env.GALPON_AGENT_TITLE ?? "",
		role: process.env.GALPON_AGENT_ROLE ?? "",
		workspaceId: process.env.GALPON_WORKSPACE_ID ?? "",
		workspace: process.env.GALPON_WORKSPACE_TITLE ?? "",
		placement: process.env.GALPON_PLACEMENT ?? "",
		// Galpon starts Pi in the folder of the agent.
		cwd: process.cwd(),
	});

	// The task list of galpon-background.ts. It keeps one manager for the
	// whole Pi process on globalThis.
	const tasks = () => {
		const manager = (globalThis as any)[TASKS];
		const list = manager?.tasks instanceof Map ? [...manager.tasks.values()] : [];
		const rows = list.map((task: any) => ({
			id: String(task.id),
			kind: task.kind === "monitor" ? "monitor" : "command",
			description: String(task.description ?? ""),
			command: firstLine(task.command),
			status: String(task.status ?? "running"),
			startedAt: Number(task.startedAt) || 0,
			endedAt: Number(task.endedAt) || 0,
			exitCode: task.exitCode ?? null,
			tail: Array.isArray(task.tail) ? task.tail.slice(-TAIL).map((line: unknown) => String(line).slice(0, 400)) : [],
			outputPath: String(task.outputPath ?? ""),
		}));
		publish("tasks", { tasks: rows });
		// The tail of a running task changes without an event.
		if (timer) clearTimeout(timer);
		timer = undefined;
		if (rows.some((row: any) => row.status === "running")) timer = setTimeout(tasks, 2000);
	};

	pi.events.on(WORK_EVENT, (value: unknown) => publish("work", value));
	pi.events.on(COUNTS_EVENT, () => tasks());

	pi.on("session_start", (_event: unknown, ctx: any) => {
		ui = ctx.ui;
		for (const [key, line] of waiting) {
			try {
				ui.setWidget(`galpon-app:${key}`, [line]);
				sent.set(key, line);
			} catch {
				// The next change sends it again.
			}
		}
		waiting.clear();
		identity();
		tasks();
	});
	// Galpon reads a new agent title at the start of each turn.
	pi.on("agent_start", () => identity());
	pi.on("agent_settled", () => tasks());
	pi.on("session_shutdown", () => {
		if (timer) clearTimeout(timer);
		timer = undefined;
	});

	pi.registerCommand("galpon-app-stop", {
		description: "Stop a background command or monitor (Herdr app tab)",
		handler: async (args: string) => {
			const manager = (globalThis as any)[TASKS];
			const id = String(args ?? "").trim();
			if (id && typeof manager?.stop === "function") manager.stop(id);
			tasks();
		},
	});
}
