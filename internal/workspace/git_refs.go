package workspace

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func resolveRefSHA(gitDir, ref string) (string, error) {
	dirs := candidateGitDirs(gitDir)
	if sha, refErr := resolveLooseRefSHA(dirs, ref); sha != "" {
		return sha, nil
	} else if sha, packedErr := resolvePackedRefSHA(dirs, ref); sha != "" {
		return sha, nil
	} else {
		return "", resolveRefLookupError(ref, refErr, packedErr)
	}
}

func resolveLooseRefSHA(dirs []string, ref string) (string, error) {
	var firstErr error
	for _, dir := range dirs {
		refValue, err := readGitPath(dir, ref)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sha := strings.TrimSpace(refValue)
		if validSHA(sha) {
			return sha, nil
		}
	}
	return "", firstErr
}

func resolvePackedRefSHA(dirs []string, ref string) (string, error) {
	var firstErr error
	for _, dir := range dirs {
		packedRefs, err := readGitPath(dir, "packed-refs")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if sha := findPackedRefSHA(packedRefs, ref); sha != "" {
			return sha, nil
		}
	}
	return "", firstErr
}

func findPackedRefSHA(packedRefs, ref string) string {
	for _, line := range strings.Split(packedRefs, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != ref {
			continue
		}
		if validSHA(fields[0]) {
			return fields[0]
		}
	}
	return ""
}

func resolveRefLookupError(ref string, refErr, packedErr error) error {
	if refErr != nil && !errors.Is(refErr, os.ErrNotExist) {
		return refErr
	}
	if packedErr != nil && !errors.Is(packedErr, os.ErrNotExist) {
		return packedErr
	}
	return fmt.Errorf("ref %s not found", ref)
}

func candidateGitDirs(gitDir string) []string {
	dirs := []string{gitDir}
	commonDir, err := resolveCommonGitDir(gitDir)
	if err != nil || commonDir == "" || commonDir == gitDir {
		return dirs
	}
	return append(dirs, commonDir)
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
