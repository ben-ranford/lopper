package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/version"
)

const (
	// ServerPreviewFeature is the feature flag name that enables MCP server startup.
	ServerPreviewFeature   = "mcp-server-preview"
	jsonrpcVersion         = "2.0"
	defaultProtocolVersion = "2024-11-05"
	methodInitialize       = "initialize"
	methodToolsList        = "tools/list"
	methodToolsCall        = "tools/call"
	methodInitialized      = "notifications/initialized"
	methodCancelled        = "notifications/cancelled"
	codeParseError         = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeInternalError      = -32603
	defaultServerName      = "lopper"
	defaultServerVersion   = "dev"
	defaultTopN            = 20
	defaultLanguage        = "auto"
	defaultScopeMode       = analysis.ScopeModePackage
	defaultRuntimeProfile  = "node-import"
	defaultTimeoutMillis   = 0
	maxTimeoutMillis       = 24 * 60 * 60 * 1000
	errorCodeToolFailed    = "tool_failed"
	errorCodeInvalidInput  = "invalid_input"
	errorCodeTimeout       = "timeout"
	errorCodeCancelled     = "cancelled"
)

type Options struct {
	Analyzer         analysis.Analyser
	LanguageRegistry *language.Registry
	FeatureRegistry  *featureflags.Registry
	Features         featureflags.Set
	MutationRunner   MutationRunner
	ServerName       string
	ServerVersion    string
}

type Server struct {
	analyzer         analysis.Analyser
	languageRegistry *language.Registry
	featureRegistry  *featureflags.Registry
	features         featureflags.Set
	mutationRunner   MutationRunner
	serverName       string
	serverVersion    string
	writeMu          sync.Mutex
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type serverCapabilities struct {
	Tools map[string]any `json:"tools"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

var currentVersion = version.Current

func NewServer(opts Options) *Server {
	featureRegistry := opts.FeatureRegistry
	if featureRegistry == nil {
		featureRegistry = featureflags.DefaultRegistry()
	}
	features := opts.Features
	if features.Snapshot() == nil {
		features = resolveDefaultServerFeatures(featureRegistry)
	}

	serverVersion := opts.ServerVersion
	if serverVersion == "" {
		serverVersion = currentVersion().Version
	}
	if serverVersion == "" {
		serverVersion = defaultServerVersion
	}

	serverName := opts.ServerName
	if serverName == "" {
		serverName = defaultServerName
	}

	return &Server{
		analyzer:         opts.Analyzer,
		languageRegistry: opts.LanguageRegistry,
		featureRegistry:  featureRegistry,
		features:         features,
		mutationRunner:   opts.MutationRunner,
		serverName:       serverName,
		serverVersion:    serverVersion,
	}
}

func resolveDefaultServerFeatures(registry *featureflags.Registry) featureflags.Set {
	info := currentVersion()
	channel, err := featureflags.NormalizeChannel(info.BuildChannel)
	if err != nil {
		return featureflags.Set{}
	}
	var lock *featureflags.ReleaseLock
	if channel == featureflags.ChannelRelease {
		lock, err = featureflags.DefaultReleaseLock(info.Version)
		if err != nil {
			return featureflags.Set{}
		}
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: channel,
		Lock:    lock,
	})
	if err != nil {
		return featureflags.Set{}
	}
	return features
}

func Serve(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	return NewServer(opts).Serve(ctx, in, out)
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil {
		return errors.New("mcp input is not configured")
	}
	if out == nil {
		return errors.New("mcp output is not configured")
	}

	reader := bufio.NewReader(in)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		payload, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			response := newErrorResponse(nil, codeParseError, err.Error(), nil)
			return s.writeResponse(out, response)
		}

		response := s.handlePayload(ctx, payload)
		if response == nil {
			continue
		}
		if err := s.writeResponse(out, response); err != nil {
			return err
		}
	}
}

func (s *Server) handlePayload(ctx context.Context, payload []byte) *rpcResponse {
	var req rpcRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return newErrorResponse(nil, codeParseError, "parse error", err.Error())
	}
	if req.JSONRPC != jsonrpcVersion || req.Method == "" {
		return newErrorResponse(req.ID, codeInvalidRequest, "invalid JSON-RPC request", nil)
	}
	if !hasRequestID(req) {
		return s.handleNotification(req)
	}

	switch req.Method {
	case methodInitialize:
		return newResultResponse(req.ID, s.initialize(req.Params))
	case methodToolsList:
		return newResultResponse(req.ID, map[string]any{"tools": s.tools()})
	case methodToolsCall:
		result, rpcErr := s.callTool(ctx, req.Params)
		if rpcErr != nil {
			return newErrorResponse(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		}
		return newResultResponse(req.ID, result)
	default:
		return newErrorResponse(req.ID, codeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method), nil)
	}
}

func (s *Server) handleNotification(_ rpcRequest) *rpcResponse {
	return nil
}

func (s *Server) initialize(params json.RawMessage) initializeResult {
	protocolVersion := defaultProtocolVersion
	if len(params) > 0 {
		var parsed initializeParams
		if err := json.Unmarshal(params, &parsed); err == nil && parsed.ProtocolVersion != "" {
			protocolVersion = parsed.ProtocolVersion
		}
	}
	return initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities: serverCapabilities{
			Tools: map[string]any{},
		},
		ServerInfo: serverInfo{
			Name:    s.serverName,
			Version: s.serverVersion,
		},
		Instructions: "Use Lopper MCP tools for local dependency surface analysis. Mutation tools are explicit, feature-gated, and require confirmation.",
	}
}

func (s *Server) writeResponse(out io.Writer, response *rpcResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		fallback, fallbackErr := json.Marshal(newErrorResponse(response.ID, codeInternalError, "marshal response failed", err.Error()))
		if fallbackErr != nil {
			return errors.Join(err, fallbackErr)
		}
		payload = fallback
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeFrame(out, payload)
}

func newResultResponse(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{
		JSONRPC: jsonrpcVersion,
		ID:      responseID(id),
		Result:  result,
	}
}

func newErrorResponse(id json.RawMessage, code int, message string, data any) *rpcResponse {
	return &rpcResponse{
		JSONRPC: jsonrpcVersion,
		ID:      responseID(id),
		Error: &rpcError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

func responseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	copied := make(json.RawMessage, len(id))
	copy(copied, id)
	return copied
}

func hasRequestID(req rpcRequest) bool {
	return len(req.ID) > 0
}
