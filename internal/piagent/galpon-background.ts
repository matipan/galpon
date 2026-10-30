/**
 * Background commands and monitors for Galpon's Pi runtime.
 *
 * `bash` with `run_in_background` sends one notice when its command ends.
 * `Monitor` runs a script and sends one notice for each group of stdout lines.
 * A notice reaches the model at its next tool call while it works, and starts
 * a new turn while it is idle. Tasks belong to the Pi process: they continue
 * through an extension reload, and they stop when the session ends.
 * Alt+T and `/tasks` open a screen with the tasks and the output of a task.
 */
import { spawn, type ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import { closeSync, createWriteStream, fstatSync, mkdtempSync, openSync, readSync, type WriteStream } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { StringDecoder } from "node:string_decoder";
import { stripVTControlCharacters } from "node:util";
import { Type } from "@earendil-works/pi-ai";
import {
	createBashToolDefinition, getAgentDir, getShellConfig, keyHint, SettingsManager,
	type ExtensionAPI, type ExtensionContext, type ToolDefinition,
} from "@earendil-works/pi-coding-agent";
import { matchesKey, Text, truncateToWidth, visibleWidth, wrapTextWithAnsi } from "@earendil-works/pi-tui";
import { consoleGlyph, consoleIcon } from "./builtin/rpiv-todo/view/tool-frame.ts";

/** Custom message type of a background task notice. */
export const BACKGROUND_MESSAGE = "galpon-background";

// The task list lives on the process, so that tasks continue through a reload.
// Keep the manager methods compatible: a reload can load newer code than the
// code that created the manager.
const MANAGER = Symbol.for("galpon.pi.background-tasks");
// Monitor lines that arrive within this time come as one notice.
const GROUP_MS = 200;
// Notices that are ready within this time go to the model in one message.
const OUTBOX_MS = 100;
// How often an idle check runs again while Pi is busy outside a turn.
const BUSY_RETRY_MS = 1_000;
const DEFAULT_MONITOR_TIMEOUT_MS = 5 * 60_000;
const MAX_MONITOR_TIMEOUT_MS = 60 * 60_000;
// A monitor that sends more than this in one minute is too noisy and stops.
const NOISE_WINDOW_MS = 60_000;
const MAX_NOTICES_PER_WINDOW = 30;
const MAX_LINES_PER_WINDOW = 500;
const MAX_NOTICE_LINES = 40;
const MAX_LINE_CHARS = 1_000;
const MAX_PARTIAL_LINE_CHARS = 64 * 1024;
const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;
const TAIL_LINES = 30;
const TAIL_CHARS = 4_000;
const STOP_GRACE_MS = 2_000;
const MAX_RUNNING_TASKS = 50;
const MAX_FINISHED_TASKS = 100;
// The task screen shows at most this much of the end of an output file.
const SCREEN_BYTES = 512 * 1024;
const SCREEN_REFRESH_MS = 1_000;
const TASKS_KEY = "alt+t";

type TaskKind = "command" | "monitor";
type TaskStatus = "running" | "completed" | "failed" | "stopped" | "timed_out";

export type BackgroundNotice = {
	id: string;
	kind: TaskKind;
	description: string;
	status: TaskStatus;
	summary?: string;
	lines?: string[];
	omitted?: number;
	tail?: string;
	outputPath: string;
};

type NoticeDetails = { notices: BackgroundNotice[] };

type Binding = {
	send(content: string, details: NoticeDetails): void;
	idle(): boolean;
	status(text: string | undefined): void;
};

type BashOptions = { commandPrefix?: string; shellPath?: string };

type ShellCommand = { shell: string; args: string[]; stdin?: string; cwd: string; env: NodeJS.ProcessEnv };

type Task = {
	id: string;
	kind: TaskKind;
	description: string;
	command: string;
	outputPath: string;
	startedAt: number;
	endedAt?: number;
	status: TaskStatus;
	exitCode?: number | null;
	signal?: string | null;
	child?: ChildProcess;
	stream: WriteStream;
	written: number;
	tail: string[];
	timer?: NodeJS.Timeout;
	closed: boolean;
	group: string[];
	groupLines: number;
	groupTimer?: NodeJS.Timeout;
	recent: Array<{ at: number; lines: number }>;
};

const plain = (value: unknown) => stripVTControlCharacters(String(value ?? "")).replace(/[\p{Cc}\p{Cf}]/gu, " ");
const clip = (value: string, limit = MAX_LINE_CHARS) => value.length > limit ? `${value.slice(0, limit)}…` : value;
const firstLine = (value: string) => clip(plain(value.split("\n").find(line => line.trim()) ?? "").trim(), 80);

function elapsed(ms: number): string {
	const seconds = Math.max(0, Math.round(ms / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return seconds % 60 ? `${minutes}m${String(seconds % 60).padStart(2, "0")}s` : `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	return minutes % 60 ? `${hours}h${String(minutes % 60).padStart(2, "0")}m` : `${hours}h`;
}

/** Splits a byte stream into lines. Multibyte characters can span chunks. */
class Lines {
	private readonly decoder = new StringDecoder("utf8");
	private partial = "";
	private readonly line: (text: string) => void;
	constructor(line: (text: string) => void) {
		this.line = line;
	}
	write(chunk: Buffer): void {
		this.push(this.decoder.write(chunk));
	}
	end(): void {
		this.push(this.decoder.end());
		if (!this.partial) return;
		const rest = this.partial;
		this.partial = "";
		this.line(rest.replace(/\r$/, ""));
	}
	private push(text: string): void {
		if (!text) return;
		const parts = (this.partial + text).split("\n");
		this.partial = parts.pop() ?? "";
		// Output without newlines, such as a progress bar, must not grow forever.
		if (this.partial.length > MAX_PARTIAL_LINE_CHARS) {
			parts.push(this.partial);
			this.partial = "";
		}
		for (const part of parts) this.line(part.endsWith("\r") ? part.slice(0, -1) : part);
	}
}

// Detached children lead their own process group, so a stop reaches the
// whole tree, including the children of a pipeline.
function signalGroup(child: ChildProcess | undefined, signal: NodeJS.Signals): boolean {
	const pid = child?.pid;
	if (!pid) return false;
	try {
		process.kill(process.platform === "win32" ? pid : -pid, signal);
		return true;
	} catch {
		try {
			return child!.kill(signal);
		} catch {
			return false;
		}
	}
}

function terminate(child: ChildProcess | undefined): void {
	if (!signalGroup(child, "SIGTERM")) return;
	const timer = setTimeout(() => {
		const pid = child?.pid;
		if (!pid) return;
		try {
			process.kill(process.platform === "win32" ? pid : -pid, 0);
		} catch {
			return;
		}
		signalGroup(child, "SIGKILL");
	}, STOP_GRACE_MS);
	timer.unref?.();
}

function base(task: Task): Pick<BackgroundNotice, "id" | "kind" | "description" | "outputPath"> {
	return { id: task.id, kind: task.kind, description: task.description, outputPath: task.outputPath };
}

function tailText(task: Task): string {
	let text = task.tail.join("\n");
	if (text.length > TAIL_CHARS) text = `…${text.slice(text.length - TAIL_CHARS)}`;
	return text;
}

function statusText(task: Task): string {
	const exit = task.exitCode !== undefined && task.exitCode !== null ? ` with exit code ${task.exitCode}` : task.signal ? ` by signal ${task.signal}` : "";
	switch (task.status) {
	case "running": return "is running";
	case "completed": return `completed${exit}`;
	case "failed": return `failed${exit}`;
	case "stopped": return "was stopped";
	case "timed_out": return "timed out";
	}
}

function noticeText(notice: BackgroundNotice): string {
	const parts = [
		"<task-notification>",
		`<task-id>${notice.id}</task-id>`,
		`<task-type>${notice.kind === "command" ? "background command" : "monitor"}</task-type>`,
		`<description>${notice.description}</description>`,
		`<status>${notice.status}</status>`,
	];
	if (notice.summary) parts.push(`<summary>${notice.summary}</summary>`);
	if (notice.lines?.length || notice.omitted) {
		parts.push("<event>", ...(notice.lines ?? []));
		if (notice.omitted) parts.push(`[${notice.omitted} more lines are in the output file]`);
		parts.push("</event>");
	}
	if (notice.tail !== undefined) parts.push("<output-tail>", notice.tail || "(no output)", "</output-tail>");
	parts.push(`<output-file>${notice.outputPath}</output-file>`, "</task-notification>");
	return parts.join("\n");
}

function messageText(notices: BackgroundNotice[]): string {
	return ["Background task notice from Galpon. It is not a message from the user.", ...notices.map(noticeText)].join("\n\n");
}

// Notices of one task become one notice, with its lines in order and its
// latest status.
function mergeNotices(notices: BackgroundNotice[]): BackgroundNotice[] {
	const merged = new Map<string, BackgroundNotice>();
	for (const notice of notices) {
		const earlier = merged.get(notice.id);
		if (!earlier) {
			merged.set(notice.id, notice);
			continue;
		}
		const lines = [...(earlier.lines ?? []), ...(notice.lines ?? [])];
		const shown = lines.slice(0, MAX_NOTICE_LINES);
		const omitted = (earlier.omitted ?? 0) + (notice.omitted ?? 0) + lines.length - shown.length;
		merged.set(notice.id, { ...earlier, ...notice, lines: shown, omitted });
	}
	return [...merged.values()];
}

class BackgroundTasks {
	private readonly tasks = new Map<string, Task>();
	private directory: string | undefined;
	private binding: Binding | undefined;
	// Notices wait here until the model can take them: at the end of the
	// current turn while Pi works, or at once while Pi is idle.
	private outbox: BackgroundNotice[] = [];
	private outboxTimer: NodeJS.Timeout | undefined;
	// After the user aborts a run, the waiting notices do not start a turn by
	// themselves. The next run or the next notice takes them.
	private held = false;
	private statusText: string | undefined;

	constructor() {
		process.once("exit", () => {
			for (const task of this.tasks.values()) {
				if (task.status === "running") signalGroup(task.child, "SIGTERM");
			}
		});
	}

	bind(binding: Binding): void {
		this.binding = binding;
		// A new runtime starts with no status from this module.
		this.statusText = undefined;
		this.refresh();
		this.schedule();
	}

	unbind(): void {
		this.binding = undefined;
		if (this.outboxTimer) clearTimeout(this.outboxTimer);
		this.outboxTimer = undefined;
	}

	start(kind: TaskKind, input: { command: string; description: string; shell: ShellCommand; timeoutMs?: number }): Task {
		let running = 0;
		for (const task of this.tasks.values()) if (task.status === "running") running++;
		if (running >= MAX_RUNNING_TASKS) throw new Error(`${MAX_RUNNING_TASKS} background tasks are running. Stop one with TaskStop first.`);
		this.directory ??= mkdtempSync(join(tmpdir(), "galpon-pi-tasks-"));
		const id = `${kind === "command" ? "b" : "m"}${randomUUID().replace(/-/g, "").slice(0, 7)}`;
		const outputPath = join(this.directory, `${id}.output`);
		const stream = createWriteStream(outputPath, { flags: "a", mode: 0o600 });
		stream.on("error", () => {});
		const task: Task = {
			id, kind, description: input.description, command: input.command, outputPath, startedAt: Date.now(),
			status: "running", stream, written: 0, tail: [], closed: false, group: [], groupLines: 0, recent: [],
		};
		const { shell } = input;
		let child: ChildProcess;
		try {
			child = spawn(shell.shell, shell.args, {
				cwd: shell.cwd,
				env: shell.env,
				detached: process.platform !== "win32",
				stdio: [shell.stdin === undefined ? "ignore" : "pipe", "pipe", "pipe"],
				windowsHide: true,
			});
		} catch (error) {
			stream.end();
			throw error;
		}
		this.tasks.set(id, task);
		task.child = child;
		if (shell.stdin !== undefined) {
			child.stdin?.on("error", () => {});
			child.stdin?.end(shell.stdin);
		}
		const tail = (line: string) => {
			task.tail.push(clip(plain(line)));
			if (task.tail.length > TAIL_LINES) task.tail.shift();
		};
		const stdoutTail = new Lines(tail);
		const stderrTail = new Lines(tail);
		const events = kind === "monitor" ? new Lines(line => this.eventLine(task, line)) : undefined;
		child.stdout?.on("data", (chunk: Buffer) => {
			this.write(task, chunk);
			stdoutTail.write(chunk);
			events?.write(chunk);
		});
		child.stderr?.on("data", (chunk: Buffer) => {
			this.write(task, chunk);
			stderrTail.write(chunk);
		});
		// The shell ended. Stop what it left in its group, so that the pipes close.
		child.on("exit", () => {
			if (task.status === "running") signalGroup(child, "SIGTERM");
		});
		child.on("close", (code, signal) => {
			stdoutTail.end();
			stderrTail.end();
			events?.end();
			this.finish(task, code, signal);
		});
		child.on("error", error => this.finish(task, null, null, `Galpon could not start the command: ${error.message}`));
		if (input.timeoutMs !== undefined) {
			const timeoutMs = input.timeoutMs;
			task.timer = setTimeout(() => this.timeout(task, timeoutMs), timeoutMs);
			task.timer.unref?.();
		}
		this.refresh();
		return task;
	}

	stop(id: string): string {
		const task = this.tasks.get(id);
		if (!task) return `No background task has the ID ${id}.`;
		if (task.status !== "running") return `Task ${id} is not running. It ${statusText(task)}.`;
		this.takeGroup(task);
		this.halt(task, "stopped");
		return `Stopped task ${id} (${task.description}).`;
	}

	stopAll(): void {
		for (const task of this.tasks.values()) {
			if (task.status !== "running") continue;
			this.takeGroup(task);
			this.halt(task, "stopped");
		}
		this.outbox = [];
		this.held = false;
	}

	describe(id: string): string {
		const task = this.tasks.get(id);
		if (!task) return `No background task has the ID ${id}.`;
		const now = Date.now();
		const state = task.status === "running"
			? `is running. It started ${elapsed(now - task.startedAt)} ago.`
			: `${statusText(task)} after ${elapsed((task.endedAt ?? now) - task.startedAt)}.`;
		return [
			`Task ${task.id} (${task.kind === "command" ? "background command" : "monitor"}) ${state}`,
			`Description: ${task.description}`,
			`Command: ${clip(task.command, 500)}`,
			`Output file: ${task.outputPath}`,
			"Last output lines:",
			tailText(task) || "(no output)",
		].join("\n");
	}

	list(): string[] {
		const now = Date.now();
		return [...this.tasks.values()].map(task => {
			const time = elapsed((task.endedAt ?? now) - task.startedAt);
			return `${task.id}  ${task.kind}  ${task.status === "running" ? `running ${time}` : `${task.status} after ${time}`}  ${task.description}`;
		});
	}

	// Pi lifecycle: a new run takes held notices.
	runStarted(): void {
		this.held = false;
	}

	// Pi takes steering messages right after this event, before the next model
	// request. A busy agent gets all waiting notices here, in one message.
	turnEnded(message: any): void {
		if (message?.stopReason === "aborted") {
			this.held = true;
			return;
		}
		if (message?.stopReason !== "error") this.deliver();
	}

	runSettled(): void {
		this.schedule();
	}

	private add(notice: BackgroundNotice): void {
		this.outbox.push(notice);
		this.held = false;
		this.schedule();
	}

	private schedule(delay = OUTBOX_MS): void {
		if (!this.binding || this.held || this.outboxTimer || this.outbox.length === 0) return;
		this.outboxTimer = setTimeout(() => {
			this.outboxTimer = undefined;
			if (!this.binding) return;
			// While Pi works, the end of the current turn delivers the notices. Check
			// again later in case Pi is busy without a turn, for example to compact.
			if (this.binding.idle()) this.deliver();
			else this.schedule(BUSY_RETRY_MS);
		}, delay);
		this.outboxTimer.unref?.();
	}

	// The same call starts a turn while Pi is idle, and adds a steering message
	// while Pi works.
	private deliver(): void {
		if (!this.binding || this.held || this.outbox.length === 0) return;
		const notices = mergeNotices(this.outbox.splice(0));
		try {
			this.binding.send(messageText(notices), { notices });
		} catch {
			// The runtime is gone, for example during a reload. Bind tries again.
			this.outbox.unshift(...notices);
		}
	}

	private refresh(): void {
		let running = 0;
		for (const task of this.tasks.values()) if (task.status === "running") running++;
		const text = running ? `${consoleGlyph("◷", "bg")} ${running} background · ${TASKS_KEY}` : undefined;
		// An unchanged status must not redraw the footer.
		if (!this.binding || text === this.statusText) return;
		this.statusText = text;
		try {
			this.binding.status(text);
		} catch {
			// Status is display only.
		}
	}

	private write(task: Task, chunk: Buffer): void {
		if (task.written >= MAX_OUTPUT_BYTES) return;
		const room = MAX_OUTPUT_BYTES - task.written;
		task.stream.write(chunk.length <= room ? chunk : chunk.subarray(0, room));
		task.written += Math.min(chunk.length, room);
		if (task.written >= MAX_OUTPUT_BYTES) task.stream.write(`\n[Galpon stopped writing this file at ${MAX_OUTPUT_BYTES / 1024 / 1024} MiB.]\n`);
	}

	private eventLine(task: Task, line: string): void {
		if (task.status !== "running") return;
		if (task.group.length < MAX_NOTICE_LINES) task.group.push(clip(plain(line)));
		task.groupLines++;
		if (this.recentLines(task) + task.groupLines > MAX_LINES_PER_WINDOW) {
			this.emitGroup(task);
			return;
		}
		if (!task.groupTimer) {
			task.groupTimer = setTimeout(() => this.emitGroup(task), GROUP_MS);
			task.groupTimer.unref?.();
		}
	}

	private recentLines(task: Task): number {
		const now = Date.now();
		task.recent = task.recent.filter(event => now - event.at < NOISE_WINDOW_MS);
		return task.recent.reduce((total, event) => total + event.lines, 0);
	}

	private takeGroup(task: Task): { lines: string[]; omitted: number } {
		if (task.groupTimer) clearTimeout(task.groupTimer);
		task.groupTimer = undefined;
		const lines = task.group;
		const omitted = task.groupLines - lines.length;
		task.group = [];
		task.groupLines = 0;
		return { lines, omitted };
	}

	private emitGroup(task: Task): void {
		const { lines, omitted } = this.takeGroup(task);
		if (task.status !== "running" || lines.length + omitted === 0) return;
		const recentLines = this.recentLines(task) + lines.length + omitted;
		task.recent.push({ at: Date.now(), lines: lines.length + omitted });
		const notice: BackgroundNotice = { ...base(task), status: "running", lines, omitted };
		if (task.recent.length > MAX_NOTICES_PER_WINDOW || recentLines > MAX_LINES_PER_WINDOW) {
			this.halt(task, "stopped");
			notice.status = "stopped";
			notice.summary = `Galpon stopped this monitor because it sent ${recentLines} lines in ${task.recent.length} notices within one minute. Use a tighter filter and start it again.`;
		}
		this.add(notice);
	}

	private timeout(task: Task, timeoutMs: number): void {
		if (task.status !== "running") return;
		const { lines, omitted } = this.takeGroup(task);
		this.halt(task, "timed_out");
		this.add({
			...base(task), status: "timed_out", lines, omitted,
			summary: `The monitor stopped at its timeout of ${elapsed(timeoutMs)}. Start it again if you still need it.`,
		});
	}

	private halt(task: Task, status: TaskStatus): void {
		task.status = status;
		task.endedAt = Date.now();
		if (task.timer) clearTimeout(task.timer);
		if (task.groupTimer) clearTimeout(task.groupTimer);
		task.groupTimer = undefined;
		terminate(task.child);
		this.refresh();
	}

	private finish(task: Task, code: number | null, signal: NodeJS.Signals | null, failure?: string): void {
		if (task.closed) return;
		task.closed = true;
		if (task.timer) clearTimeout(task.timer);
		task.stream.end();
		task.exitCode = code;
		task.signal = signal;
		const { lines, omitted } = this.takeGroup(task);
		// A stop, a timeout, or the noise limit ended the task, and that step
		// already sent its notice if one was due.
		if (task.status === "running") {
			task.endedAt = Date.now();
			task.status = !failure && code === 0 ? "completed" : "failed";
			const subject = task.kind === "command" ? "The command" : "The script";
			const duration = elapsed(task.endedAt - task.startedAt);
			const summary = failure
				?? (code !== null ? `${subject} exited with code ${code} after ${duration}.` : `${subject} ended by signal ${signal ?? "unknown"} after ${duration}.`);
			this.add(task.kind === "command"
				? { ...base(task), status: task.status, summary, tail: tailText(task) }
				: { ...base(task), status: task.status, summary, lines, omitted });
		}
		this.refresh();
		this.prune();
	}

	private prune(): void {
		const finished = [...this.tasks.values()].filter(task => task.status !== "running" && task.closed);
		for (const task of finished.slice(0, Math.max(0, finished.length - MAX_FINISHED_TASKS))) this.tasks.delete(task.id);
	}
}

const tasks = (): BackgroundTasks => ((globalThis as any)[MANAGER] ??= new BackgroundTasks());

/** The shell options that Pi gives its own bash tool. */
export function bashOptions(ctx: ExtensionContext): BashOptions {
	const settings = SettingsManager.create(ctx.cwd, undefined, { projectTrusted: ctx.isProjectTrusted() });
	return { commandPrefix: settings.getShellCommandPrefix(), shellPath: settings.getShellPath() };
}

// The same shell, prefix, and environment as Pi's bash tool.
function shellCommand(command: string, options: BashOptions, ctx: ExtensionContext | undefined): ShellCommand {
	const config = getShellConfig(options.shellPath) as { shell: string; args: string[]; commandTransport?: "argv" | "stdin" };
	const resolved = options.commandPrefix ? `${options.commandPrefix}\n${command}` : command;
	const stdin = config.commandTransport === "stdin";
	const env: NodeJS.ProcessEnv = { ...process.env };
	const pathKey = Object.keys(env).find(key => key.toLowerCase() === "path") ?? "PATH";
	const bin = join(getAgentDir(), "bin");
	if (!(env[pathKey] ?? "").split(delimiter).includes(bin)) env[pathKey] = [bin, env[pathKey]].filter(Boolean).join(delimiter);
	for (const name of ["PI_SESSION_ID", "PI_SESSION_FILE", "PI_PROVIDER", "PI_MODEL", "PI_REASONING_LEVEL"]) delete env[name];
	if (ctx) {
		env.PI_SESSION_ID = ctx.sessionManager.getSessionId();
		const file = ctx.sessionManager.getSessionFile();
		if (file) env.PI_SESSION_FILE = file;
		if (ctx.model) {
			env.PI_PROVIDER = ctx.model.provider;
			env.PI_MODEL = ctx.model.id;
		}
		if (ctx.thinkingLevel) env.PI_REASONING_LEVEL = ctx.thinkingLevel;
	}
	return { shell: config.shell, args: stdin ? config.args : [...config.args, resolved], stdin: stdin ? resolved : undefined, cwd: ctx?.cwd || process.cwd(), env };
}

const RUN_IN_BACKGROUND = "Run the command in the background and return at once with a task ID. When the command ends, you get a notice with its exit code and its last output lines, at your next tool call or as a new turn. Do not sleep or poll while it runs: do other work or end your turn. The timeout parameter does not apply to a background command.";

const BASH_GUIDELINES = [
	"Use run_in_background for a long command when you do not need its result at once. Then do other work or end your turn; a notice arrives when the command ends.",
	"Do not use sleep or poll loops to wait for something. Use run_in_background for one notice, or Monitor for a stream of events.",
];

const MONITOR_DESCRIPTION = `Start a background monitor: a shell script whose stdout lines become events. The monitor runs while you keep working. Its events reach you as notices at your next tool call, or start a new turn when you are idle. Notices come from Galpon, not from the user, even when you wait for the user to answer.

Choose by the number of notices that you need:
- One notice when something ends or becomes ready ("tell me when the build finishes"): use bash with run_in_background and a command that exits at that point, for example \`until curl -sf localhost:3000/health; do sleep 2; done\`.
- One notice for each occurrence ("tell me each time an ERROR line appears"): use Monitor with a script that does not end, such as \`tail -f\`, \`inotifywait -m\`, or a \`while true\` poll loop.
- One notice for each occurrence until a known end ("report each CI check, stop when the run completes"): use Monitor with a script that prints lines and then exits.

Rules for the script:
- Only stdout is the event stream. Stderr goes only to the output file. Add \`2>&1\` when the errors of a command must reach your filter.
- Each pipe stage must flush every line, or lines stay in its buffer: use \`grep --line-buffered\`, \`sed -u\`, or \`fflush()\` in awk. Do not pipe into \`head\`.
- Print only the lines that you will act on. Include failure lines, not only success lines. If the job can crash or hang, make sure that the script prints something in that case: silence looks the same as "still running".
- In poll loops, continue after a failed request (\`curl ... || true\`). Poll remote APIs every 30 seconds or more.
- Lines that arrive within ${GROUP_MS} ms come as one notice. Galpon stops a monitor that sends more than ${MAX_NOTICES_PER_WINDOW} notices or ${MAX_LINES_PER_WINDOW} lines in one minute. Start it again with a tighter filter.

The monitor ends when the script exits (you get its exit code), when timeout_ms passes, or when you call TaskStop. With persistent: true, it runs until TaskStop or the end of the session.`;

/** Adds run_in_background to a bash tool definition. Other calls run unchanged. */
export function backgroundBash(definition: ToolDefinition<any, any>, options: BashOptions): ToolDefinition<any, any> {
	const parameters = Type.Object({
		...(definition.parameters as any).properties,
		run_in_background: Type.Optional(Type.Boolean({ description: RUN_IN_BACKGROUND })),
		description: Type.Optional(Type.String({ description: "Short label for a background command. Its notice shows this label." })),
	});
	const backgroundOutput = Type.Object({ task_id: Type.String(), status: Type.Literal("running"), output_file: Type.String() });
	return {
		...definition,
		description: `${definition.description} Set run_in_background to run a long command in the background; you get a notice when it ends.`,
		promptGuidelines: [...(definition.promptGuidelines ?? []), ...BASH_GUIDELINES],
		parameters,
		...((definition as any).outputSchema ? { outputSchema: Type.Union([(definition as any).outputSchema, backgroundOutput]) } : {}),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			const { run_in_background: background, description, ...input } = (params ?? {}) as Record<string, any>;
			if (!background) return definition.execute(toolCallId, input, signal, onUpdate, ctx);
			const command = String(input.command ?? "");
			const task = tasks().start("command", {
				command, description: plain(description).trim() || firstLine(command), shell: shellCommand(command, options, ctx),
			});
			return {
				content: [{ type: "text", text: `Command running in the background with ID ${task.id}.\nOutput file: ${task.outputPath}\nYou get a notice when it ends. Do not sleep or poll its output: do other work or end your turn.` }],
				details: undefined,
				structuredContent: { task_id: task.id, status: "running", output_file: task.outputPath },
			} as any;
		},
	};
}

/**
 * The answer that Galpon saves for a run. A notice that continues a run after
 * its final answer starts a separate exchange, as a Claude Code task
 * notification does. That exchange must not replace the final answer.
 */
export class RunAnswer {
	private final = false;
	private held = false;

	/** A new run starts. */
	reset(): void {
		this.final = false;
		this.held = false;
	}

	/** Returns whether this finished message is the latest answer of the run. */
	accept(message: any): boolean {
		if (message?.role === "custom" && message.customType === BACKGROUND_MESSAGE) {
			if (this.final) this.held = true;
			return false;
		}
		// New input from the user or from Galpon continues the request itself.
		if (message?.role === "user" || message?.role === "custom") {
			this.final = false;
			this.held = false;
			return false;
		}
		if (message?.role !== "assistant" || this.held) return false;
		this.final = message.stopReason === "stop" && !(message.content ?? []).some((part: any) => part?.type === "toolCall");
		return true;
	}
}

function renderNotices(message: any, options: { expanded: boolean; outputPad?: number }, theme: any) {
	const notices: BackgroundNotice[] = Array.isArray(message.details?.notices) ? message.details.notices : [];
	const pad = options.outputPad ?? 0;
	let cache: { width: number; lines: string[] } | undefined;
	return {
		render(width: number): string[] {
			if (width < 1) return [];
			if (cache?.width === width) return cache.lines;
			const lines = [...new Text(theme.bold(`${consoleGlyph("◷", "bg")}  BACKGROUND`), pad, 0).render(width)];
			if (!notices.length) lines.push(...new Text(theme.fg("toolOutput", plain(message.content)), pad, 0).render(width));
			for (const notice of notices) {
				const state = notice.status === "running" ? "event" : notice.status.replace("_", " ");
				const color = notice.status === "failed" || notice.status === "timed_out" ? "error" : notice.status === "stopped" ? "warning" : "muted";
				lines.push(...new Text(`${theme.fg("accent", plain(notice.description))}  ${theme.fg(color, `${notice.id} · ${state}`)}`, pad, 0).render(width));
				if (notice.summary) lines.push(...new Text(theme.fg("muted", plain(notice.summary)), pad + 2, 0).render(width));
				// Show the first lines of an event and the last lines of a command.
				const event = [...(notice.lines ?? []), ...(notice.omitted ? [`… ${notice.omitted} more lines in ${notice.outputPath}`] : [])];
				const tail = notice.tail ? notice.tail.split("\n") : [];
				const shownEvent = options.expanded ? event : event.slice(0, 3);
				const shownTail = options.expanded ? tail : tail.slice(-3);
				for (const line of [...shownEvent, ...shownTail]) lines.push(...new Text(theme.fg("toolOutput", plain(line)), pad + 2, 0).render(width));
				const hidden = event.length - shownEvent.length + tail.length - shownTail.length;
				if (hidden) lines.push(...new Text(keyHint("app.tools.expand", `${hidden} more lines`), pad + 2, 0).render(width));
			}
			cache = { width, lines: lines.map(line => truncateToWidth(line, width, "")) };
			return cache.lines;
		},
		invalidate() {
			cache = undefined;
		},
	};
}

type Output = { rows: string[]; size: number; earlier: boolean; missing: boolean };

// All tasks, newest first. A reload can keep a manager that older code made,
// so read its task map and do not call a method that the old code may not have.
function taskList(): Task[] {
	const map = (tasks() as any).tasks;
	return map instanceof Map ? [...map.values()].sort((a: Task, b: Task) => b.startedAt - a.startedAt) : [];
}

// The end of an output file as display rows. A carriage return overwrites its
// line, as in a terminal. A file of the same size gives the known rows again.
function readOutput(path: string, known?: Output): Output {
	let fd: number | undefined;
	try {
		fd = openSync(path, "r");
		const size = fstatSync(fd).size;
		if (known && !known.missing && known.size === size) return known;
		const length = Math.min(size, SCREEN_BYTES);
		const buffer = Buffer.alloc(length);
		const read = readSync(fd, buffer, 0, length, size - length);
		const earlier = size > length;
		let text = buffer.subarray(0, read).toString("utf8");
		// A partial read can start in the middle of a line.
		if (earlier) text = text.slice(text.indexOf("\n") + 1);
		const lines = text.split("\n");
		if (lines.at(-1) === "") lines.pop();
		const rows = lines.map(line => plain((line.replace(/\r+$/, "").split("\r").pop() ?? "").replace(/\t/g, "    ")).trimEnd());
		return { rows, size, earlier, missing: false };
	} catch {
		return { rows: [], size: 0, earlier: false, missing: true };
	} finally {
		if (fd !== undefined) closeSync(fd);
	}
}

function taskState(task: Task): string {
	const now = Date.now();
	return task.status === "running" ? `running ${elapsed(now - task.startedAt)}` : `${statusText(task)} after ${elapsed((task.endedAt ?? now) - task.startedAt)}`;
}

function taskMark(task: Task, theme: any): string {
	switch (task.status) {
	case "running": return theme.fg("accent", consoleGlyph("◷", "bg"));
	case "completed": return theme.fg("success", consoleIcon("success"));
	case "stopped": return theme.fg("warning", consoleIcon("stopped"));
	default: return theme.fg("error", consoleIcon("failure"));
	}
}

/**
 * Lists the background tasks and shows the output of one task. The output
 * updates while the task runs, and the view stays at the end of the output
 * until the user scrolls up. The screen is a focused overlay, because Pi
 * keeps Page Up and Page Down for the transcript while no overlay has focus.
 * The list is a panel at the bottom. The output fills the terminal.
 */
class TaskScreen {
	private selected = 0;
	private task: Task | undefined;
	private output: Output | undefined;
	private wrapped: { width: number; output: Output; rows: string[] } | undefined;
	private top = 0;
	private maxTop = 0;
	private follow = true;
	private timer: NodeJS.Timeout | undefined;
	// The task status on the screen, to see when the task ends.
	private shown: TaskStatus | undefined;
	// With one task, the screen opens its output, and escape closes the screen.
	private readonly listed: boolean;

	constructor(private tui: any, private theme: any, private keys: any, private onClose: () => void, task?: Task) {
		const list = taskList();
		this.listed = !task && list.length > 1;
		if (!this.listed) this.show(task ?? list[0]);
		this.timer = setInterval(() => this.tick(), SCREEN_REFRESH_MS);
		this.timer.unref?.();
	}

	private show(task: Task | undefined): void {
		this.task = task;
		this.output = task ? readOutput(task.outputPath) : undefined;
		this.wrapped = undefined;
		this.top = 0;
		this.follow = true;
	}

	// New output, a new status, and elapsed times change the screen.
	private tick(): void {
		if (this.task) {
			const before = this.output;
			this.output = readOutput(this.task.outputPath, before);
			if (this.output === before && this.task.status !== "running" && this.task.status === this.shown) return;
		}
		this.tui.requestRender();
	}

	private height(): number {
		return Math.max(8, Number(this.tui.terminal?.rows ?? 24));
	}

	private scroll(delta: number): void {
		this.top = Math.min(Math.max(0, (this.follow ? this.maxTop : this.top) + delta), this.maxTop);
		this.follow = this.top >= this.maxTop;
	}

	handleInput(data: string): void {
		const keys = this.keys;
		const cancel = keys.matches(data, "tui.select.cancel") || data === "q";
		if (!this.task) {
			const list = taskList();
			if (cancel) return this.finish();
			if (keys.matches(data, "tui.select.up")) this.selected = Math.max(0, this.selected - 1);
			else if (keys.matches(data, "tui.select.down")) this.selected = Math.min(Math.max(0, list.length - 1), this.selected + 1);
			else if (keys.matches(data, "tui.select.confirm") && list[this.selected]) this.show(list[this.selected]);
			else return;
		} else {
			const page = Math.max(1, this.height() - 6);
			if (cancel) {
				if (!this.listed) return this.finish();
				this.show(undefined);
			} else if (keys.matches(data, "tui.select.up")) this.scroll(-1);
			else if (keys.matches(data, "tui.select.down")) this.scroll(1);
			else if (keys.matches(data, "tui.select.pageUp")) this.scroll(-page);
			else if (keys.matches(data, "tui.select.pageDown")) this.scroll(page);
			else if (matchesKey(data, "home")) {
				this.top = 0;
				this.follow = this.maxTop === 0;
			} else if (matchesKey(data, "end")) this.follow = true;
			else return;
		}
		this.tui.requestRender();
	}

	render(width: number): string[] {
		width = Math.max(1, width);
		const theme = this.theme;
		// Full lines hide the transcript under the overlay.
		const fit = (line: string) => {
			const fitted = truncateToWidth(line, width, "…");
			return fitted + " ".repeat(Math.max(0, width - visibleWidth(fitted)));
		};
		const band = (line: string) => theme.bg("selectedBg", fit(line));
		const title = (subject?: string) => theme.fg("accent", theme.bold(`${consoleIcon("brand")} GALPON`))
			+ theme.fg("text", theme.bold(` / BACKGROUND TASKS${subject ? ` / ${subject}` : ""}`));
		const rule = theme.fg("border", consoleGlyph("─", "-").repeat(width));
		const task = this.task;
		if (!task) {
			const list = taskList();
			this.selected = Math.min(this.selected, Math.max(0, list.length - 1));
			const room = Math.max(1, this.height() - 4);
			const start = Math.min(Math.max(0, this.selected - room + 1), Math.max(0, list.length - room));
			const rows = list.slice(start, start + room).map((item, index) => {
				const chosen = start + index === this.selected;
				const label = `${chosen ? consoleIcon("focus") : " "} ${taskMark(item, theme)} ${theme.fg(chosen ? "accent" : "text", item.description)}  ${theme.fg("muted", `${item.id} · ${item.kind} · ${taskState(item)}`)}`;
				return chosen ? band(label) : fit(label);
			});
			if (!rows.length) rows.push(theme.fg("dim", "No background tasks."));
			return [rule, fit(title()), rule, ...rows, fit(theme.fg("dim", "↑↓ select · enter output · esc close"))];
		}

		this.shown = task.status;
		const output = this.output ?? readOutput(task.outputPath);
		const textWidth = Math.max(1, width - 2);
		if (this.wrapped?.width !== width || this.wrapped.output !== output) {
			const rows = output.rows.flatMap(row => row ? wrapTextWithAnsi(row, textWidth) : [""]);
			if (output.earlier) rows.unshift(`[Earlier output is only in the output file.]`);
			this.wrapped = { width, output, rows };
		}
		const rows = this.wrapped.rows;
		const bodyHeight = Math.max(1, this.height() - 5);
		this.maxTop = Math.max(0, rows.length - bodyHeight);
		this.top = this.follow ? this.maxTop : Math.min(this.top, this.maxTop);
		const body = rows.slice(this.top, this.top + bodyHeight).map(row => `  ${theme.fg("toolOutput", row)}`);
		if (!rows.length) body.push(`  ${theme.fg("dim", output.missing ? "The output file is gone." : task.status === "running" ? "No output yet." : "No output.")}`);
		while (body.length < bodyHeight) body.push("");
		const kind = task.kind === "command" ? "background command" : "monitor";
		const position = rows.length > bodyHeight ? ` · ${this.top + 1}-${Math.min(rows.length, this.top + bodyHeight)} of ${rows.length}` : "";
		const hint = `↑↓ scroll · PgUp/PgDn page · Home/End · esc ${this.listed ? "back" : "close"}${position}`;
		return [
			fit(title(task.description)),
			fit(`${taskMark(task, theme)} ${theme.fg("muted", `${task.id} · ${kind} · ${taskState(task)} · ${task.outputPath}`)}`),
			fit(theme.fg("muted", `$ ${plain(task.command.split("\n")[0])}`)),
			rule,
			...body.map(fit),
			fit(theme.fg("dim", hint)),
		];
	}

	finish(): void {
		this.dispose();
		this.onClose();
	}

	invalidate(): void {
		this.wrapped = undefined;
	}

	dispose(): void {
		clearInterval(this.timer);
		this.timer = undefined;
	}
}

let screen: TaskScreen | undefined;

// Opens the task screen, or the output of one task when the ID is given.
async function openTaskScreen(ctx: ExtensionContext, id = ""): Promise<void> {
	if (ctx.mode !== "tui" || screen) return;
	const list = taskList();
	const task = id ? list.find(item => item.id === id) : undefined;
	if (id && !task) {
		ctx.ui.notify(`No background task has the ID ${id}.`, "warning");
		return;
	}
	if (!list.length) {
		ctx.ui.notify("No background tasks.", "info");
		return;
	}
	try {
		await ctx.ui.custom<void>(
			(tui, theme, keybindings, done) => (screen = new TaskScreen(tui, theme, keybindings, () => done(undefined), task)),
			{ overlay: true, overlayOptions: { anchor: "bottom-left", width: "100%", maxHeight: "100%" } },
		);
	} finally {
		screen = undefined;
	}
}

/** Registers the background tools and delivers their notices to this session. */
export function registerBackground(pi: ExtensionAPI): void {
	let options: BashOptions = {};
	const result = (text: string) => ({ content: [{ type: "text" as const, text }], details: undefined });

	pi.registerTool({
		name: "Monitor",
		label: "Monitor",
		description: MONITOR_DESCRIPTION,
		promptSnippet: "Stream events from a background script: each stdout line becomes a notice",
		promptGuidelines: ["Use Monitor instead of sleep or poll loops when you need each new event from a log, a CI run, or an API."],
		parameters: Type.Object({
			description: Type.String({ description: "Short, specific label. Each notice shows it, for example \"errors in deploy.log\"." }),
			command: Type.String({ description: "Shell script. Each line that it writes to stdout is an event." }),
			timeout_ms: Type.Optional(Type.Number({ description: `Stop the monitor after this many milliseconds. Default ${DEFAULT_MONITOR_TIMEOUT_MS}. Maximum ${MAX_MONITOR_TIMEOUT_MS}. Not used when persistent is true.` })),
			persistent: Type.Optional(Type.Boolean({ description: "Run until TaskStop or the end of the session, with no timeout." })),
		}),
		renderCall(args: any, theme: any) {
			return new Text(`${theme.fg("toolTitle", theme.bold("Monitor"))} ${theme.fg("accent", plain(args?.description))}\n${theme.fg("muted", `$ ${firstLine(String(args?.command ?? ""))}`)}`, 0, 0);
		},
		async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
			const requested = Number(params.timeout_ms);
			const timeoutMs = params.persistent ? undefined
				: Number.isFinite(requested) && requested > 0 ? Math.min(Math.max(requested, 1_000), MAX_MONITOR_TIMEOUT_MS) : DEFAULT_MONITOR_TIMEOUT_MS;
			const description = plain(params.description).trim() || firstLine(params.command);
			const task = tasks().start("monitor", { command: params.command, description, shell: shellCommand(params.command, options, ctx), timeoutMs });
			const end = timeoutMs === undefined ? "It runs until TaskStop or the end of the session." : `It stops after ${elapsed(timeoutMs)}.`;
			return result(`Monitor ${task.id} started: ${description}.\nOutput file: ${task.outputPath}\nEach stdout line arrives as a notice while you work. ${end} Do not sleep or poll: do other work or end your turn. Stop it with TaskStop.`);
		},
	});

	pi.registerTool({
		name: "TaskStop",
		label: "TaskStop",
		description: "Stop a background command or a monitor, with its child processes. You get no notice for a task that you stop.",
		parameters: Type.Object({ task_id: Type.String({ description: "ID of the background command or monitor" }) }),
		async execute(_toolCallId, params) {
			return result(tasks().stop(String(params.task_id).trim()));
		},
	});

	pi.registerTool({
		name: "TaskOutput",
		label: "TaskOutput",
		description: "Show the status, the exit code, and the last output lines of a background command or a monitor. Use it to read a result again, or to check a task that you lost track of. Do not use it to wait: you get a notice when the task ends.",
		parameters: Type.Object({ task_id: Type.String({ description: "ID of the background command or monitor" }) }),
		async execute(_toolCallId, params) {
			return result(tasks().describe(String(params.task_id).trim()));
		},
	});

	pi.registerMessageRenderer(BACKGROUND_MESSAGE, renderNotices as any);

	pi.registerCommand("tasks", {
		description: `Show background commands and monitors and their output (${TASKS_KEY})`,
		handler: async (args, ctx) => {
			if (ctx.mode === "tui") return openTaskScreen(ctx, args.trim());
			const lines = tasks().list();
			ctx.ui.notify(lines.length ? lines.join("\n") : "No background tasks.", "info");
		},
	});

	pi.registerShortcut(TASKS_KEY, {
		description: "Show background commands and monitors and their output",
		handler: ctx => openTaskScreen(ctx),
	});

	pi.on("session_start", (_event, ctx) => {
		options = bashOptions(ctx);
		// In the terminal, Galpon's tool frames register bash with this support.
		if (ctx.mode !== "tui" && pi.getAllTools().some(tool => tool.name === "bash" && tool.sourceInfo?.source === "builtin")) {
			const active = pi.getActiveTools();
			pi.registerTool(backgroundBash(createBashToolDefinition(ctx.cwd, options), options));
			pi.setActiveTools(active);
		}
		tasks().bind({
			send: (content, details) => pi.sendMessage({ customType: BACKGROUND_MESSAGE, content, display: true, details }, { triggerTurn: true, deliverAs: "steer" }),
			idle: () => ctx.isIdle(),
			status: text => ctx.ui.setStatus("galpon-background", text),
		});
	});
	pi.on("agent_start", () => tasks().runStarted());
	pi.on("turn_end", event => tasks().turnEnded((event as any).message));
	pi.on("agent_settled", () => tasks().runSettled());
	pi.on("session_shutdown", event => {
		// A reload keeps the tasks. The next runtime binds and takes their notices.
		if (event.reason !== "reload") tasks().stopAll();
		// The screen belongs to this runtime. A reload opens no screen again.
		screen?.finish();
		tasks().unbind();
	});
}
