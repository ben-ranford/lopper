package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

type transportWitness struct {
	Args                    []string
	Series, PythonHome, CWD string
	Input                   []byte
}

func TestMain(m *testing.M) {
	if os.Getenv("LOPPER_PYTHON_MAIN_CHILD") == "1" {
		main()
		return
	}
	if os.Getenv("LOPPER_PYTHON_TRANSPORT_CHILD") == "1" {
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
		if err != nil {
			os.Exit(90)
		}
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(91)
		}
		witness := transportWitness{Args: os.Args[1:], Series: os.Getenv("SERIES"), PythonHome: os.Getenv("PYTHONHOME"), CWD: cwd, Input: input}
		if err := json.NewEncoder(os.Stdout).Encode(witness); err != nil {
			os.Exit(92)
		}
		if _, err := os.Stderr.Write([]byte("fixture stderr\n")); err != nil {
			os.Exit(93)
		}
		os.Exit(7)
	}
	os.Exit(m.Run())
}

func TestTransportRealProcessPreservesStreamsStatusAndCWD(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	root := providerTestRoot(t)
	input := []byte{'a', 0, 255, '\n'}
	env := append(os.Environ(), "LOPPER_PYTHON_TRANSPORT_CHILD=1", "SERIES=v1.8", "PYTHONHOME=untrusted")
	var stdout, stderr bytes.Buffer
	args := []string{"-c", "literal program", "argument with spaces", ""}
	cmd, err := pythonCommand(context.Background(), executable, args, env, bytes.NewReader(input), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = root
	commandErr, joinErr := runJoined(cmd)
	var exit *exec.ExitError
	if !errors.As(commandErr, &exit) || exit.ExitCode() != 7 || joinErr != nil {
		t.Fatalf("transport status/join changed: %v / %v", commandErr, joinErr)
	}
	var got transportWitness
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := transportWitness{Args: append([]string{"-I", "-S", "-B"}, args...), Series: "v1.8", CWD: root, Input: input}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transport semantics changed: got %+v want %+v", got, want)
	}
	if stderr.String() != "fixture stderr\n" {
		t.Fatalf("stderr changed: %q", stderr.String())
	}
}
