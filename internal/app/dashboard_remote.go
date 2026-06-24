package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/gitexec"
)

const (
	DashboardRemoteReposPreviewFeature = "dashboard-remote-repos-preview"
	dashboardRepoCacheEnv              = "LOPPER_DASHBOARD_REPO_CACHE"
	dashboardRepoCacheHashLength       = 16
)

type dashboardRepoURLSpec struct {
	normalized string
	scheme     string
	name       string
}

type dashboardRepoMaterializer struct {
	cacheRoot string
	gitPath   string
}

var (
	dashboardRemoteCacheRootFn     = dashboardRemoteCacheRoot
	newDashboardRepoMaterializerFn = newDashboardRepoMaterializer
	execDashboardGitCommandFn      = gitexec.CommandContext
	resolveDashboardGitBinaryFn    = gitexec.ResolveBinaryPath
	dashboardUserCacheDirFn        = os.UserCacheDir
	dashboardRemoveAllFn           = os.RemoveAll
)

func (a *App) prepareDashboardExecutionPlan(ctx context.Context, request DashboardRequest, repos []dashboard.RepoInput) dashboardExecutionPlan {
	initialResults := initialDashboardResults(repos)
	prepared := make([]dashboardPreparedRepo, 0, len(repos))
	var materializer *dashboardRepoMaterializer
	var materializerErr error

	for index, repo := range repos {
		if strings.TrimSpace(repo.RepoURL) == "" {
			prepared = append(prepared, dashboardPreparedRepo{index: index, input: repo})
			continue
		}

		if materializer == nil && materializerErr == nil {
			materializer, materializerErr = newDashboardRepoMaterializerFn()
		}
		if materializerErr != nil {
			initialResults[index].Err = fmt.Errorf("materialize repoUrl: %w", materializerErr)
			continue
		}

		checkoutPath, err := materializer.Materialize(ctx, repo.RepoURL)
		repo.Path = checkoutPath
		initialResults[index].Input = repo
		if err != nil {
			initialResults[index].Err = fmt.Errorf("materialize repoUrl: %w", err)
			continue
		}

		prepared = append(prepared, dashboardPreparedRepo{index: index, input: repo})
	}

	return planPreparedDashboardExecution(request, prepared, initialResults)
}

func newDashboardRepoMaterializer() (*dashboardRepoMaterializer, error) {
	cacheRoot, err := dashboardRemoteCacheRootFn()
	if err != nil {
		return nil, err
	}
	gitPath, err := resolveDashboardGitBinaryFn()
	if err != nil {
		return nil, err
	}
	return &dashboardRepoMaterializer{
		cacheRoot: cacheRoot,
		gitPath:   gitPath,
	}, nil
}

func dashboardRemoteCacheRoot() (string, error) {
	if override := strings.TrimSpace(os.Getenv(dashboardRepoCacheEnv)); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("%s must be an absolute path", dashboardRepoCacheEnv)
		}
		return filepath.Clean(override), nil
	}
	userCacheDir, err := dashboardUserCacheDirFn()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	return filepath.Join(userCacheDir, "lopper", "dashboard", "repos"), nil
}

func (m *dashboardRepoMaterializer) Materialize(ctx context.Context, repoURL string) (string, error) {
	spec, err := parseDashboardRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	checkoutPath, err := dashboardCheckoutPath(m.cacheRoot, spec)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.cacheRoot, 0o750); err != nil {
		return checkoutPath, fmt.Errorf("create dashboard repo cache: %w", err)
	}

	if m.checkoutUsable(ctx, checkoutPath, spec.normalized) {
		if err := m.refreshCheckout(ctx, checkoutPath, spec); err != nil {
			return checkoutPath, err
		}
		return checkoutPath, nil
	}

	if err := dashboardRemoveAllFn(checkoutPath); err != nil {
		return checkoutPath, fmt.Errorf("reset dashboard repo checkout: %w", err)
	}
	if err := m.cloneCheckout(ctx, checkoutPath, spec); err != nil {
		if cleanupErr := dashboardRemoveAllFn(checkoutPath); cleanupErr != nil {
			return checkoutPath, fmt.Errorf("%w; cleanup failed: %w", err, cleanupErr)
		}
		return checkoutPath, err
	}
	return checkoutPath, nil
}

func (m *dashboardRepoMaterializer) checkoutUsable(ctx context.Context, checkoutPath, repoURL string) bool {
	if _, err := os.Stat(filepath.Join(checkoutPath, ".git")); err != nil {
		return false
	}
	output, err := m.runGit(ctx, "-C", checkoutPath, "remote", "get-url", "origin")
	return err == nil && strings.TrimSpace(string(output)) == repoURL
}

func (m *dashboardRepoMaterializer) cloneCheckout(ctx context.Context, checkoutPath string, spec dashboardRepoURLSpec) error {
	if _, err := m.runGit(ctx, gitArgsForURL(spec.normalized, "clone", "--no-tags", "--depth=1", "--", spec.normalized, checkoutPath)...); err != nil {
		return fmt.Errorf("clone remote repo: %w", err)
	}
	return m.pinCheckout(ctx, checkoutPath, spec, "HEAD")
}

func (m *dashboardRepoMaterializer) refreshCheckout(ctx context.Context, checkoutPath string, spec dashboardRepoURLSpec) error {
	if _, err := m.runGit(ctx, gitArgsForURL(spec.normalized, "-C", checkoutPath, "fetch", "--prune", "--depth=1", "origin", "HEAD")...); err != nil {
		return fmt.Errorf("fetch remote repo: %w", err)
	}
	return m.pinCheckout(ctx, checkoutPath, spec, "FETCH_HEAD")
}

func (m *dashboardRepoMaterializer) pinCheckout(ctx context.Context, checkoutPath string, spec dashboardRepoURLSpec, ref string) error {
	if _, err := m.runGit(ctx, gitArgsForURL(spec.normalized, "-C", checkoutPath, "checkout", "--detach", "--force", ref)...); err != nil {
		return fmt.Errorf("checkout remote repo: %w", err)
	}
	if _, err := m.runGit(ctx, gitArgsForURL(spec.normalized, "-C", checkoutPath, "reset", "--hard", ref)...); err != nil {
		return fmt.Errorf("reset remote repo checkout: %w", err)
	}
	if _, err := m.runGit(ctx, gitArgsForURL(spec.normalized, "-C", checkoutPath, "clean", "-fdx")...); err != nil {
		return fmt.Errorf("clean remote repo checkout: %w", err)
	}
	return nil
}

func (m *dashboardRepoMaterializer) runGit(ctx context.Context, args ...string) ([]byte, error) {
	command, err := execDashboardGitCommandFn(ctx, m.gitPath, args...)
	if err != nil {
		return nil, err
	}
	command.Env = append(gitexec.SanitizedEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func gitConfigArgsForURL(repoURL string) []string {
	spec, err := parseDashboardRepoURL(repoURL)
	if err == nil && spec.scheme == "file" {
		return []string{"-c", "protocol.file.allow=always"}
	}
	return nil
}

func gitArgsForURL(repoURL string, args ...string) []string {
	prefix := gitConfigArgsForURL(repoURL)
	if len(prefix) == 0 {
		return append([]string{}, args...)
	}
	combined := make([]string, 0, len(prefix)+len(args))
	combined = append(combined, prefix...)
	return append(combined, args...)
}

func parseDashboardRepoURL(raw string) (dashboardRepoURLSpec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return dashboardRepoURLSpec{}, fmt.Errorf("repoUrl is required")
	}
	if !strings.Contains(trimmed, "://") {
		return dashboardRepoURLSpec{}, fmt.Errorf("repoUrl must use https://, ssh://, or file://")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return dashboardRepoURLSpec{}, err
	}
	if parsed.Scheme == "" {
		return dashboardRepoURLSpec{}, fmt.Errorf("repoUrl must use https://, ssh://, or file://")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return dashboardRepoURLSpec{}, fmt.Errorf("repoUrl cannot include query strings or fragments")
	}

	switch parsed.Scheme {
	case "https":
		if err := validateNetworkRepoURL(parsed, false); err != nil {
			return dashboardRepoURLSpec{}, err
		}
	case "ssh":
		if err := validateNetworkRepoURL(parsed, true); err != nil {
			return dashboardRepoURLSpec{}, err
		}
	case "file":
		if err := validateFileRepoURL(parsed); err != nil {
			return dashboardRepoURLSpec{}, err
		}
	default:
		return dashboardRepoURLSpec{}, fmt.Errorf("unsupported repoUrl protocol %q", parsed.Scheme)
	}

	normalized := strings.TrimRight(parsed.String(), "/")
	return dashboardRepoURLSpec{
		normalized: normalized,
		scheme:     parsed.Scheme,
		name:       inferDashboardRepoURLName(parsed),
	}, nil
}

func validateNetworkRepoURL(parsed *url.URL, allowUser bool) error {
	if parsed.Host == "" {
		return fmt.Errorf("repoUrl host is required")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("repoUrl path is required")
	}
	if parsed.User != nil {
		if !allowUser {
			return fmt.Errorf("repoUrl cannot include credentials")
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return fmt.Errorf("repoUrl cannot include passwords")
		}
	}
	return nil
}

func validateFileRepoURL(parsed *url.URL) error {
	if parsed.User != nil {
		return fmt.Errorf("file repoUrl cannot include credentials")
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return fmt.Errorf("file repoUrl host must be empty or localhost")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("file repoUrl path is required")
	}
	if !filepath.IsAbs(parsed.Path) {
		return fmt.Errorf("file repoUrl path must be absolute")
	}
	parsed.Path = filepath.ToSlash(filepath.Clean(parsed.Path))
	return nil
}

func inferDashboardRepoURLName(parsed *url.URL) string {
	base := filepath.Base(parsed.Path)
	base = strings.TrimSuffix(base, ".git")
	if base == "." || base == string(filepath.Separator) || base == "" {
		if parsed.Host != "" {
			return parsed.Host
		}
		return strings.TrimSpace(parsed.String())
	}
	return base
}

func dashboardCheckoutPath(cacheRoot string, spec dashboardRepoURLSpec) (string, error) {
	root := filepath.Clean(cacheRoot)
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("dashboard repo cache root must be absolute")
	}
	sum := sha256.Sum256([]byte(spec.normalized))
	hash := hex.EncodeToString(sum[:])[:dashboardRepoCacheHashLength]
	checkoutPath := filepath.Join(root, sanitizeDashboardCheckoutName(spec.name)+"-"+hash)
	if !pathWithinDir(root, checkoutPath) {
		return "", fmt.Errorf("dashboard repo checkout path escapes cache root")
	}
	return checkoutPath, nil
}

func sanitizeDashboardCheckoutName(value string) string {
	trimmed := strings.TrimSpace(value)
	var b strings.Builder
	lastDash := false
	for _, r := range trimmed {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-'
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	sanitized := strings.Trim(b.String(), ".-_")
	if sanitized == "" {
		return "repo"
	}
	return sanitized
}

func pathWithinDir(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
