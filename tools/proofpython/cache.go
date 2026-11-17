package main

import (
	"errors"
	"os"
	"path/filepath"
)

func cachedPython(runner, agent string) (string, error) {
	if runner == "" {
		return "", errors.New("runner tool cache is required")
	}
	if err := canonicalDirectory(runner); err != nil {
		return "", err
	}
	if agent != "" && agent != runner {
		return "", errors.New("effective tool cache differs from independent runner cache")
	}
	root := filepath.Join(runner, "Python", "3.13.16", "x64")
	if err := canonicalDirectory(root); err != nil {
		return "", err
	}
	if err := canonicalRegular(root + ".complete"); err != nil {
		return "", errors.New("completed cached interpreter is absent")
	}
	if info, err := os.Stat(filepath.Join(root, "python.exe")); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("cached interpreter missing")
	}
	return root, nil
}
