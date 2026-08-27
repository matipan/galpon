package piagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/matipan/galpon/internal/config"
)

const (
	skipPackageSetupEnv = "GALPON_TEST_SKIP_PI_PACKAGE_SETUP"
	bundledTodoName     = "@matipan/rpiv-todo"
	bundledTodoVersion  = "2.7.1-galpon.1"
)

type requiredPackage struct {
	Source  string
	Name    string
	Version string
}

var requiredPackages = []requiredPackage{
	{Source: "npm:pi-image-tools@1.4.0", Name: "pi-image-tools", Version: "1.4.0"},
	{Source: "npm:pi-mcp-adapter@2.27.0", Name: "pi-mcp-adapter", Version: "2.27.0"},
	{Source: "npm:pi-web-access@0.24.2", Name: "pi-web-access", Version: "0.24.2"},
}

var packageCommand = runPiPackageCommand

var replacedPackages = []string{
	"npm:pi-image-preview",
	"npm:pi-image-paste",
	"npm:@juicesharp/rpiv-todo",
}

type piSettings struct {
	Packages []any `json:"packages"`
}

type packageManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Pi      struct {
		Extensions []string `json:"extensions"`
	} `json:"pi"`
}

func EnsureRequiredPackages(ctx context.Context, cfg config.Config) error {
	skipRequiredPackages := os.Getenv(skipPackageSetupEnv) == "1"
	if skipRequiredPackages && strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")) == "" {
		return fmt.Errorf("%s requires an isolated PI_CODING_AGENT_DIR", skipPackageSetupEnv)
	}
	configDir, err := piConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(configDir, ".galpon-package-setup.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock Pi package setup: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	bundledTodoPath, err := materializeBundledTodoPackage(configDir)
	if err != nil {
		return err
	}
	settings, err := readPiSettings(configDir)
	if err != nil {
		return err
	}
	settings, err = normalizeRequiredPackageSettings(configDir, settings, bundledTodoPath)
	if err != nil {
		return err
	}
	if !hasExactStringPackage(settings, bundledTodoPath) || !validBundledTodoPackage(bundledTodoPath) {
		return fmt.Errorf("bundled Pi TODO package is not available: %s", bundledTodoPath)
	}
	if skipRequiredPackages {
		return nil
	}
	missing := missingRequiredPackages(configDir, settings)
	legacy := installedLegacyPackages(settings)
	if len(missing) == 0 && len(legacy) == 0 {
		return nil
	}
	if offlineEnabled() && len(missing) != 0 {
		return fmt.Errorf("required Pi packages are not available offline: %s; start Galpon once without PI_OFFLINE", strings.Join(missing, ", "))
	}
	for _, source := range missing {
		if err := packageCommand(ctx, cfg.PiBin, "install", source, "--no-approve"); err != nil {
			return err
		}
	}
	for _, source := range legacy {
		if err := packageCommand(ctx, cfg.PiBin, "remove", source); err != nil {
			return err
		}
	}
	settings, err = readPiSettings(configDir)
	if err != nil {
		return err
	}
	if remaining := missingRequiredPackages(configDir, settings); len(remaining) != 0 {
		return fmt.Errorf("pi package setup finished without required packages: %s", strings.Join(remaining, ", "))
	}
	if remaining := installedLegacyPackages(settings); len(remaining) != 0 {
		return fmt.Errorf("pi package setup did not remove replaced packages: %s", strings.Join(remaining, ", "))
	}
	if !hasExactStringPackage(settings, bundledTodoPath) || !validBundledTodoPackage(bundledTodoPath) {
		return fmt.Errorf("pi package setup did not retain the bundled TODO package: %s", bundledTodoPath)
	}
	return nil
}

// Keep one package source for all Galpon state roots that use this Pi configuration.
func materializeBundledTodoPackage(configDir string) (string, error) {
	packagePath := filepath.Join(configDir, "galpon", "packages", "rpiv-todo-"+bundledTodoVersion)
	if err := materializeDirectory("builtin/rpiv-todo", packagePath); err != nil {
		return "", fmt.Errorf("install bundled rpiv-todo: %w", err)
	}
	return packagePath, nil
}

func piConfigDir() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); configured != "" {
		return filepath.Abs(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

func readPiSettings(configDir string) (piSettings, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if os.IsNotExist(err) {
		return piSettings{}, nil
	}
	if err != nil {
		return piSettings{}, fmt.Errorf("read Pi settings: %w", err)
	}
	var settings piSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return piSettings{}, fmt.Errorf("parse Pi settings: %w", err)
	}
	return settings, nil
}

func normalizeRequiredPackageSettings(configDir string, settings piSettings, bundledTodoPath string) (piSettings, error) {
	settingsPath := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	document := make(map[string]any)
	settingsMissing := os.IsNotExist(err)
	if err != nil && !settingsMissing {
		return piSettings{}, fmt.Errorf("read Pi settings: %w", err)
	}
	if !settingsMissing {
		if err := json.Unmarshal(data, &document); err != nil {
			return piSettings{}, fmt.Errorf("parse Pi settings: %w", err)
		}
	}
	entries, _ := document["packages"].([]any)
	changed := false
	for _, required := range requiredPackages {
		valid := validNPMPackage(configDir, required)
		next := make([]any, 0, len(entries)+1)
		inserted := false
		for _, entry := range entries {
			source := packageEntrySource(entry)
			if npmIdentity(source) != required.Name {
				next = append(next, entry)
				continue
			}
			if valid && !inserted {
				next = append(next, required.Source)
				inserted = true
				if value, ok := entry.(string); !ok || value != required.Source {
					changed = true
				}
			} else {
				changed = true
			}
		}
		if valid && !inserted {
			next = append(next, required.Source)
			changed = true
		}
		entries = next
	}
	if validBundledTodoPackage(bundledTodoPath) {
		next := make([]any, 0, len(entries)+1)
		inserted := false
		for _, entry := range entries {
			source := packageEntrySource(entry)
			if !isBundledTodoPackageSource(configDir, source) {
				next = append(next, entry)
				continue
			}
			if sameLocalPackageSource(configDir, source, bundledTodoPath) && !inserted {
				next = append(next, bundledTodoPath)
				inserted = true
				if value, ok := entry.(string); !ok || value != bundledTodoPath {
					changed = true
				}
			} else {
				changed = true
			}
		}
		if !inserted {
			next = append(next, bundledTodoPath)
			changed = true
		}
		entries = next
	}
	if !changed && !settingsMissing {
		return settings, nil
	}
	document["packages"] = entries
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return piSettings{}, err
	}
	temporary := settingsPath + ".galpon.tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return piSettings{}, fmt.Errorf("write Pi settings: %w", err)
	}
	if err := os.Rename(temporary, settingsPath); err != nil {
		_ = os.Remove(temporary)
		return piSettings{}, fmt.Errorf("replace Pi settings: %w", err)
	}
	return readPiSettings(configDir)
}

func packageEntrySource(entry any) string {
	switch value := entry.(type) {
	case string:
		return value
	case map[string]any:
		source, _ := value["source"].(string)
		return source
	default:
		return ""
	}
}

func packageSources(settings piSettings) []string {
	values := make([]string, 0, len(settings.Packages))
	for _, entry := range settings.Packages {
		if source := packageEntrySource(entry); source != "" {
			values = append(values, source)
		}
	}
	return values
}

func missingRequiredPackages(configDir string, settings piSettings) []string {
	missing := make([]string, 0)
	for _, required := range requiredPackages {
		if !hasExactStringPackage(settings, required.Source) || !validNPMPackage(configDir, required) {
			missing = append(missing, required.Source)
		}
	}
	return missing
}

func hasExactStringPackage(settings piSettings, source string) bool {
	for _, entry := range settings.Packages {
		if value, ok := entry.(string); ok && value == source {
			return true
		}
	}
	return false
}

func installedLegacyPackages(settings piSettings) []string {
	sources := packageSources(settings)
	legacy := make([]string, 0)
	for _, replaced := range replacedPackages {
		for _, source := range sources {
			if npmIdentity(source) == npmIdentity(replaced) {
				legacy = append(legacy, replaced)
				break
			}
		}
	}
	return legacy
}

func npmIdentity(source string) string {
	value := strings.TrimPrefix(source, "npm:")
	if strings.HasPrefix(value, "@") {
		if slash := strings.Index(value, "/"); slash >= 0 {
			if version := strings.Index(value[slash+1:], "@"); version >= 0 {
				return value[:slash+1+version]
			}
		}
		return value
	}
	if version := strings.Index(value, "@"); version >= 0 {
		return value[:version]
	}
	return value
}

func sameLocalPackageSource(configDir, source, requiredPath string) bool {
	resolved, ok := localPackagePath(configDir, source)
	if !ok {
		return false
	}
	required, err := filepath.Abs(requiredPath)
	return err == nil && filepath.Clean(resolved) == filepath.Clean(required)
}

func isBundledTodoPackageSource(configDir, source string) bool {
	resolved, ok := localPackagePath(configDir, source)
	if !ok {
		return false
	}
	if validBundledTodoPackage(resolved) {
		return true
	}
	return strings.HasSuffix(filepath.ToSlash(resolved), "/runtime/pi/packages/rpiv-todo-"+bundledTodoVersion)
}

func localPackagePath(configDir, source string) (string, bool) {
	if source == "" || strings.Contains(source, ":") {
		return "", false
	}
	resolved := source
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(configDir, resolved)
	}
	absolute, err := filepath.Abs(resolved)
	return absolute, err == nil
}

func validBundledTodoPackage(packagePath string) bool {
	return validPiPackage(filepath.Join(packagePath, "package.json"), bundledTodoName, bundledTodoVersion)
}

func validNPMPackage(configDir string, required requiredPackage) bool {
	manifestPath := filepath.Join(configDir, "npm", "node_modules", filepath.FromSlash(required.Name), "package.json")
	return validPiPackage(manifestPath, required.Name, required.Version)
}

func validPiPackage(manifestPath, name, version string) bool {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return false
	}
	var manifest packageManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Name != name || manifest.Version != version || len(manifest.Pi.Extensions) == 0 {
		return false
	}
	for _, extension := range manifest.Pi.Extensions {
		if strings.ContainsAny(extension, "*?!") {
			continue
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(strings.TrimPrefix(extension, "./")))); err != nil {
			return false
		}
	}
	return true
}

func runPiPackageCommand(ctx context.Context, piBin string, args ...string) error {
	command := exec.CommandContext(ctx, piBin, args...)
	command.Env = append(os.Environ(), "PI_TELEMETRY=0", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if len(message) > 2000 {
		message = message[len(message)-2000:]
	}
	return fmt.Errorf("pi package command %q failed: %w: %s", strings.Join(args, " "), err, message)
}

func offlineEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PI_OFFLINE"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
