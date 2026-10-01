package testutil

import (
	"path/filepath"
	"testing"
)

// WriteLodashMapFixture writes the minimal ES-module map fixture shared by
// adapter, application, and CLI codemod regressions. Git setup stays with callers.
func WriteLodashMapFixture(t *testing.T, repo, source string) string {
	t.Helper()
	sourcePath := filepath.Join(repo, "index.js")
	MustWriteFileWithModes(t, sourcePath, source, 0o644, 0o755)
	dependencyRoot := filepath.Join(repo, "node_modules", "lodash")
	MustWriteFileWithModes(t, filepath.Join(dependencyRoot, "package.json"), "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n", 0o644, 0o755)
	MustWriteFileWithModes(t, filepath.Join(dependencyRoot, "index.js"), "export { map } from './map.js'\n", 0o644, 0o755)
	MustWriteFileWithModes(t, filepath.Join(dependencyRoot, "map.js"), "export default function map() {}\n", 0o644, 0o755)
	return sourcePath
}
