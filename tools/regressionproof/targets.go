package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ben-ranford/lopper/internal/prmetadata"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func validateProofTarget(targetOS string) error {
	switch targetOS {
	case "", "linux", "windows":
		return nil
	default:
		return fmt.Errorf("unsupported regression proof target %q: expected linux or windows", targetOS)
	}
}

func requireNativeProofTarget(targetOS string) error {
	if targetOS != "" && targetOS != runtime.GOOS {
		return fmt.Errorf("regression proof target %s requires a native %s runner; running on %s", targetOS, targetOS, runtime.GOOS)
	}
	if targetOS != "" && runtime.GOARCH != "amd64" {
		return fmt.Errorf("regression proof target %s requires the hosted amd64 architecture; running on %s", targetOS, runtime.GOARCH)
	}
	return nil
}

func selectTargetDeclarations(repoRoot string, declarations []prmetadata.RegressionDeclaration, targetOS string) ([]prmetadata.RegressionDeclaration, error) {
	if targetOS == "" {
		return declarations, nil
	}
	var selected []prmetadata.RegressionDeclaration
	for _, declaration := range declarations {
		target, err := declarationTarget(repoRoot, declaration)
		if err != nil {
			return nil, err
		}
		if target == targetOS {
			selected = append(selected, declaration)
		}
	}
	return selected, nil
}

func declarationTarget(repoRoot string, declaration prmetadata.RegressionDeclaration) (string, error) {
	packageDir := filepath.Join(repoRoot, filepath.FromSlash(declaration.PackagePath))
	entries, err := os.ReadDir(packageDir)
	if err != nil {
		return "", fmt.Errorf("read regression package %s: %w", declaration.PackagePath, err)
	}
	var target string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(packageDir, entry.Name())
		data, err := safeio.ReadFileUnder(repoRoot, path)
		if err != nil {
			return "", fmt.Errorf("read regression source %s: %w", path, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
		if err != nil {
			return "", fmt.Errorf("parse regression source %s: %w", path, err)
		}
		if !declaresTest(file, declaration.TestName) {
			continue
		}
		if target != "" {
			return "", fmt.Errorf("regression test %s::%s is declared in multiple files", declaration.PackagePath, declaration.TestName)
		}
		target, err = testFileTarget(packageDir, entry.Name(), data)
		if err != nil {
			return "", fmt.Errorf("route regression test %s::%s: %w", declaration.PackagePath, declaration.TestName, err)
		}
	}
	if target == "" {
		return "", fmt.Errorf("regression test %s::%s has no matching test function", declaration.PackagePath, declaration.TestName)
	}
	return target, nil
}

func declaresTest(file *ast.File, testName string) bool {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == testName {
			return true
		}
	}
	return false
}

func testFileTarget(dir, name string, data []byte) (string, error) {
	// Prefer Linux whenever both supported hosts can compile the declaration.
	// MatchFile honors Go's filename, architecture and build-tag constraints.
	for _, targetOS := range []string{"linux", "windows"} {
		// Both jobs must classify with identical contexts, independent of their
		// host defaults, or conditional cgo/architecture tags can omit a proof.
		buildContext := build.Context{
			GOOS: targetOS, GOARCH: "amd64", Compiler: "gc", CgoEnabled: true,
			BuildTags: []string{regressionProofBuildTag}, ToolTags: []string{"amd64.v1"},
			ReleaseTags: build.Default.ReleaseTags,
		}
		buildContext.OpenFile = func(string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
		matches, err := buildContext.MatchFile(dir, name)
		if err != nil {
			return "", err
		}
		if matches {
			return targetOS, nil
		}
	}
	return "", fmt.Errorf("test source %s cannot run on a supported native linux or windows proof runner", name)
}
