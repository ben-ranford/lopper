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
	return prmetadata.ValidateRegressionPlatform(targetOS)
}

func requireNativeProofTarget(targetOS string) error {
	if targetOS != "" && targetOS != runtime.GOOS {
		return fmt.Errorf("regression proof target %s requires a native %s runner; running on %s", targetOS, targetOS, runtime.GOOS)
	}
	if targetOS != "" && runtime.GOARCH != proofTargetArchitecture(targetOS) {
		return fmt.Errorf("regression proof target %s requires the hosted %s architecture; running on %s", targetOS, proofTargetArchitecture(targetOS), runtime.GOARCH)
	}
	return nil
}

func selectTargetDeclarations(repoRoot string, declarations []prmetadata.RegressionDeclaration, targetOS string) ([]prmetadata.RegressionDeclaration, error) {
	if targetOS == "" {
		return declarations, validateLocalDeclarationTargets(repoRoot, declarations)
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
		data, file, err := readRegressionSource(repoRoot, path)
		if err != nil {
			return "", err
		}
		if !declaresTest(file, declaration.TestName) {
			continue
		}
		if target != "" {
			return "", fmt.Errorf("regression test %s::%s is declared in multiple files", declaration.PackagePath, declaration.TestName)
		}
		target, err = testFileTarget(packageDir, entry.Name(), data, declaration.TargetOS)
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

func proofTargetArchitecture(targetOS string) string {
	if targetOS == "darwin" {
		return "arm64"
	}
	return "amd64"
}

func testFileTarget(dir, name string, data []byte, declaredTarget string) (string, error) {
	// Portable declarations keep Linux priority. All jobs use identical contexts.
	targets := []string{"linux", "windows", "darwin"}
	if declaredTarget != "" {
		targets = []string{declaredTarget}
	}
	for _, targetOS := range targets {
		matches, err := testFileMatchesTarget(dir, name, data, targetOS)
		if err != nil {
			return "", err
		}
		if matches {
			return targetOS, nil
		}
	}
	return "", fmt.Errorf("test source %s cannot run on a supported native linux, windows or darwin proof runner (declared target %q)", name, declaredTarget)
}

func testFileMatchesTarget(dir, name string, data []byte, targetOS string) (bool, error) {
	arch := proofTargetArchitecture(targetOS)
	toolTags := []string{"amd64.v1"}
	if arch == "arm64" {
		toolTags = []string{"arm64.v8.0"}
	}
	buildContext := build.Context{
		GOOS: targetOS, GOARCH: arch, Compiler: "gc", CgoEnabled: true,
		BuildTags: []string{regressionProofBuildTag}, ToolTags: toolTags,
		ReleaseTags: build.Default.ReleaseTags,
	}
	buildContext.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return buildContext.MatchFile(dir, name)
}

func validateLocalDeclarationTargets(repoRoot string, declarations []prmetadata.RegressionDeclaration) error {
	for _, declaration := range declarations {
		if declaration.TargetOS == "" {
			continue
		}
		if _, err := declarationTarget(repoRoot, declaration); err != nil {
			return err
		}
		if err := requireNativeProofTarget(declaration.TargetOS); err != nil {
			return err
		}
	}
	return nil
}

func readRegressionSource(repoRoot, path string) ([]byte, *ast.File, error) {
	data, err := safeio.ReadFileUnder(repoRoot, path)
	if err != nil {
		return nil, nil, fmt.Errorf("read regression source %s: %w", path, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("parse regression source %s: %w", path, err)
	}
	return data, file, nil
}
