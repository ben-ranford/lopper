package runtime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const descendantFailureOutputLimit = 64 * 1024

// The shared exec.Cmd writer serializes stdout/stderr writes. This mutex also
// protects the failure snapshot, which can run while the helper is still live.
type descendantFailureOutput struct {
	mu      sync.Mutex
	tail    []byte
	omitted int
}

func (o *descendantFailureOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(data)
	previous := len(o.tail)
	if n > descendantFailureOutputLimit {
		data = data[n-descendantFailureOutputLimit:]
	}
	if excess := len(o.tail) + len(data) - descendantFailureOutputLimit; excess > 0 {
		o.tail = o.tail[excess:]
	}
	o.tail = append(o.tail, data...)
	o.omitted += previous + n - len(o.tail)
	return n, nil
}

func (o *descendantFailureOutput) Snapshot() (string, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.tail), o.omitted
}

func descendantMarkerSnapshot(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("read_error=%v", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, 257))
	closeErr := file.Close()
	return fmt.Sprintf("contents=%q truncated=%t read_error=%v close_error=%v", contents[:min(256, len(contents))], len(contents) > 256, readErr, closeErr)
}

func TestDescendantFailureOutputSnapshot(t *testing.T) {
	var output descendantFailureOutput
	const writes = 1000
	const chunk = "helper stdout/stderr\n"
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range writes {
			if _, err := output.Write([]byte(chunk)); err != nil {
				t.Error(err)
			}
		}
	}()
	for range writes {
		if snapshot, omitted := output.Snapshot(); len(snapshot) > descendantFailureOutputLimit || omitted != 0 {
			t.Fatalf("unexpected output snapshot: length=%d omitted=%d", len(snapshot), omitted)
		}
	}
	<-done
	if snapshot, omitted := output.Snapshot(); snapshot != strings.Repeat(chunk, writes) || omitted != 0 {
		t.Fatalf("incomplete helper output: length=%d omitted=%d", len(snapshot), omitted)
	}
	for _, data := range []string{strings.Repeat("x", descendantFailureOutputLimit+13), "last output"} {
		if n, err := output.Write([]byte(data)); n != len(data) || err != nil {
			t.Fatalf("write output: n=%d error=%v", n, err)
		}
	}
	if snapshot, omitted := output.Snapshot(); snapshot != strings.Repeat("x", descendantFailureOutputLimit-len("last output"))+"last output" || omitted != writes*len(chunk)+13+len("last output") {
		t.Fatalf("incorrect bounded tail: length=%d omitted=%d", len(snapshot), omitted)
	}
}

func TestDescendantMarkerSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.pid")
	if got := descendantMarkerSnapshot(path); !strings.Contains(got, "read_error=") || strings.Contains(got, "read_error=<nil>") {
		t.Fatalf("missing marker error was lost: %s", got)
	}
	if err := os.WriteFile(path, []byte("invalid pid\n"+strings.Repeat("x", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	got := descendantMarkerSnapshot(path)
	if !strings.Contains(got, `contents="invalid pid\n`) || !strings.Contains(got, "truncated=true read_error=<nil> close_error=<nil>") || strings.Contains(got, strings.Repeat("x", 257)) {
		t.Fatalf("incorrect bounded marker snapshot: %s", got)
	}
}
