package nativeagent

import (
	"context"
	"encoding/json"
	"strings"
)

// The daemon owns delivery retries. Remove only Galpon submissions left between
// queue admission and turn start; preserve messages queued by the native user.
func (d *codexDriver) queuedSubmissions(ctx context.Context, threadID, clientID string) ([]string, error) {
	var ids []string
	cursor := ""
	for {
		params := map[string]any{"threadId": threadID, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		data, err := d.call(ctx, "thread/queue/list", params)
		if err != nil {
			if strings.Contains(err.Error(), "thread not found") || strings.Contains(err.Error(), "no rollout found") {
				return nil, nil
			}
			return nil, err
		}
		var page struct {
			Data []struct {
				ID       string `json:"id"`
				ClientID string `json:"clientUserMessageId"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, err
		}
		for _, entry := range page.Data {
			if strings.HasPrefix(entry.ClientID, "galpon:"+d.options.Agent.ID+":") && (clientID == "" || entry.ClientID == clientID) {
				ids = append(ids, entry.ID)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	return ids, nil
}

func (d *codexDriver) Withdraw(ctx context.Context, token string) (bool, error) {
	ids, err := d.queuedSubmissions(ctx, d.id, "galpon:"+d.options.Agent.ID+":"+token)
	if err != nil || len(ids) == 0 {
		return false, err
	}
	for _, id := range ids {
		data, err := d.call(ctx, "thread/queue/delete", map[string]any{"threadId": d.id, "queuedSubmissionId": id})
		if err != nil {
			return false, err
		}
		var result struct {
			Deleted bool `json:"deleted"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return false, err
		}
		if !result.Deleted {
			return false, nil
		}
	}
	return true, nil
}

func (d *codexDriver) clearQueued(ctx context.Context, threadID string) error {
	ids, err := d.queuedSubmissions(ctx, threadID, "")
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := d.call(ctx, "thread/queue/delete", map[string]any{"threadId": threadID, "queuedSubmissionId": id}); err != nil {
			return err
		}
	}
	return nil
}
