package nativeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Permission prompts of Claude Code in the agent tab. Claude Code asks with a
// can_use_tool control request; the tab shows a dialog, and the answer goes
// back as the control response: allow, with the input and optional
// permission updates, or deny, with a message for the model. A question of
// the AskUserQuestion tool and the plan of ExitPlanMode come the same way.

const (
	answerOther = "Other…"
	answerNote  = "No, and tell Claude what to do…"
)

func (a *claudeApp) controlRequest(event map[string]any) {
	id := stringValue(event["request_id"])
	request := objectValue(event["request"])
	if request["subtype"] != "can_use_tool" {
		_ = a.driver.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": id,
			"error": "the agent tab of Galpon does not handle " + stringValue(request["subtype"])}})
		return
	}
	dialog := &claudeDialog{requestID: id, request: request, answers: map[string]any{}}
	switch stringValue(request["tool_name"]) {
	case "AskUserQuestion":
		dialog.kind = "question"
		dialog.questions = anyList(objectValue(request["input"])["questions"])
		a.askQuestion(dialog)
	case "ExitPlanMode":
		dialog.kind = "plan"
		a.askPlan(dialog)
	default:
		dialog.kind = "tool"
		a.askTool(dialog)
	}
}

// openDialog shows one dialog of a request. Each dialog has its own ID.
func (a *claudeApp) openDialog(d *claudeDialog, method, title, message string, options []string) {
	a.mu.Lock()
	d.steps++
	id := fmt.Sprintf("%s:%d", d.requestID, d.steps)
	a.dialogs[id] = d
	a.mu.Unlock()
	request := map[string]any{"type": "extension_ui_request", "id": id, "method": method, "title": title}
	if message != "" {
		request["message"] = message
	}
	switch method {
	case "select":
		request["options"] = options
	case "input":
		request["placeholder"] = "Type your answer"
	}
	a.emit(request)
}

// cancelDialogs closes the dialogs of a request that Claude Code withdrew,
// for example after an interrupt.
func (a *claudeApp) cancelDialogs(requestID string) {
	a.mu.Lock()
	var closed []string
	for id, dialog := range a.dialogs {
		if dialog.requestID == requestID {
			closed = append(closed, id)
			delete(a.dialogs, id)
		}
	}
	a.mu.Unlock()
	for _, id := range closed {
		a.emit(map[string]any{"type": "dialog_closed", "id": id})
	}
}

func (a *claudeApp) answer(command map[string]any) {
	id := stringValue(command["id"])
	a.mu.Lock()
	d := a.dialogs[id]
	delete(a.dialogs, id)
	a.mu.Unlock()
	if d == nil {
		return
	}
	cancelled := command["cancelled"] == true
	value := stringValue(command["value"])
	if waiting := d.waiting; waiting != "" {
		d.waiting = ""
		value = strings.TrimSpace(value)
		switch {
		case waiting == "other" && (cancelled || value == ""):
			a.askQuestion(d)
		case waiting == "other":
			d.answers[questionText(d)] = value
			d.index++
			a.askQuestion(d)
		case cancelled || value == "":
			a.deny(d, "The person denied this request.")
		case waiting == "plan-note":
			a.deny(d, "The person does not approve the plan yet. Change the plan as follows, then ask again: "+value)
		default:
			a.deny(d, "The person denied this tool call and said: "+value)
		}
		return
	}
	if cancelled {
		a.deny(d, "The person dismissed this request.")
		return
	}
	if d.kind == "question" {
		if value == answerOther {
			d.waiting = "other"
			a.openDialog(d, "input", questionText(d), "", nil)
			return
		}
		d.answers[questionText(d)] = value
		d.index++
		a.askQuestion(d)
		return
	}
	switch d.actions[value] {
	case "allow":
		a.allow(d, objectValue(d.request["input"]), nil)
	case "always":
		a.allow(d, objectValue(d.request["input"]), anyList(d.request["permission_suggestions"]))
	case "accept-edits":
		a.allow(d, objectValue(d.request["input"]), []any{map[string]any{"type": "setMode", "mode": "acceptEdits", "destination": "session"}})
	case "keep-planning":
		a.deny(d, "The person wants to keep planning. Stay in plan mode and improve the plan.")
	case "note":
		d.waiting = "note"
		a.openDialog(d, "input", "What should Claude do instead?", "", nil)
	case "plan-note":
		d.waiting = "plan-note"
		a.openDialog(d, "input", "What should change in the plan?", "", nil)
	default:
		a.deny(d, "The person denied this tool call.")
	}
}

func (a *claudeApp) allow(d *claudeDialog, input map[string]any, permissions []any) {
	if input == nil {
		input = map[string]any{}
	}
	response := map[string]any{"behavior": "allow", "updatedInput": input}
	if len(permissions) > 0 {
		response["updatedPermissions"] = permissions
	}
	_ = a.driver.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": d.requestID, "response": response}})
}

func (a *claudeApp) deny(d *claudeDialog, message string) {
	_ = a.driver.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": d.requestID,
		"response": map[string]any{"behavior": "deny", "message": message}}})
}

// --- tools

func (a *claudeApp) askTool(d *claudeDialog) {
	name := stringValue(d.request["tool_name"])
	title, body := claudePermissionText(name, objectValue(d.request["input"]), stringValue(d.request["description"]))
	if reason := strings.TrimSpace(stripANSI(stringValue(d.request["decision_reason"]))); reason != "" {
		body = strings.TrimSpace(body + "\n\n" + reason)
	}
	options := []string{"Yes"}
	d.actions = map[string]string{"Yes": "allow", "No": "deny", answerNote: "note"}
	if suggestions := anyList(d.request["permission_suggestions"]); len(suggestions) > 0 {
		label := "Yes, " + claudeSuggestionLabel(suggestions)
		options = append(options, label)
		d.actions[label] = "always"
	}
	options = append(options, "No", answerNote)
	a.openDialog(d, "select", title, body, options)
}

// claudePermissionText gives the title and the text of a permission dialog.
func claudePermissionText(name string, input map[string]any, description string) (string, string) {
	path := firstText(stringValue(input["file_path"]), stringValue(input["notebook_path"]), stringValue(input["path"]))
	base := filepath.Base(path)
	path = homePath(path)
	switch name {
	case "Bash":
		body := "```sh\n" + stringValue(input["command"]) + "\n```"
		if text := stringValue(input["description"]); text != "" {
			body = text + "\n\n" + body
		}
		return "Run this command?", body
	case "Edit":
		return "Edit " + base + "?", "`" + path + "`\n\n" + editPreview(stringValue(input["old_string"]), stringValue(input["new_string"]))
	case "MultiEdit":
		var parts []string
		for _, raw := range anyList(input["edits"]) {
			edit := objectValue(raw)
			parts = append(parts, editPreview(stringValue(edit["old_string"]), stringValue(edit["new_string"])))
		}
		return "Edit " + base + "?", "`" + path + "`\n\n" + strings.Join(parts, "\n\n")
	case "Write":
		content := stringValue(input["content"])
		lines := strings.Count(content, "\n")
		if content != "" && !strings.HasSuffix(content, "\n") {
			lines++
		}
		return "Write " + base + "?", fmt.Sprintf("`%s` · %d lines\n\n```\n%s\n```", path, lines, firstLines(content, 24))
	case "NotebookEdit":
		return "Edit the notebook " + base + "?", "`" + path + "`"
	case "Read":
		return "Read " + base + "?", "`" + path + "`"
	case "Glob", "Grep":
		return "Search the files?", "`" + firstText(stringValue(input["pattern"]), path) + "`" + pathSuffix(stringValue(input["path"]))
	case "WebFetch":
		return "Fetch this page?", stringValue(input["url"]) + quoted(stringValue(input["prompt"]))
	case "WebSearch":
		return "Search the web?", "`" + stringValue(input["query"]) + "`"
	case "Agent", "Task":
		return "Start a subagent?", "**" + firstText(stringValue(input["description"]), "Subagent") + "**" + quoted(stringValue(input["prompt"]))
	case "Skill":
		return "Use the skill " + firstText(stringValue(input["skill"]), stringValue(input["command"]), "") + "?", description
	}
	if server, tool, ok := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__"); ok && strings.HasPrefix(name, "mcp__") {
		return fmt.Sprintf("Use %s from %s?", tool, server), jsonBlock(input)
	}
	return "Allow " + name + "?", jsonBlock(input)
}

func pathSuffix(path string) string {
	if path == "" {
		return ""
	}
	return " in `" + homePath(path) + "`"
}

// rulePath writes the absolute path of a permission rule, which Claude Code
// starts with "//", with "~" for the home folder.
func rulePath(content string) string {
	if rest, ok := strings.CutPrefix(content, "//"); ok {
		if short := homePath("/" + rest); short != "/"+rest {
			return short
		}
	}
	return content
}

// homePath writes a path in the home folder with "~".
func homePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}

func quoted(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > 1200 {
		text = text[:1200] + "…"
	}
	return "\n\n> " + strings.ReplaceAll(text, "\n", "\n> ")
}

func jsonBlock(input map[string]any) string {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil || len(input) == 0 {
		return ""
	}
	text := string(data)
	if len(text) > 1500 {
		text = text[:1500] + "\n…"
	}
	return "```json\n" + text + "\n```"
}

func firstLines(text string, count int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > count {
		return strings.Join(lines[:count], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-count)
	}
	return strings.Join(lines, "\n")
}

// editPreview shows a replacement as a diff.
func editPreview(before, after string) string {
	var lines []string
	for _, line := range strings.Split(firstLines(before, 20), "\n") {
		lines = append(lines, "-"+line)
	}
	for _, line := range strings.Split(firstLines(after, 20), "\n") {
		lines = append(lines, "+"+line)
	}
	return "```diff\n" + strings.Join(lines, "\n") + "\n```"
}

// claudeSuggestionLabel says what the permission updates that Claude Code
// suggests allow from now on.
func claudeSuggestionLabel(suggestions []any) string {
	where := func(destination string) string {
		switch destination {
		case "session":
			return " in this session"
		case "localSettings", "projectSettings":
			return " in this project"
		case "userSettings":
			return " everywhere"
		}
		return ""
	}
	for _, raw := range suggestions {
		suggestion := objectValue(raw)
		destination := where(stringValue(suggestion["destination"]))
		switch suggestion["type"] {
		case "setMode":
			if suggestion["mode"] == "acceptEdits" {
				return "and accept edits" + destination
			}
			return "and switch to " + permissionModeLabel(stringValue(suggestion["mode"])) + destination
		case "addRules", "replaceRules":
			var rules []string
			for _, rawRule := range anyList(suggestion["rules"]) {
				rule := objectValue(rawRule)
				text := stringValue(rule["toolName"])
				if content := stringValue(rule["ruleContent"]); content != "" {
					text += "(" + rulePath(content) + ")"
				}
				rules = append(rules, text)
			}
			if len(rules) > 0 {
				return "and always allow " + strings.Join(rules, ", ") + destination
			}
		case "addDirectories":
			var directories []string
			for _, directory := range anyList(suggestion["directories"]) {
				directories = append(directories, homePath(stringValue(directory)))
			}
			if len(directories) > 0 {
				return "and allow access to " + strings.Join(directories, ", ") + destination
			}
		}
	}
	return "and do not ask again"
}

func permissionModeLabel(mode string) string {
	switch mode {
	case "acceptEdits":
		return "accept edits"
	case "plan":
		return "plan mode"
	case "bypassPermissions":
		return "bypass permissions"
	case "auto":
		return "auto mode"
	case "dontAsk":
		return "do not ask"
	}
	return "ask first"
}

// --- questions

func questionText(d *claudeDialog) string {
	if d.index >= len(d.questions) {
		return ""
	}
	return stringValue(objectValue(d.questions[d.index])["question"])
}

func (a *claudeApp) askQuestion(d *claudeDialog) {
	if d.index >= len(d.questions) {
		input := map[string]any{}
		for key, value := range objectValue(d.request["input"]) {
			input[key] = value
		}
		input["answers"] = d.answers
		a.allow(d, input, nil)
		return
	}
	question := objectValue(d.questions[d.index])
	var options, lines []string
	for _, raw := range anyList(question["options"]) {
		option := objectValue(raw)
		label := stringValue(option["label"])
		if label == "" {
			continue
		}
		options = append(options, label)
		if description := stringValue(option["description"]); description != "" {
			lines = append(lines, fmt.Sprintf("- **%s** · %s", label, description))
		}
	}
	options = append(options, answerOther)
	body := strings.Join(lines, "\n")
	if header := stringValue(question["header"]); header != "" {
		body = strings.TrimSpace("**" + header + "**\n\n" + body)
	}
	if question["multiSelect"] == true {
		body = strings.TrimSpace(body + "\n\nChoose one, or choose Other… to name more than one.")
	}
	if len(d.questions) > 1 {
		body = strings.TrimSpace(fmt.Sprintf("Question %d of %d\n\n", d.index+1, len(d.questions)) + body)
	}
	a.openDialog(d, "select", stringValue(question["question"]), body, options)
}

// --- the plan

func (a *claudeApp) askPlan(d *claudeDialog) {
	plan := stringValue(objectValue(d.request["input"])["plan"])
	if plan == "" {
		a.mu.Lock()
		if call := a.calls[stringValue(d.request["tool_use_id"])]; call != nil {
			plan = stringValue(objectValue(call.args)["plan"])
		}
		a.mu.Unlock()
	}
	if plan == "" {
		plan = a.latestPlan()
	}
	if len(plan) > 8000 {
		plan = plan[:8000] + "\n\n…"
	}
	if plan == "" {
		plan = "Claude finished planning and wants to start the work."
	}
	options := []string{"Yes, start the work", "Yes, and accept edits", "No, keep planning", "No, and tell Claude what to change…"}
	d.actions = map[string]string{options[0]: "allow", options[1]: "accept-edits", options[2]: "keep-planning", options[3]: "plan-note"}
	a.openDialog(d, "select", "Start the work?", plan, options)
}

// latestPlan reads the plan file that Claude Code wrote last, in the last
// hour: Claude Code saves a plan to a file before it leaves plan mode.
func (a *claudeApp) latestPlan() string {
	entries, err := os.ReadDir(filepath.Join(a.driver.configDirectory, "plans"))
	if err != nil {
		return ""
	}
	newest, at := "", time.Now().Add(-time.Hour)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") || !info.ModTime().After(at) {
			continue
		}
		newest, at = entry.Name(), info.ModTime()
	}
	if newest == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(a.driver.configDirectory, "plans", newest))
	if err != nil {
		return ""
	}
	return string(data)
}
