package nativeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

func blockedDelivery(t *testing.T) (*runtimeController, *daemonFixture, string, *fenceDriver) {
	t.Helper()
	c, daemon, source := controllerFixture(t)
	d := &fenceDriver{}
	c.driver = d
	job := incomingJob()
	job.SentAt = time.Now().Add(-2 * time.Minute).UnixMilli()
	c.jobs = []*runtimeJob{job}
	c.active = job.Token
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.closed || job.Blocked == "" || len(daemon.matching("/settle")) != 0 || len(daemon.matching("/claim")) != 0 {
		t.Fatal("uncertain delivery closed the writer, settled, or admitted other work")
	}
	statuses := daemon.matching("/status")
	if len(statuses) == 0 || !strings.Contains(statuses[len(statuses)-1].Body["error"].(string), "paused incoming work") {
		t.Fatal("missing delivery blocker")
	}
	return c, daemon, source, d
}

func TestForegroundDeliveryWaitsForSavedInputWithoutClosingTheWriter(t *testing.T) {
	c, daemon, source, d := blockedDelivery(t)
	job := c.jobs[0]
	c.lastRenew = time.Time{}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(daemon.matching("/renew")) != 1 {
		t.Fatal("blocked delivery lost its operation lease")
	}
	appendRecord(t, source, userRecord("request", "", job.Prompt), true)
	if err := c.event(nativeEvent{Kind: "input", ID: "request-prompt", Input: job.Prompt}); err != nil {
		t.Fatal(err)
	}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if job.Blocked != "" || len(daemon.matching("/start")) != 1 || d.closed {
		t.Fatal("saved input did not resume the owned delivery")
	}
	appendRecord(t, source, finalRecord("answer", "request", "Done"), true)
	if err := c.event(nativeEvent{Kind: "finish", ID: "request-prompt", Final: "Done"}); err != nil {
		t.Fatal(err)
	}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := daemon.matching("/settle"); len(calls) != 1 || calls[0].Body["response"] != "Done" {
		t.Fatalf("late saved delivery did not settle: %#v", calls)
	}
}

func TestBlockedDeliveryFailsAfterWriterExitAndAcrossRecovery(t *testing.T) {
	c, daemon, _, d := blockedDelivery(t)
	saved, err := os.ReadFile(c.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var journal runtimeJournal
	if err := json.Unmarshal(saved, &journal); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := c.finishBlocked(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := daemon.matching("/settle"); len(calls) != 1 || !strings.Contains(calls[0].Body["error"].(string), "writer stopped") {
		t.Fatalf("exit did not fail the held delivery: %#v", calls)
	}
	if !c.hasExpiredDelivery(journal.Jobs[0].Prompt) {
		t.Fatal("expired delivery identity was lost")
	}
	replacement, newDaemon, _ := controllerFixture(t)
	replacement.recovery["operation"] = journal.Jobs[0]
	newDaemon.delivery = &app.CoordinationOperationDelivery{Operation: model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "inbound", RuntimeID: replacement.runtimeID, Attempt: 4, ProtocolGeneration: 3}}
	if err := replacement.claim(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := replacement.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := newDaemon.matching("/settle"); len(calls) != 1 || calls[0].Body["attempt"] != float64(4) || calls[0].Body["response"] != "" || calls[0].Body["error"] == "" {
		t.Fatalf("recovered blocked delivery was not fenced and failed: %#v", calls)
	}
}

func TestPastedMarkersAreHumanInputUnlessTheirDeliveryExpired(t *testing.T) {
	c, _, _ := controllerFixture(t)
	c.rememberExpired("expired")
	if err := c.event(nativeEvent{Kind: "input", ID: "human", Input: "Please repeat " + deliveryMarker("another-runtime")}); err != nil {
		t.Fatal(err)
	}
	if len(c.jobs) != 1 || c.jobs[0].Token != "" {
		t.Fatal("pasted marker was not admitted as human input")
	}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var journal runtimeJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	c.expiredTokens = journal.ExpiredTokens
	response := httptest.NewRecorder()
	c.handler(t.Context()).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt_id":"late","prompt":"Work [Galpon delivery: expired]"}`)))
	var decision map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decision); err != nil {
		t.Fatal(err)
	}
	if decision["decision"] != "block" || len(c.jobs) != 1 {
		t.Fatalf("expired delivery was admitted by the hook: %s", response.Body.String())
	}
}

func TestCompactionUsesCurrentInputAfterLocalCommandsAndFailedTurns(t *testing.T) {
	c, daemon, source := controllerFixture(t)
	appendRecord(t, source, userRecord("old", "", "Old task"), true)
	appendRecord(t, source, map[string]any{"type": "assistant", "uuid": "error", "parentUuid": "old", "isApiErrorMessage": true, "message": map[string]any{"role": "assistant", "content": "API Error", "stop_reason": "stop_sequence"}}, true)
	for i, text := range []string{"/compact", "<command-name>/compact</command-name>", "<local-command-stdout>Compacted</local-command-stdout>"} {
		appendRecord(t, source, userRecord(string(rune('a'+i)), "error", text), true)
	}
	if err := c.transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	if c.transcript.unfinishedClaudeTurn() != "" {
		t.Fatal("idle fallback revived an old failed or local-command turn")
	}
	for _, event := range c.transcript.events {
		if event.Kind == "user_message" && event.Content != "Old task" {
			t.Fatalf("local command was mirrored as user input: %#v", event)
		}
	}
	appendRecord(t, source, userRecord("interrupted", "error", "An older task without terminal evidence"), true)
	job := incomingJob()
	c.jobs = []*runtimeJob{job}
	appendRecord(t, source, userRecord("request", "interrupted", job.Prompt), true)
	appendRecord(t, source, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "boundary", "logicalParentUuid": "missing"}, true)
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "summary", "parentUuid": "boundary", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "Summary"}}, true)
	appendRecord(t, source, finalRecord("answer", "summary", "Completed"), true)
	if err := c.event(nativeEvent{Kind: "finish", ID: "request-prompt", Final: "Completed"}); err != nil {
		t.Fatal(err)
	}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := daemon.matching("/settle"); len(calls) != 1 || calls[0].Body["response"] != "Completed" {
		t.Fatalf("historical local commands broke current compaction: %#v", calls)
	}
}
