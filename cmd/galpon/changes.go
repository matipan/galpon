package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/changes"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/tui"
)

// herdrAppChanges serves the Changes view of the Herdr app. Each verb prints
// one JSON object; an error is an object with "error".
//
//	changes view <agent> [--checkout K --branch B --set S --follow]
//	                                                the work, the change set it resolves to,
//	                                                its diff and the fingerprints, in one call
//	changes work <agent>                            the checkouts, branches and commits
//	changes diff <agent> --checkout K --set S       the diff of a change set
//	changes lines <agent> --checkout K --set S --file F --from N --old N --count N
//	changes mark <agent> <marks JSON>               save viewed marks
//	changes fingerprint <folder>...                 a value that changes with the worktree
func herdrAppChanges(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return modError(errors.New("herdr-app changes needs view, work, diff, lines, mark or fingerprint"))
	}
	if args[0] == "fingerprint" {
		return printModResult(map[string]any{"fingerprints": fingerprints(args[1:])})
	}
	if len(args) < 2 {
		return modError(fmt.Errorf("herdr-app changes %s needs an agent ID", args[0]))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src, err := changesSource(ctx, cfg, args[1])
	if err != nil {
		return modError(err)
	}
	switch args[0] {
	case "view":
		ask, onlyNew, err := changesPick(args[2:])
		if err != nil {
			return modError(err)
		}
		return printModResult(changesView(ctx, src, ask, onlyNew))
	case "work":
		return printModResult(changesWork(ctx, src))
	case "diff":
		req, rest, err := changesRequest(args[2:])
		if err != nil {
			return modError(err)
		}
		if len(rest) > 0 {
			return modError(fmt.Errorf("unknown herdr-app changes diff arguments %q", rest))
		}
		out, err := changes.LoadDiff(ctx, src, req)
		if err != nil {
			return modError(err)
		}
		return printModResult(out)
	case "lines":
		req, rest, err := changesRequest(args[2:])
		if err != nil {
			return modError(err)
		}
		set := flag.NewFlagSet("lines", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		file := set.String("file", "", "")
		from := set.Int("from", 1, "")
		old := set.Int("old", 1, "")
		count := set.Int("count", 20, "")
		if err := set.Parse(rest); err != nil {
			return modError(err)
		}
		rows, err := changes.LoadLines(ctx, src, req, *file, *from, *old, *count)
		if err != nil {
			return modError(err)
		}
		return printModResult(map[string]any{"rows": rows})
	case "mark":
		if len(args) < 3 {
			return modError(errors.New("herdr-app changes mark needs the marks as JSON"))
		}
		var marks []changes.Mark
		if err := json.Unmarshal([]byte(args[2]), &marks); err != nil {
			return modError(fmt.Errorf("the marks have bad JSON: %w", err))
		}
		dir := ""
		if len(marks) > 0 {
			work := changes.LoadWork(ctx, src)
			for _, repository := range work.Repositories {
				for _, checkout := range repository.Checkouts {
					if checkout.Key == marks[0].Checkout {
						dir = checkout.Path
					}
				}
			}
		}
		if err := changes.SetMarks(ctx, cfg.StateDir, src.Agent.ID, dir, marks); err != nil {
			return modError(err)
		}
		return printModResult(map[string]any{"ok": true})
	default:
		return modError(fmt.Errorf("unknown herdr-app changes command %q", args[0]))
	}
}

// viewResult is what the view reads when it opens and when the worktrees
// change: the work, the change set that the view asked for as Choose
// resolves it, its diff and the fingerprints of the checkouts.
type viewResult struct {
	Work         changes.Work      `json:"work"`
	Selection    *changes.Pick     `json:"selection"`
	Followed     bool              `json:"followed,omitempty"`
	Diff         *changes.Diff     `json:"diff,omitempty"`
	DiffError    string            `json:"diffError,omitempty"`
	Fingerprints map[string]string `json:"fingerprints"`
}

// changesView reads the work, resolves the change set and reads its diff,
// so that the view needs one process. The fingerprints come before the
// diff: a change during the read then shows in the next poll, and is never
// lost.
func changesView(ctx context.Context, src changes.Source, ask *changes.Pick, onlyNew bool) viewResult {
	out := viewResult{Work: changesWork(ctx, src)}
	var folders []string
	for _, repository := range out.Work.Repositories {
		for _, checkout := range repository.Checkouts {
			if !checkout.Missing {
				folders = append(folders, checkout.Path)
			}
		}
	}
	out.Fingerprints = fingerprints(folders)
	out.Selection, out.Followed = changes.Choose(out.Work, ask)
	if out.Selection != nil {
		req := changes.Request{Checkout: out.Selection.Checkout, Set: out.Selection.Set, Base: changes.BaseFor(out.Work, *out.Selection), OnlyNew: onlyNew}
		if diff, err := changes.LoadDiff(ctx, src, req); err != nil {
			out.DiffError = err.Error()
		} else {
			out.Diff = &diff
		}
	}
	return out
}

// changesWork reads the work of the agent with the attention of its pull
// requests, as the pull request list of Galpon shows it.
func changesWork(ctx context.Context, src changes.Source) changes.Work {
	work := changes.LoadWork(ctx, src)
	for r := range work.Repositories {
		for c := range work.Repositories[r].Checkouts {
			for b := range work.Repositories[r].Checkouts[c].Branches {
				if pull := work.Repositories[r].Checkouts[c].Branches[b].PullRequest; pull != nil {
					pull.Attention, pull.Reason = tui.PullRequestAttention(pull.Source())
				}
			}
		}
	}
	return work
}

// changesPick reads the change set that the view asks for: no options ask
// for the default of the work.
func changesPick(args []string) (*changes.Pick, bool, error) {
	var pick changes.Pick
	onlyNew := false
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--checkout":
			pick.Checkout, err = value()
		case "--branch":
			pick.Branch, err = value()
		case "--set":
			pick.Set, err = value()
		case "--follow":
			pick.Follow = true
		case "--only-new":
			onlyNew = true
		default:
			err = fmt.Errorf("unknown herdr-app changes view argument %q", args[i])
		}
		if err != nil {
			return nil, false, err
		}
	}
	if pick.Checkout == "" || pick.Branch == "" || pick.Set == "" {
		return nil, onlyNew, nil
	}
	return &pick, onlyNew, nil
}

// changesRequest reads the options that name a change set. It returns the
// arguments that it does not know.
func changesRequest(args []string) (changes.Request, []string, error) {
	var req changes.Request
	var rest []string
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--checkout":
			req.Checkout, err = value()
		case "--set":
			req.Set, err = value()
		case "--base":
			req.Base, err = value()
		case "--only-new":
			req.OnlyNew = true
		case "--open":
			var file string
			file, err = value()
			req.Files = append(req.Files, file)
		default:
			rest = append(rest, args[i])
		}
		if err != nil {
			return req, nil, err
		}
	}
	if req.Set == "" {
		return req, nil, errors.New("the change set needs --set")
	}
	return req, rest, nil
}

// changesSource reads what the Changes view needs from the daemon: the
// agent, the dashboard and the pull requests.
func changesSource(ctx context.Context, cfg config.Config, agentID string) (changes.Source, error) {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return changes.Source{}, err
	}
	dashboard, err := client.DashboardWithHidden(ctx)
	if err != nil {
		return changes.Source{}, err
	}
	src := changes.Source{Dashboard: dashboard, StateDir: cfg.StateDir, Now: time.Now()}
	found := false
	for _, agent := range dashboard.Agents {
		if agent.ID == agentID {
			src.Agent, found = agent, true
			break
		}
	}
	if !found {
		return src, fmt.Errorf("agent %s is not in Galpon", agentID)
	}
	// The view still works without pull requests: their states are extra.
	if pulls, err := client.PullRequests(ctx); err == nil {
		src.PullRequests = pulls.PullRequests
	}
	return src, nil
}

// fingerprints gives a value for each folder that changes when its head, its
// index or its files change. The view polls it to know when to read the
// diff again.
func fingerprints(folders []string) map[string]string {
	out := map[string]string{}
	for _, folder := range folders {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
		cmd.Dir = folder
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
		status, err := cmd.Output()
		cancel()
		if err != nil {
			out[folder] = "error"
			continue
		}
		hash := sha256.New()
		hash.Write(status)
		// The status does not change when a changed file changes again, so
		// the times and sizes of the changed files count too.
		for _, entry := range strings.Split(string(status), "\x00") {
			name := statusPath(entry)
			if name == "" {
				continue
			}
			if info, err := os.Lstat(folder + string(os.PathSeparator) + name); err == nil {
				_, _ = fmt.Fprintf(hash, "%s %d %d\n", name, info.Size(), info.ModTime().UnixNano())
			}
		}
		out[folder] = hex.EncodeToString(hash.Sum(nil))[:16]
	}
	return out
}

// statusPath gives the path of an entry of `git status --porcelain=v2 -z`.
func statusPath(entry string) string {
	switch {
	case strings.HasPrefix(entry, "1 "):
		fields := strings.SplitN(entry, " ", 9)
		if len(fields) == 9 {
			return fields[8]
		}
	case strings.HasPrefix(entry, "2 "):
		fields := strings.SplitN(entry, " ", 10)
		if len(fields) == 10 {
			return fields[9]
		}
	case strings.HasPrefix(entry, "? "):
		return strings.TrimPrefix(entry, "? ")
	case strings.HasPrefix(entry, "u "):
		fields := strings.SplitN(entry, " ", 11)
		if len(fields) == 11 {
			return fields[10]
		}
	}
	return ""
}
