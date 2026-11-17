package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

type probeReady struct {
	Kind       string             `json:"kind"`
	Nonce      string             `json:"nonce"`
	Executable string             `json:"executable"`
	Prefix     string             `json:"prefix"`
	Version    []int              `json:"version"`
	Origins    map[string]*string `json:"origins"`
}

type limitedOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
	cancel func()
}

func (lo *limitedOutput) Write(data []byte) (int, error) {
	lo.mu.Lock()
	defer lo.mu.Unlock()
	if len(data) > lo.limit-lo.buffer.Len() {
		lo.cancel()
		return 0, errors.New("probe output limit exceeded")
	}
	return lo.buffer.Write(data)
}
func (lo *limitedOutput) bytes() []byte {
	lo.mu.Lock()
	defer lo.mu.Unlock()
	return append([]byte{}, lo.buffer.Bytes()...)
}

func decodeReady(data []byte, nonce, root, executable string) (probeReady, error) {
	if len(data) == 0 || len(data) > maxHandshakeBytes || bytes.Count(data, []byte{'\n'}) != 1 || data[len(data)-1] != '\n' {
		return probeReady{}, errors.New("invalid READY framing")
	}
	if err := validateProbeJSON(data); err != nil {
		return probeReady{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var ready probeReady
	if err := decoder.Decode(&ready); err != nil {
		return probeReady{}, err
	}
	if ready.Kind != "READY" || ready.Nonce != nonce || !strings.EqualFold(ready.Executable, executable) || !strings.EqualFold(ready.Prefix, root) {
		return probeReady{}, errors.New("probe READY identity mismatch")
	}
	if len(ready.Version) != 3 || ready.Version[0] != 3 || ready.Version[1] != 13 || ready.Version[2] != 16 {
		return probeReady{}, errors.New("unexpected Python version")
	}
	if len(ready.Origins) != 5 {
		return probeReady{}, errors.New("incomplete module origins")
	}
	for _, name := range []string{"json", "re", "encodings", "_json", "_sre"} {
		if _, ok := ready.Origins[name]; !ok {
			return probeReady{}, errors.New("missing module origin")
		}
	}
	return ready, nil
}
