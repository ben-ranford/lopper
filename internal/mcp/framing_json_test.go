package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func TestServeRejectsTrailingJSONBeforeDispatch(t *testing.T) {
	repo := t.TempDir()
	requests := []struct {
		name string
		req  rpcRequest
	}{
		{name: "initialize", req: rpcRequest{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`1`), Method: methodInitialize}},
		{name: "mutation", req: rpcRequest{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`1`), Method: methodToolsCall, Params: mustJSON(t, map[string]any{
			"name": toolApplyCodemod, "arguments": map[string]any{"repoPath": repo, "dependency": "lodash", "confirmApply": true},
		})}},
		{name: "notification", req: rpcRequest{JSONRPC: jsonrpcVersion, Method: methodInitialized}},
		{name: "invalid request", req: rpcRequest{JSONRPC: "invalid", ID: json.RawMessage(`1`), Method: methodInitialize}},
	}
	suffixes := []struct {
		name string
		data string
	}{
		{name: "object", data: ` {"jsonrpc":"2.0","id":2,"method":"tools/list"}`},
		{name: "number", data: ` 42`},
		{name: "null", data: ` null`},
		{name: "string", data: ` "extra"`},
		{name: "boolean", data: ` true`},
		{name: "array", data: ` []`},
		{name: "invalid bytes", data: ` trailing`},
		{name: "incomplete object", data: ` {`},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			for _, suffix := range suffixes {
				t.Run(suffix.name, func(t *testing.T) {
					runner := &fakeMutationRunner{}
					server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
					malformed := append(mustJSON(t, request.req), suffix.data...)
					recovery := mustJSON(t, rpcRequest{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`3`), Method: methodInitialize})
					responses := serveJSONFrames(t, server, malformed, recovery)
					if runner.applyCalled || runner.baselineCalled || runner.dashboardCalled {
						t.Error("malformed frame dispatched a mutation")
					}
					if len(responses) != 2 {
						t.Fatalf("expected parse error and recovery response, got %#v", responses)
					}
					first := responses[0]
					if first.Error == nil || first.Error.Code != codeParseError || string(first.ID) != "null" || first.Result != nil {
						t.Errorf("expected parse error with null ID and no result, got %#v", first)
					}
					if responses[1].Error != nil || string(responses[1].ID) != "3" || responses[1].Result == nil {
						t.Errorf("expected successful next frame, got %#v", responses[1])
					}
				})
			}
		})
	}
}

func TestServeAcceptsTrailingJSONWhitespace(t *testing.T) {
	runner := &fakeMutationRunner{}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
	payload := mustJSON(t, rpcRequest{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`1`), Method: methodToolsCall, Params: mustJSON(t, map[string]any{
		"name": toolApplyCodemod, "arguments": map[string]any{"repoPath": t.TempDir(), "dependency": "lodash", "confirmApply": true},
	})})
	responses := serveJSONFrames(t, server, append(payload, " \t\r\n"...))
	if len(responses) != 1 || responses[0].Error != nil || string(responses[0].ID) != "1" || !runner.applyCalled {
		t.Fatalf("expected whitespace-terminated mutation to execute, got responses=%#v called=%v", responses, runner.applyCalled)
	}
}

func serveJSONFrames(t *testing.T, server *Server, payloads ...[]byte) []rpcResponse {
	t.Helper()
	var input, output bytes.Buffer
	for _, payload := range payloads {
		writeTestFrame(t, &input, payload)
	}
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve frames: %v", err)
	}
	reader := bufio.NewReader(&output)
	var responses []rpcResponse
	for {
		payload, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return responses
		}
		if err != nil {
			t.Fatalf("read response frame: %v", err)
		}
		var response rpcResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		responses = append(responses, response)
	}
}
