package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/apphost"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/tui"
)

// errNoAppHost tells that an agent has no running app host.
var errNoAppHost = errors.New("the agent has no running app host")

// appHostRequest calls the app host of an agent: the host that runs an agent
// of the app view. A host that does not run gives errNoAppHost.
func appHostRequest(ctx context.Context, cfg config.Config, agentID, method, path string, body any) (map[string]any, error) {
	info, ok := apphost.ReadInfo(apphost.Dir(cfg.StateDir, agentID))
	if !ok {
		return nil, errNoAppHost
	}
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", info.Port, path), reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set(apphost.TokenHeader, info.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("the app host gave no JSON: %w", err)
	}
	if response.StatusCode >= 400 {
		if text, _ := out["error"].(string); text != "" {
			return out, errors.New(text)
		}
		return out, fmt.Errorf("the app host answered %s", response.Status)
	}
	return out, nil
}

// herdrAppBrief prints what the command center shows about an agent: its
// last message and its last reply, and, for an agent in the app view, the
// open dialogs of its host and whether it works now:
// herdr-app brief <agent-id>.
func herdrAppBrief(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app brief needs an agent ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	brief, err := client.AgentBrief(ctx, args[0])
	if err != nil {
		return modError(err)
	}
	prompt, from := deliveryText(brief.Prompt)
	out := map[string]any{
		"agentId": args[0], "reply": clipText(brief.Reply, 4000), "replyAt": brief.ReplyAt,
		"prompt": clipText(prompt, 1200), "from": from, "promptAt": brief.PromptAt,
	}
	hostCtx, cancelHost := context.WithTimeout(ctx, 2*time.Second)
	defer cancelHost()
	if state, err := appHostRequest(hostCtx, cfg, args[0], http.MethodGet, "/state", nil); err == nil {
		out["live"] = state["alive"] == true
		out["streaming"] = state["streaming"] == true
		out["dialogs"] = state["dialogs"]
	}
	return printModResult(out)
}

// deliveryHeader is the first line of a Galpon delivery as a Pi agent gets
// it, such as "Work request from Galpón agent Planner [delivery 12]:".
var deliveryHeader = regexp.MustCompile(`^(?:Work request|Question|One-way information|Message \d+ of \d+|Blocked work notification)(?: from Galpón agent (.+?))? \[delivery [^\]]+\]:\n\n`)

// deliveryText gives the text of a message to an agent without the protocol
// lines of a Galpon delivery, and the agent that sent it.
func deliveryText(text string) (string, string) {
	match := deliveryHeader.FindStringSubmatchIndex(text)
	if match == nil {
		return text, ""
	}
	from := ""
	if match[2] >= 0 {
		from = text[match[2]:match[3]]
	}
	body := text[match[1]:]
	if index := strings.Index(body, "\n\n---\n\nDelivery instructions:"); index >= 0 {
		body = body[:index]
	}
	return strings.TrimSpace(body), from
}

func clipText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}

// herdrAppReply sends text to an agent as a message of the person. An agent
// in the app view gets it as if the person typed it in its tab: while it
// works, the text steers its run. Another agent gets a Galpon message.
func herdrAppReply(ctx context.Context, cfg config.Config, client *app.Client, agentID, text string) tui.NativeResult {
	text = strings.TrimSpace(text)
	if text == "" {
		return tui.NativeResult{Error: "write a message before sending"}
	}
	stateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	state, err := appHostRequest(stateCtx, cfg, agentID, http.MethodGet, "/state", nil)
	cancel()
	if err != nil || state["alive"] != true {
		return tui.NativeSend(ctx, client, agentID, text)
	}
	command := map[string]any{"type": "prompt", "message": text}
	if state["streaming"] == true {
		command["streamingBehavior"] = "steer"
	}
	if _, err := appHostRequest(ctx, cfg, agentID, http.MethodPost, "/rpc", command); err != nil {
		return tui.NativeResult{Error: err.Error()}
	}
	return tui.NativeResult{OK: true, Message: "Sent"}
}
