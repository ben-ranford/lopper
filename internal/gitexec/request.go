package gitexec

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	filterProbeCommand = "git hash-object --stdin --__LOPPER_LOCKFILE_FILTER_PROBE__"
	verifyFlag         = "--verify"
	nameOnlyFlag       = "--name-only"
)

type gitRequest struct {
	args  []string
	repo  bool
	safe  bool
	hooks bool
	file  bool
	probe string
}

// ValidateArgs checks the supported Git operation and every argument position.
// It is independent of executable selection so platform-specific trusted Git
// resolvers can apply the same boundary. It grants no fixture-setup operations.
func ValidateArgs(args ...string) error {
	_, err := parseRequest(args)
	return err
}

func parseRequest(args []string) (gitRequest, error) {
	request := gitRequest{args: slices.Clone(args)}
	if slices.Equal(request.args, []string{"--version"}) {
		return request, nil
	}
	tail, err := request.globals()
	if err != nil || !request.operation(tail) {
		return gitRequest{}, fmt.Errorf("unsupported git request")
	}
	return request, nil
}

func (r *gitRequest) globals() ([]string, error) {
	args := r.args
	configs := make(map[string]string)
	for len(args) != 0 && (args[0] == "-C" || args[0] == "-c") {
		if len(args) < 2 {
			return nil, fmt.Errorf("missing git global argument")
		}
		if err := r.applyGlobal(args[0], args[1], configs); err != nil {
			return nil, err
		}
		args = args[2:]
	}
	if !r.configs(configs) {
		return nil, fmt.Errorf("unsupported git config")
	}
	return args, nil
}

func (r *gitRequest) applyGlobal(option, value string, configs map[string]string) error {
	if option == "-C" {
		if r.repo || !directoryOperand(value) {
			return fmt.Errorf("invalid git working directory")
		}
		r.repo = true
		return nil
	}
	key, setting, ok := strings.Cut(value, "=")
	if _, duplicate := configs[key]; !ok || duplicate {
		return fmt.Errorf("invalid or repeated git config")
	}
	configs[key] = setting
	return nil
}

func (r *gitRequest) configs(configs map[string]string) bool {
	safeCount := 0
	for _, entry := range forcedGitConfigOverrides {
		if value, exists := configs[entry.key]; exists {
			if value != entry.value {
				return false
			}
			safeCount++
			delete(configs, entry.key)
		}
	}
	if safeCount != 0 && safeCount != len(forcedGitConfigOverrides) {
		return false
	}
	r.safe = safeCount != 0
	if value, exists := configs["core.hooksPath"]; exists {
		if value != os.DevNull {
			return false
		}
		r.hooks = true
		delete(configs, "core.hooksPath")
	}
	if value, exists := configs["protocol.file.allow"]; exists {
		if value != "always" {
			return false
		}
		r.file = true
		delete(configs, "protocol.file.allow")
	}
	if len(configs) == 0 {
		return true
	}
	return r.filterConfigs(configs)
}

func (r *gitRequest) filterConfigs(configs map[string]string) bool {
	if len(configs) != 3 || !r.safe || r.hooks || r.file {
		return false
	}
	for _, driver := range []string{"set", "unset", "unspecified"} {
		prefix := "filter." + driver + "."
		if configs[prefix+"clean"] == filterProbeCommand && configs[prefix+"process"] == filterProbeCommand && configs[prefix+"required"] == "true" {
			r.probe = driver
			return true
		}
	}
	return false
}

func directoryOperand(value string) bool {
	return value != "" && !strings.ContainsRune(value, 0) && filepath.IsAbs(value)
}

func (r *gitRequest) operation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if r.probe != "" {
		return r.repo && len(args) == 3 && args[0] == "hash-object" && args[2] == "--stdin" &&
			strings.HasPrefix(args[1], "--path=") && literalFilename(strings.TrimPrefix(args[1], "--path="))
	}
	if !r.repo {
		return r.materialize(args)
	}
	switch args[0] {
	case "rev-parse":
		return r.revParse(args[1:])
	case "diff":
		return !r.file && !r.hooks && validDiff(args[1:]) || r.safe && r.hooks && !r.file && proofDiff(args[1:])
	case "status":
		return !r.file && !r.hooks && slices.Equal(args[1:], []string{"--porcelain"})
	case "ls-files":
		return !r.file && !r.hooks && validFiles(args[1:])
	case "check-attr":
		return r.safe && !r.file && !r.hooks && slices.Equal(args[1:], []string{"--stdin", "-z", "--all"})
	case "config":
		return r.safe && !r.file && !r.hooks && slices.Equal(args[1:], []string{"--null", "--includes", "--get-regexp", `^filter\..*\.(clean|process)$`})
	case "worktree":
		return r.hooks && !r.file && validWorktree(args[1:])
	case "merge-base":
		return r.safe && r.hooks && !r.file && len(args) == 4 && args[1] == "--" && revision(args[2]) && args[3] == "HEAD"
	default:
		return !r.safe && !r.hooks && r.dashboard(args)
	}
}

func (r *gitRequest) revParse(args []string) bool {
	if slices.Equal(args, []string{verifyFlag, "HEAD"}) {
		return true
	}
	if r.file {
		return false
	}
	if slices.Equal(args, []string{"--is-inside-work-tree"}) || slices.Equal(args, []string{verifyFlag, "--quiet", "HEAD"}) {
		return !r.hooks
	}
	if slices.Equal(args, []string{"--show-toplevel"}) || slices.Equal(args, []string{"--show-prefix"}) {
		return r.hooks
	}
	return r.hooks && len(args) == 2 && args[0] == verifyFlag && strings.HasSuffix(args[1], "^{commit}") && ValidObjectID(strings.TrimSuffix(args[1], "^{commit}"))
}

func validWorktree(args []string) bool {
	if len(args) == 3 && args[0] == "remove" && args[1] == "--force" {
		return directoryOperand(args[2])
	}
	if len(args) < 4 || args[0] != "add" || args[1] != "--detach" {
		return false
	}
	operands := args[2:]
	if operands[0] == "--force" {
		operands = operands[1:]
	}
	return len(operands) == 2 && directoryOperand(operands[0]) && ValidObjectID(operands[1])
}

func literalFilename(value string) bool {
	// Paths are data only in --path= or after -- with the literal pathspec
	// prefix. Preserve whitespace, leading dashes, and shell punctuation.
	return value != "" && !strings.ContainsRune(value, 0)
}

func literalPaths(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, value := range args {
		if !strings.HasPrefix(value, ":(literal)") || !literalFilename(strings.TrimPrefix(value, ":(literal)")) {
			return false
		}
	}
	return true
}

func takePrefix(args, prefix []string) ([]string, bool) {
	if len(args) < len(prefix) || !slices.Equal(args[:len(prefix)], prefix) {
		return nil, false
	}
	return args[len(prefix):], true
}

func validDiff(args []string) bool {
	tail, ok := takePrefix(args, []string{"--no-ext-diff", "--no-textconv"})
	if !ok {
		return false
	}
	if slices.Equal(tail, []string{nameOnlyFlag, "--diff-filter=ACMRD", "HEAD~1..HEAD"}) {
		return true
	}
	if operands, matched := takePrefix(tail, []string{"--name-status", "-z", "--find-renames", "--find-copies", "--diff-filter=ACMRD"}); matched {
		return len(operands) == 1 && revisionRange(operands[0])
	}
	if len(tail) != 0 && (tail[0] == "HEAD" || tail[0] == "--cached") {
		tail = tail[1:]
	}
	paths, matched := takePrefix(tail, []string{nameOnlyFlag, "-z", "--"})
	// Codemod cleanliness checks intentionally cover the whole worktree. The
	// fixed -- terminator still prevents filenames from introducing options.
	return matched && (len(paths) == 0 || literalPaths(paths))
}

func proofDiff(args []string) bool {
	return len(args) == 4 && args[0] == nameOnlyFlag && args[1] == "--diff-filter=ACMR" &&
		strings.HasSuffix(args[2], "..HEAD") && ValidObjectID(strings.TrimSuffix(args[2], "..HEAD")) && args[3] == "--"
}

func validFiles(args []string) bool {
	if slices.Equal(args, []string{"--others", "--exclude-standard"}) ||
		slices.Equal(args, []string{"--others", "--exclude-standard", "-z", "--"}) {
		return true
	}
	if len(args) != 0 && args[0] == "--cached" {
		args = args[1:]
	}
	paths, ok := takePrefix(args, []string{"--others", "--exclude-standard", "-z", "--"})
	return ok && literalPaths(paths)
}
