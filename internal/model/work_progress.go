package model

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	WorkSummaryLimit        = 240
	WorkMilestoneLimit      = 8
	WorkMilestoneLabelLimit = 80
	WorkCountLimit          = 8
	WorkCountLabelLimit     = 40
	WorkCounterMaximum      = 1_000_000_000
)

var unsafeProgressText = regexp.MustCompile(`(?i)(?:bearer\s+[a-z0-9._-]+|(?:password|secret|api[_-]?key|token)\s*[:=]|\bsk-[a-z0-9_-]{8,}|chain[- ]of[- ]thought|private reasoning|\beta\b|\bETA\b|\d+\s*%)`)

func validateProgressText(name, value string, required bool, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if value == "" {
		return "", nil
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return "", fmt.Errorf("%s exceeds the %d-character limit", name, limit)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u001b' || r == '\n' || r == '\r' {
			return "", fmt.Errorf("%s must be one line of visible text", name)
		}
	}
	if strings.ContainsAny(value, `/\\`) {
		return "", fmt.Errorf("%s must not contain a path", name)
	}
	if unsafeProgressText.MatchString(value) {
		return "", fmt.Errorf("%s contains unsafe progress text", name)
	}
	return value, nil
}

// ValidateWorkProgress is the common live-ingest and checkpoint-restore
// boundary for safe agent-reported progress fields.
func ValidateWorkProgress(value WorkProgressEvent) (WorkProgressEvent, error) {
	if value.Version != 1 {
		return value, fmt.Errorf("progress version must be 1")
	}
	var err error
	value.EventID, err = validateProgressText("event_id", value.EventID, true, 100)
	if err != nil {
		return value, err
	}
	validPhase := map[string]bool{"planning": true, "working": true, "verifying": true, "waiting": true, "blocked": true, "finishing": true}
	value.Phase = strings.ToLower(strings.TrimSpace(value.Phase))
	if !validPhase[value.Phase] {
		return value, fmt.Errorf("progress phase is invalid")
	}
	value.Summary, err = validateProgressText("summary", value.Summary, true, WorkSummaryLimit)
	if err != nil {
		return value, err
	}
	value.Blocker, err = validateProgressText("blocker", value.Blocker, false, WorkSummaryLimit)
	if err != nil {
		return value, err
	}
	if value.Phase == "blocked" && value.Blocker == "" {
		return value, fmt.Errorf("blocked progress requires a blocker")
	}
	if value.Phase != "blocked" && value.Blocker != "" {
		return value, fmt.Errorf("a blocker is only valid for blocked progress")
	}
	if len(value.Milestones) > WorkMilestoneLimit {
		return value, fmt.Errorf("progress accepts at most %d milestones", WorkMilestoneLimit)
	}
	milestoneStates := map[string]bool{"pending": true, "active": true, "completed": true, "blocked": true}
	labels := make(map[string]bool, len(value.Milestones))
	for index := range value.Milestones {
		item := &value.Milestones[index]
		item.Label, err = validateProgressText(fmt.Sprintf("milestone %d label", index), item.Label, true, WorkMilestoneLabelLimit)
		if err != nil {
			return value, err
		}
		item.State = strings.ToLower(strings.TrimSpace(item.State))
		if !milestoneStates[item.State] {
			return value, fmt.Errorf("milestone %d state is invalid", index)
		}
		key := strings.ToLower(item.Label)
		if labels[key] {
			return value, fmt.Errorf("milestone labels must be unique")
		}
		labels[key] = true
	}
	if len(value.Counts) > WorkCountLimit {
		return value, fmt.Errorf("progress accepts at most %d factual counts", WorkCountLimit)
	}
	labels = make(map[string]bool, len(value.Counts))
	for index := range value.Counts {
		item := &value.Counts[index]
		item.Label, err = validateProgressText(fmt.Sprintf("count %d label", index), item.Label, true, WorkCountLabelLimit)
		if err != nil {
			return value, err
		}
		if item.Completed < 0 || item.Total < 0 || item.Completed > item.Total || item.Total > WorkCounterMaximum {
			return value, fmt.Errorf("count %d values are invalid", index)
		}
		key := strings.ToLower(item.Label)
		if labels[key] {
			return value, fmt.Errorf("count labels must be unique")
		}
		labels[key] = true
	}
	return value, nil
}
