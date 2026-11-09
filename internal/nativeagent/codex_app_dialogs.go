package nativeagent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Approvals and questions of Codex in the agent tab. Codex asks with a server
// request of the app-server; the tab shows a dialog, and the answer goes back
// as the result of the request: a decision for a command, a file change or
// more permissions, and the answers of the person for a question.

const codexNote = "No, and tell Codex what to do…"

// codexDialog is a request of Codex that the person answers in the tab, in
// one or more dialogs.
type codexDialog struct {
	requestID json.RawMessage
	method    string
	params    map[string]any
	key       string
	steps     int
	decisions map[string]any
	questions []any
	index     int
	answers   map[string]any
	// waiting is what the text of an input dialog answers.
	waiting string
}

// request handles a server request of Codex. It runs on the reader of the
// connection, so it opens the dialog and returns at once.
func (a *codexApp) request(id json.RawMessage, method string, params map[string]any) {
	a.mu.Lock()
	a.nextDialog++
	d := &codexDialog{requestID: id, method: method, params: params, key: fmt.Sprintf("codex-%d", a.nextDialog), answers: map[string]any{}}
	a.mu.Unlock()
	switch method {
	case "item/commandExecution/requestApproval":
		a.askCommand(d)
	case "item/fileChange/requestApproval":
		a.askChange(d)
	case "item/permissions/requestApproval":
		a.askPermissions(d)
	case "item/tool/requestUserInput":
		d.questions = anyList(params["questions"])
		a.askQuestion(d)
	case "mcpServer/elicitation/request":
		a.askElicitation(d)
	default:
		a.driver.respondError(id, "the agent tab of Galpon does not handle "+method)
	}
}

// openDialog shows one dialog of a request. Each dialog has its own ID.
func (a *codexApp) openDialog(d *codexDialog, method, title, message string, options []string) {
	a.mu.Lock()
	d.steps++
	id := fmt.Sprintf("%s:%d", d.key, d.steps)
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

// resolved closes the dialogs of a request that Codex resolved without the
// tab, for example after a stop of the turn.
func (a *codexApp) resolved(requestID any) {
	want, err := json.Marshal(requestID)
	if err != nil {
		return
	}
	a.mu.Lock()
	var closed []string
	for id, dialog := range a.dialogs {
		if string(dialog.requestID) == string(want) {
			closed = append(closed, id)
			delete(a.dialogs, id)
		}
	}
	a.mu.Unlock()
	for _, id := range closed {
		a.emit(map[string]any{"type": "dialog_closed", "id": id})
	}
}

func (a *codexApp) answer(command map[string]any) {
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
		case waiting == "other" || waiting == "secret":
			d.answers[stringValue(objectValue(d.questions[d.index])["id"])] = map[string]any{"answers": []string{value}}
			d.index++
			a.askQuestion(d)
		case waiting == "note":
			a.decide(d, "decline")
			if !cancelled && value != "" {
				go func() {
					if err := a.prompt(map[string]any{"message": value}, "steer"); err != nil {
						a.notify("Galpon could not give Codex your note: "+err.Error(), "error")
					}
				}()
			}
		}
		return
	}
	if d.method == "item/tool/requestUserInput" {
		if cancelled {
			a.finishQuestions(d)
			return
		}
		if value == answerOther {
			d.waiting = "other"
			a.openDialog(d, "input", stringValue(objectValue(d.questions[d.index])["question"]), "", nil)
			return
		}
		d.answers[stringValue(objectValue(d.questions[d.index])["id"])] = map[string]any{"answers": []string{value}}
		d.index++
		a.askQuestion(d)
		return
	}
	if d.method == "mcpServer/elicitation/request" {
		action := "decline"
		if cancelled {
			action = "cancel"
		} else if value == "Accept" {
			action = "accept"
		}
		a.driver.respond(d.requestID, map[string]any{"action": action, "content": nil, "_meta": nil})
		return
	}
	if cancelled {
		a.decide(d, "cancel")
		return
	}
	decision, ok := d.decisions[value]
	if !ok {
		decision = "decline"
	}
	if decision == "note" {
		d.waiting = "note"
		a.openDialog(d, "input", "What should Codex do instead?", "", nil)
		return
	}
	a.decide(d, decision)
}

// decide answers an approval.
func (a *codexApp) decide(d *codexDialog, decision any) {
	if d.method == "item/permissions/requestApproval" {
		if decision == "accept" || decision == "acceptForSession" {
			scope := "turn"
			if decision == "acceptForSession" {
				scope = "session"
			}
			a.driver.respond(d.requestID, map[string]any{"permissions": objectValue(d.params["permissions"]), "scope": scope})
			return
		}
		a.driver.respond(d.requestID, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
		return
	}
	a.driver.respond(d.requestID, map[string]any{"decision": decision})
}

// --- approvals

func (a *codexApp) askCommand(d *codexDialog) {
	params := d.params
	command := codexCommand(stringValue(params["command"]))
	title := "Run this command?"
	var parts []string
	if reason := strings.TrimSpace(stringValue(params["reason"])); reason != "" {
		parts = append(parts, reason)
	}
	if network := objectValue(params["networkApprovalContext"]); network != nil {
		title = "Allow network access?"
		parts = append(parts, fmt.Sprintf("`%s` · %s", stringValue(network["host"]), stringValue(network["protocol"])))
	}
	if command != "" {
		parts = append(parts, "```sh\n"+command+"\n```")
	}
	if cwd := stringValue(params["cwd"]); cwd != "" && cwd != a.options.Directory {
		parts = append(parts, "in `"+homePath(cwd)+"`")
	}
	d.decisions = map[string]any{"Yes": "accept", "No": "decline", codexNote: "note"}
	options := []string{"Yes"}
	if prefix := anyList(params["proposedExecpolicyAmendment"]); len(prefix) > 0 {
		var words []string
		for _, word := range prefix {
			words = append(words, stringValue(word))
		}
		label := "Yes, and always allow commands that start with " + strings.Join(words, " ")
		options = append(options, label)
		d.decisions[label] = map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": prefix}}
	} else {
		options = append(options, "Yes, for this session")
		d.decisions["Yes, for this session"] = "acceptForSession"
	}
	for _, raw := range anyList(params["proposedNetworkPolicyAmendments"]) {
		amendment := objectValue(raw)
		if amendment["action"] != "allow" {
			continue
		}
		label := "Yes, and always allow " + stringValue(amendment["host"])
		options = append(options, label)
		d.decisions[label] = map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": amendment}}
		break
	}
	options = append(options, "No", codexNote)
	a.openDialog(d, "select", title, strings.Join(parts, "\n\n"), options)
}

func (a *codexApp) askChange(d *codexDialog) {
	a.mu.Lock()
	item := a.items[stringValue(d.params["itemId"])]
	a.mu.Unlock()
	changes := anyList(item["changes"])
	title := "Apply these changes?"
	if len(changes) == 1 {
		title = "Edit " + filepath.Base(stringValue(objectValue(changes[0])["path"])) + "?"
	}
	var parts []string
	if reason := strings.TrimSpace(stringValue(d.params["reason"])); reason != "" {
		parts = append(parts, reason)
	}
	if root := stringValue(d.params["grantRoot"]); root != "" {
		parts = append(parts, "Codex asks to write in `"+homePath(root)+"`.")
	}
	for _, raw := range changes {
		change := objectValue(raw)
		diff := firstLines(codexDiff(change), 40)
		parts = append(parts, "`"+homePath(stringValue(change["path"]))+"`\n\n```diff\n"+diff+"\n```")
	}
	d.decisions = map[string]any{"Yes": "accept", "Yes, for this session": "acceptForSession", "No": "decline", codexNote: "note"}
	a.openDialog(d, "select", title, strings.Join(parts, "\n\n"), []string{"Yes", "Yes, for this session", "No", codexNote})
}

func (a *codexApp) askPermissions(d *codexDialog) {
	permissions := objectValue(d.params["permissions"])
	var parts []string
	if reason := strings.TrimSpace(stringValue(d.params["reason"])); reason != "" {
		parts = append(parts, reason)
	}
	if network := permissions["network"]; network != nil {
		parts = append(parts, "Network: "+codexJSON(network))
	}
	if files := permissions["fileSystem"]; files != nil {
		parts = append(parts, "Files: "+codexJSON(files))
	}
	d.decisions = map[string]any{"Yes, for this turn": "accept", "Yes, for this session": "acceptForSession", "No": "decline"}
	a.openDialog(d, "select", "Give Codex more permissions?", strings.Join(parts, "\n\n"), []string{"Yes, for this turn", "Yes, for this session", "No"})
}

func codexJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return "`" + string(data) + "`"
}

func (a *codexApp) askElicitation(d *codexDialog) {
	server := stringValue(d.params["serverName"])
	message := firstText(stringValue(d.params["message"]), stringValue(d.params["description"]), stringValue(d.params["title"]))
	if mode := stringValue(d.params["mode"]); mode == "form" || mode == "openai/form" {
		if properties := objectValue(objectValue(d.params["requestedSchema"])["properties"]); len(properties) > 0 {
			// The tab has no form for the fields: the terminal of Codex has.
			message = strings.TrimSpace(message + "\n\nThis request has fields. Open the agent in a terminal to fill them in.")
		}
	}
	a.openDialog(d, "select", "The MCP server "+server+" asks", message, []string{"Accept", "Decline"})
}

// --- questions

func (a *codexApp) askQuestion(d *codexDialog) {
	if d.index >= len(d.questions) {
		a.finishQuestions(d)
		return
	}
	question := objectValue(d.questions[d.index])
	if question["isSecret"] == true {
		d.waiting = "secret"
		a.openDialog(d, "input", stringValue(question["question"]), "", nil)
		return
	}
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
	if len(options) == 0 {
		d.waiting = "other"
		a.openDialog(d, "input", stringValue(question["question"]), stringValue(question["header"]), nil)
		return
	}
	if question["isOther"] != false {
		options = append(options, answerOther)
	}
	body := strings.Join(lines, "\n")
	if header := stringValue(question["header"]); header != "" {
		body = strings.TrimSpace("**" + header + "**\n\n" + body)
	}
	if len(d.questions) > 1 {
		body = strings.TrimSpace(fmt.Sprintf("Question %d of %d\n\n", d.index+1, len(d.questions)) + body)
	}
	a.openDialog(d, "select", stringValue(question["question"]), body, options)
}

func (a *codexApp) finishQuestions(d *codexDialog) {
	a.driver.respond(d.requestID, map[string]any{"answers": d.answers})
}
