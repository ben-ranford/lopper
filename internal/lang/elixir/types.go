package elixir

import (
	"regexp"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

const (
	mixExsName     = "mix.exs"
	mixLockName    = "mix.lock"
	maxDetectFiles = 1200
	maxScanFiles   = 2400
)

var (
	importPattern  = regexp.MustCompile(`(?m)^[ \t]*(alias|import|use|require)[ \t]+([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)*)`)
	aliasAsPattern = regexp.MustCompile(`\bas:\s*([A-Z][A-Za-z0-9_]*)\b`)
	appsPathRegex  = regexp.MustCompile(`apps_path:\s*["']([^"']+)["']`)
	quotedDepKey   = regexp.MustCompile(`"([a-z0-9_-]+)"\s*:`)
	depsPattern    = regexp.MustCompile(`\{\s*:([a-zA-Z0-9_]+)\s*,`)
)

type scanResult struct {
	files    []shared.FileUsage
	declared map[string]struct{}
}
