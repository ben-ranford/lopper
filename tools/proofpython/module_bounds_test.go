package main

import "testing"

func TestModuleCountRejectsPartialExcessAndOverflow(t *testing.T) {
	if count, err := moduleCount(maxModules*8, 8); err != nil || count != maxModules {
		t.Fatalf("exact snapshot rejected: %d %v", count, err)
	}
	for _, pair := range [][2]uint32{{0, 8}, {9, 8}, {(maxModules + 1) * 8, 8}, {0xffffffff, 8}, {8, 0}, {8, 16}} {
		if _, err := moduleCount(pair[0], pair[1]); err == nil {
			t.Fatalf("invalid snapshot admitted: %v", pair)
		}
	}
	if err := modulePathLength(maxPathUnits - 1); err != nil {
		t.Fatal(err)
	}
	for _, length := range []uintptr{0, maxPathUnits, maxPathUnits + 1, ^uintptr(0)} {
		if err := modulePathLength(length); err == nil {
			t.Fatalf("truncated path admitted: %d", length)
		}
	}
}
