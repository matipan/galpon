import { request as httpRequest } from "node:http";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type JSONValue = Record<string, any> | any[] | string | number | boolean | null;

const socketPath = process.env.GALPON_SOCKET ?? "";
const agentId = process.env.GALPON_AGENT_ID ?? "";
const agentTitle = process.env.GALPON_AGENT_TITLE ?? "Agent";
const agentRole = process.env.GALPON_AGENT_ROLE ?? "";
const workspaceTitle = process.env.GALPON_WORKSPACE_TITLE ?? "Workspace";
const placement = process.env.GALPON_PLACEMENT ?? "";
const runtimeId = process.env.GALPON_RUNTIME_ID ?? "";

function api(method: string, path: string, body?: JSONValue, signal?: AbortSignal): Promise<any> {
	return new Promise((resolve, reject) => {
		let settled = false;
		const succeed = (value: any) => {
			if (settled) return;
			settled = true;
			resolve(value);
		};
		const fail = (error: Error) => {
			if (settled) return;
			settled = true;
			reject(error);
		};
		const data = body === undefined ? undefined : Buffer.from(JSON.stringify(body));
		const request = httpRequest({
			method,
			path,
			socketPath,
			signal,
			headers: data ? { "content-type": "application/json", "content-length": data.length } : undefined,
		}, response => {
			const chunks: Buffer[] = [];
			response.on("data", chunk => chunks.push(Buffer.from(chunk)));
			response.on("aborted", () => fail(new Error("Galpón response was aborted")));
			response.on("error", fail);
			response.on("close", () => {
				if (!response.complete) fail(new Error("Galpón response closed before it completed"));
			});
			response.on("end", () => {
				const text = Buffer.concat(chunks).toString("utf8");
				let value: any = {};
				if (text.trim()) {
					try { value = JSON.parse(text); } catch { value = { error: text.trim() }; }
				}
				if ((response.statusCode ?? 500) < 200 || (response.statusCode ?? 500) >= 300) {
					fail(new Error(value.error ?? `Galpón returned HTTP ${response.statusCode}`));
					return;
				}
				succeed(value);
			});
		});
		request.on("error", fail);
		if (data) request.write(data);
		request.end();
	});
}

function toolResult(value: any) {
	return {
		content: [{ type: "text" as const, text: JSON.stringify(value, null, 2) }],
		details: value,
	};
}

function assistantText(message: any): string {
	if (message?.role !== "assistant") return "";
	if (typeof message.content === "string") return message.content.trim();
	if (!Array.isArray(message.content)) return "";
	return message.content
		.filter((part: any) => part?.type === "text" || part?.type === "output_text")
		.map((part: any) => String(part.text ?? ""))
		.join("\n")
		.trim();
}

export default function galpon(pi: ExtensionAPI) {
	let timer: NodeJS.Timeout | undefined;
	let stopped = false;
	let polling = false;
	let activeMessageIds: string[] = [];
	let awaitingAgentCalls = 0;
	let completionPending = false;
	let deliveryRunActive = false;
	let finishing = false;
	let lastAssistant = "";
	let activeContext: any;

	const callTool = async (name: string, args: Record<string, any>, signal?: AbortSignal) => {
		return api("POST", `/v1/runtime/tools/${name}`, { agentId, args }, signal);
	};

	pi.registerCommand("finish", {
		description: "Close this Herdr tab and hide this Galpón agent",
		handler: async (_args, ctx) => {
			const confirmed = await ctx.ui.confirm(
				`Finish ${agentTitle}?`,
				"This closes the Herdr tab and hides this agent and its unshared private worktrees. Files and the Pi session remain until galpon cleanup.",
			);
			if (!confirmed) {
				ctx.ui.notify("Finish cancelled", "info");
				return;
			}
			try {
				await api("POST", `/v1/runtime/agents/${agentId}/finish`, { runtimeId });
			} catch (error) {
				ctx.ui.notify(`Could not finish agent: ${error instanceof Error ? error.message : String(error)}`, "error");
				return;
			}
			ctx.ui.notify(`Finishing ${agentTitle}…`, "info");
			ctx.shutdown();
		},
	});

	pi.registerTool({
		name: "galpon_list_repositories",
		label: "Galpón repositories",
		description: "List the repositories that Galpón manages.",
		parameters: Type.Object({}),
		async execute(_id, _params, signal) { return toolResult(await callTool("list_repositories", {}, signal)); },
	});
	pi.registerTool({
		name: "galpon_list_workspaces",
		label: "Galpón workspaces",
		description: "List active Galpón workspaces.",
		parameters: Type.Object({}),
		async execute(_id, _params, signal) { return toolResult(await callTool("list_workspaces", {}, signal)); },
	});
	pi.registerTool({
		name: "galpon_create_workspace",
		label: "Create workspace",
		description: "Create an empty durable Galpón coordination workspace. Agent placement creates worktrees later.",
		parameters: Type.Object({
			title: Type.String({ description: "Workspace title" }),
		}),
		async execute(_id, params, signal) { return toolResult(await callTool("create_workspace", params, signal)); },
	});
	pi.registerTool({
		name: "galpon_list_agents",
		label: "Galpón agents",
		description: "List durable Galpón agents and their current state.",
		parameters: Type.Object({}),
		async execute(_id, _params, signal) { return toolResult(await callTool("list_agents", {}, signal)); },
	});
	pi.registerTool({
		name: "galpon_create_agent",
		label: "Create agent",
		description: "Create and start a durable Pi agent with an independent context source and file placement. If prompt is set, Galpón queues it before Pi starts so the agent begins work as soon as its runtime is ready. The result then includes initialMessage, whose ID can be used with galpon_read_message or galpon_await_agent.",
		parameters: Type.Object({
			title: Type.String({ description: "Agent title" }),
			workspace: Type.String({ description: "Workspace ID or exact title" }),
			role: Type.Optional(Type.String({ description: "Optional role, such as implementer, reviewer, or coordinator" })),
			prompt: Type.Optional(Type.String({ description: "Initial work request to queue before the new agent starts" })),
			context_agent: Type.Optional(Type.String({ description: "Existing agent ID or exact title whose Pi conversation must be forked" })),
			repository: Type.Optional(Type.String({ description: "Primary repository ID or exact title for a new private placement" })),
			remote: Type.Optional(Type.String({ description: "Primary source remote" })),
			ref: Type.Optional(Type.String({ description: "Primary source reference" })),
			secondary: Type.Optional(Type.Array(Type.Object({
				repository: Type.String({ description: "Secondary repository ID or exact title" }),
				remote: Type.Optional(Type.String({ description: "Secondary source remote" })),
				ref: Type.Optional(Type.String({ description: "Secondary source reference" })),
			}))),
			placement_agent: Type.Optional(Type.String({ description: "Existing agent whose complete placement must be copied" })),
			share: Type.Optional(Type.Boolean({ description: "Share the placement agent's exact worktrees instead of creating private forks" })),
			cwd: Type.Optional(Type.String({ description: "Absolute directory for an agent with no managed worktree" })),
		}),
		async execute(_id, params, signal) { return toolResult(await callTool("create_agent", params, signal)); },
	});
	pi.registerTool({
		name: "galpon_cleanup_agents",
		label: "Clean up agents",
		description: "Permanently remove the specified agents created directly or indirectly by this agent. Use galpon_list_agents to inspect IDs and creator relationships, then pass only the requested agent IDs. A selected agent cannot be removed while one of its descendants is not selected. This closes managed Herdr views and removes private worktrees, Pi sessions, and related messages. It never removes the calling agent. Use only after an explicit cleanup request and after delegated results are no longer needed.",
		parameters: Type.Object({
			agent_ids: Type.Array(Type.String({ description: "Exact Galpón agent ID" }), { minItems: 1, uniqueItems: true, description: "Agent IDs to remove permanently" }),
		}),
		async execute(_id, params, signal) { return toolResult(await callTool("cleanup_agents", params, signal)); },
	});
	pi.registerTool({
		name: "galpon_send_agent",
		label: "Send agent message",
		description: "Queue a durable message for another Galpón agent and start that agent if necessary. Returns a message ID immediately.",
		parameters: Type.Object({
			agent: Type.String({ description: "Target agent ID or exact title" }),
			prompt: Type.String({ description: "Work request or question" }),
		}),
		async execute(_id, params, signal) { return toolResult(await callTool("send_agent", params, signal)); },
	});
	pi.registerTool({
		name: "galpon_read_message",
		label: "Read agent message",
		description: "Read the current state and result of a Galpón agent message.",
		parameters: Type.Object({ message_id: Type.String({ description: "Message ID from galpon_send_agent" }) }),
		async execute(_id, params, signal) { return toolResult(await callTool("read_message", params, signal)); },
	});
	pi.registerTool({
		name: "galpon_await_agent",
		label: "Wait for agent",
		description: "Wait for up to 60 seconds by default for a Galpón agent message. Galpón rejects circular waits. A queued or delivered result is still pending; do not wait repeatedly without finishing the current turn or doing other useful work.",
		parameters: Type.Object({
			message_id: Type.String({ description: "Message ID from galpon_send_agent" }),
			timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 300, description: "Maximum wait for this call, from 1 to 300 seconds (default 60)" })),
		}),
		async execute(_id, params, signal, onUpdate) {
			awaitingAgentCalls++;
			const started = Date.now();
			let updating = false;
			const progress = setInterval(async () => {
				if (updating || signal?.aborted) return;
				updating = true;
				try {
					const value = await callTool("read_message", { message_id: params.message_id }, signal);
					onUpdate?.({
						content: [{ type: "text", text: `Waiting for agent ${value.targetAgentId}: ${value.status} (${Math.round((Date.now() - started) / 1000)}s)` }],
						details: value,
					});
				} catch {
					// The main wait reports connection and cancellation errors.
				} finally {
					updating = false;
				}
			}, 5000);
			progress.unref?.();
			try {
				return toolResult(await callTool("await_agent", params, signal));
			} finally {
				clearInterval(progress);
				awaitingAgentCalls--;
				schedule(0);
			}
		},
	});

	const schedule = (delay = 350) => {
		if (stopped) return;
		if (timer) clearTimeout(timer);
		timer = setTimeout(() => void poll(), delay);
		timer.unref?.();
	};

	const finishActive = async () => {
		if (activeMessageIds.length === 0) return true;
		if (finishing) return false;
		finishing = true;
		const failure = lastAssistant ? "" : "Pi agent settled without a final text response";
		try {
			for (const messageId of [...activeMessageIds]) {
				try {
					await api("POST", `/v1/runtime/agents/${agentId}/messages/${messageId}/complete`, {
						runtimeId,
						response: lastAssistant,
						error: failure,
					});
					pi.appendEntry("galpon-delivery", { messageId, status: failure ? "failed" : "completed" });
					activeMessageIds = activeMessageIds.filter(id => id !== messageId);
				} catch {
					// Keep this delivery active so the next poll can retry completion.
				}
			}
			if (activeMessageIds.length === 0) {
				completionPending = false;
				lastAssistant = "";
			}
			return activeMessageIds.length === 0;
		} finally {
			finishing = false;
		}
	};

	const claimMessages = async (limit = 20) => {
		const messages: any[] = [];
		for (let index = 0; index < limit; index++) {
			const value = await api("POST", `/v1/runtime/agents/${agentId}/claim`, { runtimeId });
			if (!value.message) break;
			messages.push(value.message);
		}
		return messages;
	};

	const formatMessages = (messages: any[]) => messages.map((message, index) => {
		const sender = message.senderAgentId ? ` from Galpón agent ${message.senderAgentId}` : "";
		return `${messages.length > 1 ? `Message ${index + 1} of ${messages.length}` : "Message"}${sender} [delivery ${message.id}]:\n\n${message.prompt}`;
	}).join("\n\n---\n\n");

	const poll = async () => {
		if (stopped || polling || !activeContext) return;
		polling = true;
		try {
			if (awaitingAgentCalls !== 0) return;
			if (completionPending) {
				await finishActive();
				return;
			}
			if (activeMessageIds.length !== 0 && !deliveryRunActive) return;
			if (activeMessageIds.length === 0 && !activeContext.isIdle()) return;
			const messages = await claimMessages();
			if (messages.length === 0) return;
			const steering = activeMessageIds.length !== 0 && deliveryRunActive;
			if (!steering) lastAssistant = "";
			for (const message of messages) {
				activeMessageIds.push(message.id);
				pi.appendEntry("galpon-delivery", { messageId: message.id, status: "delivered" });
			}
			pi.sendUserMessage(formatMessages(messages), { deliverAs: steering ? "steer" : "followUp" });
		} catch {
			// The daemon can restart while Pi stays open. The next poll reconnects.
		} finally {
			polling = false;
			schedule(activeMessageIds.length !== 0 ? 700 : 350);
		}
	};

	pi.on("session_start", async (_event, ctx) => {
		activeContext = ctx;
		stopped = false;
		ctx.ui.setTitle(`${agentTitle} · ${workspaceTitle}`);
		ctx.ui.setStatus("galpon", `GALPÓN  ${workspaceTitle}`);
		pi.setSessionName(agentTitle);
		await api("POST", `/v1/runtime/agents/${agentId}/register`, {
			runtimeId,
			sessionId: ctx.sessionManager.getSessionId(),
			sessionPath: ctx.sessionManager.getSessionFile() ?? "",
		});
		schedule(0);
	});

	pi.on("before_agent_start", event => ({
		systemPrompt: event.systemPrompt + `\n\nYou are the durable Galpón agent ${agentTitle} in workspace ${workspaceTitle}.${agentRole ? ` Your role is ${agentRole}.` : ""}${placement ? ` Your placement is ${placement}.` : ""} Galpón provides optional tools for repository, workspace, agent, and cross-agent operations. Agent roles and names do not have special built-in behavior. Use these tools only when the user requests coordination or when the current task clearly requires it. Galpón batches queued cross-agent messages into the target's active turn so coordination updates do not create a backlog of separate turns. Address every message in a delivered batch. Agents that you create are recorded as your descendants. Use galpon_cleanup_agents only when the user explicitly asks for cleanup: list the agents, select the exact relevant IDs, and do not clean agents whose results are still needed. Never create a synchronous wait cycle by asking an agent to wait for you while you wait for it. If galpon_await_agent returns a queued or delivered message, finish the current turn or do other useful work before you wait again.`,
	}));

	pi.on("message_end", event => {
		if (event.message?.role === "assistant") lastAssistant = assistantText(event.message);
	});
	pi.on("agent_start", async () => {
		if (activeMessageIds.length !== 0 && !deliveryRunActive) {
			deliveryRunActive = true;
			lastAssistant = "";
		}
		await api("POST", `/v1/runtime/agents/${agentId}/status`, { runtimeId, status: "running" }).catch(() => {});
	});
	pi.on("agent_settled", async () => {
		if (deliveryRunActive) {
			deliveryRunActive = false;
			completionPending = true;
			await finishActive();
		}
		await api("POST", `/v1/runtime/agents/${agentId}/status`, { runtimeId, status: "idle" }).catch(() => {});
		schedule(0);
	});
	pi.on("session_before_switch", (_event, ctx) => {
		ctx.ui.notify("This Pi session belongs to one Galpón agent. Open or create another agent with Ctrl-K.", "warning");
		return { cancel: true };
	});
	pi.on("session_before_fork", (_event, ctx) => {
		ctx.ui.notify("Create another Galpón agent with Ctrl-K instead of forking this session.", "warning");
		return { cancel: true };
	});
	pi.on("session_shutdown", async () => {
		stopped = true;
		if (timer) clearTimeout(timer);
		await api("POST", `/v1/runtime/agents/${agentId}/stop`, { runtimeId }).catch(() => {});
	});
}
