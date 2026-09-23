import { stripVTControlCharacters } from "node:util";
import { keyHint, type Theme, type ToolDefinition } from "@earendil-works/pi-coding-agent";
import type { TSchema } from "typebox";
import { Text, truncateToWidth, type Component } from "@earendil-works/pi-tui";

type ToolRenderContext = Parameters<NonNullable<ToolDefinition<any, any>["renderCall"]>>[2];

export const consoleActive = () => process.env.GALPON_CONSOLE_ACTIVE === "1";
export const consoleGlyph = (unicode: string, fallback: string) => process.env.GALPON_ASCII === "1" ? fallback : unicode;
// Keep these meanings aligned with internal/tui/console_icons.go.
export const CONSOLE_ICONS = {
	brand: ["⌂", "[G]"], section: ["▰", "::"], agent: ["◈", "[A]"], workspace: ["▦", "[W]"],
	repository: ["▣", "[R]"], worktree: ["⑂", "[T]"], delegation: ["⊶", "&"], message: ["✉", "[M]"],
	reasoning: ["∴", "R:"], focus: ["›", ">"], collapsed: ["▸", "[+]"], expanded: ["▾", "[-]"],
	add: ["+", "+"], more: ["…", "..."], pending: ["○", "o"], idle: ["–", "-"],
	attention: ["!", "!"], success: ["✓", "v"], failure: ["×", "x"], canceled: ["⊘", "/"],
	stopped: ["■", "#"], unknown: ["?", "?"],
} as const;
export function consoleIcon(name: keyof typeof CONSOLE_ICONS): string {
	const [symbol, fallback] = CONSOLE_ICONS[name];
	return consoleGlyph(symbol, fallback);
}
export const ACTIVITY_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"] as const;
export const ACTIVITY_INTERVAL_MS = 80;
export function activityFrames(): readonly string[] {
	if (process.env.GALPON_UI_MOTION === "0") return [consoleGlyph("◐", "*")];
	return process.env.GALPON_ASCII === "1" ? ["|", "/", "-", "\\"] : ACTIVITY_FRAMES;
}
export function activityGlyph(frame = Math.floor(Date.now() / ACTIVITY_INTERVAL_MS)): string {
	const frames = activityFrames();
	return frames[frame % frames.length];
}
const plain = (value: unknown) => stripVTControlCharacters(String(value ?? "")).replace(/[\p{Cc}\p{Cf}]/gu, " ").trim();

// Some native renderers own an inner Box. Remove only its tool background;
// syntax, diff additions/deletions, and the user's actual theme stay intact.
const flatThemes = new WeakMap<Theme, Theme>();
function flatToolTheme(theme: Theme): Theme {
	if (!consoleActive()) return theme;
	let flat = flatThemes.get(theme);
	if (!flat) {
		const neutral = (color: string) => ["toolPendingBg", "toolSuccessBg", "toolErrorBg"].includes(color);
		flat = new Proxy(theme, {
			get(target, property) {
				if (property === "bg") return (color: Parameters<Theme["bg"]>[0], text: string) => neutral(color) ? text : target.bg(color, text);
				if (property === "getBgAnsi") return (color: Parameters<Theme["getBgAnsi"]>[0]) => neutral(color) ? "" : target.getBgAnsi(color);
				const value = Reflect.get(target, property, target);
				return typeof value === "function" ? value.bind(target) : value;
			},
		});
		flatThemes.set(theme, flat);
	}
	return flat;
}

// Call and result components share their failure state. Some tools report
// failure in result details, after the call component has already been built.
const frameStates = new WeakMap<object, { failed: boolean; hasResult: boolean }>();
function frameState(context: ToolRenderContext) {
	let state = frameStates.get(context.state);
	if (!state) {
		state = { failed: false, hasResult: false };
		frameStates.set(context.state, state);
	}
	return state;
}

function frameColor(theme: Theme, failed: boolean): (text: string) => string {
	const token = failed ? "error" : "accent";
	const rgb = (color: string) => color.match(/\x1b\[38;2;(\d+);(\d+);(\d+)m/)?.slice(1).map(Number);
	const color = rgb(theme.getFgAnsi(token));
	const muted = rgb(theme.getFgAnsi("muted"));
	if (!color || !muted) return text => theme.fg(token, text);
	const channels = color.map((value, index) => Math.round(value * 0.75 + muted[index] * 0.25));
	return text => `\x1b[38;2;${channels.join(";")}m${text}\x1b[39m`;
}

// Decorate components, not tool results. Native syntax/diff renderers keep
// their own state and expansion. Pi still owns image display and mouse input.
class ToolFrame implements Component {
	constructor(
		readonly inner: Component,
		private readonly theme: Theme,
		private readonly slot: "call" | "result",
		private readonly context: ToolRenderContext | undefined,
		private readonly callLines = 3,
	) {}

	invalidate() { this.inner.invalidate(); }

	render(width: number): string[] {
		if (width <= 0) return [];
		const lines = [...this.inner.render(Math.max(1, width - 3))];
		while (lines.length && !stripVTControlCharacters(lines[0]).trim()) lines.shift();
		while (lines.length && !stripVTControlCharacters(lines[lines.length - 1]).trim()) lines.pop();
		const error = this.context?.isError === true || (this.context && frameState(this.context).failed) === true;
		const border = frameColor(this.theme, error);
		const rail = border(consoleGlyph("│  ", "|  "));
		if (this.slot === "call") {
			const visible = this.context?.expanded ? lines : lines.slice(0, this.callLines);
			const output = visible.map((line, index) =>
				border(index === 0 ? consoleGlyph("┌─ ", "+- ") : consoleGlyph("│  ", "|  ")) + line);
			if (visible.length < lines.length) {
				output.push(rail + this.theme.fg("muted", `${lines.length - visible.length} more call lines · ${keyHint("app.tools.expand", "expand")}`));
			}
			if (this.context?.executionStarted && this.context.isPartial && !frameState(this.context).hasResult) {
				output.push(rail + this.theme.fg("warning", `${activityGlyph()} running`));
			}
			return output.map(line => truncateToWidth(line, width, ""));
		}
		const partial = this.context?.isPartial === true;
		const state = error ? `${this.theme.bold(consoleIcon("failure"))} failed` : partial ? `${activityGlyph()} running` : consoleIcon("success");
		return [
			...lines.map(line => rail + line),
			border(consoleGlyph("└─", "+-")) + " " + this.theme.fg(error ? "error" : partial ? "warning" : "success", state),
		].map(line => truncateToWidth(line, width, ""));
	}
}

function nativeContext<T extends ToolRenderContext>(context: T): T {
	return { ...context, lastComponent: context.lastComponent instanceof ToolFrame ? context.lastComponent.inner : context.lastComponent };
}

/** Keep execution, schemas, prompt metadata, and native slot caches intact. */
export function frameTool<TParams extends TSchema, TDetails>(
	definition: ToolDefinition<TParams, TDetails>,
	options: { resultError?: (result: { details?: unknown }) => boolean } = {},
): ToolDefinition<TParams, TDetails> {
	return {
		...definition,
		renderShell: "self",
		renderCall(args, theme, context) {
			const params = args as Record<string, unknown>;
			const target = plain(params?.agent || params?.title);
			const child = definition.renderCall?.(args, flatToolTheme(theme), nativeContext(context)) ?? new Text(
				theme.fg("toolTitle", theme.bold(`${definition.label || definition.name}${target ? ` / ${target}` : ""}`))
				+ (context.expanded ? `\n${JSON.stringify(args, null, 2)}` : ""), 0, 0,
			);
			return consoleActive() ? new ToolFrame(child, theme, "call", context, ["edit", "write"].includes(definition.name) ? 10 : 3) : child;
		},
		renderResult(result, renderOptions, theme, context) {
			const displayContext = { ...context, isError: context.isError || options.resultError?.(result) === true };
			Object.assign(frameState(context), { failed: displayContext.isError, hasResult: true });
			let child = definition.renderResult?.(result, renderOptions, flatToolTheme(theme), nativeContext(displayContext));
			if (!child) {
				const text = result.content.filter(part => part.type === "text").map(part => (part as { text: string }).text).join("\n");
				const all = stripVTControlCharacters(text).split("\n");
				const lines = renderOptions.expanded ? all : all.slice(0, 6);
				if (lines.length < all.length) lines.push(keyHint("app.tools.expand", `${all.length - lines.length} more lines`));
				child = new Text(theme.fg("toolOutput", lines.join("\n")), 0, 0);
			}
			return consoleActive() ? new ToolFrame(child, theme, "result", displayContext) : child;
		},
	};
}
