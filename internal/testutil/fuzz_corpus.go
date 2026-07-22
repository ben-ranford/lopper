package testutil

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type ByteFuzzSeed struct {
	Name string
	Data []byte
}

func LoadByteFuzzCorpus(tb testing.TB, dir string) []ByteFuzzSeed {
	tb.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("read fuzz corpus %s: %v", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		tb.Fatalf("open fuzz corpus root %s: %v", dir, err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			tb.Fatalf("close fuzz corpus root %s: %v", dir, err)
		}
	}()

	seeds := make([]ByteFuzzSeed, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			tb.Fatalf("fuzz corpus %s contains nested directory %s", dir, entry.Name())
		}

		body := readRootedFuzzSeed(tb, root, dir, entry.Name())
		path := filepath.Join(dir, entry.Name())
		seeds = append(seeds, ByteFuzzSeed{
			Name: entry.Name(),
			Data: parseByteFuzzSeed(tb, path, body),
		})
	}

	slices.SortFunc(seeds, func(a, b ByteFuzzSeed) int {
		return strings.Compare(a.Name, b.Name)
	})
	return seeds
}

func readRootedFuzzSeed(tb testing.TB, root *os.Root, dir string, name string) string {
	tb.Helper()

	file, err := root.Open(name)
	if err != nil {
		tb.Fatalf("open fuzz seed %s: %v", filepath.Join(dir, name), err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			tb.Fatalf("close fuzz seed %s: %v", filepath.Join(dir, name), err)
		}
	}()

	body, err := io.ReadAll(file)
	if err != nil {
		tb.Fatalf("read fuzz seed %s: %v", filepath.Join(dir, name), err)
	}
	return string(body)
}

func parseByteFuzzSeed(tb testing.TB, path string, raw string) []byte {
	tb.Helper()

	header, body, ok := strings.Cut(raw, "\n")
	if !ok {
		tb.Fatalf("fuzz seed %s is missing a payload line", path)
	}
	if header != "go test fuzz v1" {
		tb.Fatalf("fuzz seed %s has unexpected header %q", path, header)
	}

	payload := strings.TrimSpace(body)
	if !strings.HasPrefix(payload, "[]byte(") || !strings.HasSuffix(payload, ")") {
		tb.Fatalf("fuzz seed %s must wrap a []byte literal, got %q", path, payload)
	}

	literal := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(payload, "[]byte("), ")"))
	value, err := strconv.Unquote(literal)
	if err != nil {
		tb.Fatalf("unquote fuzz seed %s: %v", path, err)
	}
	return []byte(value)
}
