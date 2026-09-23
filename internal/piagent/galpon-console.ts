import { stripVTControlCharacters } from "node:util";
import { CustomEditor, type Theme, getMarkdownTheme, keyHint, type ExtensionAPI, type ExtensionContext } from "@earendil-works/pi-coding-agent";
import { Markdown, matchesKey, truncateToWidth, visibleWidth, wrapTextWithAnsi } from "@earendil-works/pi-tui";
import { ACTIVITY_INTERVAL_MS, activityFrames, consoleIcon, frameTool } from "./builtin/rpiv-todo/view/tool-frame.ts";
import { registerNativeToolFrames } from "./galpon-native-tools.ts";

const plain = (value: unknown) => stripVTControlCharacters(String(value ?? "")).replace(/[\p{Cc}\p{Cf}]/gu, " ").trim();
const count = (n: unknown) => typeof n === "number" && Number.isFinite(n) && n >= 0 ? n : 0;
const tokens = (n: number) => n < 1000 ? String(n) : n < 10000 ? `${(n/1000).toFixed(1)}k` : n < 1000000 ? `${Math.round(n/1000)}k` : `${(n/1000000).toFixed(1)}M`;
const ascii = () => process.env.GALPON_ASCII === "1";
const rule = (theme: Theme, width: number) => theme.fg("border", (ascii() ? "-" : "─").repeat(Math.max(0,width)));

function usage(entries: readonly any[]) {
	const total = { input:0,output:0,cacheRead:0,cacheWrite:0,cost:0,hit:undefined as number|undefined };
	for (const entry of entries) {
		const u = entry.type === "message" && ["assistant","toolResult"].includes(entry.message?.role) ? entry.message.usage : ["usage","compaction","branch_summary"].includes(entry.type) ? entry.usage : undefined;
		if (!u) continue;
		for (const key of ["input","output","cacheRead","cacheWrite"] as const) total[key] += count(u[key]);
		total.cost += count(u.cost?.total);
		if (entry.type === "message" && entry.message.role === "assistant") {
			const prompt = count(u.input)+count(u.cacheRead)+count(u.cacheWrite);
			total.hit = prompt > 0 ? count(u.cacheRead)/prompt*100 : undefined;
		}
	}
	return total;
}

function segments(values: string[],width: number): string[] {
	if (width < 1) return [];
	const lines: string[] = [];
	let line = "";
	for (const value of values) {
		if (line && visibleWidth(line)+2+visibleWidth(value)>width) { lines.push(line); line=""; }
		if (visibleWidth(value)>width) {
			if (line) { lines.push(line); line=""; }
			lines.push(...wrapTextWithAnsi(value,width));
		} else { line += (line ? "  " : "")+value; }
	}
	if (line) lines.push(line);
	return lines.map(line=>truncateToWidth(line,width,""));
}

class ConsoleEditor extends CustomEditor {
	consoleTheme?: () => Theme;
	inputHint = "";
	render(width: number): string[] {
		if (width <= 0) return [];
		const lines = super.render(width);
		const theme = this.consoleTheme?.();
		// Only replace the native top rule. The input, cursor, completion and
		// paste state remain in CustomEditor, including after terminal resize.
		if (lines.length) {
			const label = ` ${this.getText().startsWith("!") ? "COMMAND" : "INPUT"} `;
			let overflow = plain(lines[0]).match(/↑\s+\d+\s+more/)?.[0];
			if (overflow && ascii()) overflow = overflow.replace("↑","above:");
			const hint = ` ${overflow || this.inputHint} `;
			const right = visibleWidth(label+hint)+2 <= width ? hint : "";
			const fill = Math.max(0,width-visibleWidth(label+right));
			lines[0] = truncateToWidth((theme?.fg("accent",label) || label)+(theme ? rule(theme,fill) : "-".repeat(fill))+(theme?.fg("muted",right) || right),width,"");
		}
		return lines;
	}
}

export function registerConsole(pi: ExtensionAPI) {
	let ctx: ExtensionContext | undefined;
	let enabled = false, failed = false;
	let setNativeFrames: ((enabled: boolean) => void) | undefined;
	let stats = true;
	let ownedEditor: NonNullable<ReturnType<ExtensionContext["ui"]["getEditorComponent"]>> | undefined;
	let branch = "";
	let usageDirty = true, totals = usage([]);
	let refresh: (()=>void) | undefined;
	let disposeBranch: (()=>void) | undefined;
	const indicator = () => {
		if (!enabled || !ctx) return;
		const frames = activityFrames().map(frame => ctx!.ui.theme.fg("warning", frame));
		ctx.ui.setWorkingIndicator({ frames, intervalMs: ACTIVITY_INTERVAL_MS });
	};
	const enable = (context: ExtensionContext) => {
		if (context.mode !== "tui" || enabled) return;
		ctx=context; enabled=true; usageDirty=true;
		process.env.GALPON_CONSOLE_ACTIVE = "1";
		if (setNativeFrames) setNativeFrames(true);
		else setNativeFrames = registerNativeToolFrames(pi, context);
		ctx.ui.setHeader((_tui,theme)=>({
			render(width) {
				const theme = context.ui.theme;
				const workspace=plain(process.env.GALPON_WORKSPACE_TITLE);
				const title=plain(process.env.GALPON_AGENT_TITLE || ctx?.sessionManager.getSessionName() || "Pi");
				const brand = theme.bold(theme.fg("accent",`${consoleIcon("brand")} GALPON`));
				return [truncateToWidth(brand+theme.bold(theme.fg("text",`  /  ${workspace ? workspace+" / " : ""}${title}`)),width),rule(theme,width)];
			}, invalidate() {},
		}));
		if (!ctx.ui.getEditorComponent()) {
			ownedEditor=(tui,theme,keybindings)=>{
				const editor=new ConsoleEditor(tui,theme,keybindings);
				editor.consoleTheme=()=>context.ui.theme;
				editor.inputHint=`${keybindings.getKeys("tui.input.submit").join("/") || "unbound"} send / ${keybindings.getKeys("tui.input.newLine")[0] || "unbound"} newline`;
				return editor;
			};
			ctx.ui.setEditorComponent(ownedEditor);
		}
		ctx.ui.setFooter((tui,theme,data)=>{
			disposeBranch?.();
			refresh=()=>tui.requestRender();
			disposeBranch=data.onBranchChange(refresh);
			return {
				dispose() { disposeBranch?.(); disposeBranch=undefined; refresh=undefined; },
				invalidate() {},
				render(width) {
					if (!ctx) return [];
					const theme = ctx.ui.theme;
					branch=data.getGitBranch() || "";
					const field=(label:string,value:string)=>theme.fg("muted",label+" ")+theme.fg("text",plain(value));
					const level = pi.getThinkingLevel();
					const primary = [field("MODEL",ctx.model?.id || "not selected"),field(consoleIcon("reasoning"),level)];
					if (branch) primary.push(field("REF",truncateToWidth(plain(branch),Math.max(12,Math.min(40,width-20)))));
					if (failed) primary.push(theme.fg("error",`${consoleIcon("failure")} request failed`));
					const lines=segments(primary,width);
					if (stats) {
						if (usageDirty) { totals=usage(ctx.sessionManager.getEntries()); usageDirty=false; }
						const total=totals;
						const context=ctx.getContextUsage();
						const contextText=context?.tokens != null && context.percent != null ? `${tokens(context.tokens)}/${tokens(context.contextWindow)} ${context.percent.toFixed(1)}%` : `unknown/${tokens(ctx.model?.contextWindow || 0)}`;
						const subscription=Boolean(ctx.model && (ctx.modelRegistry.isUsingOAuth(ctx.model) || ctx.model.provider === "kimi-coding"));
						lines.push(...segments([field("IN",tokens(total.input)),field("OUT",tokens(total.output)),field("CACHE R/W",`${tokens(total.cacheRead)}/${tokens(total.cacheWrite)}`),field("HIT",total.hit === undefined ? "unknown" : total.hit.toFixed(1)+"%"),field("COST",`$${total.cost.toFixed(3)}${subscription?" (sub)":""}`),field("CTX",contextText)],width));
					}
					// Work Dock already shows delegation counts. Keep other extension
					// status messages, together instead of one row per extension.
					const statuses = [...data.getExtensionStatuses()].filter(([key,value])=>key !== "galpon" && value).map(([,value])=>theme.fg("muted",plain(value)));
					lines.push(...segments(statuses,width));
					return lines;
				},
			};
		});
		indicator();
	};
	const disable = (restoreTools = true) => {
		if (!enabled || !ctx) return;
		enabled=false; delete process.env.GALPON_CONSOLE_ACTIVE;
		if (restoreTools) setNativeFrames?.(false);
		disposeBranch?.(); disposeBranch=undefined; refresh=undefined;
		ctx.ui.setHeader(undefined); ctx.ui.setFooter(undefined); ctx.ui.setWorkingIndicator();
		if (ownedEditor && ctx.ui.getEditorComponent() === ownedEditor) ctx.ui.setEditorComponent(undefined);
		ownedEditor=undefined;
	};
	pi.on("session_start",(_event,context)=>enable(context));
	pi.on("session_shutdown",()=>{disable(false);ctx=undefined;});
	pi.on("agent_start",(_event,context)=>{ctx=context;failed=false;indicator();refresh?.();});
	pi.on("agent_settled",(_event,context)=>{ctx=context;usageDirty=true;refresh?.();});
	pi.on("message_end",(event)=>{usageDirty=true;if(event.message.role==="assistant" && event.message.stopReason==="error") failed=true;refresh?.();});
	pi.on("session_compact",()=>{usageDirty=true;refresh?.();});
	pi.on("session_tree",()=>{usageDirty=true;refresh?.();});
	pi.on("turn_end",()=>{usageDirty=true;refresh?.();});
	pi.on("tool_execution_end",()=>{usageDirty=true;refresh?.();});
	pi.registerMessageRenderer("galpon-operation",(message,{expanded,outputPad},theme)=>{
		const content=typeof message.content === "string" ? message.content : JSON.stringify(message.content);
		const text=expanded ? content : content.slice(0,1000);
		const hint=!expanded && text.length<content.length ? `\n\n${keyHint("app.tools.expand","expand full communication")}` : "";
		return new Markdown(`**${consoleIcon("message")} COORDINATION**\n\n${text}${hint}`,outputPad,0,getMarkdownTheme());
	});
	pi.registerCommand("galpon-ui",{
		description:"Console display: on, off, stats, still, motion, info",
		handler:async(args,context)=>{
			if (context.mode !== "tui") return;
			switch(args.trim()) {
			case "off": disable(); return;
			case "on": enable(context); return;
			case "stats": stats=!stats;refresh?.();return;
			case "still": process.env.GALPON_UI_MOTION="0";indicator();return;
			case "motion": process.env.GALPON_UI_MOTION="1";indicator();return;
			case "info":
				await context.ui.custom((_tui,theme,keys,done)=>{
					let offset=0;
					return {
						render(width) {
							const lines=wrapTextWithAnsi(`Directory\n${plain(context.cwd)}\n\nGit branch\n${plain(branch || "none")}\n\nModel\n${plain(context.model?.provider)}/${plain(context.model?.id)}\n\nReasoning\n${pi.getThinkingLevel()}\n\nUse /session for full session statistics.`,Math.max(1,width));
							const height=Math.max(3,_tui.terminal.rows-7);
							const start=Math.min(offset,Math.max(0,lines.length-height));
							return [theme.fg("accent",theme.bold(`${consoleIcon("brand")} GALPON / SESSION DETAIL`)),rule(theme,width),...lines.slice(start,start+height).map(line=>theme.fg("text",line)),theme.fg("muted","up/down scroll · esc back")].map(line=>truncateToWidth(line,width));
						},
						handleInput(data) {if(matchesKey(data,"escape") || matchesKey(data,"enter")) done(undefined);else if(matchesKey(data,"down")) offset++;else if(matchesKey(data,"up")) offset=Math.max(0,offset-1);_tui.requestRender();},
						invalidate() {},
					};
				}); return;
			default: context.ui.notify("/galpon-ui on | off | stats | still | motion | info","info");
			}
		},
	});
}

// Galpon-owned registrations use the same frame as native tools and Work Dock.
// Other extensions retain their own execution backends and renderers.
export function withConsole(pi: ExtensionAPI): ExtensionAPI {
	registerConsole(pi);
	return {
		...pi,
		registerTool(definition) {
			pi.registerTool(frameTool(definition));
		},
	};
}
