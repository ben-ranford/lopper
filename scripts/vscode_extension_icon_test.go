package scripts

import (
	"errors"
	"image"
	"image/png"
	"os"
	"path"
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
	excluded := false
	for _, line := range strings.Split(ignore, "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}

		include := strings.HasPrefix(pattern, "!")
		if include {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		if extensionIgnorePatternMatchesPath(pattern, "images/lopper-icon.png") {
			excluded = !include
		}
	}
	return excluded
}

func extensionIgnorePatternMatchesPath(pattern, filePath string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	directoryPattern := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return false
	}

	patternParts := strings.Split(pattern, "/")
	fileParts := strings.Split(filePath, "/")
	if len(patternParts) == 1 {
		for _, filePart := range fileParts {
			matched, err := path.Match(pattern, filePart)
			if err == nil && matched {
				return true
			}
		}
		return false
	}

	if directoryPattern {
		patternParts = append(patternParts, "**")
	}
	return extensionIgnorePathPartsMatch(patternParts, fileParts)
}

func extensionIgnorePathPartsMatch(patternParts, fileParts []string) bool {
	if len(patternParts) == 0 {
		return len(fileParts) == 0
	}
	if patternParts[0] == "**" {
		return extensionIgnorePathPartsMatch(patternParts[1:], fileParts) ||
			(len(fileParts) > 0 && extensionIgnorePathPartsMatch(patternParts, fileParts[1:]))
	}
	if len(fileParts) == 0 {
		return false
	}
	matched, err := path.Match(patternParts[0], fileParts[0])
	return err == nil && matched && extensionIgnorePathPartsMatch(patternParts[1:], fileParts[1:])
}

func TestExcludesExtensionIcon(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		ignore string
		want   bool
	}{
		{name: "literal icon", ignore: "images/lopper-icon.png", want: true},
		{name: "directory glob", ignore: "images/**", want: true},
		{name: "extension glob", ignore: "**/*.png", want: true},
		{name: "unexcluded icon", ignore: "images/**\n!images/lopper-icon.png", want: false},
		{name: "unrelated pattern", ignore: "**/*.map", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := excludesExtensionIcon(tc.ignore); got != tc.want {
				t.Fatalf("excludesExtensionIcon(%q) = %t, want %t", tc.ignore, got, tc.want)
			}
		})
	}
}
