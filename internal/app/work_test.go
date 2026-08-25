package app

import (
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestValidateWorkProgressAcceptsBoundedFacts(t *testing.T) {
	value, err := ValidateWorkProgress(model.WorkProgressEvent{
		Version: 1, EventID: "phase-1", Phase: "blocked", Summary: "Waiting for a product choice", Blocker: "A product choice is required",
		Milestones: []model.WorkMilestone{{Label: "Schema", State: "completed"}, {Label: "UI", State: "blocked"}},
		Counts:     []model.WorkCount{{Label: "tests", Completed: 4, Total: 9}},
	})
	if err != nil || value.Phase != "blocked" || len(value.Counts) != 1 {
		t.Fatalf("validated progress = %#v, %v", value, err)
	}
}

func TestReportWorkProgressRejectsMissingActiveRequest(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	application := &App{Store: st}
	_, _, err = application.ReportWorkProgress(t.Context(), "agent", "runtime", "missing-message", 1, model.WorkProgressEvent{
		Version: 1, EventID: "phase-1", Phase: "working", Summary: "A safe checkpoint",
	})
	if err == nil || !IsInvalidRequest(err) || err.Error() != "report_progress requires an active delegated request delivery" {
		t.Fatalf("missing request error = %v", err)
	}
}

func TestValidateWorkProgressRejectsUnsafeFieldsAndLimits(t *testing.T) {
	base := model.WorkProgressEvent{Version: 1, EventID: "event", Phase: "working", Summary: "Safe checkpoint"}
	tests := []struct {
		name   string
		mutate func(*model.WorkProgressEvent)
	}{
		{name: "version", mutate: func(value *model.WorkProgressEvent) { value.Version = 2 }},
		{name: "phase", mutate: func(value *model.WorkProgressEvent) { value.Phase = "thinking" }},
		{name: "newline", mutate: func(value *model.WorkProgressEvent) { value.Summary = "one\ntwo" }},
		{name: "path", mutate: func(value *model.WorkProgressEvent) { value.Summary = "editing internal/store/work.go" }},
		{name: "secret", mutate: func(value *model.WorkProgressEvent) { value.Summary = "token=secret-value" }},
		{name: "spaced api key", mutate: func(value *model.WorkProgressEvent) { value.Summary = "API key=abcd1234efgh5678" }},
		{name: "password phrase", mutate: func(value *model.WorkProgressEvent) { value.Summary = "password is hunter2" }},
		{name: "aws key", mutate: func(value *model.WorkProgressEvent) { value.Summary = "AKIAIOSFODNN7EXAMPLE" }},
		{name: "github classic", mutate: func(value *model.WorkProgressEvent) { value.Summary = "ghp_123456789012345678901234567890123456" }},
		{name: "github fine grained", mutate: func(value *model.WorkProgressEvent) { value.Summary = "github_pat_123456789012345678901234567890" }},
		{name: "jwt", mutate: func(value *model.WorkProgressEvent) { value.Summary = "abcdefghij.abcdefghij.abcdefghij" }},
		{name: "pem", mutate: func(value *model.WorkProgressEvent) { value.Summary = "-----BEGIN PRIVATE KEY-----" }},
		{name: "opaque", mutate: func(value *model.WorkProgressEvent) { value.Summary = "12345678901234567890123456789012" }},
		{name: "percentage", mutate: func(value *model.WorkProgressEvent) { value.Summary = "work is 50% complete" }},
		{name: "eta", mutate: func(value *model.WorkProgressEvent) { value.Summary = "ETA is tomorrow" }},
		{name: "reasoning", mutate: func(value *model.WorkProgressEvent) { value.Summary = "private reasoning follows" }},
		{name: "bidi", mutate: func(value *model.WorkProgressEvent) { value.Summary = "safe\u202ereported" }},
		{name: "unexpected blocker", mutate: func(value *model.WorkProgressEvent) { value.Blocker = "Blocked" }},
		{name: "missing blocker", mutate: func(value *model.WorkProgressEvent) { value.Phase = "blocked" }},
		{name: "count", mutate: func(value *model.WorkProgressEvent) {
			value.Counts = []model.WorkCount{{Label: "tests", Completed: 2, Total: 1}}
		}},
		{name: "long", mutate: func(value *model.WorkProgressEvent) { value.Summary = strings.Repeat("x", model.WorkSummaryLimit+1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if _, err := ValidateWorkProgress(value); err == nil {
				t.Fatalf("unsafe progress was accepted: %#v", value)
			}
		})
	}
}
