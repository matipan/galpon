package neovimreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFactoryReviewEnvironmentIsIsolated(t *testing.T) {
	t.Setenv("SECRET_FACTORY_TEST", "do-not-copy")
	dir := t.TempDir()
	values := reviewEnvironment(dir, "/runtime", "run-id", "/input")
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, "SECRET_FACTORY_TEST") || strings.Contains(joined, "do-not-copy") {
		t.Fatal("environment copied an unrelated value")
	}
	for _, required := range []string{"HOME=" + dir, "GALPON_REVIEW_NVIM_RUN_ID=run-id", "GALPON_REVIEW_NVIM_RUNTIME=/runtime", "GALPON_REVIEW_NVIM_INPUT=/input"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %s", required, joined)
		}
	}
	_ = os.Getenv("PATH")
}
func TestReadFactoryArtifactCompilesPreparedAnnotations(t *testing.T) {
	directory := t.TempDir()
	text := "# Plan\n\nChange the server.\nAdd tests."
	input := map[string]any{"version": 1, "runId": "run", "sourceEntryId": "feature:plan", "sourceHash": digestText(text), "sourceTextHash": digestText(text), "text": text}
	snapshot := map[string]any{"version": 1, "runId": "run", "sourceEntryId": "feature:plan", "sourceHash": digestText(text), "sourceTextHash": digestText(text), "status": "prepare", "items": []map[string]any{{"start": 2, "end": 3, "quote": "Change the server.\nAdd tests.", "comment": "Keep the API compatible."}}}
	writeFactoryReviewJSON(t, filepath.Join(directory, "input.json"), input)
	writeFactoryReviewJSON(t, filepath.Join(directory, "snapshot.json"), snapshot)
	review, err := ReadFactoryArtifact(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !review.Prepared || len(review.Annotations) != 1 {
		t.Fatalf("review = %#v", review)
	}
	prompt := review.Prompt()
	for _, expected := range []string{"Plan line 3-4", "> Change the server.", "Feedback: Keep the API compatible.", "complete revised plan"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q: %s", expected, prompt)
		}
	}
}

func TestReadFactoryArtifactTreatsCancelAsNoFeedback(t *testing.T) {
	directory := t.TempDir()
	text := "Plan"
	input := map[string]any{"version": 1, "runId": "run", "sourceEntryId": "feature:plan", "sourceHash": digestText(text), "sourceTextHash": digestText(text), "text": text}
	snapshot := map[string]any{"version": 1, "runId": "run", "sourceEntryId": "feature:plan", "sourceHash": digestText(text), "sourceTextHash": digestText(text), "status": "cancel", "items": []any{}}
	writeFactoryReviewJSON(t, filepath.Join(directory, "input.json"), input)
	writeFactoryReviewJSON(t, filepath.Join(directory, "snapshot.json"), snapshot)
	review, err := ReadFactoryArtifact(directory)
	if err != nil {
		t.Fatal(err)
	}
	if review.Prepared || review.Prompt() != "" {
		t.Fatalf("cancelled review = %#v", review)
	}
}

func writeFactoryReviewJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDigestTextIsStable(t *testing.T) {
	first := digestText("artifact")
	if first == "" {
		t.Fatal("digest is empty")
	}
	if first == digestText("other") {
		t.Fatal("digest did not bind content")
	}
}
