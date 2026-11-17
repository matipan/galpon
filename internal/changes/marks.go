package changes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// marks are the files that the person marked as viewed in the Changes view of
// one agent: by checkout and path, the content ID of the version that the
// person saw. A file whose content changed after that is not viewed any more,
// and the view can show what changed since.
type marks map[string]map[string]string

func marksPath(stateDir, agentID string) string {
	return filepath.Join(stateDir, "agents", agentID, "changes", "viewed.json")
}

func loadMarks(stateDir, agentID string) marks {
	out := marks{}
	if stateDir == "" || agentID == "" {
		return out
	}
	if data, err := os.ReadFile(marksPath(stateDir, agentID)); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// seen gives the content ID that the person saw for a path, or "".
func (m marks) seen(checkout, path string) string {
	return m[checkout][path]
}

// Mark is a change of the viewed marks of one file.
type Mark struct {
	Checkout string `json:"checkout"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Viewed   bool   `json:"viewed"`
}

// SetMarks saves viewed marks for an agent. A file lock keeps two views from
// losing each other's marks. When dir is a checkout, the content of a viewed
// file that only the worktree has goes into the Git objects of the checkout,
// so that the view can later show what changed since the person looked.
func SetMarks(ctx context.Context, stateDir, agentID, dir string, list []Mark) error {
	if stateDir == "" || agentID == "" {
		return errors.New("the viewed marks need the Galpon state folder and an agent")
	}
	path := marksPath(stateDir, agentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	current := loadMarks(stateDir, agentID)
	for _, mark := range list {
		if mark.Viewed {
			if current[mark.Checkout] == nil {
				current[mark.Checkout] = map[string]string{}
			}
			current[mark.Checkout][mark.Path] = mark.Content
			if dir != "" {
				keepContent(ctx, dir, mark.Path, mark.Content)
			}
			continue
		}
		delete(current[mark.Checkout], mark.Path)
		if len(current[mark.Checkout]) == 0 {
			delete(current, mark.Checkout)
		}
	}
	data, err := json.Marshal(current)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// keepContent writes the worktree version of a file into the Git objects of
// the checkout when it is the version with the content ID and Git does not
// have it yet. Git keeps such an object for about two weeks.
func keepContent(ctx context.Context, dir, path, content string) {
	if content == "" || strings.Contains(content, ":") {
		return
	}
	if _, err := git(ctx, dir, "cat-file", "-e", content); err == nil {
		return
	}
	id, err := git(ctx, dir, "hash-object", "--", path)
	if err != nil || strings.TrimSpace(id) != content {
		return
	}
	_, _ = git(ctx, dir, "hash-object", "-w", "--", path)
}
