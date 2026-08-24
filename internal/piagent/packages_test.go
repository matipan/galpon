package piagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/matipan/galpon/internal/config"
)

func TestEnsureRequiredPackagesAcceptsPinnedInstallation(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	t.Setenv("PI_OFFLINE", "1")
	writeRequiredPackageFixture(t, configDir)
	called := false
	previous := packageCommand
	packageCommand = func(context.Context, string, ...string) error { called = true; return nil }
	t.Cleanup(func() { packageCommand = previous })
	if err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: stateDir, PiBin: "pi"}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("valid package setup executed Pi")
	}
	settings, err := readPiSettings(configDir)
	if err != nil {
		t.Fatal(err)
	}
	bundledTodo := filepath.Join(stateDir, "runtime", "pi", "packages", "rpiv-todo-2.7.1-galpon.1")
	if !hasExactStringPackage(settings, bundledTodo) {
		t.Fatalf("bundled TODO package was not registered globally: %#v", settings.Packages)
	}
}

func TestEnsureRequiredPackagesFailsOfflineWithoutNetwork(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	t.Setenv("PI_OFFLINE", "true")
	called := false
	previous := packageCommand
	packageCommand = func(context.Context, string, ...string) error { called = true; return nil }
	t.Cleanup(func() { packageCommand = previous })
	err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: t.TempDir(), PiBin: "pi"})
	if err == nil {
		t.Fatal("missing offline packages succeeded")
	}
	if called {
		t.Fatal("offline setup executed Pi")
	}
}

func TestEnsureRequiredPackagesNormalizesFilteredObjectEntry(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	t.Setenv("PI_OFFLINE", "1")
	writeRequiredPackageFixture(t, configDir)
	settingsPath := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	packages := settings["packages"].([]any)
	packages[0] = map[string]any{"source": requiredPackages[0].Source, "extensions": []any{}}
	writeJSON(t, settingsPath, settings)
	if err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: t.TempDir(), PiBin: "pi"}); err != nil {
		t.Fatal(err)
	}
	normalized, err := readPiSettings(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasExactStringPackage(normalized, requiredPackages[0].Source) {
		t.Fatalf("filtered package was not normalized: %#v", normalized.Packages)
	}
}

func TestEnsureRequiredPackagesNormalizesFilteredBundledTodoEntry(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	t.Setenv("PI_OFFLINE", "1")
	writeRequiredPackageFixture(t, configDir)
	values, err := Materialize(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	staleTodo := filepath.Join(t.TempDir(), "runtime", "pi", "packages", "rpiv-todo-2.7.1-galpon.1")
	settings["packages"] = append(settings["packages"].([]any),
		map[string]any{"source": values.TodoPackage, "extensions": []any{}},
		staleTodo,
	)
	writeJSON(t, settingsPath, settings)
	if err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: stateDir, PiBin: "pi"}); err != nil {
		t.Fatal(err)
	}
	normalized, err := readPiSettings(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasExactStringPackage(normalized, values.TodoPackage) {
		t.Fatalf("filtered bundled TODO package was not normalized: %#v", normalized.Packages)
	}
	if hasExactStringPackage(normalized, staleTodo) {
		t.Fatalf("stale bundled TODO package was not removed: %#v", normalized.Packages)
	}
}

func TestEnsureRequiredPackagesRepairsWrongVersionOnline(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	writeRequiredPackageFixture(t, configDir)
	wrong := requiredPackages[0]
	wrong.Version = "0.0.0"
	writeRequiredPackageManifest(t, configDir, wrong)
	var calls [][]string
	previous := packageCommand
	packageCommand = func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, slices.Clone(args))
		if len(args) >= 2 && args[0] == "install" && args[1] == requiredPackages[0].Source {
			settings, err := readPiSettings(configDir)
			if err != nil {
				return err
			}
			settings.Packages = append(settings.Packages, requiredPackages[0].Source)
			writeJSON(t, filepath.Join(configDir, "settings.json"), settings)
			writeRequiredPackageManifest(t, configDir, requiredPackages[0])
		}
		return nil
	}
	t.Cleanup(func() { packageCommand = previous })
	if err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: t.TempDir(), PiBin: "pi"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) < 2 || calls[0][0] != "install" || calls[0][1] != requiredPackages[0].Source {
		t.Fatalf("package repair calls = %#v", calls)
	}
}

func TestInstalledLegacyPackagesMatchesPinnedSources(t *testing.T) {
	settings := piSettings{Packages: []any{
		"npm:pi-image-preview@0.1.5",
		map[string]any{"source": "npm:@juicesharp/rpiv-todo@2.7.1"},
		"npm:unrelated@1.0.0",
	}}
	got := installedLegacyPackages(settings)
	for _, want := range []string{"npm:pi-image-preview", "npm:@juicesharp/rpiv-todo"} {
		if !slices.Contains(got, want) {
			t.Fatalf("legacy packages %v omitted %s", got, want)
		}
	}
}

func TestMaterializeIncludesBundledTodoExtension(t *testing.T) {
	values, err := Materialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{values.TodoExtension, filepath.Join(values.TodoPackage, "integrations", "galpon.ts"), filepath.Join(values.TodoPackage, "LICENSE")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("bundled todo file %s: %v", path, err)
		}
	}
}

func writeRequiredPackageFixture(t *testing.T, configDir string) {
	t.Helper()
	packages := make([]any, 0, len(requiredPackages))
	for _, required := range requiredPackages {
		packages = append(packages, required.Source)
		writeRequiredPackageManifest(t, configDir, required)
	}
	writeJSON(t, filepath.Join(configDir, "settings.json"), map[string]any{"packages": packages})
}

func writeRequiredPackageManifest(t *testing.T, configDir string, required requiredPackage) {
	t.Helper()
	root := filepath.Join(configDir, "npm", "node_modules", filepath.FromSlash(required.Name))
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(root, "package.json"), map[string]any{
		"name": required.Name, "version": required.Version,
		"pi": map[string]any{"extensions": []string{"./index.ts"}},
	})
	if err := os.WriteFile(filepath.Join(root, "index.ts"), []byte("export default () => {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
