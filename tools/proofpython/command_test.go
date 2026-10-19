package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPythonTransportKeepsArgumentsAndSeries(t *testing.T) {
	args := []string{"-c", "import sys; print(sys.argv)", "with spaces", ""}
	env := []string{"SERIES=v1.8", "PYTHONPATH=untrusted", "Path=kept", "LD_PRELOAD=bad", "PYTHONHOME=bad", "DYLD_INSERT_LIBRARIES=bad"}
	executable := filepath.Join(providerTestRoot(t), "python.exe")
	if err := os.WriteFile(executable, []byte("compiled runtime fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd, err := pythonCommand(context.Background(), executable, args, env, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{executable, "-I", "-S", "-B", "-c", args[1], "with spaces", ""}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("arguments changed: %q", cmd.Args)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"SERIES=v1.8", "Path=kept"}) {
		t.Fatalf("environment changed: %q", cmd.Env)
	}
	if len(args) != 4 || len(env) != 6 {
		t.Fatal("caller slices changed")
	}
	if _, err := pythonCommand(context.Background(), "python.exe", args, env, nil, nil, nil); err == nil {
		t.Fatal("ambient interpreter accepted")
	}
}

func TestProofCommandRejectsOtherFamilies(t *testing.T) {
	for _, args := range [][]string{{"test", "-count=1", "./internal/githubaction"}, {"run", "./tools/regressionproof", "--base-sha", "abc", "--target-os", "windows"}} {
		if err := proofArguments(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{nil, {"run", "./evil"}, {"test", "./..."}, {"sh", "-c", "anything"}} {
		if err := proofArguments(args); err == nil {
			t.Fatalf("unapproved command accepted: %q", args)
		}
	}
}

func TestProofCommandFlagsRemainWithinNativeFamilies(t *testing.T) {
	for _, args := range [][]string{
		{"test", "-run", "TestInstaller", "-race", "-json", "-p=1", "-timeout=20m", "./internal/githubaction"},
		{"run", "./tools/regressionproof", "--repo", ".", "--body-file", "body.md", "--title", "fix(action): install", "--base-sha", "abc", "--target-os", "windows"},
	} {
		if err := proofArguments(args); err != nil {
			t.Fatalf("approved invocation rejected: %v", err)
		}
	}
	for _, args := range [][]string{
		{"test", "-run", "./internal/githubaction"}, {"test", "-exec=untrusted", "./internal/githubaction"},
		{"run", "./tools/regressionproof", "--target-os", "linux"},
		{"run", "./tools/regressionproof", "--target-os", "windows", "--target-os", "windows"},
		{"run", "./tools/regressionproof", "--repo"}, {"run", "./tools/regressionproof"},
	} {
		if err := proofArguments(args); err == nil {
			t.Fatalf("unapproved invocation accepted: %q", args)
		}
	}
}

func TestPythonCommandRequiresCanonicalRegularExecutable(t *testing.T) {
	root := providerTestRoot(t)
	executable := filepath.Join(root, "python.exe")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"relative":      "python.exe",
		"missing":       filepath.Join(root, "absent.exe"),
		"directory":     root,
		"dot component": root + string(os.PathSeparator) + "." + string(os.PathSeparator) + "python.exe",
	} {
		t.Run(name, func(t *testing.T) {
			if cmd, err := pythonCommand(context.Background(), path, nil, nil, nil, nil, nil); err == nil || cmd != nil {
				t.Fatalf("untrusted executable accepted: %v %v", cmd, err)
			}
		})
	}
}
