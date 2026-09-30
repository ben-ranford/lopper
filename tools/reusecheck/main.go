package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/reusecheck"
)

var processExit = os.Exit

func main() { processExit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) (result int) {
	flags := flag.NewFlagSet("reusecheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	exceptionsPath := flags.String("exceptions", "", "reviewed exact-source exception JSON")
	legacy := flags.Bool("legacy-advisory", true, "report existing #1613/#1614 migration sites as advisory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		if _, err := fmt.Fprintln(stderr, "unexpected positional arguments"); err != nil {
			return 2
		}
		return 2
	}
	sourceRoot, err := openSourceRoot(*root)
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 2
		}
		return 2
	}
	return scanRoot(&rootHandle{root: sourceRoot}, *exceptionsPath, *legacy, stdout, stderr)
}

type rootFS interface {
	ReadFile(string) ([]byte, error)
	FileSystem() fs.FS
	Close() error
}

type rootHandle struct {
	root *os.Root
}

func (r *rootHandle) ReadFile(path string) ([]byte, error) {
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(r.root.Name(), path)
		if err != nil {
			return nil, err
		}
		path = relative
	}
	return r.root.ReadFile(path)
}
func (r *rootHandle) FileSystem() fs.FS { return r.root.FS() }
func (r *rootHandle) Close() error      { return r.root.Close() }

func scanRoot(root rootFS, exceptionsPath string, legacy bool, stdout, stderr io.Writer) (result int) {
	defer func() { result = finishRoot(result, root.Close(), stderr) }()
	exceptions, err := loadExceptions(root, exceptionsPath)
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 2
		}
		return 2
	}
	violations, err := scanSources(root, exceptions, legacy, stdout)
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 2
		}
		return 2
	}
	if violations > 0 {
		return 1
	}
	return 0
}

func scanSources(root rootFS, exceptions []reusecheck.Exception, legacy bool, stdout io.Writer) (int, error) {
	sources := make(map[string][]byte)
	err := fs.WalkDir(root.FileSystem(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if skipDirectory(path, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := root.ReadFile(path)
		if err == nil {
			sources[filepath.ToSlash(path)] = data
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	return scanPackageSources(sources, exceptions, legacy, stdout)
}

func skipDirectory(path, name string) bool {
	return path != "." && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata")
}

func scanPackageSources(sources map[string][]byte, exceptions []reusecheck.Exception, legacy bool, stdout io.Writer) (int, error) {
	findings, err := reusecheck.AnalyzeSources(sources)
	if err != nil {
		return 0, err
	}
	violations := 0
	for _, finding := range findings {
		data := sources[finding.Path]
		if reusecheck.Approved(finding, data, exceptions) {
			continue
		}
		finding.Advisory = finding.Advisory || legacy && reusecheck.LegacyAdvisory(finding, data)
		if _, err := fmt.Fprintln(stdout, finding.String()); err != nil {
			return violations, err
		}
		if !finding.Advisory {
			violations++
		}
	}
	return violations, nil
}

func finishRoot(result int, closeErr error, stderr io.Writer) int {
	if closeErr == nil {
		return result
	}
	if _, writeErr := fmt.Fprintln(stderr, closeErr); writeErr != nil {
		return 2
	}
	return 2
}

func openSourceRoot(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(absolute)
}

func loadExceptions(root rootFS, path string) ([]reusecheck.Exception, error) {
	if path == "" {
		return nil, nil
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return reusecheck.ReadExceptions(bytes.NewReader(data))
}
