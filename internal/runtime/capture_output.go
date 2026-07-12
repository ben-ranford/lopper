package runtime

import (
	"fmt"
	"sync"
)

const runtimeCommandOutputLimit = 64 * 1024

type boundedRuntimeCommandOutput struct {
	mu        sync.Mutex
	data      []byte
	start     int
	size      int
	truncated bool
}

func newRuntimeCommandOutput() *boundedRuntimeCommandOutput {
	return &boundedRuntimeCommandOutput{data: make([]byte, runtimeCommandOutputLimit)}
}

func (b *boundedRuntimeCommandOutput) Write(p []byte) (int, error) {
	written := len(p)
	if written == 0 {
		return 0, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if written >= len(b.data) {
		b.truncated = b.truncated || b.size > 0 || written > len(b.data)
		copy(b.data, p[written-len(b.data):])
		b.start = 0
		b.size = len(b.data)
		return written, nil
	}

	if overflow := b.size + written - len(b.data); overflow > 0 {
		b.truncated = true
		b.start = (b.start + overflow) % len(b.data)
		b.size -= overflow
	}
	b.append(p)
	return written, nil
}

func (b *boundedRuntimeCommandOutput) append(p []byte) {
	end := (b.start + b.size) % len(b.data)
	first := min(len(p), len(b.data)-end)
	copy(b.data[end:], p[:first])
	copy(b.data, p[first:])
	b.size += len(p)
}

func (b *boundedRuntimeCommandOutput) diagnostic() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	output := make([]byte, b.size)
	first := min(b.size, len(b.data)-b.start)
	copy(output, b.data[b.start:b.start+first])
	copy(output[first:], b.data[:b.size-first])
	if !b.truncated {
		return output
	}

	notice := []byte(fmt.Sprintf("[runtime test command output truncated to last %d bytes]\n", len(b.data)))
	return append(notice, output...)
}
