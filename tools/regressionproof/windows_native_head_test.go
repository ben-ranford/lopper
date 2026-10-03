//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestWindowsProofNativeGoAndCgo(t *testing.T) {
	// Exercise Start through the production boundary, including a real C
	// compiler invocation. A cross-compile cannot establish these guarantees.
	// Hosted Go discovers its default cache through LOCALAPPDATA when GOCACHE
	// is unset; use a fresh directory so an inherited override cannot mask it.
	cacheRoot := t.TempDir()
	t.Setenv("GOCACHE", "")
	t.Setenv("LOCALAPPDATA", cacheRoot)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":            "module example.com/nativeproof\n\ngo 1.23\n",
		"pkg/value.go":      "package pkg\n/* static int value(void) { return 42; } */\nimport \"C\"\nfunc Value() int { return int(C.value()) }\n",
		"pkg/value_test.go": "package pkg\nimport \"testing\"\nfunc TestNativeValue(t *testing.T) { if Value() != 42 { t.Fatal(\"C compiler result\") } }\n",
	})
	r := &runner{}
	importPath, err := r.resolvePackage(context.Background(), root, "./pkg")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.compilePackage(context.Background(), root, "./pkg"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(cacheRoot, "go-build")); err != nil || !info.IsDir() {
		t.Fatalf("native proof did not create its default Windows cache: %v", err)
	}
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestNativeValue"}
	if err := r.expectPass(context.Background(), root, importPath, declaration); err != nil {
		t.Fatal(err)
	}
}
