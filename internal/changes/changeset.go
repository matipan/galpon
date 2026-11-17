package changes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/matipan/galpon/internal/changes/diff"
	"github.com/matipan/galpon/internal/changes/syntax"
)

// Limits of a diff. Content above maxContent has no line diff, and a change
// set above maxRows changed lines shows its largest files folded.
const (
	maxContent   = 1 << 20
	maxLines     = 20_000
	maxRows      = 20_000
	maxUntracked = 20
	binaryProbe  = 8000
	worktreeSide = "worktree"
)

// Request names a change set of a checkout.
//
// Set is "branch:<name>" for the work of a branch after its base, with the
// uncommitted work when the checkout shows the branch; "detached" for the
// work of a detached checkout; "uncommitted"; "commit:<sha>"; or
// "range:<oldest>..<newest>". Base is the commit that a branch set starts
// from; without it the view finds the merge base with the base branch.
// OnlyNew keeps only the files that changed after the person looked, each
// compared with the version the person saw. Files gives the paths that get
// rows even when the view folds them by default.
type Request struct {
	Checkout string
	Set      string
	Base     string
	OnlyNew  bool
	Files    []string
}

// Diff is the diff of a change set.
type Diff struct {
	Checkout string   `json:"checkout"`
	Set      string   `json:"set"`
	Old      string   `json:"old"`
	New      string   `json:"new"`
	Totals   Totals   `json:"totals"`
	Files    []File   `json:"files"`
	Notices  []string `json:"notices,omitempty"`
}

// Totals count the files and lines of a diff. Generated files, vendored
// files and lockfiles count apart.
type Totals struct {
	Files            int `json:"files"`
	Added            int `json:"added"`
	Removed          int `json:"removed"`
	GeneratedAdded   int `json:"generatedAdded"`
	GeneratedRemoved int `json:"generatedRemoved"`
	Viewed           int `json:"viewed"`
}

// File is one file of a diff.
type File struct {
	Path     string              `json:"path"`
	OldPath  string              `json:"oldPath,omitempty"`
	Status   string              `json:"status"`
	Language string              `json:"language,omitempty"`
	Binary   bool                `json:"binary,omitempty"`
	Large    bool                `json:"large,omitempty"`
	OldSize  int64               `json:"oldSize"`
	NewSize  int64               `json:"newSize"`
	Added    int                 `json:"added"`
	Removed  int                 `json:"removed"`
	Changes  int                 `json:"changes"`
	Noise    int                 `json:"noise"`
	Count    int                 `json:"count,omitempty"`
	Classes  []string            `json:"classes,omitempty"`
	Entities []diff.EntityChange `json:"entities,omitempty"`
	Content  string              `json:"content,omitempty"`
	Viewed   bool                `json:"viewed,omitempty"`
	Changed  bool                `json:"changed,omitempty"`
	Deferred bool                `json:"deferred,omitempty"`
	Rows     []diff.Row          `json:"rows,omitempty"`
	Notices  []string            `json:"notices,omitempty"`
}

// sides are the two states that a change set compares: Git revisions, or the
// worktree for the new side.
type sides struct {
	dir      string
	old      string
	new      string
	worktree bool
	// head is the commit that the checkout shows, for the uncommitted marks
	// of a branch total.
	head string
}

// entry is a changed file of a change set before its content is read.
type entry struct {
	path    string
	oldPath string
	status  string
	oldID   string
	newID   string
	oldMode string
	newMode string
	count   int
}

// LoadDiff makes the diff of a change set of the agent of src.
func LoadDiff(ctx context.Context, src Source, req Request) (Diff, error) {
	out := Diff{Checkout: req.Checkout, Set: req.Set, Files: []File{}}
	checkout, err := findCheckout(ctx, src, req.Checkout)
	if err != nil {
		return out, err
	}
	s, err := resolveSides(ctx, checkout, req)
	if err != nil {
		return out, err
	}
	out.Old, out.New = s.old, s.new
	if s.worktree {
		out.New = worktreeSide
	}
	entries, notices, err := changedFiles(ctx, s)
	if err != nil {
		return out, err
	}
	out.Notices = append(out.Notices, notices...)
	files, err := buildFiles(ctx, src, checkout, s, req, entries)
	if err != nil {
		return out, err
	}
	out.Files = files
	for _, file := range files {
		out.Totals.Files++
		if file.Viewed {
			out.Totals.Viewed++
		}
		if hasClass(file, "generated") || hasClass(file, "vendored") || hasClass(file, "lockfile") {
			out.Totals.GeneratedAdded += max(0, file.Added)
			out.Totals.GeneratedRemoved += max(0, file.Removed)
			continue
		}
		out.Totals.Added += max(0, file.Added)
		out.Totals.Removed += max(0, file.Removed)
	}
	return out, nil
}

// LoadLines gives the rows of unchanged lines of a file of a change set, from
// line from of its new version and line oldFrom of its old version, both from
// 1. The view asks for them when the person opens a fold.
func LoadLines(ctx context.Context, src Source, req Request, file string, from, oldFrom, count int) ([]diff.Row, error) {
	checkout, err := findCheckout(ctx, src, req.Checkout)
	if err != nil {
		return nil, err
	}
	s, err := resolveSides(ctx, checkout, req)
	if err != nil {
		return nil, err
	}
	var content []byte
	if s.worktree {
		content, err = readWorktree(s.dir, file)
	} else {
		var text string
		text, err = git(ctx, s.dir, "show", s.new+":"+file)
		content = []byte(text)
	}
	if err != nil {
		return nil, err
	}
	return diff.ContextRows(content, syntax.Detect(file, content), from, oldFrom, count), nil
}

func findCheckout(ctx context.Context, src Source, key string) (Checkout, error) {
	for _, checkout := range placementCheckouts(ctx, src) {
		if checkout.Key == key || (key == "" && checkout.Primary) {
			return checkout, nil
		}
	}
	return Checkout{}, fmt.Errorf("checkout %q is not in the placement of %s", key, src.Agent.Title)
}

// resolveSides turns a change set into its two states.
func resolveSides(ctx context.Context, checkout Checkout, req Request) (sides, error) {
	dir := checkout.Path
	s := sides{dir: dir}
	head, hasHead := revParse(ctx, dir, "HEAD")
	s.head = head
	current := ""
	if out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		current = strings.TrimSpace(out)
	}
	set := req.Set
	switch {
	case set == "uncommitted":
		if !hasHead {
			s.old = emptyTree
		} else {
			s.old = head
		}
		s.worktree = true
	case set == "detached" || strings.HasPrefix(set, "branch:"):
		name := strings.TrimPrefix(set, "branch:")
		live := set == "detached" || name == current
		tip := head
		if !live {
			resolved, ok := revParse(ctx, dir, "refs/heads/"+name)
			if !ok {
				return s, fmt.Errorf("branch %s is gone", name)
			}
			tip = resolved
		}
		base := strings.TrimSpace(req.Base)
		if base == "" {
			checkout.Head.Branch = current
			if _, mb, _, ok := closestBase(ctx, dir, baseCandidates(ctx, &checkout), tip); ok {
				base = mb
			}
		}
		if base == "" {
			base = emptyTree
		} else if _, ok := revParse(ctx, dir, base); !ok {
			return s, fmt.Errorf("the base %s of branch %s is gone", short(base), name)
		}
		s.old = base
		if live {
			s.worktree = true
		} else {
			s.new = tip
		}
	case strings.HasPrefix(set, "commit:"):
		sha := strings.TrimPrefix(set, "commit:")
		if _, ok := revParse(ctx, dir, sha); !ok {
			return s, fmt.Errorf("commit %s is gone", short(sha))
		}
		s.new = sha
		s.old = emptyTree
		if parent, ok := revParse(ctx, dir, sha+"^"); ok {
			s.old = parent
		}
	case strings.HasPrefix(set, "range:"):
		from, to, ok := strings.Cut(strings.TrimPrefix(set, "range:"), "..")
		if !ok {
			return s, fmt.Errorf("the range %q needs <oldest>..<newest>", set)
		}
		if _, ok := revParse(ctx, dir, to); !ok {
			return s, fmt.Errorf("commit %s is gone", short(to))
		}
		s.new = to
		s.old = emptyTree
		if parent, ok := revParse(ctx, dir, from+"^"); ok {
			s.old = parent
		}
	default:
		return s, fmt.Errorf("unknown change set %q", set)
	}
	return s, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// changedFiles lists the files that differ between the two sides, with
// renames. A worktree side adds the untracked files; a folder of many
// untracked files is one entry.
func changedFiles(ctx context.Context, s sides) ([]entry, []string, error) {
	var raw string
	var err error
	if s.worktree {
		raw, err = git(ctx, s.dir, "diff-index", "-z", "-M", "--raw", "--no-ext-diff", s.old)
	} else {
		raw, err = git(ctx, s.dir, "diff-tree", "-r", "-z", "-M", "--raw", "--no-ext-diff", s.old, s.new)
	}
	if err != nil {
		return nil, nil, err
	}
	entries := parseRaw(raw)
	var notices []string
	if s.worktree {
		untracked, notice := untrackedFiles(ctx, s.dir)
		entries = append(entries, untracked...)
		if notice != "" {
			notices = append(notices, notice)
		}
	}
	return entries, notices, nil
}

// parseRaw reads the -z raw output of git diff-index and git diff-tree.
func parseRaw(raw string) []entry {
	var out []entry
	fields := strings.Split(raw, "\x00")
	for i := 0; i < len(fields); i++ {
		head := fields[i]
		if !strings.HasPrefix(head, ":") {
			continue
		}
		parts := strings.Fields(head[1:])
		if len(parts) < 5 || i+1 >= len(fields) {
			continue
		}
		e := entry{oldMode: parts[0], newMode: parts[1], oldID: parts[2], newID: parts[3], status: parts[4][:1]}
		e.path = fields[i+1]
		i++
		if (e.status == "R" || e.status == "C") && i+1 < len(fields) {
			e.oldPath = e.path
			e.path = fields[i+1]
			i++
		}
		out = append(out, e)
	}
	return out
}

// untrackedFiles lists the untracked files of a checkout. A folder that holds
// more than maxUntracked untracked files is one entry with their count.
func untrackedFiles(ctx context.Context, dir string) ([]entry, string) {
	out, err := git(ctx, dir, "ls-files", "-z", "--others", "--exclude-standard", "--directory", "--no-empty-directory")
	if err != nil {
		return nil, ""
	}
	var entries []entry
	folders := 0
	for _, name := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		if !strings.HasSuffix(name, "/") {
			entries = append(entries, entry{path: name, status: "?"})
			continue
		}
		files := countFiles(filepath.Join(dir, name), maxUntracked+1)
		if files > maxUntracked {
			entries = append(entries, entry{path: name, status: "?", count: countFiles(filepath.Join(dir, name), 100_000)})
			folders++
			continue
		}
		inside, err := git(ctx, dir, "ls-files", "-z", "--others", "--exclude-standard", "--", name)
		if err != nil {
			continue
		}
		for _, file := range strings.Split(strings.TrimRight(inside, "\x00"), "\x00") {
			if file != "" {
				entries = append(entries, entry{path: file, status: "?"})
			}
		}
	}
	if folders > 0 {
		return entries, fmt.Sprintf("%d untracked folders show as one row each.", folders)
	}
	return entries, ""
}

func countFiles(dir string, limit int) int {
	count := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			count++
			if count >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	return count
}

// buildFiles reads the content of each changed file and makes its diff.
func buildFiles(ctx context.Context, src Source, checkout Checkout, s sides, req Request, entries []entry) ([]File, error) {
	reader, err := newBlobReader(ctx, s.dir)
	if err != nil {
		return nil, err
	}
	defer reader.close()
	attributes := readAttributes(ctx, s.dir, entries)
	viewed := loadMarks(src.StateDir, src.Agent.ID)
	wanted := map[string]bool{}
	for _, name := range req.Files {
		wanted[name] = true
	}
	uncommitted := map[string]entry{}
	staged := map[string]bool{}
	if s.worktree {
		if s.head != "" && s.old != s.head {
			if raw, err := git(ctx, s.dir, "diff-index", "-z", "--raw", "--no-ext-diff", s.head); err == nil {
				for _, e := range parseRaw(raw) {
					uncommitted[e.path] = e
				}
			}
		}
		if s.head != "" {
			if names, err := git(ctx, s.dir, "diff-index", "-z", "--cached", "--name-only", s.head); err == nil {
				for _, name := range strings.Split(names, "\x00") {
					staged[name] = true
				}
			}
		}
	}
	hashes := worktreeHashes(ctx, s, entries)
	type work struct {
		file     File
		old, new []byte
		language *syntax.Language
		overlays map[int]int
		textual  bool
	}
	var items []*work
	for _, e := range entries {
		item := &work{file: File{Path: e.path, OldPath: e.oldPath, Status: statusName(e.status)}}
		file := &item.file
		if e.count > 0 {
			file.Status, file.Count, file.Deferred = "untracked", e.count, true
			file.Classes = []string{"untracked", "folder"}
			items = append(items, item)
			continue
		}
		if e.oldMode == "160000" || e.newMode == "160000" {
			file.Classes = append(file.Classes, "submodule")
			file.Notices = append(file.Notices, fmt.Sprintf("Submodule moves from %s to %s.", short(e.oldID), short(e.newID)))
			file.Deferred = true
			items = append(items, item)
			continue
		}
		oldPath := e.path
		if e.oldPath != "" {
			oldPath = e.oldPath
		}
		var oldSize, newSize int64
		if e.status != "A" && e.status != "?" && !zeroID(e.oldID) {
			data, size, ok, err := reader.read(e.oldID, maxContent)
			if err != nil {
				return nil, err
			}
			if ok {
				item.old, oldSize = data, size
			}
		}
		switch {
		case e.status == "D":
		case s.worktree:
			data, size, err := readWorktreeSized(s.dir, e.path)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				file.Notices = append(file.Notices, err.Error())
			}
			item.new, newSize = data, size
		default:
			data, size, ok, err := reader.read(e.newID, maxContent)
			if err != nil {
				return nil, err
			}
			if ok {
				item.new, newSize = data, size
			}
		}
		file.OldSize, file.NewSize = oldSize, newSize
		file.Large = oldSize > maxContent || newSize > maxContent
		file.Binary = isBinary(item.old) || isBinary(item.new) || attributes[e.path].binary
		head := item.new
		if head == nil {
			head = item.old
		}
		item.language = syntax.Detect(e.path, head)
		if item.language != nil {
			file.Language = item.language.Name
		}
		file.Classes = append(file.Classes, classes(e.path, head, attributes[e.path])...)
		if e.status == "?" {
			file.Classes = append(file.Classes, "untracked", "uncommitted")
		} else if _, ok := uncommitted[e.path]; ok {
			file.Classes = append(file.Classes, "uncommitted")
		}
		if staged[e.path] {
			file.Classes = append(file.Classes, "staged")
		}
		if file.Large {
			file.Classes = append(file.Classes, "large")
		}
		if file.Binary {
			file.Classes = append(file.Classes, "binary")
		}
		file.Content = contentID(s, e, hashes)
		if seen := viewed.seen(checkout.Key, e.path); seen != "" {
			file.Viewed = seen == file.Content
			file.Changed = !file.Viewed
			if file.Changed && !file.Binary && !file.Large {
				if data, _, ok, err := reader.read(seen, maxContent); err == nil && ok {
					if req.OnlyNew {
						item.old = data
						file.Notices = append(file.Notices, "Compared with the version that you viewed.")
					} else {
						item.overlays = insertedLines(data, item.new, diff.FlagNew, item.overlays)
					}
				}
			}
		}
		if req.OnlyNew && file.Viewed {
			continue
		}
		if prior, ok := uncommitted[e.path]; ok && !file.Binary && !file.Large {
			var headData []byte
			if prior.status != "A" && !zeroID(prior.oldID) {
				headData, _, _, _ = reader.read(prior.oldID, maxContent)
			}
			item.overlays = insertedLines(headData, item.new, diff.FlagUncommitted, item.overlays)
		} else if e.status == "?" && !file.Binary && !file.Large {
			item.overlays = insertedLines(nil, item.new, diff.FlagUncommitted, item.overlays)
		}
		item.textual = !file.Binary && !file.Large
		foldedByDefault := hasClass(*file, "generated") || hasClass(*file, "vendored") || hasClass(*file, "lockfile") || file.Binary || file.Large
		file.Deferred = foldedByDefault && !wanted[e.path] && !wanted[oldPath]
		items = append(items, item)
	}
	if len(wanted) > 0 {
		for _, item := range items {
			if !wanted[item.file.Path] && !wanted[item.file.OldPath] {
				item.file.Deferred = true
			}
		}
	}
	var inputs []diff.Input
	var targets []*work
	for _, item := range items {
		if !item.textual {
			continue
		}
		if item.file.Deferred {
			// A folded text file still counts its lines.
			if lineCount(item.old)+lineCount(item.new) <= 2*maxLines {
				oldLines, _ := diff.SplitLines(item.old)
				newLines, _ := diff.SplitLines(item.new)
				for _, e := range diff.Lines(oldLines, newLines) {
					switch e.Op {
					case diff.Insert:
						item.file.Added += e.N
					case diff.Delete:
						item.file.Removed += e.N
					}
				}
			}
			continue
		}
		var language diff.Language
		if item.language != nil {
			language = item.language
		}
		inputs = append(inputs, diff.Input{Path: item.file.Path, OldPath: item.file.OldPath, Old: item.old, New: item.new, Language: language, Overlays: item.overlays})
		targets = append(targets, item)
	}
	rows := 0
	for i, result := range diff.Compare(inputs, diff.DefaultOptions) {
		file := &targets[i].file
		file.Rows, file.Added, file.Removed = result.Rows, result.Added, result.Removed
		file.Changes, file.Noise, file.Entities = result.Changes, result.Noise, result.Entities
		file.Notices = append(file.Notices, result.Notices...)
		if result.Moved {
			file.Classes = append(file.Classes, "moved")
		}
		if result.Changes > 0 && result.Noise == result.Changes {
			file.Classes = append(file.Classes, "format")
		}
		rows += len(result.Rows)
	}
	// A change set with more rows than one view holds folds its largest
	// files. They keep their counts and load their rows when opened.
	if rows > maxRows && len(wanted) == 0 {
		order := append([]*work(nil), targets...)
		sort.SliceStable(order, func(i, j int) bool { return len(order[i].file.Rows) > len(order[j].file.Rows) })
		for _, item := range order {
			if rows <= maxRows {
				break
			}
			rows -= len(item.file.Rows)
			item.file.Rows = nil
			item.file.Deferred = true
			item.file.Notices = append(item.file.Notices, "This file folds because the change set is large. Open it to load its diff.")
		}
	}
	out := make([]File, 0, len(items))
	for _, item := range items {
		out = append(out, item.file)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := folded(out[i]), folded(out[j])
		if a != b {
			return !a
		}
		return lessPath(out[i].Path, out[j].Path)
	})
	return out, nil
}

// folded reports whether a file goes to the end of the list: generated
// files, vendored files, lockfiles and folders of untracked files.
func folded(file File) bool {
	return hasClass(file, "generated") || hasClass(file, "vendored") || hasClass(file, "lockfile") || hasClass(file, "folder")
}

// lessPath orders paths as a file tree shows them: folders before files, and
// names in order inside a folder.
func lessPath(a, b string) bool {
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		aFolder, bFolder := i < len(pa)-1, i < len(pb)-1
		if pa[i] == pb[i] && aFolder && bFolder {
			continue
		}
		if aFolder != bFolder {
			return aFolder
		}
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// insertedLines marks the lines of the new text that a diff with the older
// text finds added.
func insertedLines(older, newer []byte, flag int, into map[int]int) map[int]int {
	if into == nil {
		into = map[int]int{}
	}
	oldLines, _ := diff.SplitLines(older)
	newLines, _ := diff.SplitLines(newer)
	if len(oldLines)+len(newLines) > 2*maxLines {
		return into
	}
	for _, e := range diff.Lines(oldLines, newLines) {
		if e.Op != diff.Insert {
			continue
		}
		for line := e.B; line < e.B+e.N; line++ {
			into[line] |= flag
		}
	}
	return into
}

func lineCount(data []byte) int {
	return bytes.Count(data, []byte("\n")) + 1
}

func zeroID(id string) bool {
	return strings.Trim(id, "0") == ""
}

func statusName(code string) string {
	switch code {
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "R":
		return "renamed"
	case "C":
		return "copied"
	case "T":
		return "type"
	case "U":
		return "conflict"
	case "?":
		return "untracked"
	default:
		return "modified"
	}
}

func hasClass(file File, class string) bool {
	for _, c := range file.Classes {
		if c == class {
			return true
		}
	}
	return false
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), binaryProbe)], 0) >= 0
}

// readWorktree reads a file of a checkout. A symbolic link reads as its
// target, as Git stores it.
func readWorktree(dir, name string) ([]byte, error) {
	data, _, err := readWorktreeSized(dir, name)
	return data, err
}

func readWorktreeSized(dir, name string) ([]byte, int64, error) {
	full := filepath.Join(dir, filepath.FromSlash(name))
	if rel, err := filepath.Rel(dir, full); err != nil || strings.HasPrefix(rel, "..") {
		return nil, 0, fmt.Errorf("the path %s leaves the checkout", name)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		return []byte(target), int64(len(target)), err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, nil
	}
	if info.Size() > maxContent {
		return nil, info.Size(), nil
	}
	data, err := os.ReadFile(full)
	return data, int64(len(data)), err
}

// worktreeHashes gives the Git blob IDs of the worktree versions of the
// changed files that Git did not hash yet, with one Git command.
func worktreeHashes(ctx context.Context, s sides, entries []entry) map[string]string {
	out := map[string]string{}
	if !s.worktree {
		return out
	}
	var names []string
	for _, e := range entries {
		if e.status != "D" && e.count == 0 && zeroID(e.newID) {
			names = append(names, e.path)
		}
	}
	if len(names) == 0 {
		return out
	}
	data, err := gitBytes(ctx, s.dir, []byte(strings.Join(names, "\n")+"\n"), "hash-object", "--stdin-paths")
	if err != nil {
		return out
	}
	ids := strings.Fields(string(data))
	for i, name := range names {
		if i < len(ids) {
			out[name] = ids[i]
		}
	}
	return out
}

// contentID gives the Git blob ID of the new version of a file, for its
// viewed mark. A deleted file has the ID of its old version with a prefix.
func contentID(s sides, e entry, hashes map[string]string) string {
	if e.status == "D" {
		return "deleted:" + e.oldID
	}
	if !s.worktree || !zeroID(e.newID) {
		return e.newID
	}
	return hashes[e.path]
}

type attributes struct {
	generated bool
	vendored  bool
	binary    bool
}

// readAttributes reads the Git attributes that change how a file shows:
// linguist-generated, linguist-vendored, and -diff or binary.
func readAttributes(ctx context.Context, dir string, entries []entry) map[string]attributes {
	out := map[string]attributes{}
	var input bytes.Buffer
	for _, e := range entries {
		if e.count == 0 {
			input.WriteString(e.path)
			input.WriteByte(0)
		}
	}
	if input.Len() == 0 {
		return out
	}
	data, err := gitBytes(ctx, dir, input.Bytes(), "check-attr", "-z", "--stdin", "linguist-generated", "linguist-vendored", "diff", "binary")
	if err != nil {
		return out
	}
	fields := strings.Split(string(data), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		name, attribute, value := fields[i], fields[i+1], fields[i+2]
		a := out[name]
		set := value == "set" || value == "true"
		switch attribute {
		case "linguist-generated":
			a.generated = set
		case "linguist-vendored":
			a.vendored = set
		case "diff":
			a.binary = a.binary || value == "unset"
		case "binary":
			a.binary = a.binary || set
		}
		out[name] = a
	}
	return out
}

var lockfiles = map[string]bool{
	"go.sum": true, "package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"bun.lockb": true, "bun.lock": true, "cargo.lock": true, "poetry.lock": true, "uv.lock": true, "pipfile.lock": true,
	"gemfile.lock": true, "composer.lock": true, "flake.lock": true, "mix.lock": true, "pubspec.lock": true, "podfile.lock": true,
}

// classes labels a file as generated, vendored or a lockfile.
func classes(name string, head []byte, attrs attributes) []string {
	var out []string
	base := strings.ToLower(path.Base(name))
	lower := strings.ToLower(name)
	if attrs.generated || generatedName(base) || generatedHeader(head) {
		out = append(out, "generated")
	}
	if attrs.vendored || strings.HasPrefix(lower, "vendor/") || strings.Contains(lower, "/vendor/") ||
		strings.HasPrefix(lower, "node_modules/") || strings.Contains(lower, "/node_modules/") || strings.HasPrefix(lower, "third_party/") {
		out = append(out, "vendored")
	}
	if lockfiles[base] {
		out = append(out, "lockfile")
	}
	return out
}

func generatedName(base string) bool {
	for _, suffix := range []string{".pb.go", ".pb.gw.go", "_pb2.py", "_pb2_grpc.py", ".pb.ts", ".min.js", ".min.css", ".map", "_generated.go", ".generated.ts", ".generated.go", ".gen.go", ".gen.ts"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.HasPrefix(base, "zz_generated")
}

// generatedHeader finds the comment of the Go convention for generated code
// in the first lines of a file: "Code generated ... DO NOT EDIT.".
func generatedHeader(head []byte) bool {
	limit := head
	for i, lines := 0, 0; i < len(head); i++ {
		if head[i] == '\n' {
			lines++
			if lines == 5 {
				limit = head[:i]
				break
			}
		}
	}
	text := string(limit)
	return strings.Contains(text, "Code generated") && strings.Contains(text, "DO NOT EDIT")
}
