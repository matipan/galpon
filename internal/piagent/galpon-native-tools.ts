import {
	createBashToolDefinition, createEditToolDefinition, createFindToolDefinition,
	createGrepToolDefinition, createLsToolDefinition, createPowerShellToolDefinition,
	createReadToolDefinition, createWriteToolDefinition, SettingsManager,
	type ExtensionAPI, type ExtensionContext, type ToolDefinition,
} from "@earendil-works/pi-coding-agent";
import { frameTool } from "./builtin/rpiv-todo/view/tool-frame.ts";

/** Public Pi definitions retain the native implementation and native renderers. */
export function registerNativeToolFrames(pi: ExtensionAPI, ctx: ExtensionContext): (enabled: boolean) => void {
	if (ctx.mode !== "tui") return () => {};
	// Match the options Pi supplies to its own builtin definitions. Do not write
	// settings, change active tools, or replace extension/SDK execution backends.
	const settings = SettingsManager.create(ctx.cwd, undefined, { projectTrusted: ctx.isProjectTrusted() });
	const factories: Record<string, () => ToolDefinition<any, any>> = {
		read: () => createReadToolDefinition(ctx.cwd, { autoResizeImages: settings.getImageAutoResize() }),
		bash: () => createBashToolDefinition(ctx.cwd, { commandPrefix: settings.getShellCommandPrefix(), shellPath: settings.getShellPath() }),
		powershell: () => createPowerShellToolDefinition(ctx.cwd),
		edit: () => createEditToolDefinition(ctx.cwd),
		write: () => createWriteToolDefinition(ctx.cwd),
		grep: () => createGrepToolDefinition(ctx.cwd),
		find: () => createFindToolDefinition(ctx.cwd),
		ls: () => createLsToolDefinition(ctx.cwd),
	};
	const definitions = pi.getAllTools().filter(tool => tool.sourceInfo.source === "builtin" && factories[tool.name])
		.map(tool => factories[tool.name]());
	const setEnabled = (enabled: boolean) => {
		const active = pi.getActiveTools();
		for (const definition of definitions) pi.registerTool(enabled ? frameTool(definition) : definition);
		pi.setActiveTools(active);
	};
	setEnabled(true);
	return setEnabled;
}
