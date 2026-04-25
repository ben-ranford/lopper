package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"unicode"
)

const runtimeBinDirsEnvKey = "LOPPER_RUNTIME_BIN_DIRS"
const defaultWindowsPathExt = ".COM;.EXE;.BAT;.CMD"

var runtimeOS = goruntime.GOOS

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

func buildRuntimeCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	fields, err := parseRuntimeCommand(command)
	if err != nil {
		return nil, err
	}
	if err := validateRuntimeCommand(command, fields); err != nil {
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

func ValidateCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	fields, err := parseRuntimeCommand(command)
	if err != nil {
		return err
	}
	return validateRuntimeCommand(command, fields)
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

func validateRuntimeCommand(command string, fields []string) error {
	if containsUnsafeRuntimeCommandSyntax(command) {
		return fmt.Errorf("runtime test command uses indirect command execution operators; pass a direct executable and arguments instead")
	}

	if len(fields) == 0 {
		return fmt.Errorf("runtime test command is required")
	}

	return rejectRuntimeCommandUnsafeFlags(fields[0], fields[1:])
}

func containsUnsafeRuntimeCommandSyntax(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	runes := []rune(command)

	for i, ch := range runes {
		if escaped {
			escaped = false
			continue
		}

		switch ch {
		case '\\':
			if inSingleQuote {
				continue
			}
			escaped = true
		case '\'':
			if inDoubleQuote {
				continue
			}
			inSingleQuote = !inSingleQuote
		case '"':
			if inSingleQuote {
				continue
			}
			inDoubleQuote = !inDoubleQuote
		case '|', '&', ';', '>', '<', '`', '\n', '\r':
			if inSingleQuote {
				continue
			}
			return true
		case '$':
			if inSingleQuote {
				continue
			}
			if i+1 < len(runes) && runes[i+1] == '(' {
				return true
			}
		}
	}

	return false
}

func rejectRuntimeCommandUnsafeFlags(executable string, args []string) error {
	flags, ok := runtimeCommandUnsafeFlags[executable]
	if !ok {
		return nil
	}

	for _, arg := range args {
		for _, flag := range flags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return fmt.Errorf("runtime test command uses unsafe executable flag %q for %q", arg, executable)
			}
		}
	}
	return nil
}

var runtimeCommandUnsafeFlags = map[string][]string{
	"node": {"-e", "--eval", "-p", "--print"},
	"bun":  {"-e", "--eval", "-p", "--print"},
	"deno": {"eval"},
	"npm":  {"exec"},
	"pnpm": {"exec"},
	"yarn": {"dlx"},
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
		if path, ok := resolveRuntimeExecutablePathInDir(executable, dir); ok {
			return path, nil
		}
	}

	return "", fmt.Errorf("runtime test executable %q not found in trusted runtime directories", executable)
}

func resolveRuntimeExecutablePathInDir(executable, dir string) (string, bool) {
	for _, candidate := range runtimeExecutableCandidates(executable, dir) {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if !isTrustedRuntimeExecutable(info) {
			continue
		}
		return candidate, true
	}
	return "", false
}

func runtimeExecutableCandidates(executable, dir string) []string {
	candidates := make([]string, 0, 4)
	addCandidate := func(candidate string) {
		if candidate == "" {
			return
		}
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}

	base := filepath.Join(dir, executable)
	addCandidate(base)

	if !isWindowsRuntime() || filepath.Ext(executable) != "" {
		return candidates
	}
	for _, ext := range windowsExecutableExtensions(os.Getenv("PATHEXT")) {
		addCandidate(base + ext)
	}
	return candidates
}

func isTrustedRuntimeExecutable(info os.FileInfo) bool {
	if isWindowsRuntime() {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

func newAllowlistedRuntimeCommand(ctx context.Context, executable string) (*exec.Cmd, error) {
	_, ok := runtimeExecutableAllowlist[executable]
	if !ok {
		return nil, fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}
	cmd := exec.CommandContext(ctx, executable)
	configureRuntimeCommand(cmd)
	return cmd, nil
}

func runtimeSearchDirs() []string {
	configured := strings.TrimSpace(os.Getenv(runtimeBinDirsEnvKey))
	if configured != "" {
		return trustedSearchDirs(configured)
	}
	defaults := strings.Join(defaultTrustedRuntimeBinDirEntries(), string(os.PathListSeparator))
	return trustedSearchDirs(defaults)
}

func defaultTrustedRuntimeBinDirEntries() []string {
	if !isWindowsRuntime() {
		return []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"}
	}

	return []string{
		filepath.Join(strings.TrimSpace(os.Getenv("ProgramFiles")), "nodejs"),
		filepath.Join(strings.TrimSpace(os.Getenv("ProgramFiles(x86)")), "nodejs"),
		`C:\Program Files\nodejs`,
		`C:\Program Files (x86)\nodejs`,
		`C:\Windows\System32`,
	}
}

func windowsExecutableExtensions(pathExtValue string) []string {
	pathExt := strings.TrimSpace(pathExtValue)
	if pathExt == "" {
		pathExt = defaultWindowsPathExt
	}

	seen := make(map[string]struct{})
	exts := make([]string, 0)
	for _, rawExt := range strings.Split(pathExt, ";") {
		ext := strings.ToLower(strings.TrimSpace(rawExt))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		if _, ok := seen[ext]; ok {
			continue
		}
		seen[ext] = struct{}{}
		exts = append(exts, ext)
	}

	if len(exts) == 0 {
		return []string{".com", ".exe", ".bat", ".cmd"}
	}
	return exts
}

func isWindowsRuntime() bool {
	return runtimeOS == "windows"
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
		// Reject group/other-writable runtime search entries on Unix-like systems.
		if !isWindowsRuntime() && info.Mode().Perm()&0o022 != 0 {
			continue
		}

		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}
