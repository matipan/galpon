package nativeagent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Claude chooses its project directory from its config and working directory.
// Locate the session by its identity without changing the user's config root.
func findClaudeSession(configDirectory, id, preferred string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("invalid Claude session ID: %w", err)
	}
	// The agent-owned path takes precedence over snapshots staged for forks.
	if info, err := os.Stat(preferred); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return preferred, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	projects := filepath.Join(configDirectory, "projects")
	entries, err := os.ReadDir(projects)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	found := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(projects, entry.Name(), id+".jsonl")
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("multiple Claude conversation files have session ID %s; cannot select a history safely", id)
		}
		found = path
	}
	return found, nil
}

func (d *claudeDriver) findTranscript() (string, error) {
	return findClaudeSession(d.configDirectory, d.id, d.path)
}
