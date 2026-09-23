package neovimreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const factoryReviewMaxBytes = 1 << 20

// FactoryArtifactCommand prepares an isolated native Review run for a Factory
// artifact. It reuses the same pinned runtime and Neovim interface as /review,
// but it does not attach the review to a Pi conversation.
func FactoryArtifactCommand(ctx context.Context, stateDir, reviewRoot, artifactID, text string, palette map[string]string) (*exec.Cmd, string, error) {
	info, err := Inspect(ctx, stateDir)
	if err != nil {
		return nil, "", err
	}
	runID := uuid.NewString()
	directory := filepath.Join(reviewRoot, digestText(artifactID), runID)
	for _, name := range []string{"home", "config", "data", "state", "cache", "runtime", "tmp", "work"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0o700); err != nil {
			return nil, "", err
		}
	}
	input := map[string]any{"version": 1, "runId": runID, "sourceEntryId": artifactID, "sourceHash": digestText(text), "sourceTextHash": digestText(text), "text": text, "items": []any{}, "graphemes": []any{}, "palette": palette, "limits": map[string]int{"items": 50, "selectionBytes": 8192, "draftBytes": 49152}}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, "", err
	}
	inputPath := filepath.Join(directory, "input.json")
	if err = os.WriteFile(inputPath, data, 0o600); err != nil {
		return nil, "", err
	}
	entry := filepath.Join(stateDir, "runtime", "pi", "neovim-review.lua")
	if stat, statErr := os.Stat(entry); statErr != nil || !stat.Mode().IsRegular() {
		return nil, "", fmt.Errorf("native Review UI is not installed")
	}
	cmd := exec.CommandContext(ctx, info.Neovim, "-n", "--noplugin", "-i", "NONE", "-u", entry)
	cmd.Dir = filepath.Join(directory, "work")
	cmd.Env = reviewEnvironment(directory, info.Runtime, runID, inputPath)
	return cmd, directory, nil
}

type FactoryArtifactAnnotation struct {
	Start, End int
	Quote      string
	Comment    string
}

type FactoryArtifactReview struct {
	Prepared    bool
	Annotations []FactoryArtifactAnnotation
}

// ReadFactoryArtifact imports the bounded snapshot that the shared native
// Review UI wrote for a Factory plan. A cancelled review is not an error.
func ReadFactoryArtifact(directory string) (FactoryArtifactReview, error) {
	inputData, err := readFactoryReviewFile(filepath.Join(directory, "input.json"), factoryReviewMaxBytes)
	if err != nil {
		return FactoryArtifactReview{}, fmt.Errorf("read Factory Review input: %w", err)
	}
	var input struct {
		Version        int    `json:"version"`
		RunID          string `json:"runId"`
		SourceEntryID  string `json:"sourceEntryId"`
		SourceHash     string `json:"sourceHash"`
		SourceTextHash string `json:"sourceTextHash"`
		Text           string `json:"text"`
	}
	if err := json.Unmarshal(inputData, &input); err != nil {
		return FactoryArtifactReview{}, fmt.Errorf("decode Factory Review input: %w", err)
	}
	if input.Version != 1 || input.RunID == "" || input.SourceEntryID == "" || input.SourceHash == "" || input.SourceTextHash != digestText(input.Text) {
		return FactoryArtifactReview{}, fmt.Errorf("native Review input is invalid")
	}
	snapshotData, err := readFactoryReviewFile(filepath.Join(directory, "snapshot.json"), factoryReviewMaxBytes)
	if err != nil {
		return FactoryArtifactReview{}, fmt.Errorf("read Factory Review result: %w", err)
	}
	var snapshot struct {
		Version        int    `json:"version"`
		RunID          string `json:"runId"`
		SourceEntryID  string `json:"sourceEntryId"`
		SourceHash     string `json:"sourceHash"`
		SourceTextHash string `json:"sourceTextHash"`
		Status         string `json:"status"`
		Items          []struct {
			Start   int    `json:"start"`
			End     int    `json:"end"`
			Quote   string `json:"quote"`
			Comment string `json:"comment"`
		} `json:"items"`
	}
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		return FactoryArtifactReview{}, fmt.Errorf("decode Factory Review result: %w", err)
	}
	if snapshot.Version != 1 || snapshot.RunID != input.RunID || snapshot.SourceEntryID != input.SourceEntryID || snapshot.SourceHash != input.SourceHash || snapshot.SourceTextHash != input.SourceTextHash {
		return FactoryArtifactReview{}, fmt.Errorf("native Review result does not match its plan")
	}
	if snapshot.Status == "cancel" || snapshot.Status == "open" {
		return FactoryArtifactReview{}, nil
	}
	if snapshot.Status != "prepare" || len(snapshot.Items) == 0 || len(snapshot.Items) > 50 {
		return FactoryArtifactReview{}, fmt.Errorf("native Review result is invalid")
	}
	lines := strings.Split(input.Text, "\n")
	review := FactoryArtifactReview{Prepared: true, Annotations: make([]FactoryArtifactAnnotation, 0, len(snapshot.Items))}
	compiledBytes := 0
	for _, item := range snapshot.Items {
		item.Quote = strings.TrimSpace(item.Quote)
		item.Comment = strings.TrimSpace(item.Comment)
		if item.Start < 0 || item.End < item.Start || item.End >= len(lines) || item.Quote == "" || item.Comment == "" || len(item.Quote) > 8192 {
			return FactoryArtifactReview{}, fmt.Errorf("native Review contains an invalid annotation")
		}
		compiledBytes += len(item.Quote) + len(item.Comment)
		if compiledBytes > 48<<10 {
			return FactoryArtifactReview{}, fmt.Errorf("native Review annotations exceed 48 KiB")
		}
		review.Annotations = append(review.Annotations, FactoryArtifactAnnotation{Start: item.Start, End: item.End, Quote: item.Quote, Comment: item.Comment})
	}
	return review, nil
}

func (r FactoryArtifactReview) Prompt() string {
	if !r.Prepared || len(r.Annotations) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("The user annotated the current implementation plan in native Review. Revise the plan to address every annotation. Do not implement the plan yet. Return the complete revised plan for another review.\n\nAnnotations:\n")
	for index, item := range r.Annotations {
		line := fmt.Sprintf("%d. Plan line %d", index+1, item.Start+1)
		if item.End > item.Start {
			line += fmt.Sprintf("-%d", item.End+1)
		}
		out.WriteString("\n" + line + "\n")
		for _, quoteLine := range strings.Split(item.Quote, "\n") {
			out.WriteString("> " + quoteLine + "\n")
		}
		out.WriteString("Feedback: " + item.Comment + "\n")
	}
	return out.String()
}

func readFactoryReviewFile(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open review file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("review file is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("review file exceeds its size limit")
	}
	return data, nil
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func reviewEnvironment(directory, runtime, runID, input string) []string {
	allowed := map[string]bool{"PATH": true, "TERM": true, "COLORTERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true, "LANG": true, "TZ": true}
	out := []string{}
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			out = append(out, value)
		}
	}
	values := map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_CONFIG_DIRS": "config", "XDG_DATA_HOME": "data", "XDG_DATA_DIRS": "data", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "runtime", "TMPDIR": "tmp"}
	for key, name := range values {
		out = append(out, key+"="+filepath.Join(directory, name))
	}
	return append(out, "NVIM_APPNAME=galpon-review", "NVIM_LOG_FILE="+filepath.Join(directory, "nvim.log"), "GALPON_REVIEW_NVIM_RUN_ID="+runID, "GALPON_REVIEW_NVIM_RUNTIME="+runtime, "GALPON_REVIEW_NVIM_INPUT="+input, "GALPON_REVIEW_NVIM_OUTPUT="+filepath.Join(directory, "snapshot.json"))
}
