package scripts

import (
	"log"
	"os"
	"testing"
)

// Only compiler/test cache entries are shared. Each benchmark fixture retains
// its own HOME, repository, worktrees and assertion output.
var benchFixtureGoCache string

func TestMain(m *testing.M) {
	cacheDir, err := os.MkdirTemp("", "lopper-scripts-go-cache-")
	if err != nil {
		log.Printf("create benchmark fixture Go cache: %v", err)
		os.Exit(1)
	}
	benchFixtureGoCache = cacheDir
	code := m.Run()
	if err := os.RemoveAll(cacheDir); err != nil {
		log.Printf("remove benchmark fixture Go cache: %v", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
