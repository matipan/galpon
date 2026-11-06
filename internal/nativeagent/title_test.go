package nativeagent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

// renamingDriver records the names that the runtime gives its conversation,
// and the text of each submission.
type renamingDriver struct {
	fenceDriver
	names, inputs []string
}

func (d *renamingDriver) Rename(_ context.Context, title string) error {
	d.names = append(d.names, title)
	return nil
}

func (d *renamingDriver) Submit(_ context.Context, input nativeInput, _ string) (string, error) {
	d.inputs = append(d.inputs, input.Text)
	return "", nil
}

func TestRuntimeUsesTheNewTitleOfARenamedAgent(t *testing.T) {
	c, daemon, _ := controllerFixture(t)
	driver := &renamingDriver{}
	c.driver = driver
	c.agent.Title = "Original"
	step := func() {
		t.Helper()
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := c.step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	daemon.mu.Lock()
	daemon.title = "Billing lead"
	daemon.delivery = &app.CoordinationOperationDelivery{
		Operation: model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "inbound", RuntimeID: c.runtimeID, Attempt: 1, ProtocolGeneration: 3},
		Message:   &model.AgentMessage{ID: "message", Prompt: "Check the invoices", Act: "request", SenderTitle: "Planner"},
	}
	daemon.mu.Unlock()
	step()
	if !slices.Equal(driver.names, []string{"Billing lead"}) {
		t.Fatalf("conversation names = %v, want the new title", driver.names)
	}
	if len(driver.inputs) != 1 || !strings.Contains(driver.inputs[0], "You are the durable Galpon agent Billing lead.") {
		t.Fatalf("the delivery after the rename = %q, want the new title", driver.inputs)
	}

	// The runtime reads the title again only after titleEvery.
	daemon.mu.Lock()
	daemon.title = "Night shift"
	daemon.mu.Unlock()
	step()
	if reads := len(daemon.matching("/title")); reads != 1 {
		t.Fatalf("title reads = %d, want 1 before titleEvery passes", reads)
	}
	c.titleAt = time.Now().Add(-titleEvery)
	step()
	if !slices.Equal(driver.names, []string{"Billing lead", "Night shift"}) {
		t.Fatalf("conversation names = %v, want the second title after titleEvery", driver.names)
	}
}
