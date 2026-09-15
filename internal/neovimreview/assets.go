package neovimreview

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxArchiveEntries = 2048
	maxArchiveBytes   = 16 << 20
	maxArchiveFile    = 8 << 20

	renderArchiveSHA  = "d0721b7bd73e03bf531be679a4111cb0f958c4bfaba190a52e658727cb1a4425"
	iconsArchiveSHA   = "5577366a3174afdae2ba8baa1f1361afaae19cc13de87f899835e4ae47dcbf7e"
	grammarArchiveSHA = "a712569a59f127fd44a1cb59eecdb05d63fa2ccb4622b7d822bff199bf6fb133"

	renderRoot  = "render-markdown.nvim-f422cb5c6855f150e2ddcfaf44e7157b98b34f6a"
	iconsRoot   = "mini.icons-e56797f90192d81f1fda02e662fc3e8e3d775027"
	grammarRoot = "tree-sitter-markdown-a0a00f817d02412bd92c54d316f164d827b57b5c"
)

//go:embed vendor/render-markdown.tar.gz vendor/mini-icons.tar.gz vendor/tree-sitter-markdown.tar.gz vendor/queries/LICENSE vendor/queries/markdown/*.scm vendor/queries/markdown_inline/*.scm
var embeddedAssets embed.FS

var queryFiles = map[string]string{
	"markdown/folds.scm":             "f9cb82eb75c5d3c6946903bb2318278d3ca378e507b14569d2bb8af8bba69b95",
	"markdown/highlights.scm":        "7b71d4994cf3968e16d033ac59d28bb034427dd41be6a057b7a2985e2dfbe5b8",
	"markdown/indents.scm":           "52550be38883524e3eb393982cbc3e0ff08e7bcfb8ae6e42152eeec531409faa",
	"markdown/injections.scm":        "34090de78df99b5ea84c2fbb9324f2b7c32e602043595286e83f1a1ac126073c",
	"markdown_inline/highlights.scm": "7db9147da61de7491ede9155e554dc033d6a1b86ecd1b21f6849c018be9ac875",
	"markdown_inline/injections.scm": "051f44218e0fe25cd949dfccad80699d5456ee379c7c4cfa18a5165776ecab0e",
}

const queryLicenseSHA = "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"

type archiveSpec struct {
	name     string
	root     string
	sha256   string
	data     []byte
	selectFn func(string) (string, bool)
	required []string
}

func embeddedArchive(name string) ([]byte, error) {
	data, err := embeddedAssets.ReadFile("vendor/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", name, err)
	}
	return data, nil
}

func extractArchive(destination string, spec archiveSpec) error {
	sum := sha256.Sum256(spec.data)
	if hex.EncodeToString(sum[:]) != spec.sha256 {
		return fmt.Errorf("%s checksum does not match the pinned source", spec.name)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(spec.data))
	if err != nil {
		return fmt.Errorf("open %s: %w", spec.name, err)
	}
	defer func() { _ = compressed.Close() }()

	reader := tar.NewReader(compressed)
	seen := make(map[string]bool)
	var total int64
	entries := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("extract %s: %w", spec.name, err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("extract %s: archive has too many entries", spec.name)
		}
		if header.Size < 0 || header.Size > maxArchiveFile {
			return fmt.Errorf("extract %s: entry %q has an invalid size", spec.name, header.Name)
		}
		total += header.Size
		if total > maxArchiveBytes {
			return fmt.Errorf("extract %s: expanded archive is too large", spec.name)
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		clean, err := cleanArchivePath(header.Name)
		if err != nil {
			return fmt.Errorf("extract %s: %w", spec.name, err)
		}
		if clean != spec.root && !strings.HasPrefix(clean, spec.root+"/") {
			return fmt.Errorf("extract %s: entry %q is outside the pinned archive root", spec.name, header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("extract %s: entry %q has unsupported type %d", spec.name, header.Name, header.Typeflag)
		}
		relative := strings.TrimPrefix(clean, spec.root+"/")
		targetRelative, selected := spec.selectFn(relative)
		if !selected {
			continue
		}
		if seen[targetRelative] {
			return fmt.Errorf("extract %s: duplicate selected path %q", spec.name, targetRelative)
		}
		seen[targetRelative] = true
		target, err := safeDestination(destination, targetRelative)
		if err != nil {
			return fmt.Errorf("extract %s: %w", spec.name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("extract %s: %w", spec.name, err)
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("extract %s: %w", spec.name, err)
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %s: write %q: %w", spec.name, targetRelative, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("extract %s: close %q: %w", spec.name, targetRelative, closeErr)
		}
	}
	for _, required := range spec.required {
		if !seen[required] {
			return fmt.Errorf("extract %s: pinned archive is missing %q", spec.name, required)
		}
	}
	return nil
}

func cleanArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != strings.TrimSuffix(name, "/") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}

func safeDestination(root, relative string) (string, error) {
	clean, err := cleanArchivePath(relative)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	rootWithSeparator := filepath.Clean(root) + string(os.PathSeparator)
	if !strings.HasPrefix(target, rootWithSeparator) {
		return "", fmt.Errorf("path %q escapes its destination", relative)
	}
	return target, nil
}

func writeEmbeddedFile(destination, asset, expectedSHA string) error {
	data, err := fs.ReadFile(embeddedAssets, asset)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", asset, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expectedSHA {
		return fmt.Errorf("embedded %s checksum does not match the pinned source", asset)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", destination, err)
	}
	return nil
}
