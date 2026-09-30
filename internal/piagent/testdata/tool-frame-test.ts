import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { stripVTControlCharacters } from "node:util";
import { createBashToolDefinition, initTheme, ToolExecutionComponent } from "@earendil-works/pi-coding-agent";
import { Text, visibleWidth } from "@earendil-works/pi-tui";
import { frameTool } from "../builtin/rpiv-todo/view/tool-frame.ts";

// A framed tool must show its current state on every render, although Pi
// renders the complete transcript on each frame and frames reuse their output.
export default function () {
	const resultPath = process.env.GALPON_TOOL_FRAME_TEST_RESULT;
	if (!resultPath) return;
	const previousConsole = process.env.GALPON_CONSOLE_ACTIVE;
	try {
		initTheme("dark", false);
		process.env.GALPON_CONSOLE_ACTIVE = "1";
		const ui = { requestRender() {} } as any;
		const tool = new ToolExecutionComponent("bash", "call-1", { command: "go test ./..." }, { showImages: false }, frameTool(createBashToolDefinition("/tmp")) as any, ui, "/tmp");
		const render = (width = 100) => {
			const lines = tool.render(width);
			assert(lines.every(line => visibleWidth(line) <= width), `Rendered beyond ${width} columns`);
			return lines.map(stripVTControlCharacters).join("\n");
		};
		const result = (text: string, isError = false) => ({ content: [{ type: "text", text }], details: undefined, isError }) as any;

		tool.markExecutionStarted();
		tool.setArgsComplete();
		assert.match(render(), /go test/);
		tool.updateResult(result("first partial line"), true);
		assert.match(render(), /first partial line[\s\S]*running/);
		tool.updateResult(result("first partial line\nsecond partial line"), true);
		assert.match(render(), /second partial line[\s\S]*running/);

		tool.updateResult(result("FAIL internal/app", true), false);
		const failed = render();
		assert.match(failed, /FAIL internal\/app[\s\S]*failed/);
		assert.doesNotMatch(failed, /running|partial line/);
		assert.equal(render(), failed, "An unchanged tool changed between frames");

		tool.updateResult(result("ok internal/app"), false);
		const passed = render();
		assert.match(passed, /ok internal\/app/);
		assert.doesNotMatch(passed, /failed|FAIL/);

		const narrow = render(30);
		assert.notEqual(narrow, passed, "A width change reused the wide frame");
		assert.equal(render(), passed, "Returning to the first width changed the frame");

		// Some renderers change their own component between tool updates, for
		// example to show elapsed time. The frame must show the new content.
		const output = new Text("elapsed 1s", 0, 0);
		const clock = new ToolExecutionComponent("clock", "call-2", {}, { showImages: false }, frameTool({
			name: "clock", label: "clock", description: "clock", parameters: {} as any,
			execute: async () => ({ content: [], details: undefined }),
			renderResult: () => output,
		} as any) as any, ui, "/tmp");
		clock.markExecutionStarted();
		clock.updateResult(result(""), false);
		assert.match(stripVTControlCharacters(clock.render(100).join("\n")), /elapsed 1s/);
		output.setText("elapsed 2s");
		assert.match(stripVTControlCharacters(clock.render(100).join("\n")), /elapsed 2s/, "A frame kept stale inner content");
		writeFileSync(resultPath, JSON.stringify({ ok: true }));
	} catch (error) {
		writeFileSync(resultPath, JSON.stringify({ ok: false, error: error instanceof Error ? error.stack : String(error) }));
	} finally {
		if (previousConsole === undefined) delete process.env.GALPON_CONSOLE_ACTIVE;
		else process.env.GALPON_CONSOLE_ACTIVE = previousConsole;
	}
}
