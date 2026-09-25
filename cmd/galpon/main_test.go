package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestDaemonEnvironmentKeepsConfigurationWithoutAgentIdentity(t *testing.T) {
	configuration := []string{
		"PATH=/usr/bin", "HOME=/home/test", "GALPON_STATE_DIR=/custom/state",
		"GALPON_PI_BIN=/opt/pi", "GALPON_PI_PROVIDER=test", "GALPON_PI_MODEL=test-model",
		"GALPON_HERDR_BIN=/opt/herdr", "PI_CODING_AGENT_DIR=/custom/pi", "TEST_PROVIDER_API_KEY=test-only",
		"GALPON_ASCII=1", "GALPON_UI_MOTION=off",
	}
	inherited := append([]string(nil), configuration...)
	inherited = append(inherited,
		"GALPON_SOCKET=/old/socket", "GALPON_RUNTIME_ID=old-runtime", "GALPON_AGENT_ID=old-agent",
		"GALPON_AGENT_TITLE=Old agent", "GALPON_AGENT_ROLE=reviewer", "GALPON_WORKSPACE_ID=old-workspace",
		"GALPON_WORKSPACE_TITLE=Old workspace", "GALPON_PROTOCOL_GENERATION=2", "GALPON_PI_EXTENSION=/old/galpon.ts",
		"GALPON_PLACEMENT=old placement", "GALPON_CONSOLE_ACTIVE=1", "GALPON_CHECKPOINT_PASSPHRASE=test-only",
		"GALPON_RUNTIME_ID=duplicate-old-runtime",
	)
	original := append([]string(nil), inherited...)
	if got := daemonEnvironment(inherited); !reflect.DeepEqual(got, configuration) {
		t.Fatalf("daemon environment = %v, want configuration only", got)
	}
	if !reflect.DeepEqual(inherited, original) {
		t.Fatal("daemon startup changed the caller's environment")
	}
}

func TestCommunicationUpgradeCommandIsTerminalOnlyAndValidatesArguments(t *testing.T) {
	cfg := config.Config{}
	for _, args := range [][]string{{}, {"status"}, {"upgrade", "extra"}, {"upgrade", "--idle-timeout", "0"}} {
		if err := communicationCommand(cfg, args); err == nil || !strings.Contains(err.Error(), "usage: galpon communication upgrade") {
			t.Fatalf("communicationCommand(%v) = %v", args, err)
		}
	}
}

func TestCommunicationRuntimeRecoveryCommandRequiresExactIdentities(t *testing.T) {
	cfg := config.Config{}
	for _, args := range [][]string{{"recover-runtime"}, {"recover-runtime", "--agent", "agent"}, {"recover-runtime", "--runtime", "runtime"}, {"recover-runtime", "--agent", "agent", "--runtime", "runtime", "extra"}} {
		if err := communicationCommand(cfg, args); err == nil || !strings.Contains(err.Error(), "usage: galpon communication recover-runtime") {
			t.Fatalf("communicationCommand(%v) = %v", args, err)
		}
	}
}

func TestOperationsTextAndJSONKeepObservedAndReportedFactsSeparate(t *testing.T) {
	now := time.Now().UnixMilli()
	projection := model.AgentOperations{
		Version: 1, Agent: model.OperationsAgent{ID: "worker", Title: "Worker", Status: "running"}, Workspace: model.OperationsWorkspace{ID: "workspace", Title: "Work"},
		Summary: model.AgentOperationsSummary{Current: 1, Received: 1, Delegated: 1, NeedsAttention: 1},
		Current: []model.WorkItem{{ID: "root", Title: "Worker", Direction: "received", Priority: "reported_blocker",
			Observation: model.WorkObservation{State: "started", Source: "observed", Lease: "stale", LeaseObservedAt: now - 4_000},
			Checkpoint:  &model.WorkCheckpoint{Phase: "blocked", Summary: "Waiting for a choice", Source: "reported"}}},
		Attention:  []model.WorkItem{{ID: "failure", Title: "Reviewer", Direction: "delegated", Observation: model.WorkObservation{State: "failed", Source: "observed"}}},
		Activity:   &model.OperationsActivityLane{Version: 1, Facts: []model.OperationsActivityFact{{Category: "tool: read", Status: "completed", Source: "observed", ObservedAt: now - 3_000}}},
		Truncation: model.AgentOperationsTruncation{Truncated: true},
	}
	var text bytes.Buffer
	if err := printOperationsText(&text, projection); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Operations · Worker · Work · more facts omitted", "1 current item", "Current work", "Attention", "received", "delegated", "started", "lease observed", "Observed activity", "reported: Waiting for a choice", "stale observation"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text output omitted %q:\n%s", want, text.String())
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":1`, `"source":"observed"`, `"source":"reported"`, `"truncated":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON output omitted %q: %s", want, encoded)
		}
	}
}

type failingOperationsWriter struct{}

func (failingOperationsWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestOperationsTextReturnsWriteError(t *testing.T) {
	err := printOperationsText(failingOperationsWriter{}, model.AgentOperations{})
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("write error = %v", err)
	}
}
