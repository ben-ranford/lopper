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
	var incompatibleFile string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		fileTarget, found, err := regressionTestFileTargetForDeclaration(repoRoot, packageDir, entry.Name(), declaration)
		if err != nil {
			return "", fmt.Errorf("route regression test %s::%s: %w", declaration.PackagePath, declaration.TestName, err)
		}
		if !found {
			continue
		}
		if fileTarget == "" {
			incompatibleFile = entry.Name()
			continue
		}
		if target != "" {
			return "", fmt.Errorf("regression test %s::%s is declared in multiple files", declaration.PackagePath, declaration.TestName)
		}
		target = fileTarget
	}
	if target != "" {
		return target, nil
	}
	if incompatibleFile != "" {
		return "", fmt.Errorf("route regression test %s::%s: %w", declaration.PackagePath, declaration.TestName, unsupportedRegressionFileTargetError(incompatibleFile, declaration.TargetOS))
	}
	return "", fmt.Errorf("regression test %s::%s has no matching test function", declaration.PackagePath, declaration.TestName)
}

func regressionTestFileTargetForDeclaration(repoRoot, packageDir, filename string, declaration prmetadata.RegressionDeclaration) (string, bool, error) {
	path := filepath.Join(packageDir, filename)
	data, file, err := readRegressionSource(repoRoot, path)
	if err != nil {
		return "", false, err
	}
	if !declaresTest(file, declaration.TestName) {
		return "", false, nil
	}
	if declaration.TargetOS == "" {
		target, err := testFileTarget(packageDir, filename, data)
		return target, true, err
	}
	matches, err := testFileMatchesTarget(packageDir, filename, data, declaration.TargetOS)
	if err != nil {
		return "", true, err
	}
	if !matches {
		return "", true, nil
	}
	return declaration.TargetOS, true, nil
}

func unsupportedRegressionFileTargetError(filename, declaredTarget string) error {
	return fmt.Errorf("test source %s cannot run on a supported native linux, windows or darwin proof runner (declared target %q)", filename, declaredTarget)
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

func testFileTarget(dir, name string, data []byte) (string, error) {
	// Portable declarations keep Linux priority. All jobs use identical contexts.
	for _, targetOS := range []string{"linux", "windows", "darwin"} {
		matches, err := testFileMatchesTarget(dir, name, data, targetOS)
		if err != nil {
			return "", err
		}
		if matches {
			return targetOS, nil
		}
	}
	return "", unsupportedRegressionFileTargetError(name, "")
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
