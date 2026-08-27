package piagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	bundledTodo := filepath.Join(configDir, "galpon", "packages", "rpiv-todo-"+bundledTodoVersion)
	if !hasExactStringPackage(settings, bundledTodo) {
		t.Fatalf("bundled TODO package was not registered globally: %#v", settings.Packages)
	}
	if strings.Contains(bundledTodo, stateDir) {
		t.Fatalf("bundled TODO package depends on the Galpon state directory: %s", bundledTodo)
	}
}

func TestEnsureRequiredPackagesRejectsUnisolatedTestSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv(skipPackageSetupEnv, "1")
	if err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: t.TempDir(), PiBin: "pi"}); err == nil || !strings.Contains(err.Error(), "requires an isolated PI_CODING_AGENT_DIR") {
		t.Fatalf("unisolated test setup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("unisolated test setup touched user settings: %v", err)
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
	bundledTodo, err := materializeBundledTodoPackage(configDir)
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
		map[string]any{"source": bundledTodo, "extensions": []any{}},
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
	if !hasExactStringPackage(normalized, bundledTodo) {
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

func TestMaterializeBundledTodoPackageIncludesExtension(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	packagePath, err := materializeBundledTodoPackage(configDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(packagePath, "index.ts"), filepath.Join(packagePath, "integrations", "galpon.ts"), filepath.Join(packagePath, "LICENSE")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("bundled todo file %s: %v", path, err)
		}
	}
}

func TestEnsureRequiredPackagesUsesOneCanonicalTodoAcrossStateDirectories(t *testing.T) {
	configDir := t.TempDir()
	stateA := t.TempDir()
	stateB := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", configDir)
	t.Setenv(skipPackageSetupEnv, "1")

	for _, stateDir := range []string{stateA, stateB} {
		if err := EnsureRequiredPackages(context.Background(), config.Config{StateDir: stateDir, PiBin: "pi"}); err != nil {
			t.Fatal(err)
		}
	}

	settings, err := readPiSettings(configDir)
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(configDir, "galpon", "packages", "rpiv-todo-"+bundledTodoVersion)
	count := 0
	for _, entry := range settings.Packages {
		source := packageEntrySource(entry)
		if isBundledTodoPackageSource(configDir, source) {
			count++
			if source != canonical {
				t.Fatalf("bundled TODO source = %q, want %q", source, canonical)
			}
		}
	}
	if count != 1 {
		t.Fatalf("bundled TODO source count = %d in %#v", count, settings.Packages)
	}
	for _, stateDir := range []string{stateA, stateB} {
		legacy := filepath.Join(stateDir, "runtime", "pi", "packages", "rpiv-todo-"+bundledTodoVersion)
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("state-local TODO package exists at %s: %v", legacy, err)
		}
	}

	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Log("Pi is not installed; direct-session package check skipped")
		return
	}
	command := exec.Command(pi, "--mode", "rpc", "--no-session")
	command.Env = isolatedPiEnvironment(configDir, t.TempDir())
	command.Stdin = strings.NewReader("{\"type\":\"get_commands\"}\n")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("direct Pi session failed: %v\n%s", err, stderr.String())
	}
	var response struct {
		Data struct {
			Commands []struct {
				Name       string `json:"name"`
				SourceInfo struct {
					Path   string `json:"path"`
					Source string `json:"source"`
				} `json:"sourceInfo"`
			} `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &response); err != nil {
		t.Fatalf("parse direct Pi response: %v\n%s", err, output)
	}
	todoCommands := 0
	for _, command := range response.Data.Commands {
		if command.Name != "todos" && !strings.HasPrefix(command.Name, "todos:") {
			continue
		}
		todoCommands++
		if command.SourceInfo.Source != canonical || command.SourceInfo.Path != filepath.Join(canonical, "index.ts") {
			t.Fatalf("direct Pi TODO source = %#v, want canonical package %s", command.SourceInfo, canonical)
		}
	}
	if todoCommands != 1 {
		t.Fatalf("direct Pi TODO command count = %d in %#v", todoCommands, response.Data.Commands)
	}
}

func isolatedPiEnvironment(configDir, xdgConfigDir string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "PI_CODING_AGENT_DIR=") || strings.HasPrefix(value, "XDG_CONFIG_HOME=") || strings.HasPrefix(value, "PI_TELEMETRY=") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment,
		"PI_CODING_AGENT_DIR="+configDir,
		"XDG_CONFIG_HOME="+xdgConfigDir,
		"PI_TELEMETRY=0",
	)
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
