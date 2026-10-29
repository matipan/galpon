import { appendFileSync } from "node:fs";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { registerBackground, RunAnswer } from "../galpon-background.ts";

// Registers the background tools as Galpon does, and records the answer that
// Galpon would save for each run.
export default function (pi: ExtensionAPI) {
	registerBackground(pi);
	const answer = new RunAnswer();
	let latest = "";
	pi.on("agent_start", () => {
		answer.reset();
		latest = "";
	});
	pi.on("message_end", event => {
		const message = event.message as any;
		if (!answer.accept(message)) return;
		latest = (message.content ?? []).filter((part: any) => part?.type === "text").map((part: any) => part.text).join("").trim();
	});
	pi.on("agent_settled", () => {
		appendFileSync(process.env.GALPON_BACKGROUND_TEST_RESULT!, `${JSON.stringify({ answer: latest })}\n`);
	});
	pi.registerCommand("background-reload", {
		description: "Reload the extension runtime",
		handler: async (_args, ctx) => {
			await ctx.reload();
		},
	});
}
