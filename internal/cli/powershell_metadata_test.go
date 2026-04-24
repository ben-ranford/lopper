package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type extensionLanguageProperty struct {
	Enum             []string `json:"enum"`
	EnumDescriptions []string `json:"enumDescriptions"`
}

type extensionConfiguration struct {
	Properties map[string]extensionLanguageProperty `json:"properties"`
}

type extensionContributes struct {
	Configuration extensionConfiguration `json:"configuration"`
}

type extensionPackageManifest struct {
	Contributes extensionContributes `json:"contributes"`
}

func TestPowerShellAdapterUserFacingListsIncludePowerShell(t *testing.T) {
	if !strings.Contains(Usage(), "powershell") {
		t.Fatalf("expected CLI usage text to include powershell")
	}

	checks := map[string]string{
		"README.md":             "`powershell`",
		"docs/extensibility.md": "`powershell`",
		"docs/report-schema.md": "`powershell`",
		"CONTRIBUTING.md":       "`powershell`",
	}
	for relPath, expected := range checks {
		content := string(mustReadRepoFile(t, relPath))
		if !strings.Contains(content, expected) {
			t.Fatalf("expected %s to contain %q", relPath, expected)
		}
	}
}

func TestPowerShellAdapterVSCodeMetadataIncludesPowerShell(t *testing.T) {
	manifestData := mustReadRepoFile(t, "extensions/vscode-lopper/package.json")
	var manifest extensionPackageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("unmarshal extension package.json: %v", err)
	}

	language, ok := manifest.Contributes.Configuration.Properties["lopper.language"]
	if !ok {
		t.Fatalf("expected lopper.language property in extension package metadata")
	}
	if !slices.Contains(language.Enum, "powershell") {
		t.Fatalf("expected powershell in extension language enum, got %#v", language.Enum)
	}
	index := slices.Index(language.Enum, "powershell")
	if index < 0 || index >= len(language.EnumDescriptions) {
		t.Fatalf("expected powershell enum description at matching index, enum=%#v descriptions=%#v", language.Enum, language.EnumDescriptions)
	}
	if !strings.Contains(strings.ToLower(language.EnumDescriptions[index]), "powershell") {
		t.Fatalf("expected powershell enum description, got %q", language.EnumDescriptions[index])
	}

	configSource := string(mustReadRepoFile(t, "extensions/vscode-lopper/src/languageConfiguration.ts"))
	for _, expected := range []string{"\"powershell\"", "\".ps1\"", "\".psd1\"", "\".psm1\""} {
		if !strings.Contains(configSource, expected) {
			t.Fatalf("expected languageConfiguration.ts to contain %q", expected)
		}
	}
}

func mustReadRepoFile(t *testing.T, relativePath string) []byte {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("resolve caller path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	data, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return data
}
