// Package neovimreview prepares the pinned, offline runtime for native Neovim reviews.
package neovimreview

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const setupAdvice = "run `galpon review setup` to prepare it"

var neovimVersionPattern = regexp.MustCompile(`(?m)^NVIM v([0-9]+)\.([0-9]+)\.([0-9]+)(?:[^0-9].*)?$`)

// Info identifies a compatible Neovim executable and its prepared runtime.
type Info struct {
	Runtime string `json:"runtime"`
	Neovim  string `json:"neovim"`
	Version string `json:"version"`
}

type executableInfo struct {
	path    string
	version string
}

// Setup builds the pinned parsers and atomically prepares the review runtime.
// It uses only embedded source data. It checks Neovim's version but does not
// open an editor or load an editor configuration.
func Setup(ctx context.Context, stateDir string) (Info, error) {
	if ctx == nil {
		return Info{}, fmt.Errorf("prepare Neovim review runtime: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Info{}, fmt.Errorf("prepare Neovim review runtime: %w", err)
	}
	bundle, err := runtimeBundle()
	if err != nil {
		return Info{}, err
	}
	neovim, err := findNeovim(ctx)
	if err != nil {
		return Info{}, err
	}
	target, base, err := runtimeLocation(stateDir, bundle)
	if err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return Info{}, fmt.Errorf("create Neovim review state: %w", err)
	}
	unlock, err := lockSetup(ctx, filepath.Join(base, ".setup.lock"))
	if err != nil {
		return Info{}, err
	}
	defer unlock()

	if err := verifyRuntime(target, bundle); err == nil {
		return Info{Runtime: target, Neovim: neovim.path, Version: neovim.version}, nil
	}
	compiler, err := exec.LookPath("cc")
	if err != nil {
		return Info{}, fmt.Errorf("prepare Neovim review runtime: C compiler cc is required: %w", err)
	}
	compiler, err = filepath.Abs(compiler)
	if err != nil {
		return Info{}, fmt.Errorf("resolve C compiler: %w", err)
	}
	stage, err := os.MkdirTemp(base, "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return Info{}, fmt.Errorf("create Neovim review staging directory: %w", err)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := buildRuntime(ctx, stage, compiler); err != nil {
		return Info{}, err
	}
	if err := writeRuntimeManifest(stage, bundle); err != nil {
		return Info{}, err
	}
	if err := verifyRuntime(stage, bundle); err != nil {
		return Info{}, fmt.Errorf("verify staged Neovim review runtime: %w", err)
	}
	if err := replaceRuntime(target, stage); err != nil {
		return Info{}, err
	}
	stageOwned = false
	return Info{Runtime: target, Neovim: neovim.path, Version: neovim.version}, nil
}

// Inspect finds Neovim and verifies an existing runtime without changing files,
// compiling code, downloading data, or opening an editor.
func Inspect(ctx context.Context, stateDir string) (Info, error) {
	if ctx == nil {
		return Info{}, fmt.Errorf("inspect Neovim review runtime: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Info{}, fmt.Errorf("inspect Neovim review runtime: %w", err)
	}
	bundle, err := runtimeBundle()
	if err != nil {
		return Info{}, err
	}
	neovim, err := findNeovim(ctx)
	if err != nil {
		return Info{}, err
	}
	target, _, err := runtimeLocation(stateDir, bundle)
	if err != nil {
		return Info{}, err
	}
	if err := verifyRuntime(target, bundle); err != nil {
		return Info{}, fmt.Errorf("inspect Neovim review runtime: missing or incomplete: %v; %s", err, setupAdvice)
	}
	return Info{Runtime: target, Neovim: neovim.path, Version: neovim.version}, nil
}

func runtimeBundle() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("native Neovim review is not supported on %s; Review currently requires Linux", runtime.GOOS)
	}
	parts := []string{renderArchiveSHA, iconsArchiveSHA, grammarArchiveSHA, queryLicenseSHA}
	paths := make([]string, 0, len(queryFiles))
	for name := range queryFiles {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		parts = append(parts, name+":"+queryFiles[name])
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("v1-%s-%s-%x", runtime.GOOS, runtime.GOARCH, digest[:6]), nil
}

func runtimeLocation(stateDir, bundle string) (target, base string, err error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", "", fmt.Errorf("native Neovim review state directory is required")
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve Neovim review state directory: %w", err)
	}
	base = filepath.Join(absolute, "runtime", "neovim-review")
	return filepath.Join(base, bundle), base, nil
}

func findNeovim(ctx context.Context) (executableInfo, error) {
	name, err := exec.LookPath("nvim")
	if err != nil {
		return executableInfo{}, fmt.Errorf("native review requires Neovim 0.11.0 or newer: %w", err)
	}
	name, err = filepath.Abs(name)
	if err != nil {
		return executableInfo{}, fmt.Errorf("resolve Neovim executable: %w", err)
	}
	command := exec.CommandContext(ctx, name, "--version")
	output, err := command.Output()
	if err != nil {
		return executableInfo{}, fmt.Errorf("read Neovim version from %s: %w", name, err)
	}
	match := neovimVersionPattern.FindSubmatch(output)
	if match == nil {
		return executableInfo{}, fmt.Errorf("read Neovim version from %s: unexpected output", name)
	}
	major, _ := strconv.Atoi(string(match[1]))
	minor, _ := strconv.Atoi(string(match[2]))
	patch, _ := strconv.Atoi(string(match[3]))
	if major == 0 && minor < 11 {
		return executableInfo{}, fmt.Errorf("incompatible Neovim %d.%d.%d at %s; version 0.11.0 or newer is required", major, minor, patch, name)
	}
	return executableInfo{path: name, version: fmt.Sprintf("%d.%d.%d", major, minor, patch)}, nil
}

func buildRuntime(ctx context.Context, stage, compiler string) error {
	renderData, err := embeddedArchive("render-markdown.tar.gz")
	if err != nil {
		return err
	}
	iconsData, err := embeddedArchive("mini-icons.tar.gz")
	if err != nil {
		return err
	}
	grammarData, err := embeddedArchive("tree-sitter-markdown.tar.gz")
	if err != nil {
		return err
	}
	pluginSelector := func(plugin string) func(string) (string, bool) {
		return func(relative string) (string, bool) {
			if relative == "LICENSE" || strings.HasPrefix(relative, "lua/") || strings.HasPrefix(relative, "plugin/") {
				return pathJoin("plugins", plugin, relative), true
			}
			return "", false
		}
	}
	if err := extractArchive(stage, archiveSpec{
		name: "render-markdown.tar.gz", root: renderRoot, sha256: renderArchiveSHA, data: renderData,
		selectFn: pluginSelector("render-markdown.nvim"),
		required: []string{
			"plugins/render-markdown.nvim/LICENSE",
			"plugins/render-markdown.nvim/lua/render-markdown/init.lua",
			"plugins/render-markdown.nvim/plugin/render-markdown.lua",
		},
	}); err != nil {
		return err
	}
	if err := extractArchive(stage, archiveSpec{
		name: "mini-icons.tar.gz", root: iconsRoot, sha256: iconsArchiveSHA, data: iconsData,
		selectFn: pluginSelector("mini.icons"),
		required: []string{
			"plugins/mini.icons/LICENSE",
			"plugins/mini.icons/lua/mini/icons.lua",
		},
	}); err != nil {
		return err
	}
	grammarSelector := func(relative string) (string, bool) {
		switch {
		case relative == "LICENSE":
			return "licenses/tree-sitter-markdown-MIT.txt", true
		case relative == "tree-sitter-markdown/src/parser.c",
			relative == "tree-sitter-markdown/src/scanner.c",
			relative == "tree-sitter-markdown-inline/src/parser.c",
			relative == "tree-sitter-markdown-inline/src/scanner.c",
			strings.HasPrefix(relative, "tree-sitter-markdown/src/tree_sitter/"),
			strings.HasPrefix(relative, "tree-sitter-markdown-inline/src/tree_sitter/"):
			return pathJoin(".build", relative), true
		default:
			return "", false
		}
	}
	if err := extractArchive(stage, archiveSpec{
		name: "tree-sitter-markdown.tar.gz", root: grammarRoot, sha256: grammarArchiveSHA, data: grammarData,
		selectFn: grammarSelector,
		required: []string{
			"licenses/tree-sitter-markdown-MIT.txt",
			".build/tree-sitter-markdown/src/parser.c",
			".build/tree-sitter-markdown/src/scanner.c",
			".build/tree-sitter-markdown/src/tree_sitter/parser.h",
			".build/tree-sitter-markdown-inline/src/parser.c",
			".build/tree-sitter-markdown-inline/src/scanner.c",
			".build/tree-sitter-markdown-inline/src/tree_sitter/parser.h",
		},
	}); err != nil {
		return err
	}
	for name, digest := range queryFiles {
		destination := filepath.Join(stage, "runtime", "queries", filepath.FromSlash(name))
		if err := writeEmbeddedFile(destination, "third_party/queries/"+name, digest); err != nil {
			return err
		}
	}
	if err := writeEmbeddedFile(
		filepath.Join(stage, "licenses", "nvim-treesitter-Apache-2.0.txt"),
		"third_party/queries/LICENSE", queryLicenseSHA,
	); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stage, "config", "empty-pack"), 0o700); err != nil {
		return fmt.Errorf("create empty Neovim package directory: %w", err)
	}
	buildRoot := filepath.Join(stage, ".build")
	parserDir := filepath.Join(stage, "runtime", "parser")
	if err := os.MkdirAll(parserDir, 0o700); err != nil {
		return fmt.Errorf("create parser directory: %w", err)
	}
	for _, parser := range []struct {
		language string
		source   string
	}{
		{language: "markdown", source: "tree-sitter-markdown"},
		{language: "markdown_inline", source: "tree-sitter-markdown-inline"},
	} {
		source := filepath.Join(buildRoot, parser.source, "src")
		output := filepath.Join(parserDir, parser.language+".so")
		command := exec.CommandContext(ctx, compiler,
			"-O2", "-std=c11", "-fPIC", "-shared", "-I", source,
			"-o", output, filepath.Join(source, "parser.c"), filepath.Join(source, "scanner.c"),
		)
		privateHome := filepath.Join(buildRoot, "home")
		privateTmp := filepath.Join(buildRoot, "tmp")
		if err := os.MkdirAll(privateHome, 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(privateTmp, 0o700); err != nil {
			return err
		}
		command.Env = []string{
			"HOME=" + privateHome,
			"LANG=C",
			"LC_ALL=C",
			"PATH=" + os.Getenv("PATH"),
			"TMPDIR=" + privateTmp,
			"XDG_CONFIG_HOME=" + filepath.Join(buildRoot, "xdg-config"),
			"XDG_CACHE_HOME=" + filepath.Join(buildRoot, "xdg-cache"),
			"XDG_DATA_HOME=" + filepath.Join(buildRoot, "xdg-data"),
			"XDG_STATE_HOME=" + filepath.Join(buildRoot, "xdg-state"),
		}
		if outputText, err := command.CombinedOutput(); err != nil {
			message := strings.TrimSpace(string(outputText))
			if len(message) > 4096 {
				message = message[:4096] + "..."
			}
			if message != "" {
				return fmt.Errorf("compile %s parser with cc: %w: %s", parser.language, err, message)
			}
			return fmt.Errorf("compile %s parser with cc: %w", parser.language, err)
		}
	}
	if err := os.RemoveAll(buildRoot); err != nil {
		return fmt.Errorf("remove parser build sources: %w", err)
	}
	return nil
}

func replaceRuntime(target, stage string) error {
	var backup string
	if _, err := os.Lstat(target); err == nil {
		placeholder, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".old-")
		if err != nil {
			return fmt.Errorf("prepare old runtime backup: %w", err)
		}
		if err := os.Remove(placeholder); err != nil {
			return fmt.Errorf("prepare old runtime backup: %w", err)
		}
		backup = placeholder
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("preserve old Neovim review runtime: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect old Neovim review runtime: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("activate Neovim review runtime: %w", err)
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove replaced Neovim review runtime: %w", err)
		}
	}
	return nil
}

func lockSetup(ctx context.Context, name string) (func(), error) {
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Neovim review setup lock: %w", err)
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock Neovim review setup: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("lock Neovim review setup: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func pathJoin(parts ...string) string {
	return strings.Join(parts, "/")
}
