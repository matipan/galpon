package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

// Build from Go's module archive format, not a checkout. In particular, files
// under vendor can work in the checkout and be absent from go install @latest.
func TestInstallFromModuleArchive(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	const modulePath = "github.com/matipan/galpon"
	list := exec.CommandContext(t.Context(), "go", "list", "-deps", "-json", "./cmd/galpon")
	list.Dir = root
	list.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=readonly -buildvcs=false")
	var stderr bytes.Buffer
	list.Stderr = &stderr
	data, err := list.Output()
	if err != nil {
		t.Fatalf("list install inputs: %v\n%s", err, stderr.String())
	}
	// Enumerate build inputs so local experiments, node_modules, and other
	// ignored files cannot leak into the archive. The Go zip writer, not this
	// list, decides which of those inputs are included in a module download.
	names := map[string]bool{"go.mod": true, "go.sum": true}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var pkg struct {
			Dir        string
			GoFiles    []string
			EmbedFiles []string
			Module     *struct{ Path string }
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if pkg.Module == nil || pkg.Module.Path != modulePath {
			continue
		}
		for _, name := range append(pkg.GoFiles, pkg.EmbedFiles...) {
			relative, err := filepath.Rel(root, filepath.Join(pkg.Dir, name))
			if err != nil {
				t.Fatal(err)
			}
			names[filepath.ToSlash(relative)] = true
		}
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	files := make([]modzip.File, 0, len(paths))
	for _, name := range paths {
		files = append(files, installModuleFile{root: root, name: name})
	}
	var archive bytes.Buffer
	version := module.Version{Path: modulePath, Version: "v0.0.0-install-check"}
	if err := modzip.Create(&archive, version, files); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "module.zip")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "source")
	if err := modzip.Unzip(source, version, archivePath); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(directory, "bin")
	install := exec.CommandContext(t.Context(), "go", "install", "./cmd/galpon")
	install.Dir = source
	install.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=readonly -buildvcs=false", "GOBIN="+bin)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install from module archive: %v\n%s", err, output)
	}
	command := exec.CommandContext(t.Context(), filepath.Join(bin, "galpon"), "version")
	command.Env = append(os.Environ(), "GALPON_STATE_DIR="+filepath.Join(directory, "state"))
	if output, err := command.CombinedOutput(); err != nil || !strings.HasPrefix(string(output), "galpon ") {
		t.Fatalf("installed binary: %v\n%s", err, output)
	}
}

type installModuleFile struct {
	root string
	name string
}

func (f installModuleFile) Path() string { return f.name }
func (f installModuleFile) Lstat() (os.FileInfo, error) {
	return os.Lstat(filepath.Join(f.root, filepath.FromSlash(f.name)))
}
func (f installModuleFile) Open() (io.ReadCloser, error) {
	return os.Open(filepath.Join(f.root, filepath.FromSlash(f.name)))
}
