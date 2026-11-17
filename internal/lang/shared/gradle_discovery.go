package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	GradleDiscoveryFileBytes  = 32 << 20
	GradleDiscoveryTotalBytes = 256 << 20
)

var ErrGradleDiscoveryLimit = errors.New("gradle discovery limit exceeded")

// GradleDiscoveryError marks incomplete discovery that must not become a report.
type GradleDiscoveryError struct {
	Path, Operation, Resource string
	Limit, ObservedAtLeast    int64
	Err                       error
}

func (e *GradleDiscoveryError) Error() string {
	if e.Resource != "" {
		return fmt.Sprintf("incomplete Gradle discovery at %s: %s exceeds %d (observed at least %d); analyse a smaller intentional project scope: %v", e.Path, e.Resource, e.Limit, e.ObservedAtLeast, e.Err)
	}
	return fmt.Sprintf("incomplete Gradle discovery at %s during %s: %v", e.Path, e.Operation, e.Err)
}
func (e *GradleDiscoveryError) Unwrap() error { return e.Err }

func GradleDiscoveryFailure(path, operation string, err error) error {
	if err == nil {
		return nil
	}
	var failure *GradleDiscoveryError
	if errors.As(err, &failure) {
		return err
	}
	return &GradleDiscoveryError{Path: path, Operation: operation, Err: err}
}

// GradleDiscoveryBudget belongs to one sequential adapter/identity root pass.
// It retains byte counts, never input contents. Catalog captures reuse buffers.
type GradleDiscoveryBudget struct {
	root                 string
	perFile, total, used int64
	charged              map[string]int64
	observedRegular      map[string]struct{}
}

// NewGradleDiscoveryBudget retains an owned copy of any earlier regular-file
// observations so later catalog discovery cannot downgrade a replacement to a warning.
func NewGradleDiscoveryBudget(root string, observedRegular ...string) *GradleDiscoveryBudget {
	budget := &GradleDiscoveryBudget{root: filepath.Clean(root), perFile: GradleDiscoveryFileBytes, total: GradleDiscoveryTotalBytes, charged: make(map[string]int64)}
	if len(observedRegular) != 0 {
		budget.observedRegular = make(map[string]struct{}, len(observedRegular))
		for _, path := range observedRegular {
			budget.observedRegular[filepath.Clean(path)] = struct{}{}
		}
	}
	return budget
}

func (b *GradleDiscoveryBudget) allowance(path string) (string, int64, error) {
	if b == nil || b.perFile <= 0 || b.total <= 0 || b.perFile == math.MaxInt64 || b.total == math.MaxInt64 || b.used < 0 || b.used > b.total {
		return "", 0, errors.New("invalid Gradle discovery byte policy")
	}
	key, err := filepath.Rel(b.root, filepath.Clean(path))
	if err != nil {
		return "", 0, err
	}
	if gradleCatalogRelativePathEscapesRepo(key) {
		return "", 0, safeio.ErrPathEscapesRoot
	}
	return key, min(b.perFile, b.total-b.used+b.charged[key]), nil
}

func (b *GradleDiscoveryBudget) ReadWithinRoot(ctx context.Context, root safeio.Root, leaf, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, GradleDiscoveryFailure(path, "read", err)
	}
	key, allowance, err := b.allowance(path)
	if err != nil {
		return nil, GradleDiscoveryFailure(path, "budget", err)
	}
	data, readErr := readGradleBudgetFile(root, leaf, allowance)
	if errors.Is(readErr, safeio.ErrFileTooLarge) || int64(len(data)) > allowance {
		resource, limit, observed := "file bytes", b.perFile, b.perFile+1
		if allowance < b.perFile {
			resource, limit, observed = "unique input bytes", b.total, b.total+1
		}
		return nil, &GradleDiscoveryError{Path: path, Operation: "read", Resource: resource, Limit: limit, ObservedAtLeast: observed, Err: errors.Join(ErrGradleDiscoveryLimit, safeio.ErrFileTooLarge, readErr, ctx.Err())}
	}
	if err := errors.Join(readErr, ctx.Err()); err != nil {
		return nil, GradleDiscoveryFailure(path, "read", err)
	}
	if size := int64(len(data)); size > b.charged[key] {
		b.used += size - b.charged[key]
		b.charged[key] = size
	}
	return data, nil
}

func GradleDiscoveryWalkFailure(path string, budget RootedWalkBudget, err error) error {
	for _, boundary := range []struct {
		sentinel error
		resource string
		limit    int
	}{
		{errRootedWalkTraversalLimit, "traversal entries", budget.MaxTraversalEntries},
		{errRootedWalkFileLimit, "candidate files", budget.MaxFiles},
		{errRootedWalkWorkLimit, "candidate work items", budget.MaxWorkItems},
	} {
		if errors.Is(err, boundary.sentinel) {
			return &GradleDiscoveryError{Path: path, Operation: "walk", Resource: boundary.resource, Limit: int64(boundary.limit), ObservedAtLeast: int64(boundary.limit) + 1, Err: errors.Join(ErrGradleDiscoveryLimit, err)}
		}
	}
	return GradleDiscoveryFailure(path, "walk", err)
}

// safeio's byte-limit reader interprets zero as unlimited. At zero remaining,
// the pinned open and exactly one probe distinguish empty from growing inputs.
func readGradleBudgetFile(root safeio.Root, leaf string, allowance int64) (_ []byte, err error) {
	if allowance > 0 {
		return safeio.ReadRegularFileWithinRootLimit(root, leaf, allowance)
	}
	file, err := safeio.OpenPinnedRegularFile(root, leaf)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return io.ReadAll(io.LimitReader(file, 1))
}
