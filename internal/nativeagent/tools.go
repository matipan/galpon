package nativeagent

import "encoding/json"

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}

func arraySchema(item any, min, max int) map[string]any {
	return map[string]any{"type": "array", "items": item, "minItems": min, "maxItems": max}
}

func tools() []toolSpec {
	messageID := stringSchema("Complete message ID, including the message: prefix, returned by Galpon.")
	timeout := map[string]any{"type": "integer", "minimum": 1, "maximum": 300, "description": "Maximum observation wait in seconds. A timeout does not cancel work."}
	return []toolSpec{
		{"galpon_list_repositories", "List managed repositories.", objectSchema(map[string]any{})},
		{"galpon_list_workspaces", "List existing user-managed workspaces.", objectSchema(map[string]any{})},
		{"galpon_list_agents", "List durable agents and their current state.", objectSchema(map[string]any{})},
		{"galpon_create_agent", "Use only for agent creation or delegated work explicitly requested by the user. Create a background agent in your current workspace. Pi is the default harness. Harness choice is permanent. A context fork inherits its source harness. Task difficulty does not authorize delegation.", objectSchema(map[string]any{
			"title": stringSchema("Agent title"), "harness": enumSchema("pi", "claude", "codex"),
			"workspace": stringSchema("Omit to use your current workspace. Only that workspace's ID or exact title is allowed."),
			"role":      stringSchema("Optional role"), "prompt": stringSchema("Initial assignment to queue before launch"),
			"context_agent": stringSchema("Existing agent ID or title to fork. Both agents must use the same harness."),
			"repository":    stringSchema("Primary repository ID or title"), "remote": stringSchema("Source remote"), "ref": stringSchema("Source reference"),
			"secondary":       arraySchema(objectSchema(map[string]any{"repository": stringSchema("Repository ID or title"), "remote": stringSchema("Source remote"), "ref": stringSchema("Source reference")}, "repository"), 0, 32),
			"placement_agent": stringSchema("Existing agent whose placement to copy"), "share": map[string]any{"type": "boolean", "description": "Share the placement agent's exact worktrees"},
			"cwd": stringSchema("Existing absolute directory outside Galpon management"),
		}, "title")},
		{"galpon_send_agent", "Contact another agent only when the user explicitly requests it. Request and query require a reply; inform is one-way. Return the result of your own inbound assignment in your final response, not through this tool.", objectSchema(map[string]any{
			"agent": stringSchema("Target agent ID or exact title"), "prompt": stringSchema("Message text"), "act": enumSchema("request", "query", "inform"),
		}, "agent", "prompt")},
		{"galpon_update_agent", "Append instructions to a queued assignment within user-authorized delegation. Running and completed assignments are unchanged.", objectSchema(map[string]any{"message_id": messageID, "prompt": stringSchema("Additional instructions")}, "message_id", "prompt")},
		{"galpon_read_message", "Read a durable assignment and its result. A read alone does not cancel or acknowledge work.", objectSchema(map[string]any{"message_id": messageID}, "message_id")},
		{"galpon_await_agent", "Wait for one assignment within user-authorized delegation. A timeout does not cancel work. Do not create a circular wait.", objectSchema(map[string]any{"message_id": messageID, "timeout_seconds": timeout}, "message_id")},
		{"galpon_await_agents", "Wait for assignments with one shared timeout. Outcomes retain input order. A timeout does not cancel work. Do not create a circular wait.", objectSchema(map[string]any{"message_ids": arraySchema(messageID, 1, 16), "return_when": enumSchema("any", "all"), "timeout_seconds": timeout}, "message_ids", "return_when")},
		{"galpon_report_progress", "Report one safe factual checkpoint only while processing an active inbound delegated request. No reasoning, prompts, tool data, secrets, paths, percentages, estimates, or ETA values. Not for direct user turns or completed-result notifications.", objectSchema(map[string]any{
			"version": map[string]any{"type": "integer", "enum": []int{1}}, "event_id": stringSchema("Stable unique report ID; omit to use the tool request ID"),
			"phase":      enumSchema("planning", "working", "verifying", "waiting", "blocked", "finishing"),
			"summary":    map[string]any{"type": "string", "minLength": 1, "maxLength": 240},
			"milestones": arraySchema(objectSchema(map[string]any{"label": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "state": enumSchema("pending", "active", "completed", "blocked")}, "label", "state"), 0, 8),
			"blocker":    map[string]any{"type": "string", "maxLength": 240},
			"counts":     arraySchema(objectSchema(map[string]any{"label": map[string]any{"type": "string", "minLength": 1, "maxLength": 40}, "completed": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000000}, "total": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000000}}, "label", "completed", "total"), 0, 8),
		}, "phase", "summary")},
		{"galpon_cleanup_agents", "Permanently remove exact descendant agent IDs only after an explicit user cleanup request and after their results are no longer needed. List agents first. This never removes the caller.", objectSchema(map[string]any{"agent_ids": arraySchema(stringSchema("Exact descendant agent ID"), 1, 256)}, "agent_ids")},
	}
}

func observationIDs(name string, args map[string]any, response json.RawMessage) []string {
	var value map[string]any
	if json.Unmarshal(response, &value) != nil {
		return nil
	}
	terminal := func(value any) bool { return value == "completed" || value == "failed" }
	switch name {
	case "read_message":
		if terminal(value["status"]) {
			return []string{stringValue(args["message_id"])}
		}
	case "await_agent":
		if terminal(value["waitStatus"]) {
			return []string{stringValue(args["message_id"])}
		}
	case "await_agents":
		var ids []string
		outcomes, _ := value["outcomes"].([]any)
		for _, outcome := range outcomes {
			row := objectValue(outcome)
			if terminal(row["waitStatus"]) && stringValue(row["messageId"]) != "" {
				ids = append(ids, stringValue(row["messageId"]))
			}
		}
		return ids
	}
	return nil
}
