package neovimreview

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractArchiveRejectsWrongChecksum(t *testing.T) {
	data := testArchive(t, map[string]string{"root/file": "safe"})
	err := extractArchive(t.TempDir(), archiveSpec{
		name: "test.tar.gz", root: "root", sha256: strings.Repeat("0", 64), data: data,
		selectFn: func(relative string) (string, bool) { return relative, true },
	})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("extractArchive error = %v, want checksum failure", err)
	}
}

func TestExtractArchiveRejectsPathTraversal(t *testing.T) {
	data := testArchive(t, map[string]string{"root/../../escape": "unsafe"})
	digest := sha256.Sum256(data)
	destination := t.TempDir()
	err := extractArchive(destination, archiveSpec{
		name: "test.tar.gz", root: "root", sha256: hex.EncodeToString(digest[:]), data: data,
		selectFn: func(relative string) (string, bool) { return relative, true },
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe archive path") {
		t.Fatalf("extractArchive error = %v, want path traversal failure", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "escape")); !os.IsNotExist(err) {
		t.Fatalf("path traversal created a file: %v", err)
	}
}

func TestInspectReportsIncompleteRuntimeWithoutChangingState(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "nvim"), "#!/bin/sh\necho 'NVIM v0.12.5'\n")
	t.Setenv("PATH", bin)
	state := t.TempDir()

	_, err := Inspect(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "galpon review setup") {
		t.Fatalf("Inspect error = %v, want setup advice", err)
	}
	entries, readErr := os.ReadDir(state)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("Inspect changed the state directory: %#v", entries)
	}
}

func TestSetupCompilerFailureKeepsExistingRuntime(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "nvim"), "#!/bin/sh\necho 'NVIM v0.12.5'\n")
	writeExecutable(t, filepath.Join(bin, "cc"), "#!/bin/sh\necho 'compiler failed as requested' >&2\nexit 12\n")
	t.Setenv("PATH", bin)
	state := t.TempDir()
	bundle, err := runtimeBundle()
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := runtimeLocation(state, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "prior-runtime")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Setup(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "compiler failed as requested") {
		t.Fatalf("Setup error = %v, want compiler failure", err)
	}
	data, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("failed setup changed the prior runtime: data=%q error=%v", data, readErr)
	}
}

func TestSetupBuildsIdempotentRuntimeAndLoadsParsers(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc is not installed")
	}
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("Neovim is not installed")
	}
	state := t.TempDir()
	info, err := Setup(context.Background(), state)
	if err != nil {
		if strings.Contains(err.Error(), "0.11.0 or newer") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	manifestPath := filepath.Join(info.Runtime, manifestName)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	again, err := Setup(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if again != info {
		t.Fatalf("second Setup returned %#v, want %#v", again, info)
	}
	if !bytes.Equal(after, before) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("second Setup replaced an already ready runtime")
	}

	for _, name := range []string{
		"licenses/nvim-treesitter-Apache-2.0.txt",
		"licenses/tree-sitter-markdown-MIT.txt",
		"plugins/mini.icons/LICENSE",
		"plugins/render-markdown.nvim/LICENSE",
		"runtime/parser/markdown.so",
		"runtime/parser/markdown_inline.so",
	} {
		if _, err := os.Stat(filepath.Join(info.Runtime, filepath.FromSlash(name))); err != nil {
			t.Errorf("prepared runtime is missing %s: %v", name, err)
		}
	}

	script := filepath.Join(t.TempDir(), "load.lua")
	write := `
vim.opt.runtimepath:prepend(vim.env.GALPON_TEST_RUNTIME .. '/runtime')
assert(vim.treesitter.language.add('markdown'))
assert(vim.treesitter.language.add('markdown_inline'))
local parser = vim.treesitter.get_string_parser('# Heading\n\nText *with emphasis*.\n', 'markdown')
local trees = parser:parse()
assert(#trees == 1 and trees[1]:root():type() == 'document')
`
	if err := os.WriteFile(script, []byte(write), 0o600); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	command := exec.Command(info.Neovim,
		"--clean", "--headless", "-u", "NONE", "-i", "NONE",
		"-c", "lua dofile(vim.env.GALPON_TEST_SCRIPT)", "-c", "qa!",
	)
	command.Env = append(os.Environ(),
		"HOME="+filepath.Join(private, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(private, "config"),
		"XDG_CACHE_HOME="+filepath.Join(private, "cache"),
		"XDG_DATA_HOME="+filepath.Join(private, "data"),
		"XDG_STATE_HOME="+filepath.Join(private, "state"),
		"GALPON_TEST_RUNTIME="+info.Runtime,
		"GALPON_TEST_SCRIPT="+script,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Neovim did not load the prepared parsers: %v\n%s", err, output)
	}

	inspected, err := Inspect(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if inspected != info {
		t.Fatalf("Inspect returned %#v, want %#v", inspected, info)
	}
	parserPath := filepath.Join(info.Runtime, "runtime", "parser", "markdown.so")
	file, err := os.OpenFile(parserPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("corrupt"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Inspect(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "integrity check") || !strings.Contains(err.Error(), "galpon review setup") {
		t.Fatalf("Inspect error after corruption = %v, want integrity failure and setup advice", err)
	}
}

func TestFindNeovimRejectsOldVersion(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "nvim"), "#!/bin/sh\necho 'NVIM v0.10.4'\n")
	t.Setenv("PATH", bin)
	_, err := findNeovim(context.Background())
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("findNeovim error = %v, want incompatibility", err)
	}
}

func testArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func writeExecutable(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
