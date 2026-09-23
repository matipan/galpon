import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { stripVTControlCharacters } from "node:util";
import { initTheme, type ExtensionAPI, type Theme } from "@earendil-works/pi-coding-agent";
import { getKeybindings, KeybindingsManager, setKeybindings, visibleWidth } from "@earendil-works/pi-tui";
import { registerConsole } from "../galpon-console.ts";

type Renderer = Parameters<ExtensionAPI["registerMessageRenderer"]>[1];
type Message = Parameters<Renderer>[0];

export default function () {
	const resultPath = process.env.GALPON_COORDINATION_RENDER_TEST_RESULT;
	if (!resultPath) return;
	const previousKeys = getKeybindings();
	try {
		initTheme("dark", false);
		setKeybindings(new KeybindingsManager({
			"app.tools.expand": { defaultKeys: "ctrl+o", description: "Expand tools and messages" },
		}));
		let renderer: Renderer | undefined;
		registerConsole({
			on() {}, registerCommand() {},
			registerMessageRenderer(name: string, value: Renderer) {
				if (name === "galpon-operation") renderer = value;
			},
		} as unknown as ExtensionAPI);
		assert(renderer, "Coordination renderer was not registered");
		const theme = { bold: (value: string) => value } as Theme;
		const message = (content: Message["content"]): Message => ({
			customType: "galpon-operation", role: "custom", display: true, timestamp: 0, content,
			details: { operationId: "operation:private-metadata", claimId: "claim:private-metadata" },
		});
		const render = (value: Message, expanded: boolean, width = 80) => {
			const component = renderer!(value, { expanded, outputPad: 1 }, theme);
			assert(component, "Coordination renderer returned no component");
			const lines = component.render(width);
			assert(lines.every(line => visibleWidth(line) <= width), `Rendered beyond ${width} columns`);
			component.invalidate();
			assert.deepEqual(component.render(width), lines, "Invalidation lost message content");
			return lines.map(stripVTControlCharacters);
		};

		const request = "Work request from Galpón agent Software Factory [delivery message:transport-id]:\n\nPlease check **keyboard access**.\n\n- Keep the heading.\n- Preserve message:body-reference.\n\n```text\n[delivery message:code-reference]\n```\n\n---\n\nDelivery instructions: Address every delivery in this batch. Your final assistant text is the durable result for this batch. State what you completed, the main result, and any error or remaining work. Do not use galpon_send_agent to return a result for a current delivery. Galpón sends your final text to the requester when this turn settles.";
		const blocks = Object.freeze([Object.freeze({ type: "text" as const, text: request })]);
		const original = Object.freeze(message(blocks as unknown as Message["content"]));
		const before = JSON.stringify(original);
		const expanded = render(original, true).join("\n");
		assert.match(expanded, /(?:✉|\[M\])  COORDINATION/);
		assert.match(expanded, /Work request from Galpón agent Software Factory:/);
		assert.match(expanded, /keyboard access/);
		assert.match(expanded, /message:body-reference/);
		assert.match(expanded, /\[delivery message:code-reference\]/);
		assert.doesNotMatch(expanded, /transport-id|private-metadata|Delivery instructions|"type":"text"|\\n|\*\*keyboard/);
		assert.equal(JSON.stringify(original), before, "Rendering changed stored message data");
		assert.equal(render(message(request), true).join("\n"), expanded, "String and content-block messages differ");

		const long = message([{ type: "text", text: "Review the layout. ".repeat(100) + "\n\nFinal note: 日本語 🤝" }]);
		for (const width of [40, 80, 160]) {
			const full = render(long, true, width);
			const collapsed = render(long, false, width);
			assert.equal(collapsed.length, 8, "Collapsed message must have a heading, four body rows, and an expansion hint");
			assert.deepEqual(collapsed.slice(2, 6), full.slice(2, 6), "Preview changed wrapped message text");
			assert.match(collapsed[7], /ctrl\+o.*expand full communication/);
			assert.doesNotMatch(collapsed.join("\n"), /Final note/);
			assert.match(full.join("\n"), /Final note: 日本語 🤝/);
		}
		for (const width of [0, 1, 8, 24]) render(long, false, width);
		assert.doesNotMatch(render(message("Short message."), false).join("\n"), /expand full communication/);

		const results = message("Durable result for assignment message:first-id:\n\nFirst result.\n\n---\n\nDurable blocker for assignment message:second-id:\n\nNeed user input.\n\n---\n\nContinue the original task from the saved conversation. Use these results, then give the final result for that task.");
		const resultText = render(results, true).join("\n");
		assert.match(resultText, /Durable result:[\s\S]*First result\.[\s\S]*Durable blocker:[\s\S]*Need user input\./);
		assert.doesNotMatch(resultText, /first-id|second-id|Continue the original task/);

		const withImage = message([
			{ type: "text", text: request },
			{ type: "image", mimeType: "image/png", data: "opaque-image-bytes" },
		]);
		const imageText = render(withImage, true).join("\n");
		assert.match(imageText, /Image attachment/);
		assert.doesNotMatch(imageText, /opaque-image-bytes|Delivery instructions/);
		writeFileSync(resultPath, JSON.stringify({ ok: true }));
	} catch (error) {
		writeFileSync(resultPath, JSON.stringify({ ok: false, error: error instanceof Error ? error.stack : String(error) }));
	} finally {
		setKeybindings(previousKeys);
	}
}
