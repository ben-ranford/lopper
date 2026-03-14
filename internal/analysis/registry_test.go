package analysis

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestRegisterAdaptersBuildsRegistryFromFactories(t *testing.T) {
	registry := language.NewRegistry()
	calls := make([]string, 0, 2)
	factories := []adapterFactory{
		func() language.Adapter {
			calls = append(calls, "js-ts")
			return &testServiceAdapter{id: "js-ts", detect: language.Detection{Matched: true}}
		},
		func() language.Adapter {
			calls = append(calls, "python")
			return &testServiceAdapter{id: "python", detect: language.Detection{Matched: true}}
		},
	}

	if err := registerAdapters(registry, factories); err != nil {
		t.Fatalf("register adapters: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"js-ts", "python"}) {
		t.Fatalf("unexpected factory order: %#v", calls)
	}
	if got := registry.IDs(); !reflect.DeepEqual(got, []string{"js-ts", "python"}) {
		t.Fatalf("unexpected registered ids: %#v", got)
	}
}

func TestRegisterAdaptersRejectsNilFactory(t *testing.T) {
	err := registerAdapters(language.NewRegistry(), []adapterFactory{nil})
	if !errors.Is(err, errNilAdapterFactory) {
		t.Fatalf("expected nil factory error, got %v", err)
	}
}

func TestRegisterAdaptersRejectsNilRegistry(t *testing.T) {
	if err := registerAdapters(nil, nil); err == nil {
		t.Fatalf("expected nil registry error")
	}
}
