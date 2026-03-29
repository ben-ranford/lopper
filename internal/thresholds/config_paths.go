package thresholds

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func resolveConfigPath(repoPath, explicitPath string) (string, bool, error) {
	if explicitPath != "" {
		candidate := explicitPath
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(repoPath, candidate)
		}
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				return "", false, fmt.Errorf("config file not found: %s", candidate)
			}
			return "", false, fmt.Errorf(readConfigFileErrFmt, candidate, err)
		}
		return candidate, true, nil
	}

	for _, name := range []string{".lopper.yml", ".lopper.yaml", "lopper.json"} {
		candidate := filepath.Join(repoPath, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, fmt.Errorf(readConfigFileErrFmt, candidate, err)
		}
	}

	return "", false, nil
}

func readConfigFile(repoPath, path string, explicitProvided bool) ([]byte, error) {
	if !explicitProvided || isPathUnderRoot(repoPath, path) {
		return safeio.ReadFileUnder(repoPath, path)
	}
	return safeio.ReadFile(path)
}

func isPathUnderRoot(rootPath, targetPath string) bool {
	relative, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
