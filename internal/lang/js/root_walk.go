package js

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	defaultJSWalkEntryBudget = 200000
	jsWalkReadDirBatchSize   = 128
)

var errJSWalkEntryBudgetExceeded = errors.New("js walk entry budget exceeded")

type rootWalkFunc func(relPath string, info fs.FileInfo) (skipDir bool, stop bool, err error)
type rootWalkErrorFunc func(relPath string, err error) bool

type jsWalkEntryBudget struct {
	limit          int
	entriesVisited int
}

func newJSWalkEntryBudget(limit int) *jsWalkEntryBudget {
	return &jsWalkEntryBudget{limit: max(limit, 0)}
}

func (b *jsWalkEntryBudget) remaining() int {
	return max(b.limit-b.entriesVisited, 0)
}

func (b *jsWalkEntryBudget) reserve(entries int) {
	b.entriesVisited += entries
}

type jsWalkSummary struct {
	entriesVisited int
	truncated      bool
}

// walkRootNoFollow streams directory entries in bounded batches. Membership at
// the budget boundary follows filesystem enumeration; admitted entries are
// sorted before callbacks, and production collectors sort exposed results.
func walkRootNoFollow(ctx context.Context, root safeio.Root, visit rootWalkFunc) error {
	summary, err := walkRootNoFollowContext(ctx, root, newJSWalkEntryBudget(defaultJSWalkEntryBudget), visit, nil)
	return rootWalkResult(summary, err)
}

func walkRootNoFollowBestEffort(ctx context.Context, root safeio.Root, visit rootWalkFunc) error {
	return walkRootNoFollowBestEffortWithErrorCallback(ctx, root, visit, nil)
}

func walkRootNoFollowBestEffortWithErrorCallback(ctx context.Context, root safeio.Root, visit rootWalkFunc, onError func(relPath string, err error)) error {
	summary, err := walkRootNoFollowContext(ctx, root, newJSWalkEntryBudget(defaultJSWalkEntryBudget), visit, func(relPath string, err error) bool {
		if onError != nil {
			onError(relPath, err)
		}
		return true
	})
	return rootWalkResult(summary, err)
}

func rootWalkResult(summary jsWalkSummary, err error) error {
	if err != nil {
		return err
	}
	if summary.truncated {
		return errJSWalkEntryBudgetExceeded
	}
	return nil
}

type rootWalkState struct {
	budget    *jsWalkEntryBudget
	stopped   bool
	truncated bool
}

type rootWalkBatch struct {
	entries   []fs.DirEntry
	done      bool
	truncated bool
}

func walkRootNoFollowFrom(ctx context.Context, root safeio.Root, relDir string, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) error {
	if state.budget == nil {
		state.budget = newJSWalkEntryBudget(defaultJSWalkEntryBudget)
	}
	err := walkRootNoFollowFromContext(ctx, root, relDir, visit, state, onError)
	return rootWalkResult(state.summary(), err)
}

func walkRootNoFollowContext(ctx context.Context, root safeio.Root, budget *jsWalkEntryBudget, visit rootWalkFunc, onError rootWalkErrorFunc) (jsWalkSummary, error) {
	state := &rootWalkState{budget: budget}
	err := walkRootNoFollowFromContext(ctx, root, "", visit, state, onError)
	return state.summary(), err
}

func (s *rootWalkState) summary() jsWalkSummary {
	return jsWalkSummary{
		entriesVisited: s.budget.entriesVisited,
		truncated:      s.truncated,
	}
}

func walkRootNoFollowFromContext(ctx context.Context, root safeio.Root, relDir string, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) (err error) {
	if state.stopped || state.truncated {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	file, err := root.Open(".")
	if err != nil {
		return continueOrReturnRootWalkDir(relDir, err, onError)
	}
	defer closeReadCloserPreserveErr(file, &err)

	readDirFile, ok := file.(fs.ReadDirFile)
	if !ok {
		return continueOrReturnRootWalkDir(relDir, fs.ErrInvalid, onError)
	}

	return walkRootReadDirBatches(ctx, root, relDir, readDirFile, visit, state, onError)
}

func walkRootReadDirBatches(ctx context.Context, root safeio.Root, relDir string, readDirFile fs.ReadDirFile, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) error {
	for {
		batch, err := readRootWalkBatch(ctx, readDirFile, state.budget)
		if err != nil {
			return handleRootWalkReadError(relDir, err, onError)
		}
		done, err := processRootWalkBatch(ctx, root, relDir, batch, visit, state, onError)
		if err != nil || done {
			return err
		}
	}
}

func readRootWalkBatch(ctx context.Context, readDirFile fs.ReadDirFile, budget *jsWalkEntryBudget) (rootWalkBatch, error) {
	if err := ctx.Err(); err != nil {
		return rootWalkBatch{}, err
	}

	entries, readErr := readDirFile.ReadDir(rootWalkReadSize(budget.remaining()))
	admitted, overflow := admitRootWalkEntries(entries, budget)
	if err := ctx.Err(); err != nil {
		return rootWalkBatch{}, err
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		// ReadDir may return entries with an error. Count that enumeration work,
		// but discard the ambiguous entries without naming or visiting them.
		return rootWalkBatch{}, readErr
	}
	if len(entries) == 0 {
		if errors.Is(readErr, io.EOF) {
			return rootWalkBatch{done: true}, nil
		}
		return rootWalkBatch{}, io.ErrNoProgress
	}
	return rootWalkBatch{
		entries:   admitted,
		done:      overflow || errors.Is(readErr, io.EOF),
		truncated: overflow,
	}, nil
}

func rootWalkReadSize(remaining int) int {
	if remaining < jsWalkReadDirBatchSize {
		return remaining + 1
	}
	return jsWalkReadDirBatchSize
}

func admitRootWalkEntries(entries []fs.DirEntry, budget *jsWalkEntryBudget) ([]fs.DirEntry, bool) {
	admittedCount := min(len(entries), budget.remaining())
	budget.reserve(admittedCount)
	return entries[:admittedCount], len(entries) > admittedCount
}

func processRootWalkBatch(ctx context.Context, root safeio.Root, relDir string, batch rootWalkBatch, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) (bool, error) {
	if err := ctx.Err(); err != nil {
		return true, err
	}
	sort.Slice(batch.entries, func(i, j int) bool {
		return batch.entries[i].Name() < batch.entries[j].Name()
	})
	for _, entry := range batch.entries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if err := walkRootEntryNoFollowContext(ctx, root, relDir, entry, visit, state, onError); err != nil {
			return true, err
		}
		if state.stopped || state.truncated {
			return true, nil
		}
	}
	if batch.truncated {
		state.truncated = true
	}
	return batch.done, nil
}

func handleRootWalkReadError(relDir string, err error, onError rootWalkErrorFunc) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return continueOrReturnRootWalkDir(relDir, err, onError)
}

func walkChildRootNoFollow(ctx context.Context, root safeio.Root, relDir string, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) (err error) {
	if state.budget == nil {
		state.budget = newJSWalkEntryBudget(defaultJSWalkEntryBudget)
	}
	err = walkChildRootNoFollowContext(ctx, root, relDir, visit, state, onError)
	return rootWalkResult(state.summary(), err)
}

func walkChildRootNoFollowContext(ctx context.Context, root safeio.Root, relDir string, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) (err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return walkRootNoFollowFromContext(ctx, root, relDir, visit, state, onError)
}

func shouldContinueRootWalk(relPath string, err error, onError rootWalkErrorFunc) bool {
	return onError != nil && onError(relPath, err)
}

func continueOrReturnRootWalkDir(relDir string, err error, onError rootWalkErrorFunc) error {
	if relDir != "" && shouldContinueRootWalk(relDir, err, onError) {
		return nil
	}
	return err
}

func walkRootEntryNoFollowContext(ctx context.Context, root safeio.Root, relDir string, entry fs.DirEntry, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) error {
	relPath := rootWalkRelPath(relDir, entry.Name())
	info, err := root.Lstat(entry.Name())
	if err != nil {
		return continueOrReturnRootWalk(relPath, err, onError)
	}
	skipDir, stop, err := visit(relPath, info)
	if err != nil {
		return err
	}
	if stop {
		state.stopped = true
		return nil
	}
	if shouldSkipRootWalkChild(info, skipDir) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	childRoot, err := openRootChildNoFollow(root, entry.Name(), relPath)
	if err != nil {
		return continueOrReturnRootWalk(relPath, err, onError)
	}
	return walkChildRootNoFollowContext(ctx, childRoot, relPath, visit, state, onError)
}

func rootWalkRelPath(relDir, name string) string {
	if relDir == "" {
		return name
	}
	return filepath.Join(relDir, name)
}

func continueOrReturnRootWalk(relPath string, err error, onError rootWalkErrorFunc) error {
	if shouldContinueRootWalk(relPath, err, onError) {
		return nil
	}
	return err
}

func shouldSkipRootWalkChild(info fs.FileInfo, skipDir bool) bool {
	return !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || skipDir
}
