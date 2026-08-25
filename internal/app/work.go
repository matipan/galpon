package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func ValidateWorkProgress(value model.WorkProgressEvent) (model.WorkProgressEvent, error) {
	validated, err := model.ValidateWorkProgress(value)
	if err != nil {
		return value, invalidRequestf("%v", err)
	}
	return validated, nil
}

func workProgressFromToolArgs(args map[string]any) (model.WorkProgressEvent, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return model.WorkProgressEvent{}, invalidRequestf("progress payload is invalid")
	}
	var input struct {
		Version    int                   `json:"version"`
		EventID    string                `json:"event_id"`
		Phase      string                `json:"phase"`
		Summary    string                `json:"summary"`
		Milestones []model.WorkMilestone `json:"milestones"`
		Blocker    string                `json:"blocker"`
		Counts     []model.WorkCount     `json:"counts"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return model.WorkProgressEvent{}, invalidRequestf("progress payload is invalid")
	}
	return model.WorkProgressEvent{Version: input.Version, EventID: input.EventID, Phase: input.Phase, Summary: input.Summary, Milestones: input.Milestones, Blocker: input.Blocker, Counts: input.Counts}, nil
}

func (a *App) ReportWorkProgress(ctx context.Context, agentID, runtimeID, messageID string, attempt int, value model.WorkProgressEvent) (model.WorkProgressEvent, bool, error) {
	validated, err := ValidateWorkProgress(value)
	if err != nil {
		return value, false, err
	}
	validated.MessageID = strings.TrimSpace(messageID)
	if validated.MessageID == "" || attempt < 1 {
		return value, false, invalidRequestf("an active delivery and attempt are required")
	}
	stored, inserted, err := a.Store.PutWorkProgress(ctx, agentID, runtimeID, attempt, validated)
	if err != nil {
		if errors.Is(err, store.ErrWorkProgressLimit) {
			return stored, inserted, invalidRequestf("%v", err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return stored, inserted, invalidRequestf("report_progress requires an active delegated request delivery")
		}
		return stored, inserted, err
	}
	if validated.Phase == "blocked" {
		if err := a.Store.DispatchLifecycleEvents(ctx, 100); err != nil {
			return stored, inserted, err
		}
	}
	return stored, inserted, nil
}

func (a *App) AgentWork(ctx context.Context, agentID string, includeSettled bool) (model.WorkProjection, error) {
	if _, err := a.Store.Agent(ctx, agentID); err != nil {
		return model.WorkProjection{}, err
	}
	return a.Store.AgentWork(ctx, agentID, includeSettled)
}
