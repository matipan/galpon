package piagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReviewDataHelpers(t *testing.T) {
	runReviewFixture(t, "review-data-test.ts", "GALPON_REVIEW_DATA_TEST_RESULT")
}

func TestReviewCommand(t *testing.T) {
	assets, err := Materialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runReviewFixture(t, "review-command-test.ts", "GALPON_REVIEW_COMMAND_TEST_RESULT",
		"GALPON_PI_EXTENSION="+assets.Extension,
		"GALPON_SOCKET=", "GALPON_AGENT_ID=", "GALPON_RUNTIME_ID=",
	)
}

func runReviewFixture(t *testing.T, name, resultEnv string, env ...string) {
	t.Helper()
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	fixture, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "result.json")
	command := exec.Command(pi, "--list-models", "--extension", fixture)
	command.Env = append(os.Environ(),
		"PI_CODING_AGENT_DIR="+t.TempDir(), "PI_TELEMETRY=0", resultEnv+"="+resultPath,
	)
	command.Env = append(command.Env, env...)
	output, commandErr := command.CombinedOutput()
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("Review fixture %s did not write a result: %v\n%v\n%s", name, err, commandErr, output)
	}
	var result workDockHarnessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if commandErr != nil || !result.OK {
		t.Fatalf("Review fixture %s failed: %v; %s\n%s", name, commandErr, result.Error, output)
	}
}
