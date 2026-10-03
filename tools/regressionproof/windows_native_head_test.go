//go:build windows

package main

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestWindowsProofNativeGoAndCgo(t *testing.T) {
	// Exercise Start through the production boundary, including a real C
	// compiler invocation. A cross-compile cannot establish these guarantees.
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
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestNativeValue"}
	if err := r.expectPass(context.Background(), root, importPath, declaration); err != nil {
		t.Fatal(err)
	}
}
