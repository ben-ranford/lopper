package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
)

const runtimeTraceStateSchema = "v1"
const runtimeTraceStateSuffix = ".state.json"

type runtimeTraceState struct {
	Schema  string `json:"schema"`
	Command string `json:"command"`
}

func reuseRuntimeTraceIfPossible(tracePath, command string) (bool, error) {
	info, err := os.Stat(tracePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}

	stateData, err := os.ReadFile(runtimeTraceStatePath(tracePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	state, ok := parseRuntimeTraceState(stateData)
	if !ok {
		return false, nil
	}
	return strings.TrimSpace(state.Command) == strings.TrimSpace(command), nil
}

func runtimeTraceStatePath(tracePath string) string {
	return tracePath + runtimeTraceStateSuffix
}

func parseRuntimeTraceState(stateData []byte) (runtimeTraceState, bool) {
	var state runtimeTraceState
	if err := json.Unmarshal(stateData, &state); err != nil {
		return runtimeTraceState{}, false
	}
	if strings.TrimSpace(state.Schema) != runtimeTraceStateSchema {
		return runtimeTraceState{}, false
	}
	if strings.TrimSpace(state.Command) == "" {
		return runtimeTraceState{}, false
	}
	return state, true
}

func writeRuntimeTraceState(tracePath, command string) error {
	payload := []byte(`{"schema":"` + runtimeTraceStateSchema + `","command":` + strconv.Quote(strings.TrimSpace(command)) + `}`)
	return os.WriteFile(runtimeTraceStatePath(tracePath), payload, 0o600)
}
