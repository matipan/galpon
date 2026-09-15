package piagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNeovimReviewBridge(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	fixture, err := filepath.Abs(filepath.Join("testdata", "neovim-bridge-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "result.json")
	command := exec.Command(pi, "--list-models", "--extension", fixture)
	command.Env = append(os.Environ(),
		"PI_CODING_AGENT_DIR="+t.TempDir(),
		"PI_TELEMETRY=0",
		"GALPON_NEOVIM_BRIDGE_TEST_RESULT="+resultPath,
	)
	output, commandErr := command.CombinedOutput()
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("native Review bridge did not write a result: %v\n%v\n%s", err, commandErr, output)
	}
	var result workDockHarnessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if commandErr != nil || !result.OK {
		t.Fatalf("native Review bridge failed: %v; %s\n%s", commandErr, result.Error, output)
	}
}
