package scripts

import (
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

	transparent, opaque := false, false
	for y := icon.Bounds().Min.Y; y < icon.Bounds().Max.Y && (!transparent || !opaque); y++ {
		for x := icon.Bounds().Min.X; x < icon.Bounds().Max.X; x++ {
			_, _, _, alpha := icon.At(x, y).RGBA()
			transparent = transparent || alpha == 0
			opaque = opaque || alpha != 0
		}
	}
	if !transparent || !opaque {
		t.Fatal("VS Code extension icon must contain both transparent padding and a visible mark")
	}

	ignore := readConfig(t, "extensions/vscode-lopper/.vscodeignore")
	for _, excluded := range []string{"images/", "images/lopper-icon.png", "*.png"} {
		if strings.Contains(ignore, excluded) {
			t.Fatalf(".vscodeignore excludes the VS Code extension icon via %q", excluded)
		}
	}
	if _, err := os.Stat(repoPath(t, "extensions/vscode-lopper/images/lopper-icon.svg")); !os.IsNotExist(err) {
		t.Fatal("the extension package must ship the PNG icon, not an SVG icon")
	}
}
