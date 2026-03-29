package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const defaultTraceRelPath = ".artifacts/lopper-runtime.ndjson"
const runtimeBinDirsEnvKey = "LOPPER_RUNTIME_BIN_DIRS"
const defaultTrustedRuntimeBinDirs = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"

var runtimeExecutableAllowlist = map[string]struct{}{
	"npm":    {},
	"pnpm":   {},
	"yarn":   {},
	"bun":    {},
	"npx":    {},
	"node":   {},
	"vitest": {},
	"jest":   {},
	"mocha":  {},
	"ava":    {},
	"deno":   {},
	"make":   {},
}

type CaptureRequest struct {
	RepoPath  string
	TracePath string
	Command   string
}

func DefaultTracePath(repoPath string) string {
	return filepath.Join(repoPath, defaultTraceRelPath)
}

func Capture(ctx context.Context, req CaptureRequest) error {
	repoPath := strings.TrimSpace(req.RepoPath)
	tracePath := strings.TrimSpace(req.TracePath)
	command := strings.TrimSpace(req.Command)
	if repoPath == "" {
		return fmt.Errorf("repo path is required")
	}
	if command == "" {
		return fmt.Errorf("runtime test command is required")
	}
	if tracePath == "" {
		tracePath = DefaultTracePath(repoPath)
	}

	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		return fmt.Errorf("create runtime trace directory: %w", err)
	}
	if err := os.Remove(tracePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous runtime trace: %w", err)
	}

	cmd, err := buildRuntimeCommand(ctx, command)
	if err != nil {
		return err
	}
	cmd.Dir = repoPath
	cmd.Env = withRuntimeTraceEnv(os.Environ(), tracePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return fmt.Errorf("runtime test command failed: %w", err)
		}
		return fmt.Errorf("runtime test command failed: %w: %s", err, trimmed)
	}

	return nil
}

func buildRuntimeCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	fields, err := parseRuntimeCommand(command)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("runtime test command is required")
	}

	executable := fields[0]
	args := fields[1:]
	executablePath, err := resolveRuntimeExecutablePath(executable, runtimeSearchDirs())
	if err != nil {
		return nil, err
	}

	cmd, err := newAllowlistedRuntimeCommand(ctx, executable)
	if err != nil {
		return nil, err
	}
	cmd.Path = executablePath
	cmd.Args = append([]string{executablePath}, args...)
	return cmd, nil
}

type runtimeCommandParser struct {
	fields        []string
	current       strings.Builder
	inSingleQuote bool
	inDoubleQuote bool
	escaped       bool
	sawToken      bool
}

func parseRuntimeCommand(command string) ([]string, error) {
	var parser runtimeCommandParser
	for _, ch := range command {
		parser.consume(ch)
	}

	if parser.escaped {
		return nil, fmt.Errorf("runtime test command ends with an unfinished escape sequence")
	}
	if parser.inSingleQuote || parser.inDoubleQuote {
		return nil, fmt.Errorf("runtime test command contains an unterminated quote")
	}
	parser.flush()

	return parser.fields, nil
}

func (p *runtimeCommandParser) consume(ch rune) {
	switch {
	case p.escaped:
		p.write(ch)
		p.escaped = false
	case ch == '\\':
		if p.inSingleQuote {
			p.write(ch)
			return
		}
		p.escaped = true
		p.sawToken = true
	case ch == '\'':
		p.toggleQuote(&p.inSingleQuote, p.inDoubleQuote, ch)
	case ch == '"':
		p.toggleQuote(&p.inDoubleQuote, p.inSingleQuote, ch)
	case unicode.IsSpace(ch):
		if p.inSingleQuote || p.inDoubleQuote {
			p.write(ch)
			return
		}
		p.flush()
	default:
		p.write(ch)
	}
}

func (p *runtimeCommandParser) toggleQuote(active *bool, otherActive bool, ch rune) {
	if otherActive {
		p.write(ch)
		return
	}
	*active = !*active
	p.sawToken = true
}

func (p *runtimeCommandParser) write(ch rune) {
	p.current.WriteRune(ch)
	p.sawToken = true
}

func (p *runtimeCommandParser) flush() {
	if p.current.Len() == 0 && !p.sawToken {
		return
	}
	p.fields = append(p.fields, p.current.String())
	p.current.Reset()
	p.sawToken = false
}

func resolveRuntimeExecutablePath(executable string, searchDirs []string) (string, error) {
	if _, ok := runtimeExecutableAllowlist[executable]; !ok {
		return "", fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}

	for _, dir := range searchDirs {
		candidate := filepath.Join(dir, executable)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("runtime test executable %q not found in trusted runtime directories", executable)
}

func newAllowlistedRuntimeCommand(ctx context.Context, executable string) (*exec.Cmd, error) {
	_, ok := runtimeExecutableAllowlist[executable]
	if !ok {
		return nil, fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}
	return exec.CommandContext(ctx, executable), nil
}

func runtimeSearchDirs() []string {
	configured := strings.TrimSpace(os.Getenv(runtimeBinDirsEnvKey))
	if configured == "" {
		configured = defaultTrustedRuntimeBinDirs
	}
	return trustedSearchDirs(configured)
}

func trustedSearchDirs(dirListValue string) []string {
	seen := make(map[string]struct{})
	dirs := make([]string, 0)
	for _, raw := range filepath.SplitList(dirListValue) {
		dir := filepath.Clean(strings.TrimSpace(raw))
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}

		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		// Reject group/other-writable runtime search entries.
		if info.Mode().Perm()&0o022 != 0 {
			continue
		}

		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}

func withRuntimeTraceEnv(base []string, tracePath string) []string {
	existing := readEnvValue(base, "NODE_OPTIONS")
	updates := map[string]string{
		"LOPPER_RUNTIME_TRACE": tracePath,
	}
	nodeOptions := strings.TrimSpace(existing)
	required := "--require=./scripts/runtime/require-hook.cjs --loader=./scripts/runtime/loader.mjs"
	if nodeOptions == "" {
		updates["NODE_OPTIONS"] = required
	} else {
		updates["NODE_OPTIONS"] = nodeOptions + " " + required
	}
	return mergeEnv(base, updates)
}

func mergeEnv(base []string, updates map[string]string) []string {
	merged := make(map[string]string, len(base)+len(updates))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		merged[parts[0]] = parts[1]
	}
	for key, value := range updates {
		merged[key] = value
	}
	items := make([]string, 0, len(merged))
	for key, value := range merged {
		items = append(items, key+"="+value)
	}
	return items
}

func readEnvValue(env []string, key string) string {
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == key {
			return parts[1]
		}
	}
	return ""
}
