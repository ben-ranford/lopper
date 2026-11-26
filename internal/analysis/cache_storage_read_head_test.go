package analysis

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCachePointerReadClassifiesOrdinaryInvalidStates(t *testing.T) {
	root := t.TempDir()
	pointerPath := filepath.Join(root, "keys", "entry.json")
	if err := os.MkdirAll(filepath.Dir(pointerPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pointerPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	cache := &analysisCache{options: resolvedCacheOptions{Path: root}}
	if _, reason, err := cache.readCachePointer(cacheEntryDescriptor{AdapterID: "jvm"}, pointerPath); err != nil || reason != cachePointerCorruptReason {
		t.Fatalf("JVM pointer state: reason=%q error=%v", reason, err)
	}

	oversizedPointerPath := filepath.Join(root, "keys", "oversized.json")
	if err := os.WriteFile(oversizedPointerPath, bytes.Repeat([]byte("x"), maxCachePointerBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := cache.readCachePointer(cacheEntryDescriptor{}, oversizedPointerPath); err != nil || reason != cachePointerCorruptReason {
		t.Fatalf("bounded pointer state: reason=%q error=%v", reason, err)
	}
}
