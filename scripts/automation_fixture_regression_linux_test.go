package scripts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAutomationExamplesFixtureRunsWhileScriptOpenForWriting(t *testing.T) {
	// Keep this test nonparallel: it owns TMPDIR and all fixture slots.
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	for range cap(automationExamplesFixtureSlots) {
		automationExamplesFixtureSlots <- struct{}{}
	}
	release := sync.OnceFunc(func() { <-automationExamplesFixtureSlots })
	defer func() {
		release()
		for remaining := 1; remaining < cap(automationExamplesFixtureSlots); remaining++ {
			<-automationExamplesFixtureSlots
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	finished := make(chan struct{})
	observer := make(chan error, 1)
	go func() {
		observer <- holdAutomationFixtureWriter(ctx, root, finished, release)
	}()

	// A fresh subtest gets its own TempDir parent under the isolated TMPDIR.
	// Reusing the parent T would reuse its already initialized temp directory.
	t.Run("held-writer", func(t *testing.T) {
		output, err := runAutomationExamplesFixture(t, readRepoFile(t, "examples/lefthook.yml"))
		if err != nil {
			t.Fatalf("expected writable script fixture to run, got %v:\n%s", err, output)
		}
		assertOutputContainsAll(t, output, []string{"Automation examples preserve JSON and mutation-guard contracts."})
	})
	close(finished)
	if err := <-observer; err != nil {
		t.Fatalf("hold fixture writer through execution: %v", err)
	}
}

func holdAutomationFixtureWriter(ctx context.Context, root string, finished <-chan struct{}, release func()) (result error) {
	// Release the helper on setup errors too, so every path can finish and join.
	defer release()
	writer, err := waitForAutomationFixtureWriter(ctx, root, finished)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, writer.Close()) }()
	release()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("fixture did not finish while the writer was held: %w", ctx.Err())
	}
}

func waitForAutomationFixtureWriter(ctx context.Context, root string, finished <-chan struct{}) (*os.File, error) {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	pattern := filepath.Join(root, "*", "*", "scripts", "check-automation-examples.sh")
	for {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		switch len(matches) {
		case 1:
			return os.OpenFile(matches[0], os.O_WRONLY, 0)
		case 0:
		default:
			return nil, fmt.Errorf("expected one owned fixture script, found %d", len(matches))
		}
		select {
		case <-ticker.C:
		case <-finished:
			return nil, errors.New("fixture finished before its writable handle was acquired")
		case <-ctx.Done():
			return nil, fmt.Errorf("find owned fixture script: %w", ctx.Err())
		}
	}
}
