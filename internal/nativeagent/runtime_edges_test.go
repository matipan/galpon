package nativeagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type fenceDriver struct{ closed bool }

func (*fenceDriver) Session() (string, string)                                   { return "session", "" }
func (*fenceDriver) Submit(context.Context, nativeInput, string) (string, error) { return "", nil }
func (*fenceDriver) Withdraw(context.Context, string) (bool, error)              { return false, nil }
func (*fenceDriver) Terminal(context.Context) error                              { return nil }
func (d *fenceDriver) Close()                                                    { d.closed = true }

func incomingJob() *runtimeJob {
	return &runtimeJob{Operation: model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "inbound", RuntimeID: "current-runtime", Attempt: 3, ProtocolGeneration: 3}, Token: "delivery", Prompt: "Work " + deliveryMarker("delivery"), NativeID: "request-prompt", SentAt: time.Now().UnixMilli()}
}

func TestClaudeCompactionKeepsTheOriginalOperation(t *testing.T) {
	for _, parent := range []string{"request", "missing-parent"} {
		t.Run(parent, func(t *testing.T) {
			c, daemon, source := controllerFixture(t)
			job := incomingJob()
			c.jobs = []*runtimeJob{job}
			appendRecord(t, source, userRecord("request", "", job.Prompt), true)
			if err := c.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			appendRecord(t, source, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "boundary", "parentUuid": nil, "logicalParentUuid": parent}, true)
			appendRecord(t, source, map[string]any{"type": "user", "uuid": "summary", "parentUuid": "boundary", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "Summary of the work"}}, true)
			appendRecord(t, source, finalRecord("answer", "summary", "Completed work"), true)
			if err := c.event(nativeEvent{Kind: "finish", ID: "request-prompt", Final: "Completed work"}); err != nil {
				t.Fatal(err)
			}
			if err := c.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			settled := daemon.matching("/settle")
			if len(settled) != 1 || settled[0].Body["response"] != "Completed work" || len(c.jobs) != 0 || len(daemon.matching("/claim")) != 1 {
				t.Fatalf("operation did not settle across compaction: %#v, jobs=%d", settled, len(c.jobs))
			}
			if len(daemon.matching("/direct")) != 0 {
				t.Fatal("summary became direct work")
			}
		})
	}
}

func TestAmbiguousClaudeCompletionFailsWithinTheEvidenceDeadline(t *testing.T) {
	c, daemon, source := controllerFixture(t)
	job := incomingJob()
	c.jobs = []*runtimeJob{job}
	d := &fenceDriver{}
	c.driver = d
	appendRecord(t, source, userRecord("request", "", job.Prompt), true)
	appendRecord(t, source, userRecord("human", "request", "Another task"), true)
	appendRecord(t, source, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "boundary", "logicalParentUuid": "unknown"}, true)
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "summary", "parentUuid": "boundary", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "Ambiguous summary"}}, true)
	appendRecord(t, source, finalRecord("other-final", "summary", "Do not attribute this answer"), true)
	job.Terminal = true
	job.Final = "Do not attribute this answer"
	job.TerminalAt = time.Now().Add(-2 * time.Minute).UnixMilli()
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	settled := daemon.matching("/settle")
	if d.closed || len(settled) != 1 || settled[0].Body["response"] != "" || !strings.Contains(settled[0].Body["error"].(string), "completion evidence") {
		t.Fatalf("ambiguous completion accepted: %#v", settled)
	}
}

func TestCodexSteeringKeepsSavedDeliveryEvidence(t *testing.T) {
	c, daemon, source := controllerFixture(t)
	c.agent.Kind = "codex"
	c.transcript = newTranscript("codex", source, c.transcript.mirror)
	job := incomingJob()
	job.NativeID = "turn"
	job.SentAt = time.Now().Add(-2 * time.Minute).UnixMilli()
	c.jobs = []*runtimeJob{job}
	record := func(role, text string) {
		appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": role, "content": text, "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "turn"}}}, true)
	}
	record("user", job.Prompt)
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	record("user", "Apply the extra user constraint")
	if err := c.event(nativeEvent{Kind: "input", ID: "turn", Input: "Apply the extra user constraint"}); err != nil {
		t.Fatal(err)
	}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(daemon.matching("/settle")) != 0 || !job.Started {
		t.Fatal("steering discarded the delivered task")
	}
	appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "content": "Done", "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "turn"}}}, true)
	appendRecord(t, source, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "turn", "last_agent_message": "Done"}}, true)
	if err := c.event(nativeEvent{Kind: "finish", ID: "turn"}); err != nil {
		t.Fatal(err)
	}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := daemon.matching("/settle"); len(calls) != 1 || calls[0].Body["response"] != "Done" {
		t.Fatalf("steered delivery result = %#v", calls)
	}
}

func TestUnsavedClaudePromptDoesNotBlockTheNextClaim(t *testing.T) {
	c, daemon, _ := controllerFixture(t)
	if err := c.event(nativeEvent{Kind: "input", ID: "blocked-prompt", Input: "Blocked by a user hook"}); err != nil {
		t.Fatal(err)
	}
	c.jobs[0].SentAt = time.Now().Add(-2 * time.Minute).UnixMilli()
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(c.jobs) != 0 || c.active != "" || len(daemon.matching("/claim")) != 1 || len(daemon.matching("/settle")) != 0 {
		t.Fatal("an unsaved prompt retained operation ownership")
	}
}

func TestClaudeDeliveryTimeoutWithdrawsBufferedInput(t *testing.T) {
	c, daemon, _ := controllerFixture(t)
	c.channel = make(chan map[string]any, 1)
	d := &claudeDriver{options: launchOptions{Channel: c.channel}}
	c.driver = d
	job := incomingJob()
	c.jobs = []*runtimeJob{job}
	c.active = job.Token
	job.SentAt = time.Now().Add(-2 * time.Minute).UnixMilli()
	if _, err := d.Submit(t.Context(), nativeInput{Text: job.Prompt}, job.Token); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := d.Submit(ctx, nativeInput{Text: "Other"}, "other"); err == nil || ctx.Err() != nil {
		t.Fatal("full channel submission did not fail promptly")
	}
	if err := c.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(c.channel) != 0 || len(daemon.matching("/settle")) != 1 || len(c.jobs) != 0 {
		t.Fatal("timed-out input remained deliverable")
	}
	if _, err := d.Submit(t.Context(), nativeInput{Text: "Next"}, "next"); err != nil {
		t.Fatal(err)
	}
	// The HTTP boundary also rejects a stale entry that has no live job.
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/channel", nil)
	c.handler(t.Context()).ServeHTTP(response, request)
	if strings.TrimSpace(response.Body.String()) != "null" {
		t.Fatalf("stale channel payload escaped: %s", response.Body.String())
	}
	if err := c.event(nativeEvent{Kind: "input", ID: "late", Input: job.Prompt}); err == nil || len(c.jobs) != 0 {
		t.Fatal("expired delivery became direct work")
	}
}
