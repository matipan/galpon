package changes

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// squashCommits is the most base commits that a squash check compares.
const squashCommits = 200

// squashFiles is the most files of a branch that a squash check accepts. A
// larger branch is rarely squashed as is, and the check would be slow.
const squashFiles = 300

// squashCache keeps the answers of squash checks by branch tip and base tip:
// a squash merge of a branch never stops being one, and a new base tip gets a
// new check.
type squashCache struct {
	mu      sync.Mutex
	path    string
	answers map[string]bool
	changed bool
}

func loadSquashCache(stateDir string) *squashCache {
	cache := &squashCache{answers: map[string]bool{}}
	if stateDir == "" {
		return cache
	}
	cache.path = filepath.Join(stateDir, "cache", "changes", "squash.json")
	if data, err := os.ReadFile(cache.path); err == nil {
		_ = json.Unmarshal(data, &cache.answers)
	}
	return cache
}

func (c *squashCache) save() {
	if !c.changed || c.path == "" {
		return
	}
	// Old answers go first when the cache grows large.
	if len(c.answers) > 5000 {
		c.answers = map[string]bool{}
	}
	data, err := json.Marshal(c.answers)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	temporary := c.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(temporary, c.path)
}

// merged reports whether the base has a commit with the same patch as the
// whole branch: a squash merge of the branch. It compares Git patch IDs, so
// a base commit that also changed other lines does not match.
func (c *squashCache) merged(ctx context.Context, dir, base, mergeBase, tip string) bool {
	baseTip, ok := revParse(ctx, dir, base)
	if !ok {
		return false
	}
	key := tip + ":" + baseTip
	c.mu.Lock()
	answer, ok := c.answers[key]
	c.mu.Unlock()
	if ok {
		return answer
	}
	answer = squashMatch(ctx, dir, mergeBase, tip, baseTip)
	c.mu.Lock()
	c.answers[key] = answer
	c.changed = true
	c.mu.Unlock()
	return answer
}

func squashMatch(ctx context.Context, dir, mergeBase, tip, baseTip string) bool {
	files, err := git(ctx, dir, "diff", "--name-only", "-z", mergeBase, tip)
	if err != nil {
		return false
	}
	paths := strings.Split(strings.TrimRight(files, "\x00"), "\x00")
	if len(paths) == 0 || paths[0] == "" || len(paths) > squashFiles {
		return false
	}
	patch, err := gitBytes(ctx, dir, nil, "diff", "--no-color", "--no-ext-diff", mergeBase, tip)
	if err != nil || len(patch) == 0 {
		return false
	}
	want := patchIDs(ctx, dir, patch)
	if len(want) != 1 {
		return false
	}
	args := append([]string{"log", "-p", "--no-color", "--no-ext-diff", "--no-merges", "-n", strconv.Itoa(squashCommits), mergeBase + ".." + baseTip, "--"}, paths...)
	history, err := gitBytes(ctx, dir, nil, args...)
	if err != nil || len(history) == 0 {
		return false
	}
	for _, id := range patchIDs(ctx, dir, history) {
		if id == want[0] {
			return true
		}
	}
	return false
}

// patchIDs gives the stable patch ID of each patch in a text.
func patchIDs(ctx context.Context, dir string, patches []byte) []string {
	out, err := gitBytes(ctx, dir, patches, "patch-id", "--stable")
	if err != nil {
		return nil
	}
	var ids []string
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if fields := strings.Fields(string(line)); len(fields) >= 1 {
			ids = append(ids, fields[0])
		}
	}
	return ids
}
