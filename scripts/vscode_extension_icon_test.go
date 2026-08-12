package scripts

import (
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVSCodeExtensionIconPackageContract(t *testing.T) {
	t.Parallel()

	const canonicalMark = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <path fill="#5fc9a7" d="M8 10h48L24 54H8z"/>
</svg>`
	if got := strings.TrimSpace(readConfig(t, "assets/lopper-favicon.svg")); got != canonicalMark {
		t.Fatalf("canonical Lopper favicon mark = %q, want %q", got, canonicalMark)
	}

	var manifest struct {
		Icon string `json:"icon"`
	}
	readJSONConfig(t, "extensions/vscode-lopper/package.json", &manifest)
	if manifest.Icon != "images/lopper-icon.png" {
		t.Fatalf("VS Code extension icon = %q, want images/lopper-icon.png", manifest.Icon)
	}

	iconFile, err := os.Open(repoPath(t, "extensions/vscode-lopper/"+manifest.Icon))
	if err != nil {
		t.Fatalf("open VS Code extension icon: %v", err)
	}
	defer func() {
		if err := iconFile.Close(); err != nil {
			t.Errorf("close VS Code extension icon: %v", err)
		}
	}()
	icon, err := png.Decode(iconFile)
	if err != nil {
		t.Fatalf("decode VS Code extension icon as PNG: %v", err)
	}
	if bounds := icon.Bounds(); bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Fatalf("VS Code extension icon dimensions = %dx%d, want 256x256", bounds.Dx(), bounds.Dy())
	}

	if !hasTransparentAndOpaquePixels(icon) {
		t.Fatal("VS Code extension icon must contain transparent padding and a near-opaque visible mark")
	}

	if !extensionPackageContains(t, repoPath(t, "extensions/vscode-lopper"), manifest.Icon) {
		t.Fatalf("VS Code extension package does not contain %q", manifest.Icon)
	}
	if _, err := os.Stat(repoPath(t, "extensions/vscode-lopper/images/lopper-icon.svg")); err == nil {
		t.Fatal("the extension package must ship the PNG icon, not an SVG icon")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat SVG icon path: %v", err)
	}
}

func hasTransparentAndOpaquePixels(icon image.Image) bool {
	transparent, opaque := false, false
	for y := icon.Bounds().Min.Y; y < icon.Bounds().Max.Y && (!transparent || !opaque); y++ {
		for x := icon.Bounds().Min.X; x < icon.Bounds().Max.X; x++ {
			_, _, _, alpha := icon.At(x, y).RGBA()
			transparent = transparent || alpha == 0
			opaque = opaque || alpha >= 0xff00
		}
	}
	return transparent && opaque
}

func extensionPackageContains(t *testing.T, extensionDir, file string) bool {
	t.Helper()

	vsce := repoPath(t, "extensions/vscode-lopper/node_modules/.bin/vsce")
	command := exec.Command(vsce, "ls")
	command.Dir = extensionDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list VS Code extension package files: %v\n%s", err, output)
	}
	for _, packagedFile := range strings.Fields(string(output)) {
		if packagedFile == file {
			return true
		}
	}
	return false
}

func TestVSCodeExtensionPackagingHonorsBraceGlobIgnore(t *testing.T) {
	t.Parallel()

	extensionDir := t.TempDir()
	for name, contents := range map[string]string{
		"package.json":  `{"name":"icon-fixture","displayName":"Icon fixture","version":"1.0.0","publisher":"lopper","engines":{"vscode":"^1.85.0"},"icon":"images/lopper-icon.png"}`,
		"README.md":     "# Icon fixture\n",
		".vscodeignore": "images/*.{png,jpg}\n",
	} {
		if err := os.WriteFile(filepath.Join(extensionDir, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(extensionDir, "images"), 0o700); err != nil {
		t.Fatalf("create fixture images directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "images", "lopper-icon.png"), []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture icon: %v", err)
	}

	if extensionPackageContains(t, extensionDir, "images/lopper-icon.png") {
		t.Fatal("VSCE must omit an icon matched by a brace glob in .vscodeignore")
	}
}
