package elixir

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection, roots := newDetectionState()

	umbrellaOnly, err := detectFromRootFiles(repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}
	err = shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shouldSkipDir, func(path string, _ os.DirEntry) error {
		switch strings.ToLower(filepath.Base(path)) {
		case mixExsName:
			detection.Matched = true
			detection.Confidence += 10
			dir := filepath.Dir(path)
			if umbrellaOnly && samePath(dir, repoPath) {
				return nil
			}
			roots[dir] = struct{}{}
		case mixLockName:
			detection.Matched = true
			detection.Confidence += 8
		default:
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".ex" || ext == ".exs" {
				detection.Matched = true
				detection.Confidence += 2
			}
		}
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}
	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func newDetectionState() (language.Detection, map[string]struct{}) {
	return language.Detection{}, make(map[string]struct{})
}

func detectFromRootFiles(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	umbrellaOnly := false
	mixPath := filepath.Join(repoPath, mixExsName)
	if _, err := os.Stat(mixPath); err == nil {
		detection.Matched = true
		detection.Confidence += 55
		content, readErr := safeio.ReadFileUnder(repoPath, mixPath)
		if readErr != nil {
			return false, readErr
		}
		if umbrella, appsPath := detectUmbrellaAppsPath(content); umbrella {
			umbrellaOnly = true
			if err := addUmbrellaRoots(repoPath, appsPath, roots); err != nil {
				return false, err
			}
		} else {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(repoPath, mixLockName)); err == nil {
		detection.Matched = true
		detection.Confidence += 20
		if !umbrellaOnly {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return umbrellaOnly, nil
}

func detectUmbrellaAppsPath(content []byte) (bool, string) {
	raw := stripElixirComments(content)
	if !strings.Contains(raw, "apps_path:") {
		return false, ""
	}
	matches := appsPathRegex.FindStringSubmatch(raw)
	if len(matches) >= 2 {
		path := strings.TrimSpace(matches[1])
		if path != "" {
			return true, path
		}
	}
	return true, "apps"
}

func stripElixirComments(content []byte) string {
	masked := sanitizeElixirSource(content)

	var stripped strings.Builder
	stripped.Grow(len(content))

	for i := 0; i < len(content); i++ {
		if content[i] == '#' && masked[i] == '#' {
			i = skipElixirComment(content, i, &stripped)
			continue
		}
		stripped.WriteByte(content[i])
	}
	return stripped.String()
}

func skipElixirComment(content []byte, start int, out *strings.Builder) int {
	i := start
	for i < len(content) && content[i] != '\n' {
		i++
	}
	if i < len(content) {
		out.WriteByte('\n')
	}
	return i
}

func addUmbrellaRoots(repoPath string, appsPath string, roots map[string]struct{}) error {
	appsRoot := filepath.Join(repoPath, appsPath)
	if !shared.IsPathWithin(repoPath, appsRoot) {
		return nil
	}
	apps, err := filepath.Glob(filepath.Join(appsRoot, "*"))
	if err != nil {
		return err
	}
	for _, app := range apps {
		info, statErr := os.Stat(app)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(app, mixExsName)); err == nil {
			roots[filepath.Clean(app)] = struct{}{}
		}
	}
	return nil
}

func shouldSkipDir(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "_build", "deps", ".elixir_ls":
		return true
	default:
		return shared.ShouldSkipCommonDir(lower)
	}
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
