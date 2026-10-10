//go:build windows

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	process "github.com/ben-ranford/lopper/internal/runtime"
	"golang.org/x/sys/windows"
)

const probeProgram = `import sys,json,re,encodings,_json,_sre
rows=json.loads('[{"tag_name":"v1.8.2","draft":false,"prerelease":false}]')
assert [r['tag_name'] for r in rows if not r['draft'] and not r['prerelease'] and re.fullmatch(r'v1\.8\.\d+',r['tag_name'])]==['v1.8.2']
origins={m.__name__:getattr(m,'__file__',None) for m in (json,re,encodings,_json,_sre)}
print(json.dumps({'kind':'READY','nonce':sys.argv[1],'executable':sys.executable,'prefix':sys.prefix,'version':list(sys.version_info[:3]),'origins':origins},sort_keys=True),flush=True)
assert sys.stdin.readline()==('RELEASE '+sys.argv[1]+'\n')
`

type probeResult struct {
	Ready    []byte
	Modules  []record
	PID      int
	Creation string
}

func probe(ctx context.Context, private receipt, env []string) (result probeResult, returnErr error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	executable := filepath.Join(private.Root, "python.exe")
	if err := requireAMD64File(executable); err != nil {
		return result, err
	}
	if err := requireAMD64Process(windows.CurrentProcess()); err != nil {
		return result, err
	}
	cwd, err := privateProbeDirectory(filepath.Dir(private.Root))
	if err != nil {
		return result, err
	}
	joined := true
	defer func() {
		if joined {
			returnErr = errors.Join(returnErr, os.RemoveAll(cwd))
		}
	}()
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return result, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	stderr := &limitedOutput{limit: maxStreamBytes, cancel: cancel}
	env = append(append([]string{}, env...), "TEMP="+cwd, "TMP="+cwd)
	cmd, err := pythonCommand(ctx, executable, []string{"-c", probeProgram, nonce}, env, nil, nil, stderr)
	if err != nil {
		return result, err
	}
	cmd.Dir = cwd
	input, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProbePipe(input)) }()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProbePipe(output)) }()
	process.ConfigureCommandCancellation(cmd)
	cleanup, err := process.StartCommand(cmd)
	if err != nil {
		return result, err
	}
	joined = false
	defer func() {
		if returnErr != nil {
			cancel()
		}
		waitErr := cmd.Wait()
		joinErr := cleanup()
		joined = joinErr == nil
		returnErr = errors.Join(returnErr, waitErr, joinErr, ctx.Err())
		if len(stderr.bytes()) != 0 {
			returnErr = errors.Join(returnErr, errors.New("probe emitted stderr"))
		}
	}()
	return inspectHeldProbe(ctx, cmd, input, output, nonce, private, cancel)
}

func inspectHeldProbe(ctx context.Context, cmd *exec.Cmd, input io.Writer, output io.Reader, nonce string, private receipt, cancel context.CancelFunc) (result probeResult, returnErr error) {
	executable := filepath.Join(private.Root, "python.exe")
	owned, err := observeProcess(cmd.Process, executable)
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, windows.CloseHandle(owned.handle)) }()
	reader := bufio.NewReaderSize(output, maxHandshakeBytes)
	ready, err := readReady(ctx, reader, cancel)
	if err != nil {
		return result, err
	}
	parsed, err := decodeReady(ready, nonce, private.Root, executable)
	if err != nil {
		return result, err
	}
	if err := checkOrigins(parsed, private); err != nil {
		return result, err
	}
	modules, err := heldModules(ctx, &owned, private)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if _, err := fmt.Fprintf(input, "RELEASE %s\n", nonce); err != nil {
		return result, err
	}
	// Child exits after RELEASE. Reject any trailing frame, including a second READY.
	trailing, err := boundedBytes(reader, 0)
	if err != nil {
		return result, err
	}
	if len(trailing) != 0 {
		return result, errors.New("probe emitted extra output")
	}
	return probeResult{Ready: ready, Modules: modules, PID: cmd.Process.Pid, Creation: fmt.Sprintf("%08x%08x", owned.created.HighDateTime, owned.created.LowDateTime)}, nil
}

func readReady(ctx context.Context, reader *bufio.Reader, cancel context.CancelFunc) ([]byte, error) {
	type response struct {
		data []byte
		err  error
	}
	done := make(chan response, 1)
	go func() {
		data, err := reader.ReadSlice('\n')
		done <- response{data: append([]byte{}, data...), err: err}
	}()
	select {
	case result := <-done:
		return result.data, result.err
	case <-ctx.Done():
		cancel()
		result := <-done
		return nil, errors.Join(ctx.Err(), result.err)
	}
}

func checkOrigins(ready probeReady, private receipt) error {
	known := map[string]bool{}
	for _, row := range private.Records {
		known[filepath.Join(private.Root, filepath.FromSlash(row.Path))] = true
	}
	for _, origin := range ready.Origins {
		if origin != nil && !known[*origin] {
			return errors.New("probe module origin outside inventory")
		}
	}
	return nil
}

func privateProbeDirectory(parent string) (path string, returnErr error) {
	path, err := os.MkdirTemp(parent, "lopper-python-probe-")
	if err != nil {
		return "", err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, os.RemoveAll(path))
		}
	}()
	if err := os.Chmod(path, 0700); err != nil {
		return path, err
	}
	return path, canonicalDirectory(path)
}

func closeProbePipe(pipe io.Closer) error {
	err := pipe.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
