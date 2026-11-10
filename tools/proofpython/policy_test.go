package main

import (
	"strings"
	"testing"
)

func TestInventoryBudgetRejectsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		used, add, limit uint64
		want             bool
	}{
		{"exact", 3, 2, 5, true},
		{"over", 3, 3, 5, false},
		{"overflow", ^uint64(0), 1, ^uint64(0), false},
		{"already over", 6, 0, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used := tc.used
			err := reserve(&used, tc.add, tc.limit)
			if (err == nil) != tc.want {
				t.Fatalf("reserve returned %v", err)
			}
			if err != nil && used != tc.used {
				t.Fatal("failed reservation mutated budget")
			}
		})
	}
}

func TestInventoryRelativePaths(t *testing.T) {
	for _, path := range []string{"../x", "/x", "a/../x", "a//x", `a\b`, "a:x", "a/.", strings.Repeat("x", 256), strings.Repeat("a/", 32) + "x"} {
		if err := validateRelative(path); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	for _, path := range []string{".", "Lib/json/__init__.py", strings.Repeat("x", 255)} {
		if err := validateRelative(path); err != nil {
			t.Errorf("rejected %q: %v", path, err)
		}
	}
}

func TestEntryLimitRejectsBeforeAccounting(t *testing.T) {
	for _, budget := range []inventoryBudget{{entries: maxEntries}, {directories: maxDirectories}, {files: maxFiles}} {
		original := budget
		directory := budget.files == 0
		if err := budget.admit(directory); err == nil {
			t.Fatalf("exhausted inventory admitted: %+v", budget)
		}
		if budget != original {
			t.Fatalf("rejected entry changed accounting: %+v -> %+v", original, budget)
		}
	}
	exact := inventoryBudget{entries: maxEntries - 1, files: maxFiles - 1}
	if err := exact.admit(false); err != nil {
		t.Fatal(err)
	}
	if exact.entries != maxEntries || exact.files != maxFiles {
		t.Fatal("exact boundary accounting changed")
	}
}
