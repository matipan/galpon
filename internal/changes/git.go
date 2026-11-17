package changes

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// emptyTree is the ID of the tree without files in a SHA-1 repository.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// readOnly are the options of every Git command of the view. With
// GIT_OPTIONAL_LOCKS=0 they keep a read from writing under .git while an
// agent works in the checkout: no index refresh, no index lock and no file
// monitor cookie.
var readOnly = []string{"-c", "core.fsmonitor=false", "-c", "diff.autoRefreshIndex=false"}

// git runs Git in a checkout. GIT_OPTIONAL_LOCKS=0 and readOnly keep reads
// from writing, and GIT_TERMINAL_PROMPT=0 keeps a command from waiting for a
// password.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitBytes(ctx, dir, nil, args...)
	return string(out), err
}

func gitBytes(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append(append([]string(nil), readOnly...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func revParse(ctx context.Context, dir, rev string) (string, bool) {
	out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func mergeBase(ctx context.Context, dir, a, b string) (string, bool) {
	out, err := git(ctx, dir, "merge-base", a, b)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func isAncestor(ctx context.Context, dir, a, b string) bool {
	_, err := git(ctx, dir, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func countRange(ctx context.Context, dir, rng string) int {
	out, err := git(ctx, dir, "rev-list", "--count", rng)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// blobReader reads objects with one `git cat-file --batch` process.
type blobReader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func newBlobReader(ctx context.Context, dir string) (*blobReader, error) {
	cmd := exec.CommandContext(ctx, "git", append(append([]string(nil), readOnly...), "cat-file", "--batch")...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &blobReader{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<16)}, nil
}

// read gives the content of an object, or false when the repository does not
// have it. A blob larger than limit gives its size and no content.
func (r *blobReader) read(id string, limit int64) ([]byte, int64, bool, error) {
	if _, err := fmt.Fprintln(r.stdin, id); err != nil {
		return nil, 0, false, err
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return nil, 0, false, err
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[1] == "missing" {
		return nil, 0, false, nil
	}
	if len(fields) != 3 {
		return nil, 0, false, fmt.Errorf("git cat-file: unexpected header %q", header)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return nil, 0, false, err
	}
	data := make([]byte, size+1)
	if _, err := io.ReadFull(r.stdout, data); err != nil {
		return nil, 0, false, err
	}
	if size > limit {
		return nil, size, true, nil
	}
	return data[:size], size, true, nil
}

func (r *blobReader) close() {
	_ = r.stdin.Close()
	_ = r.cmd.Wait()
}
