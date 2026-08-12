package scripts

import (
	"errors"
	"image"
	"image/png"
	"os"
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

	if excludesExtensionIcon(readConfig(t, "extensions/vscode-lopper/.vscodeignore")) {
		t.Fatalf(".vscodeignore excludes the VS Code extension icon")
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

func excludesExtensionIcon(ignore string) bool {
	patterns := make(map[string]struct{})
	for _, line := range strings.Split(ignore, "\n") {
		patterns[strings.TrimSpace(line)] = struct{}{}
	}
	for _, excluded := range []string{"images/", "images/lopper-icon.png", "*.png"} {
		if _, exists := patterns[excluded]; exists {
			return true
		}
	}
	return false
}
