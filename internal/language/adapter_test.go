package language

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestNewAdapterContractCopiesAliases(t *testing.T) {
	aliases := []string{"golang"}
	contract := NewAdapterContract("go", aliases...)

	aliases[0] = "changed"
	got := contract.Aliases()
	if !slices.Equal(got, []string{"golang"}) {
		t.Fatalf("unexpected aliases: %#v", got)
	}

	got[0] = "mutated"
	if !slices.Equal(contract.Aliases(), []string{"golang"}) {
		t.Fatalf("expected aliases copy on read, got %#v", contract.Aliases())
	}
}

func TestAdapterLifecycleDetectUsesSharedConfidenceHandler(t *testing.T) {
	lifecycle := NewAdapterLifecycle("go", []string{"golang"}, func(ctx context.Context, repoPath string) (Detection, error) {
		if repoPath != "/repo" {
			t.Fatalf("unexpected repo path: %q", repoPath)
		}
		return Detection{Matched: true, Confidence: 80}, nil
	})

	matched, err := lifecycle.Detect(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !matched {
		t.Fatalf("expected detection match")
	}
	if lifecycle.Clock == nil {
		t.Fatalf("expected default clock to be configured")
	}
}

func TestDetectMatched(t *testing.T) {
	matched, err := DetectMatched(context.Background(), "/repo", func(context.Context, string) (Detection, error) {
		return Detection{Matched: true}, nil
	})
	if err != nil {
		t.Fatalf("detect matched: %v", err)
	}
	if !matched {
		t.Fatalf("expected match")
	}

	expected := errors.New("boom")
	if _, err := DetectMatched(context.Background(), "/repo", func(context.Context, string) (Detection, error) {
		return Detection{}, expected
	}); !errors.Is(err, expected) {
		t.Fatalf("expected detect error %v, got %v", expected, err)
	}

	if _, err := DetectMatched(context.Background(), "/repo", nil); !errors.Is(err, errDetectLifecycleUnconfigured) {
		t.Fatalf("expected unconfigured lifecycle error, got %v", err)
	}
}
