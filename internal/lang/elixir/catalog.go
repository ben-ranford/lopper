package elixir

import (
	"os"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadDeclaredDependencies(repoPath string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	readAndCollect := func(path string, collect func([]byte)) error {
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		collect(content)
		return nil
	}
	if err := readAndCollect(filepath.Join(repoPath, mixLockName), func(content []byte) {
		for _, m := range quotedDepKey.FindAllSubmatch(content, -1) {
			result[normalizeDependencyID(string(m[1]))] = struct{}{}
		}
	}); err != nil {
		return nil, err
	}
	if err := readAndCollect(filepath.Join(repoPath, mixExsName), func(content []byte) {
		for _, m := range depsPattern.FindAllSubmatch(content, -1) {
			result[normalizeDependencyID(string(m[1]))] = struct{}{}
		}
	}); err != nil {
		return nil, err
	}
	return result, nil
}
