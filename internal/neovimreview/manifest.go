package neovimreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	manifestName     = ".ready.json"
	maxManifestBytes = 1 << 20
	maxManifestFiles = 512
)

type runtimeManifest struct {
	Bundle string         `json:"bundle"`
	Files  []manifestFile `json:"files"`
}

type manifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func writeRuntimeManifest(root, bundle string) error {
	manifest := runtimeManifest{Bundle: bundle}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("runtime contains unsupported file %s", name)
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		size, digest, err := hashRegularFile(name)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, manifestFile{
			Path:   filepath.ToSlash(relative),
			Size:   size,
			SHA256: digest,
		})
		if len(manifest.Files) > maxManifestFiles {
			return fmt.Errorf("runtime has too many files")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create runtime manifest: %w", err)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if err := checkRequiredRuntimeFiles(manifest.Files); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime manifest: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxManifestBytes {
		return fmt.Errorf("runtime manifest is too large")
	}
	if err := os.WriteFile(filepath.Join(root, manifestName), data, 0o600); err != nil {
		return fmt.Errorf("write runtime manifest: %w", err)
	}
	return nil
}

func verifyRuntime(root, bundle string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("runtime path is not a directory")
	}
	manifestPath := filepath.Join(root, manifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return fmt.Errorf("ready manifest: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > maxManifestBytes {
		return fmt.Errorf("ready manifest is not a bounded regular file")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read ready manifest: %w", err)
	}
	var manifest runtimeManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode ready manifest: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return fmt.Errorf("decode ready manifest: %w", err)
	}
	if manifest.Bundle != bundle {
		return fmt.Errorf("runtime bundle is %q, expected %q", manifest.Bundle, bundle)
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxManifestFiles {
		return fmt.Errorf("ready manifest has an invalid file count")
	}
	if err := checkRequiredRuntimeFiles(manifest.Files); err != nil {
		return err
	}
	seen := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if seen[file.Path] {
			return fmt.Errorf("ready manifest repeats %q", file.Path)
		}
		seen[file.Path] = true
		if file.Size < 0 || len(file.SHA256) != sha256.Size*2 {
			return fmt.Errorf("ready manifest has invalid metadata for %q", file.Path)
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return fmt.Errorf("ready manifest has invalid checksum for %q", file.Path)
		}
		name, err := safeDestination(root, file.Path)
		if err != nil {
			return fmt.Errorf("ready manifest: %w", err)
		}
		if err := rejectSymlinkParents(root, name); err != nil {
			return fmt.Errorf("verify %q: %w", file.Path, err)
		}
		size, digest, err := hashRegularFile(name)
		if err != nil {
			return fmt.Errorf("verify %q: %w", file.Path, err)
		}
		if size != file.Size || digest != file.SHA256 {
			return fmt.Errorf("file %q failed its integrity check", file.Path)
		}
	}
	return nil
}

func checkRequiredRuntimeFiles(files []manifestFile) error {
	present := make(map[string]bool, len(files))
	for _, file := range files {
		present[file.Path] = true
	}
	required := []string{
		"licenses/nvim-treesitter-Apache-2.0.txt",
		"licenses/tree-sitter-markdown-MIT.txt",
		"plugins/mini.icons/LICENSE",
		"plugins/mini.icons/lua/mini/icons.lua",
		"plugins/render-markdown.nvim/LICENSE",
		"plugins/render-markdown.nvim/lua/render-markdown/init.lua",
		"plugins/render-markdown.nvim/plugin/render-markdown.lua",
		"runtime/parser/markdown.so",
		"runtime/parser/markdown_inline.so",
		"runtime/queries/markdown/folds.scm",
		"runtime/queries/markdown/highlights.scm",
		"runtime/queries/markdown/indents.scm",
		"runtime/queries/markdown/injections.scm",
		"runtime/queries/markdown_inline/highlights.scm",
		"runtime/queries/markdown_inline/injections.scm",
	}
	for _, name := range required {
		if !present[name] {
			return fmt.Errorf("runtime is incomplete: missing %s", name)
		}
	}
	return nil
}

func hashRegularFile(name string) (int64, string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("not a regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	if written != info.Size() {
		return 0, "", fmt.Errorf("file changed while it was read")
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func rejectSymlinkParents(root, name string) error {
	relative, err := filepath.Rel(root, name)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path uses a symbolic link")
		}
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected data after manifest")
		}
		return err
	}
	return nil
}
