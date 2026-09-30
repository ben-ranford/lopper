//go:build !windows

package main

import "github.com/ben-ranford/lopper/internal/gitexec"

func resolveProofGitBinaryPath() (string, error) {
	return gitexec.ResolveBinaryPath()
}

func proofGitEnv() []string {
	return gitexec.SanitizedEnv()
}

func proofGoBinaryPath() (string, error) {
	return "go", nil
}
